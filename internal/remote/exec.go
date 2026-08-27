package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// commandTimeout bounds any single SSH invocation the daemon makes.
//
// These are all short commands — listing panes, probing for a binary, starting
// a session — but SSH does not give up quickly on a machine that drops packets
// rather than refusing: a blackholed address takes minutes to fail on its own.
// The reconcile loop holds the daemon's lock while it runs, so one such machine
// would otherwise freeze the menu, the status listing, and every other machine.
var commandTimeout = 30 * time.Second

// waitDelay is how long to keep waiting for output after the command itself has
// been killed.
//
// It matters more here than for a local call: ssh with ControlMaster leaves a
// multiplexing process behind on purpose, and anything that inherits the pipes
// keeps Wait blocked whatever the deadline said.
var waitDelay = 2 * time.Second

// connectTimeout bounds the TCP connect specifically, in seconds, so an
// unreachable machine fails quickly rather than waiting out the operating
// system's own timeout.
const connectTimeout = 10

// maxCommandOutput bounds what one invocation may print back.
//
// Everything asked for here is small -- a pane listing, a path, an
// acknowledgement -- and the buffers it is read into grow to fit whatever
// arrives. A machine whose Herdr prints without stopping would be held in
// memory here until the timeout, at whatever rate the link carries, and the
// daemon is a long-lived process on somebody's laptop.
//
// The same size the mirror allows one frame, which is the other place bytes
// arrive from a machine.
const maxCommandOutput = 8 * 1024 * 1024

// capped collects up to a limit and counts the rest away.
//
// Not an error at the point of writing: the command is still running, and what
// has already arrived is usually the useful part -- Herdr's refusals are short
// and come first. The caller is told after.
type capped struct {
	buf bytes.Buffer
	// stop ends the command once there is no point reading more of it.
	// Without it the cap saves the memory and not the half minute: the command
	// runs to its timeout while everything it says is counted and thrown away.
	stop    func()
	overran bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := maxCommandOutput - c.buf.Len(); room > 0 {
		if len(p) <= room {
			return c.buf.Write(p)
		}
		if _, err := c.buf.Write(p[:room]); err != nil {
			return 0, err
		}
	}
	if !c.overran {
		c.overran = true
		if c.stop != nil {
			c.stop()
		}
	}
	// Reported as written, because it was dealt with. Saying otherwise is a
	// short write, which ends the command with an error about this rather than
	// about the machine.
	return len(p), nil
}

// runCommand runs one SSH invocation and returns what it printed.
//
// Both streams come back even when the command failed: Herdr signals a refusal
// by exiting non-zero and printing the error envelope, so a caller that only
// gets an exit status has been handed the least useful part of the answer.
func runCommand(argv []string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// Killing the command is not the same as being done waiting for it.
	// Wait blocks until nothing holds the other end of the pipes it reads,
	// and a child that outlives its parent inherits those -- so a command
	// that leaves anything behind defeated the timeout completely: the
	// deadline passed, the process was killed, and this went on waiting.
	// WaitDelay closes them and gives up shortly after.
	cmd.WaitDelay = waitDelay
	out, errOut := capped{stop: cancel}, capped{stop: cancel}
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	runErr := cmd.Run()
	if out.overran || errOut.overran {
		// Before the deadline is considered, because stopping it is what made
		// the deadline irrelevant and this is the more particular reason.
		// Whatever it was, it was not an answer: everything asked for here
		// fits in a fraction of the limit, and the first megabytes of a flood
		// parse no better than the rest.
		return out.buf.Bytes(), errOut.buf.Bytes(), fmt.Errorf(
			"the machine sent more than %d bytes and was cut off", maxCommandOutput)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out.buf.Bytes(), errOut.buf.Bytes(), fmt.Errorf("timed out after %s", commandTimeout)
	}
	if runErr != nil {
		msg := strings.TrimSpace(errOut.buf.String())
		if msg == "" {
			msg = runErr.Error()
		}
		return out.buf.Bytes(), errOut.buf.Bytes(), fmt.Errorf("%w: %s", runErr, msg)
	}
	return out.buf.Bytes(), errOut.buf.Bytes(), nil
}
