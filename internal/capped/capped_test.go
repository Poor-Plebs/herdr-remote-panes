package capped

import "testing"

func TestWhatCountsAsOverrunning(t *testing.T) {
	// The boundary decides whether a reply that fits exactly is reported as
	// having been cut off, which would turn a good answer into a failed pass
	// and, after enough of them, a machine given up on.
	for _, tt := range []struct {
		what    string
		writes  []int
		overran bool
	}{
		{"well under", []int{1024}, false},
		{"exactly the limit in one go", []int{Max}, false},
		{"exactly the limit in pieces", []int{Max / 2, Max / 2}, false},
		{"one byte more", []int{Max + 1}, true},
		{"one byte more, in pieces", []int{Max, 1}, true},
		{"nothing at all", []int{0}, false},
	} {
		var c Writer
		total := 0
		for _, n := range tt.writes {
			written, err := c.Write(make([]byte, n))
			if err != nil {
				t.Fatalf("%s: %v", tt.what, err)
			}
			// Every byte accounted for, or whatever copies into this stops
			// with an error about the buffer rather than about the machine.
			if written != n {
				t.Errorf("%s: reported %d of %d written", tt.what, written, n)
			}
			total += n
		}
		if c.Overran != tt.overran {
			t.Errorf("%s: overran = %v after %d bytes, want %v",
				tt.what, c.Overran, total, tt.overran)
		}
		if kept := c.Buf.Len(); kept > Max {
			t.Errorf("%s: kept %d bytes, which is past the limit", tt.what, kept)
		}
	}
}
