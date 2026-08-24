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
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out.Bytes(), errOut.Bytes(), fmt.Errorf("timed out after %s", commandTimeout)
	}
	if runErr != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = runErr.Error()
		}
		return out.Bytes(), errOut.Bytes(), fmt.Errorf("%w: %s", runErr, msg)
	}
	return out.Bytes(), errOut.Bytes(), nil
}
