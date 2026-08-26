package mirror

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The observe stream is a live feed from another machine, decoded here and
// written straight into a pane. Everything about it arrives from somewhere this
// plugin does not control: the JSON envelope, the base64 inside it, and the
// length of both. One bad line is no reason to tear down a working terminal, so
// what matters is that a bad line is refused rather than half-accepted.

func FuzzDecodeFrame(f *testing.F) {
	for _, seed := range []string{
		"", "   ", "{", "{}", `{"bytes":""}`,
		`{"bytes":"aGVsbG8="}`, `{"bytes":"aGVsbG8"}`, `{"bytes":"aGVsbG8=!!!"}`,
		`{"bytes":"not!base64!"}`, `{"type":"resize","cols":80}`,
		"herdr: something happened", `{"bytes":123}`, `{"bytes":null}`,
		`{"bytes":"aGVsbG8=","bytes":123}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, line []byte) {
		raw, ok := decodeFrame(line)

		if !ok {
			// Nothing to write, and nothing pretending there is.
			if len(raw) != 0 {
				t.Fatalf("decodeFrame(%q) said no and handed back %q anyway", line, raw)
			}
			return
		}

		// Accepted means there is something to write: a frame that yields
		// nothing is a frame that should have been refused, and writing
		// nothing to a pane on every line of a stream is a busy loop.
		if len(raw) == 0 {
			t.Fatalf("decodeFrame(%q) accepted a frame with nothing in it", line)
		}
		// Accepted means it was a JSON envelope. Anything else reaching a
		// pane is this plugin writing a machine's noise into a terminal as
		// though the machine had said it.
		if !json.Valid(bytes.TrimSpace(line)) {
			t.Fatalf("decodeFrame(%q) accepted something that is not JSON", line)
		}
		// The same line twice is the same frame. The stream is replayed on
		// reconnect, and a decoder that drifts writes something different the
		// second time.
		if again, okAgain := decodeFrame(line); !okAgain || !bytes.Equal(again, raw) {
			t.Fatalf("decodeFrame(%q) gave %q then %q", line, raw, again)
		}
	})
}

func TestAFrameThatOnlyHalfDecodedIsRefused(t *testing.T) {
	// Duplicate keys are valid JSON -- json.Valid says yes, so the fuzz target
	// above passes them straight through -- and Unmarshal takes them in order:
	// the first "bytes" lands in the struct, the second raises a type error.
	// That is the one shape where the decode error and the empty field
	// disagree, and it is the only thing keeping the two halves of that guard
	// from being interchangeable.
	//
	// The frame is malformed. Writing the half of it that did decode puts a
	// remote machine's half-read output into a terminal as though it were
	// whole.
	line := []byte(`{"bytes":"aGVsbG8=","bytes":123}`)
	if raw, ok := decodeFrame(line); ok {
		t.Errorf("a frame that failed to decode was written to the pane as %q", raw)
	}
}
