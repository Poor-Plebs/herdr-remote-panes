package mirror

import (
	"bytes"
	"testing"
)

// FuzzMouseGate throws arbitrary bytes at the gate that stands between a
// machine's output and the terminal.
//
// It is a parser on a path carrying whatever the far end sends -- a program
// there printing binary, a connection dropping mid-sequence, a machine that is
// not the Herdr this expects. decodeFrame next door is fuzzed for the same
// reason; this one was added later and was not.
//
// What is held is what the gate promises: it never panics, and everything it
// passes on came from what it was given, in the order it was given. It drops
// mouse enables and nothing else, so its output is always a subsequence of its
// input -- which catches a gate that invents bytes, reorders them, or hands on
// half of a sequence it meant to hold.
func FuzzMouseGate(f *testing.F) {
	for _, seed := range []string{
		"", "\x1b", "\x1b[", "\x1b[?", "\x1b[?1000h", "\x1b[?1000l",
		"\x1b[?1000;1002;1006h", "\x1b[?1000;25h", "\x1b]8;;x\x1b\\", "\x1b]0;t\x07",
		"\x1bM", "\x1b[3@", "\x1b[200~", "hello", "\x1b[?1000h hello \x1b[?1002h",
		"\x1b[?1006l\x1b[?1000h\x1b[2J", "\x00\x01\x02", "\x1b[?h",
	} {
		f.Add([]byte(seed), 1)
	}

	f.Fuzz(func(t *testing.T, in []byte, split int) {
		var out bytes.Buffer
		gate := newMouseGate(&out)

		// Written in two pieces, so a sequence cut anywhere is exercised.
		at := 0
		if len(in) > 0 {
			at = ((split%len(in))+len(in))%len(in) + 1
			if at > len(in) {
				at = len(in)
			}
		}
		for _, piece := range [][]byte{in[:at], in[at:]} {
			n, err := gate.Write(piece)
			if err != nil {
				t.Fatalf("Write(%q) = %v", piece, err)
			}
			if n != len(piece) {
				t.Fatalf("Write(%q) reported %d of %d, which is a short write",
					piece, n, len(piece))
			}
		}
		gate.flush()

		// Everything it passed on came from what it was given, in order.
		got := out.Bytes()
		if len(got) > len(in) {
			t.Fatalf("gave %d bytes back from %d in", len(got), len(in))
		}
		i := 0
		for _, b := range got {
			for i < len(in) && in[i] != b {
				i++
			}
			if i == len(in) {
				t.Fatalf("what came out is not what went in, in order:\n in  %q\n out %q",
					in, got)
			}
			i++
		}
	})
}
