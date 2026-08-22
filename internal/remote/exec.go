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

// connectTimeout bounds the TCP connect specifically, in seconds, so an
// unreachable machine fails quickly rather than waiting out the operating
// system's own timeout.
const connectTimeout = 10

func runCommand(argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("timed out after %s", commandTimeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	return stdout.Bytes(), nil
}
