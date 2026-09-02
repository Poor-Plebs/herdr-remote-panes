package mirror

import (
	"bytes"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/project"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
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

func TestAMirroredPaneIsNotLeftReportingTheMouse(t *testing.T) {
	// The gate is tested above on its own, and attach is what has to be using
	// it. Two correct pieces either side of a wiring nothing checks is the
	// shape of bug this plugin has had before: the gate could be perfect and
	// the pane still hand every drag to the far side.
	//
	// So this goes through attach itself, with an ssh that replays the
	// recording, and looks at what reached the terminal.
	fixture, err := filepath.Abs("testdata/attach-preamble.bin")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"last=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in *command\\ -v\\ herdr*) echo /usr/bin/herdr; exit 0;; esac\n" +
		"cat " + fixture + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = write
	attachErr := attach(remote.New("bot", ""), "term_1")
	os.Stdout = saved
	write.Close()
	got, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if attachErr != nil {
		t.Fatalf("attach: %v", attachErr)
	}

	// Nothing arriving would pass every check below by never looking.
	if len(got) == 0 {
		t.Fatal("the pane received nothing at all")
	}
	for _, mode := range []string{"1000", "1002", "1003", "1006", "1015"} {
		if n := countSeq(got, "\x1b[?"+mode+"h"); n != 0 {
			t.Errorf("?%sh reached the pane %d times: a mirrored tab still gives "+
				"every drag to the far side and cannot be selected from", mode, n)
		}
	}
	// And the terminal's own output did arrive, so this is a pane that works
	// rather than one nothing is writing to.
	raw := realAttachStream(t)
	for _, keep := range []string{"\x1b[?1049h", "\x1b[2J"} {
		if countSeq(got, keep) != countSeq(raw, keep) {
			t.Errorf("%q did not reach the pane: the gate is eating more than "+
				"the mouse", keep)
		}
	}
}

func TestEscapeLenKnowsWhereASequenceEnds(t *testing.T) {
	// Everything the gate does rests on this: get the length wrong and it
	// either passes on half a sequence, which becomes characters on the
	// screen, or holds a finished one waiting for a byte that never comes.
	for _, tt := range []struct {
		what string
		in   string
		want int
	}{
		// CSI ends at the first byte from @ to ~, and both ends of that range
		// are real finals: @ is insert-blank, ~ ends a key sequence.
		{"an ordinary CSI", "\x1b[?1000h", 8},
		{"a CSI ending at @", "\x1b[3@", 4},
		{"a CSI ending at ~", "\x1b[200~", 6},
		{"a CSI ending just inside the range", "\x1b[?7l", 5},
		{"a CSI with nothing after it yet", "\x1b[?100", -1},
		{"a bare CSI introducer", "\x1b[", -1},

		// Two bytes: ESC and one more. Held as incomplete, it never arrives.
		{"an escape and one byte", "\x1bM", 2},
		{"an escape alone", "\x1b", -1},

		// OSC runs until BEL or ST, and the recording contains one.
		{"OSC ended by BEL", "\x1b]0;title\x07", 10},
		{"OSC ended by ST", "\x1b]8;;\x1b\\", 7},
		{"OSC still going", "\x1b]0;tit", -1},
		{"OSC with ST half arrived", "\x1b]8;;\x1b", -1},
	} {
		if got := escapeLen([]byte(tt.in)); got != tt.want {
			t.Errorf("%s: escapeLen(%q) = %d, want %d", tt.what, tt.in, got, tt.want)
		}
	}
}

func TestSequencesTheGateDoesNotUnderstandStillArrive(t *testing.T) {
	// The gate stands in front of the terminal during the handshake, so
	// anything it mishandles is a pane drawing wrongly with nothing to say
	// why. These all pass through whole, and the mouse enable between them
	// still goes.
	for _, tt := range []struct{ what, in, want string }{
		{"an OSC ended by BEL", "\x1b]0;a title\x07\x1b[?1000h", "\x1b]0;a title\x07"},
		{"an OSC ended by ST", "\x1b]8;;x\x1b\\\x1b[?1000h", "\x1b]8;;x\x1b\\"},
		{"a two-byte escape", "\x1bM\x1b[?1000h", "\x1bM"},
		{"a CSI ending at @", "\x1b[3@\x1b[?1000h", "\x1b[3@"},
		{"a CSI ending at ~", "\x1b[200~\x1b[?1000h", "\x1b[200~"},
	} {
		if got := through(t, nil, []byte(tt.in)); string(got) != tt.want {
			t.Errorf("%s: got %q, want %q", tt.what, got, tt.want)
		}
	}
}

func TestAnOSCSplitAcrossWritesSurvives(t *testing.T) {
	// An OSC is the longest thing in a handshake and the likeliest to be cut.
	// Split inside the ST that ends it is the awkward case: the escape byte
	// arrives and the backslash does not.
	whole := "\x1b]8;;http://example\x1b\\\x1b[?1000h!"
	for at := 1; at < len(whole); at++ {
		got := through(t, nil, []byte(whole[:at]), []byte(whole[at:]))
		if want := "\x1b]8;;http://example\x1b\\!"; string(got) != want {
			t.Errorf("split at %d: got %q, want %q", at, got, want)
		}
	}
}

func TestNothingMalformedGetsPastTheEnableTest(t *testing.T) {
	// isMouseEnable reads into the sequence it is given, so it has to refuse
	// anything short or shaped wrong before it does. Called on every sequence
	// in a handshake, a wrong answer here is a crash in somebody's pane.
	for _, in := range []string{
		"", "\x1b", "\x1b[", "\x1b[?", "\x1b[h", "\x1b[?h",
		"\x1b]0;x\x07", "\x1bM", "[?1000h", "\x1b[1000h", "\x1b(B",
		// Shaped to look right in every place but one. Each of these is
		// refused by a different part of the test, and a chain of ors joined
		// wrongly lets exactly one of them through -- which would then be read
		// for parameters it does not have.
		"X[?1000h",    // right but for the escape
		"\x1b??1000h", // right but for the bracket
		"\x1b[!1000h", // right but for the question mark
	} {
		if isMouseEnable([]byte(in)) {
			t.Errorf("isMouseEnable(%q) said yes; it is not a mouse enable", in)
		}
	}
	// And the shortest thing that really is one.
	if !isMouseEnable([]byte("\x1b[?1000h")) {
		t.Error("a plain mouse enable was not recognised")
	}
}

func TestEveryModeDroppedFromTheHandshakeIsWrittenDown(t *testing.T) {
	// The page explaining why the mouse stopped selecting text listed five
	// modes and said the plugin drops "those five, and only those five". The
	// list here has seven: the two extra are ones `terminal attach` does not
	// send today, kept because listing one that never arrives costs nothing
	// and missing one costs a pane whose text cannot be selected.
	//
	// So the page was precise and wrong, which is worse than vague: somebody
	// reading it to work out whether their own mouse trouble is this would
	// check the five, find the mode they are looking at is not among them, and
	// conclude it is something else.
	docs, err := project.DocsText()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range mouseModes {
		// As the page writes them, which is how a terminal's documentation
		// writes them too.
		written := "?" + string(mode) + "h"
		if !strings.Contains(docs, written) {
			t.Errorf("the handshake gate drops %s and no page says so", written)
		}
	}
	if len(mouseModes) < 5 {
		t.Fatalf("the gate drops %d modes, which is fewer than the handshake sends; "+
			"this is checking nothing", len(mouseModes))
	}
}

// TestGivingUpDoesNotSwallowWhatWasBeingHeld holds the case where the two
// tests above meet.
//
// Each of them reaches one half and neither reaches this.
// TestAStreamThatStopsMidSequenceLosesNothing sends six bytes, so the limit
// never fires and flush at the end of the stream is what returns them.
// TestAStreamOfNothingButEscapesGivesUpEventually sends whole sequences, so
// nothing is in hand at the moment the limit does fire.
//
// Giving up with half a sequence held is where bytes can go missing. The limit
// opens the gate and drops what was held, so flush has nothing left to return
// and those bytes are gone for good -- after Write has already told whoever is
// copying into this that every one of them was written.
//
// The order matters as much as the bytes. Once the limit has opened the gate
// everything after it passes straight through, so anything still held has to
// go out in front of it rather than trailing the rest of the session.
func TestGivingUpDoesNotSwallowWhatWasBeingHeld(t *testing.T) {
	long := strings.Repeat("\x1b[?1000h", (preambleLimit/8)+64)
	// The preamble has to be long enough to give up on, or this is the other
	// test with a different ending.
	if len(long) <= preambleLimit {
		t.Fatalf("the preamble is %d bytes and the limit is %d, so the gate never gives up",
			len(long), preambleLimit)
	}

	got := string(through(t, nil, []byte(long+"\x1b[?100"), []byte("hello")))

	if !strings.Contains(got, "\x1b[?100") {
		t.Errorf("the half sequence in hand when the gate gave up was never written: %q", got)
	}
	if got != "\x1b[?100hello" {
		t.Errorf("got %q, want the held bytes and then what followed them", got)
	}
}
