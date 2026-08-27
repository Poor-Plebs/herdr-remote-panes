package mirror

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// realAttachStream is a recording of `herdr terminal attach` against a live
// terminal, taken from Herdr 0.8.2. Recorded rather than written out here: what
// this has to handle is what the client actually sends, and a hand-made
// approximation of it would be a test of my own guess.
func realAttachStream(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/attach-preamble.bin")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func through(t *testing.T, gate func(*mouseGate), chunks ...[]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	g := newMouseGate(&out)
	for _, chunk := range chunks {
		n, err := g.Write(chunk)
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		// Whoever copies into this expects every byte accounted for, whether
		// it was passed on or dropped.
		if n != len(chunk) {
			t.Fatalf("reported %d of %d bytes written, which is a short write", n, len(chunk))
		}
	}
	if gate != nil {
		gate(g)
	}
	g.flush()
	return out.Bytes()
}

func countSeq(b []byte, seq string) int { return bytes.Count(b, []byte(seq)) }

func TestTheMouseIsNotTurnedOnByTheAttachItself(t *testing.T) {
	// The reported bug: text cannot be selected out of a mirrored pane without
	// holding the key that takes the mouse back from the application. The
	// attach client enables the lot in its handshake -- including 1003, which
	// reports every movement -- before anything on the far side has asked.
	raw := realAttachStream(t)

	// The recording has to contain the thing being removed, or this proves
	// nothing about it.
	before := 0
	for _, mode := range []string{"1000", "1002", "1003", "1006", "1015"} {
		before += countSeq(raw, "\x1b[?"+mode+"h")
	}
	if before != 5 {
		t.Fatalf("the recording holds %d mouse enables, want the 5 that were captured", before)
	}

	got := through(t, nil, raw)

	for _, mode := range []string{"1000", "1002", "1003", "1006", "1015"} {
		if n := countSeq(got, "\x1b[?"+mode+"h"); n != 0 {
			t.Errorf("?%sh survived the gate %d times: the pane still gives every "+
				"drag to the far side", mode, n)
		}
	}
}

func TestNothingElseInTheHandshakeIsTouched(t *testing.T) {
	// The gate removes five sequences and must be invisible otherwise. It sits
	// in front of the terminal, so anything it eats by accident is a pane
	// drawing wrongly with nothing to say why.
	raw := realAttachStream(t)
	got := through(t, nil, raw)

	var want bytes.Buffer
	rest := raw
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, escape)
		if i < 0 {
			want.Write(rest)
			break
		}
		want.Write(rest[:i])
		size := escapeLen(rest[i:])
		if size < 0 {
			want.Write(rest[i:])
			break
		}
		if seq := rest[i : i+size]; !isMouseEnable(seq) {
			want.Write(seq)
		}
		rest = rest[i+size:]
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Errorf("the gate changed something other than the mouse enables:\n got %q\nwant %q",
			got, want.Bytes())
	}

	// The modes the client turns off, the alternate screen, the cursor: all of
	// it has to arrive, or the pane is left in a state nothing put it in.
	for _, keep := range []string{"\x1b[?1049h", "\x1b[?1000l", "\x1b[?2004l", "\x1b[?25l"} {
		if countSeq(got, keep) != countSeq(raw, keep) {
			t.Errorf("%q was not passed through unchanged", keep)
		}
	}
}

func TestAProgramOnTheFarSideStillGetsTheMouse(t *testing.T) {
	// The whole point of gating only the handshake. Something running over
	// there -- vim, htop -- asks for the mouse in its own output, which arrives
	// after the preamble. Taking that away would be a worse bug than the one
	// this fixes, and a much harder one to work out.
	raw := realAttachStream(t)
	vim := []byte("\x1b[?1000h\x1b[?1006h")

	got := through(t, nil, raw, vim)

	for _, mode := range []string{"1000", "1006"} {
		if countSeq(got, "\x1b[?"+mode+"h") != 1 {
			t.Errorf("a program that asked for the mouse after the preamble did not "+
				"get ?%sh: %q", mode, got)
		}
	}
}

func TestASequenceSplitAcrossWritesIsNotBroken(t *testing.T) {
	// The stream arrives in whatever sizes the reader hands over, so an escape
	// sequence can be cut anywhere. Passing on half of one leaves the rest to
	// be read as text, which puts characters on the screen that nobody sent.
	raw := realAttachStream(t)

	whole := through(t, nil, raw)
	for _, at := range []int{1, 2, 3, 7, 100, 186, 190, 200, 217, 244, 245, 300} {
		if at >= len(raw) {
			continue
		}
		split := through(t, nil, raw[:at], raw[at:])
		if !bytes.Equal(split, whole) {
			t.Errorf("split at %d gave a different result:\n split %q\nwhole %q",
				at, split, whole)
		}
	}

	// And one byte at a time, which is the worst case there is.
	var chunks [][]byte
	for i := range raw {
		chunks = append(chunks, raw[i:i+1])
	}
	if byByte := through(t, nil, chunks...); !bytes.Equal(byByte, whole) {
		t.Errorf("a byte at a time gave a different result:\n got %q\nwant %q", byByte, whole)
	}
}

func TestAMixedSetIsLeftAlone(t *testing.T) {
	// A terminal can be told to set several modes at once. Dropping "?1000;25h"
	// to be rid of the mouse would hide the cursor along with it.
	for _, tt := range []struct {
		what string
		seq  string
		drop bool
	}{
		{"mouse alone", "\x1b[?1000h", true},
		{"several mouse modes", "\x1b[?1000;1002;1006h", true},
		{"mouse with the cursor", "\x1b[?1000;25h", false},
		{"the cursor alone", "\x1b[?25h", false},
		{"turning the mouse off", "\x1b[?1000l", false},
		{"no modes at all", "\x1b[?h", false},
		{"a number that starts like one", "\x1b[?10000h", false},
	} {
		got := isMouseEnable([]byte(tt.seq))
		if got != tt.drop {
			t.Errorf("%s: isMouseEnable(%q) = %v, want %v", tt.what, tt.seq, got, tt.drop)
		}
	}
}

func TestContentEndsTheHandshakeForGood(t *testing.T) {
	// Once output has been seen the gate is done, and stays done: the far side
	// may turn the mouse on and off all session, and none of that is this
	// side's business.
	got := through(t, nil,
		[]byte("\x1b[?1000h"), // handshake: dropped
		[]byte("hello"),       // output: the preamble is over
		[]byte("\x1b[?1000h"), // the far side asking: kept
	)
	if want := "hello\x1b[?1000h"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAStreamThatStopsMidSequenceLosesNothing(t *testing.T) {
	// A preamble that never finishes would otherwise leave its last bytes held
	// for a write that never comes.
	got := through(t, nil, []byte("\x1b[?100"))
	if string(got) != "\x1b[?100" {
		t.Errorf("a half-written sequence was lost: got %q", got)
	}
}

func TestAStreamOfNothingButEscapesGivesUpEventually(t *testing.T) {
	// A preamble is a few hundred bytes. Something still introducing itself
	// after eight kilobytes is not one, and staying shut on it for the rest of
	// the session would mean a pane that never gets the mouse back even where
	// something is using it.
	long := strings.Repeat("\x1b[?1000h", (preambleLimit/8)+64)
	got := through(t, nil, []byte(long), []byte("\x1b[?1000h"))

	// The one after the limit is somebody asking, and gets through.
	if countSeq(got, "\x1b[?1000h") != 1 {
		t.Errorf("the gate never gave up: %d enables got through, want the one "+
			"sent after the limit", countSeq(got, "\x1b[?1000h"))
	}
}
