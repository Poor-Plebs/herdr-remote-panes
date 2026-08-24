package remote

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These drive the ssh layer against a stand-in for ssh itself, so the
// difference this package exists to draw -- a machine that cannot be reached,
// against one that can be reached but has no Herdr on it -- is exercised rather
// than described. The daemon does different things with those two: the second
// falls back to a plain SSH terminal and works, the first is a failure worth
// reporting.

// fakeSSH puts an ssh on PATH that behaves as the script says. The script is
// handed the whole argv, so it can answer differently for the probe, for the
// reachability check, and for a version call.
func fakeSSH(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"),
		[]byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// remoteCommandIs matches the last argument, which is what ssh would run on the
// machine.
const remoteCommandIs = `last=""; for a in "$@"; do last="$a"; done; `

func TestBinFindsHerdrWhereTheInstallerPutIt(t *testing.T) {
	// `ssh host <command>` runs no login shell, so an install under
	// ~/.local/bin is invisible to a bare "herdr" even though logging in finds
	// it. The probe looks in the usual places and hands back a path.
	fakeSSH(t, remoteCommandIs+`
case "$last" in
  *command\ -v\ herdr*) echo /home/deploy/.local/bin/herdr; exit 0;;
  *) exit 0;;
esac`)

	client := New("bot", "")
	bin, err := client.Bin()
	if err != nil {
		t.Fatalf("Bin: %v", err)
	}
	if bin != "/home/deploy/.local/bin/herdr" {
		t.Errorf("Bin = %q, want the path the probe found", bin)
	}
}

func TestBinAsksTheMachineOnlyOnce(t *testing.T) {
	// This is called from the reconcile loop, which runs every couple of
	// seconds. Probing every time would be a round trip per machine per pass to
	// learn something that does not change.
	counter := filepath.Join(t.TempDir(), "calls")
	fakeSSH(t, remoteCommandIs+`
case "$last" in
  *command\ -v\ herdr*) echo x >> `+counter+`; echo /usr/bin/herdr; exit 0;;
  *) exit 0;;
esac`)

	client := New("bot", "")
	for i := 0; i < 3; i++ {
		if _, err := client.Bin(); err != nil {
			t.Fatalf("Bin: %v", err)
		}
	}
	raw, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("the probe never ran: %v", err)
	}
	if got := len(strings.Fields(string(raw))); got != 1 {
		t.Errorf("the machine was probed %d times, want once", got)
	}
}

func TestAConfiguredPathIsNotProbedFor(t *testing.T) {
	// Saying where Herdr is should save the round trip, not add a check that it
	// was right.
	fakeSSH(t, `echo "the machine was asked: $*" >&2; exit 1`)

	client := NewWithBin("bot", "", "/opt/herdr/bin/herdr")
	bin, err := client.Bin()
	if err != nil {
		t.Fatalf("Bin: %v", err)
	}
	if bin != "/opt/herdr/bin/herdr" {
		t.Errorf("Bin = %q, want the configured path", bin)
	}
}

func TestAMachineWithNoHerdrIsNotAMachineThatIsDown(t *testing.T) {
	// The distinction the daemon acts on. A machine without Herdr is perfectly
	// usable over plain SSH, so it falls back and works; a machine that cannot
	// be reached is a failure worth reporting. Reading the first as the second
	// would refuse to connect to a machine that was fine.
	fakeSSH(t, remoteCommandIs+`
case "$last" in
  true) exit 0;;
  *) exit 1;;
esac`)

	client := New("bot", "")

	_, err := client.Bin()
	if !errors.Is(err, ErrNoHerdr) {
		t.Errorf("Bin on a machine with no Herdr = %v, want ErrNoHerdr", err)
	}
	if err := client.CheckHerdr(); !errors.Is(err, ErrNoHerdr) {
		t.Errorf("CheckHerdr on a machine with no Herdr = %v, want ErrNoHerdr", err)
	}
	// And the machine itself is fine, which is what makes the fallback right.
	if err := client.Reachable(); err != nil {
		t.Errorf("Reachable = %v, want nil: ssh to this machine works", err)
	}
}

func TestAMachineThatCannotBeReachedSaysSo(t *testing.T) {
	// Not ErrNoHerdr: falling back to a plain SSH terminal on a machine that
	// refuses ssh gives a pane that fails a moment later, with the reason gone.
	fakeSSH(t, `echo "ssh: connect to host bot port 22: Connection refused" >&2; exit 255`)

	client := New("bot", "")

	_, err := client.Bin()
	if errors.Is(err, ErrNoHerdr) {
		t.Error("a machine that refused ssh was read as one without Herdr")
	}
	if err == nil || !strings.Contains(err.Error(), "not reachable over ssh") {
		t.Errorf("Bin = %v, want it to say the machine is not reachable", err)
	}
	// The reason ssh gave is kept, since that is the whole of what there is to
	// go on.
	if err == nil || !strings.Contains(err.Error(), "Connection refused") {
		t.Errorf("Bin = %v, want ssh's own reason in it", err)
	}

	if err := client.CheckHerdr(); errors.Is(err, ErrNoHerdr) {
		t.Error("CheckHerdr read an unreachable machine as one without Herdr")
	}
	if err := client.Reachable(); err == nil {
		t.Error("Reachable said an unreachable machine was reachable")
	}
}

func TestADirectoryNamedHerdrIsNotABinary(t *testing.T) {
	// The probe tests -f as well as -x, because a directory is executable in
	// the sense -x asks about. Handed one back, every call after it would fail
	// saying something else entirely.
	dir := t.TempDir()
	fake := filepath.Join(dir, "herdr")
	if err := os.MkdirAll(fake, 0o755); err != nil {
		t.Fatal(err)
	}

	// The probe script, run here against a directory named herdr rather than
	// over ssh: what it decides is the same either way.
	script := strings.ReplaceAll(probeScript, `"$HOME/.local/bin/herdr"`, fake)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH=/nonexistent")
	out, err := cmd.Output()
	if err == nil {
		t.Errorf("the probe accepted a directory as the binary: %q", out)
	}
}

func TestAnSSHThatLeavesSomethingBehindStillGivesUp(t *testing.T) {
	// The deadline kills the command; it does not end the wait for its output.
	// Wait blocks until nothing holds the other end of the pipes, and a child
	// that outlives its parent inherits those -- so a command that leaves
	// anything behind used to defeat the timeout completely: the deadline
	// passed, ssh was killed, and this went on waiting for a grandchild.
	//
	// Neither of the two places this could happen for real does. ssh's
	// multiplexing process sends its output to /dev/null, and the remote server
	// is started with every stream redirected on purpose. This is about the
	// bound meaning what it says regardless.
	restoreTimeout, restoreDelay := commandTimeout, waitDelay
	commandTimeout, waitDelay = 100*time.Millisecond, 500*time.Millisecond
	defer func() { commandTimeout, waitDelay = restoreTimeout, restoreDelay }()

	// An ssh that hangs and leaves a child holding the pipes it was given.
	fakeSSH(t, "sleep 30 &\nsleep 30\n")

	done := make(chan error, 1)
	go func() { done <- New("bot", "").Reachable() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an ssh that never answered was treated as reachable")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("the error is %q, which does not say it gave up waiting", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the call was not given up on, so the timeout bounds nothing")
	}
}
