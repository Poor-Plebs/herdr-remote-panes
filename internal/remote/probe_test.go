package remote

import (
	"errors"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
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

func TestTheProbeLooksWhereHerdrIsActuallyInstalled(t *testing.T) {
	// `ssh host <command>` does not run a login shell, so an install under
	// ~/.local/bin — where Herdr's own installer puts it for a non-root user —
	// is invisible to `command -v`. The probe not stopping there is the whole
	// reason a machine with a hand-installed Herdr mirrors at all.
	//
	// Written down because I got this wrong from the outside: `command -v` on a
	// machine came back empty and I concluded mirroring would need herdr_bin
	// set. It did not, because of these lines.
	for _, where := range []string{
		"$HOME/.local/bin/herdr",
		"/usr/local/bin/herdr",
		"/opt/homebrew/bin/herdr",
		"$HOME/.nix-profile/bin/herdr",
		"$HOME/.local/share/mise/shims/herdr",
	} {
		if !strings.Contains(probeScript, where) {
			t.Errorf("the probe no longer looks in %s", where)
		}
	}
	// And PATH first, since that is right when it works.
	if !strings.HasPrefix(probeScript, "command -v herdr") {
		t.Errorf("the probe no longer tries the PATH first: %q", probeScript)
	}
}

// The tests below go through Run rather than around it. The refusal case has
// been held since it was fixed, but by building what Run builds -- if Run
// stopped naming the machine, or stopped reading the envelope at all, that
// test would have gone on passing. These make a machine answer instead.

func TestARemoteRefusalArrivesWithItsCode(t *testing.T) {
	// Herdr on the machine refuses the way it does here: non-zero, with the
	// error envelope on stdout. "That pane is already gone" has to stay
	// tellable from "that went wrong at the far end".
	fakeSSH(t, `printf '%s\n' '{"error":{"code":"pane_not_found","message":"pane w1:p2 not found"}}'
exit 1`)

	_, err := NewWithBin("bot", "agents", "herdr").Run("pane", "close", "w1:p2")
	if err == nil {
		t.Fatal("a refusal from the machine came back as success")
	}
	if !herdrcli.IsNotFound(err) {
		t.Errorf("the code did not survive the trip: %v", err)
	}
	if !strings.Contains(err.Error(), "bot") {
		t.Errorf("the error does not name the machine it came from: %v", err)
	}
}

func TestAFailureWithNoEnvelopeStillSaysWhatHappened(t *testing.T) {
	// Not every failure is Herdr refusing. ssh itself fails this way, and what
	// it printed is the only account of why.
	fakeSSH(t, `echo "Permission denied (publickey)." >&2
exit 255`)

	_, err := NewWithBin("bot", "agents", "herdr").Run("pane", "list")
	if err == nil {
		t.Fatal("ssh failing came back as success")
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("what ssh said was dropped: %v", err)
	}
	if !strings.Contains(err.Error(), "bot") {
		t.Errorf("the error does not name the machine: %v", err)
	}
}

func TestPaneListReadsWhatTheMachineAnswered(t *testing.T) {
	fakeSSH(t, `printf '%s\n' '{"result":{"panes":[`+
		`{"pane_id":"w1:p1","tab_id":"w1:t1","terminal_id":"term-1"},`+
		`{"pane_id":"w1:p2","tab_id":"w1:t1","terminal_id":"term-2"}]}}'`)

	panes, err := NewWithBin("bot", "agents", "herdr").PaneList()
	if err != nil {
		t.Fatalf("pane list: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("got %d panes, want 2: %+v", len(panes), panes)
	}
	if panes[0].PaneID != "w1:p1" || panes[1].TerminalID != "term-2" {
		t.Errorf("the panes did not come through as sent: %+v", panes)
	}
}

func TestTabOrderReadsTheNumbersTheMachineGave(t *testing.T) {
	// Panes are mirrored in the order they appear on the machine, so these
	// numbers decide the order of the panes on this side.
	fakeSSH(t, `printf '%s\n' '{"result":{"tabs":[`+
		`{"tab_id":"w1:t1","number":1},{"tab_id":"w1:t2","number":2}]}}'`)

	order, err := NewWithBin("bot", "agents", "herdr").TabOrder()
	if err != nil {
		t.Fatalf("tab order: %v", err)
	}
	if order["w1:t1"] != 1 || order["w1:t2"] != 2 {
		t.Errorf("the tab numbers did not come through: %v", order)
	}
}

func TestPingFailsWhenTheSessionIsNotAnswering(t *testing.T) {
	// Ping is a pane list that throws away the panes: reachable over ssh is
	// not the same as having a Herdr session that answers.
	fakeSSH(t, `printf '%s\n' '{"error":{"code":"no_session","message":"no session named agents"}}'
exit 1`)

	if err := NewWithBin("bot", "agents", "herdr").Ping(); err == nil {
		t.Error("a machine whose session is not answering pinged clean")
	}
}

func TestPingIsHappyWhenThePaneListComesBack(t *testing.T) {
	fakeSSH(t, `printf '%s\n' '{"result":{"panes":[]}}'`)

	if err := NewWithBin("bot", "agents", "herdr").Ping(); err != nil {
		t.Errorf("a machine that answered failed its ping: %v", err)
	}
}

func TestAConfiguredHerdrThatIsNotThere(t *testing.T) {
	// herdr_bin names a path on the machine, and a path can be wrong -- a
	// typo, an install that moved, a tilde that never got expanded. The
	// configured path skips the probe, so nothing else would notice.
	//
	// It has to come back as ErrNoHerdr: the machine is fine and its terminals
	// are usable over plain SSH, so the daemon falls back rather than refusing
	// to connect. The test above reaches this through a machine with no Herdr
	// at all, which fails earlier, in Bin.
	fakeSSH(t, remoteCommandIs+`
case "$last" in
  true) exit 0;;
  *--version*) exit 127;;
  *) exit 0;;
esac`)

	err := NewWithBin("bot", "", "/opt/herdr/bin/herdr").CheckHerdr()
	if !errors.Is(err, ErrNoHerdr) {
		t.Errorf("a configured herdr that does not run = %v, want ErrNoHerdr", err)
	}
	// And it says which path, because that is the thing to go and fix.
	if err != nil && !strings.Contains(err.Error(), "/opt/herdr/bin/herdr") {
		t.Errorf("the error does not name the path that did not run: %v", err)
	}
}

func TestAMachineThatCannotBeReachedIsNotAMachineWithoutHerdr(t *testing.T) {
	// The other way round, and the one that matters more. If an unreachable
	// machine came back as ErrNoHerdr the daemon would fall back to plain SSH
	// and report a machine that is quietly working -- when it is not there at
	// all, and every terminal opened on it would fail.
	fakeSSH(t, `echo 'ssh: connect to host bot port 22: Connection refused' >&2
exit 255`)

	err := NewWithBin("bot", "", "/usr/bin/herdr").CheckHerdr()
	if err == nil {
		t.Fatal("a machine that refused every connection checked out fine")
	}
	if errors.Is(err, ErrNoHerdr) {
		t.Errorf("an unreachable machine reads as one without Herdr: %v", err)
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("the error does not say the machine could not be reached: %v", err)
	}
}

func TestAConfiguredHerdrThatRunsIsAccepted(t *testing.T) {
	fakeSSH(t, `exit 0`)

	if err := NewWithBin("bot", "", "/usr/bin/herdr").CheckHerdr(); err != nil {
		t.Errorf("a machine whose herdr runs was refused: %v", err)
	}
}

func TestBinTakesOnlyTheFirstLineTheMachineSends(t *testing.T) {
	// The probe's answer is whatever the far machine printed, and a machine is
	// free to print more than one line: an rc file that writes to stdout, a
	// `command -v` that matches more than once. The first line is the path and
	// the rest is not.
	//
	// Nothing held that. Deleting the line that cuts at the newline left every
	// test in this package green, and the whole reply -- newline and all --
	// became the path this client would go on using for the rest of the
	// session, quoted into every remote command it built.
	//
	// The first line ends the way a machine's own shell might end it -- a
	// trailing space and a carriage return -- because taking the line and not
	// trimming it is a separate mistake that also passed.
	fakeSSH(t, remoteCommandIs+`
case "$last" in
  *command\ -v\ herdr*) printf '%s \r\n%s\n' /usr/local/bin/herdr "warning: something the shell said"; exit 0;;
  *) exit 0;;
esac`)

	client := New("bot", "")
	bin, err := client.Bin()
	if err != nil {
		t.Fatalf("Bin: %v", err)
	}
	if bin != "/usr/local/bin/herdr" {
		t.Errorf("Bin = %q, want only the first line the machine sent", bin)
	}
	if strings.ContainsAny(bin, "\n\r") {
		t.Errorf("the resolved path carries a line break: %q", bin)
	}
}
