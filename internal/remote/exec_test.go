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

func TestEveryConnectionIsSharedAndGivesUpOnAMachineThatHasGone(t *testing.T) {
	// This package's own comment says every connection is multiplexed through
	// an OpenSSH ControlMaster socket, "so polling a machine costs one round
	// trip rather than a full handshake". Four of the six options that make
	// that true, and that stop a vanished machine hanging the poll, could be
	// dropped with internal/remote, internal/syncd and internal/mirror all
	// green -- because the ssh every test runs is a shell script that reads
	// the last two arguments and ignores every option before them.
	//
	// Nothing observable changes when they go, which is the difficulty: the
	// plugin still works, one full handshake at a time, every two seconds, per
	// machine. This took over from TestSSHArgsBoundTheConnect, which held the
	// sixth of them and carried the sentence now on its row.
	//
	// A number here is not a preference. ControlPersist=no closes the shared
	// connection with the first command, and ServerAliveCountMax=0 turns the
	// keepalive off, so each row asserts the setting it needs and not merely
	// the name of the option.
	for _, tt := range []struct {
		option string
		why    string
	}{
		{"ControlMaster=auto",
			"ssh makes no shared connection at all, so every poll is a full handshake"},
		{"ControlPath=",
			"there is no socket to share, so there is nothing for the rest of this to do"},
		{"ControlPersist=120",
			"the shared connection goes with the command that opened it, which is not sharing"},
		{"ServerAliveInterval=15",
			"a machine that vanishes rather than closing leaves ssh waiting on a half-open connection"},
		{"ServerAliveCountMax=3",
			"without a count of missed replies the interval never adds up to a failure"},
		{"ConnectTimeout=",
			"an unreachable machine waits out the operating system's TCP timeout, which is minutes"},
	} {
		// Both forms, because they share one base and an option that reached
		// only the interactive one would leave every poll -- which is all of
		// them but a handful -- without it.
		for _, tty := range []bool{false, true} {
			args := strings.Join(New("workbox", "").SSHArgs(tty), " ")
			if !strings.Contains(args, tt.option) {
				t.Errorf("SSHArgs(tty=%v) does not carry %s, so %s:\n  %s",
					tty, tt.option, tt.why, args)
			}
		}
	}
}

// TestAFailureWithNothingOnStderrSaysItOnce holds the other half of what a
// failed command reports.
//
// When the command prints its reason, the reason is the message and the exit
// status is the prefix -- "exit status 3: the reason" -- which is what
// TestRunCommandReportsStderr holds. When it prints nothing there is no reason
// to add, and what was added instead was the exit status a second time. For a
// command that could not start at all, that is a whole sentence twice over.
//
// It matters because these do not stay here. Every caller puts what it was
// doing in front, so the menu draws "bot is not reachable over ssh: " and then
// the same words twice, which is how one failure becomes a paragraph.
func TestAFailureWithNothingOnStderrSaysItOnce(t *testing.T) {
	for _, tt := range []struct {
		what  string
		argv  []string
		twice string
	}{
		{"a command that failed quietly", []string{"sh", "-c", "exit 3"}, "exit status 3"},
		{"a command that could not start", []string{"/nonexistent/ssh", "bot"}, "no such file or directory"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			_, errOut, err := runCommand(tt.argv)
			if err == nil {
				t.Fatal("a failing command should error")
			}
			// The case this names, or it is testing the branch beside it.
			if len(strings.TrimSpace(string(errOut))) != 0 {
				t.Fatalf("stderr carried %q, so this is the other branch", errOut)
			}
			if n := strings.Count(err.Error(), tt.twice); n != 1 {
				t.Errorf("%q says %q %d times, want once", err, tt.twice, n)
			}
		})
	}
}
