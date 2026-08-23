package remote

import (
	"strings"
	"testing"
	"time"
)

func TestRunCommandBoundsAHangingCommand(t *testing.T) {
	// SSH does not give up quickly on a machine that drops packets rather than
	// refusing — a blackholed address takes minutes to fail on its own. The
	// reconcile loop holds the daemon's lock while it runs, so one such machine
	// would freeze the menu, the status listing, and every other machine.
	original := commandTimeout
	commandTimeout = 200 * time.Millisecond
	defer func() { commandTimeout = original }()

	start := time.Now()
	_, _, err := runCommand([]string{"sleep", "30"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a command that outlives the deadline should fail")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want it to say it timed out", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %s, want it bounded near %s", elapsed, commandTimeout)
	}
}

func TestRunCommandReturnsOutput(t *testing.T) {
	out, _, err := runCommand([]string{"echo", "hello"})
	if err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello" {
		t.Errorf("output = %q, want %q", out, "hello")
	}
}

func TestRunCommandReportsStderr(t *testing.T) {
	// The reason a command failed is in its stderr, and losing it leaves
	// "exit status 255" as the only clue.
	_, _, err := runCommand([]string{"sh", "-c", "echo the reason >&2; exit 3"})
	if err == nil {
		t.Fatal("a failing command should error")
	}
	if !strings.Contains(err.Error(), "the reason") {
		t.Errorf("error = %v, want it to carry stderr", err)
	}
}

func TestSSHArgsBoundTheConnect(t *testing.T) {
	// Without this, an unreachable machine waits out the operating system's
	// TCP timeout, which is minutes.
	args := strings.Join(New("workbox", "").SSHArgs(false), " ")
	if !strings.Contains(args, "ConnectTimeout=") {
		t.Errorf("ssh args %q should bound the connect", args)
	}
}
