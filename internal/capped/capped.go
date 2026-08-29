// Package capped collects what a command prints, up to a limit, and says when
// there was more.
//
// Everything this plugin asks of a Herdr -- here or on another machine -- is
// small: a pane listing, a path, an acknowledgement. What it printed was read
// into a buffer that grows to fit, so one that printed without stopping would
// be held in memory until the command timed out, in a process that lives on
// somebody's laptop all day.
//
// One package rather than the same twenty lines in two, because the two would
// drift: this plugin has already had one gate written twice and got backwards
// in one of them.
package capped

import "bytes"

// Max is what one command may print back.
//
// Everything asked for here is small -- a pane listing, a path, an
// acknowledgement -- and the buffers it is read into grow to fit whatever
// arrives. A machine whose Herdr prints without stopping would be held in
// memory here until the timeout, at whatever rate the link carries, and the
// daemon is a long-lived process on somebody's laptop.
//
// The same size the mirror allows one frame, which is the other place bytes
// arrive from a machine.
const Max = 8 * 1024 * 1024

// Writer collects up to [Max] and counts the rest away.
type Writer struct {
	Buf bytes.Buffer
	// stop ends the command once there is no point reading more of it.
	// Without it the cap saves the memory and not the half minute: the command
	// runs to its timeout while everything it says is counted and thrown away.
	Stop    func()
	Overran bool
}

// Write keeps what fits and counts the rest away, stopping the command the
// first time something does not fit.
//
// Not an error at the point of writing: the command is still running, and what
// has already arrived is usually the useful part -- Herdr's refusals are short
// and come first. The caller is told after.
func (c *Writer) Write(p []byte) (int, error) {
	if room := Max - c.Buf.Len(); room > 0 {
		if len(p) <= room {
			return c.Buf.Write(p)
		}
		if _, err := c.Buf.Write(p[:room]); err != nil {
			return 0, err
		}
	}
	if !c.Overran {
		c.Overran = true
		if c.Stop != nil {
			c.Stop()
		}
	}
	// Reported as written, because it was dealt with. Saying otherwise is a
	// short write, which ends the command with an error about this rather than
	// about the machine.
	return len(p), nil
}
