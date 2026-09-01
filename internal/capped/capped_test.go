package capped

import "testing"

// theLimit is what [Max] is expected to be, written out rather than read from
// it. A case that says Max says nothing about what Max should be: raise the
// constant and the case rises with it, so this table passed for any value the
// bound could take -- including one that holds a gigabyte of a runaway command
// in a daemon that sits on somebody's laptop all day.
//
// Writing it out is also what keeps the table cheap. Sizes taken from Max grow
// with Max, so raising the constant to see what notices allocated a thousand
// times eight megabytes and took the machine down rather than failing.
//
// Deliberately not `= Max`. Made to follow the constant, this goes back to
// testing the mechanism and nothing about the number.
const theLimit = 8 * 1024 * 1024

func TestTheLimitIsEightMegabytes(t *testing.T) {
	// The size itself, said once and plainly. What it costs to be wrong is in
	// Max's own comment: this is the most one command may print back, and the
	// buffers it arrives in grow to fit whatever comes.
	if Max != theLimit {
		t.Errorf("Max = %d, want %d -- if this moved on purpose, move theLimit "+
			"with it and check the mirror, which takes its frame bound from here",
			Max, theLimit)
	}
}

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
		{"exactly the limit in one go", []int{theLimit}, false},
		{"exactly the limit in pieces", []int{theLimit / 2, theLimit / 2}, false},
		{"one byte more", []int{theLimit + 1}, true},
		{"one byte more, in pieces", []int{theLimit, 1}, true},
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
		if kept := c.Buf.Len(); kept > theLimit {
			t.Errorf("%s: kept %d bytes, which is past the limit", tt.what, kept)
		}
	}
}
