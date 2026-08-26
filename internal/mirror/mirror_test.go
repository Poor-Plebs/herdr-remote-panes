package mirror

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestShouldReportFailure(t *testing.T) {
	boom := errors.New("ssh: connection closed")

	tests := []struct {
		name        string
		err         error
		askedToStop bool
		want        bool
	}{
		{
			// The connection dropped on its own. Without recording it, the
			// machine's space empties and nothing brings it back.
			name: "a bridge that dies on its own is a failure",
			err:  boom, askedToStop: false, want: true,
		},
		{
			// Closing a pane signals this process, and the bridge then exits
			// with an error too. Recording that would reopen the terminal
			// someone just closed.
			name: "an exit that was asked for is not a failure",
			err:  boom, askedToStop: true, want: false,
		},
		{
			name: "a clean exit is never a failure",
			err:  nil, askedToStop: false, want: false,
		},
		{
			name: "a clean exit after being asked to stop is not a failure",
			err:  nil, askedToStop: true, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldReportFailure(tt.err, tt.askedToStop); got != tt.want {
				t.Errorf("shouldReportFailure(%v, %v) = %v, want %v",
					tt.err, tt.askedToStop, got, tt.want)
			}
		})
	}
}

func TestDescribeCommand(t *testing.T) {
	// The ssh options are the same on every call -- control socket, keepalive,
	// timeouts -- and printing them buried the one part that differs, the
	// machine, behind a hundred and fifty characters of noise in a pane that is
	// about to close.
	argv := []string{
		"ssh", "-o", "ControlMaster=auto", "-o", "ControlPath=/tmp/hrp-abc.sock",
		"-o", "ControlPersist=120", "-o", "ServerAliveInterval=15",
		"-o", "ConnectTimeout=10", "-tt", "-o", "BatchMode=no",
		"--", "bot",
	}
	if got := describeCommand(argv); got != "ssh bot" {
		t.Errorf("describeCommand = %q, want %q", got, "ssh bot")
	}

	// Whatever is being run on the machine is worth keeping.
	withCommand := append(append([]string{}, argv...), "herdr pane list")
	if got := describeCommand(withCommand); got != "ssh bot herdr pane list" {
		t.Errorf("describeCommand = %q, want the remote command kept", got)
	}

	// Anything not built that way is left as it is rather than mangled.
	plain := []string{"stty", "size"}
	if got := describeCommand(plain); got != "stty size" {
		t.Errorf("describeCommand = %q, want it unchanged", got)
	}
	if got := describeCommand(nil); got != "" {
		t.Errorf("describeCommand(nil) = %q", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	// A plain SSH pane has neither a name nor a remote terminal, and the
	// message read as "[herdr-remote-panes] : exit status 255" -- a colon
	// introducing nothing. The machine identifies the pane in that case.
	if got := firstNonEmpty("", "", "bot"); got != "bot" {
		t.Errorf("firstNonEmpty = %q, want the machine", got)
	}
	if got := firstNonEmpty("shell@bot", "term_1", "bot"); got != "shell@bot" {
		t.Errorf("firstNonEmpty = %q, want the name", got)
	}
	if got := firstNonEmpty("", "", ""); got != "" {
		t.Errorf("firstNonEmpty = %q, want nothing", got)
	}
}

func TestFailureLogIsRolledOverRatherThanGrowingForever(t *testing.T) {
	// Every failed pane appended to this and nothing ever shortened it, so it
	// grew for as long as the plugin was installed -- slowly, but with no end
	// to it, in a directory nobody thinks to look in.
	dir := t.TempDir()
	path := filepath.Join(dir, "mirror.log")

	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxLogBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	appendToLog(path, "the newest failure")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= maxLogBytes {
		t.Errorf("the log is still %d bytes; it was not rolled over", info.Size())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "the newest failure") {
		t.Error("the message that triggered the rollover was lost")
	}

	// One generation is kept: the failure that began a run of them is worth
	// not losing outright.
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("the previous log was not kept: %v", err)
	}

	// A second rollover replaces that generation rather than accumulating.
	if err := os.WriteFile(path, bytes.Repeat([]byte("y"), maxLogBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	appendToLog(path, "later still")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want the log and one generation", names)
	}
}

func TestFailureLogIsCreatedPrivate(t *testing.T) {
	// It records which machines were being reached and why it failed.
	dir := t.TempDir()
	path := filepath.Join(dir, "mirror.log")
	appendToLog(path, "could not reach bot")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions are %o, want 600", perm)
	}
}

func TestEveryModeSupervisesItsChild(t *testing.T) {
	// A pane closes when somebody shuts it and when its link drops, and those
	// have to be told apart: one should stay shut, the other should come back.
	// That is what the stop signal records, and it is also what forwards the
	// signal on to the ssh underneath. Two of the three modes did it; observe
	// did not, so a stop killed it outright and left its ssh to work out on its
	// own that nobody was listening.
	source, err := os.ReadFile("mirror.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)

	for _, mode := range []string{"func shell(", "func attach(", "func streamOnce("} {
		start := strings.Index(body, mode)
		if start < 0 {
			t.Fatalf("%s is gone; this test needs rewriting", mode)
		}
		end := strings.Index(body[start:], "\n}\n")
		if end < 0 {
			t.Fatalf("could not find the end of %s", mode)
		}
		if !strings.Contains(body[start:start+end], "watchForStop") {
			t.Errorf("%s does not supervise its child", mode)
		}
	}
}

func TestTakeoverIsOnUnlessTurnedOff(t *testing.T) {
	// Attaching to a terminal is exclusive, and a pane that closed without
	// tidying up leaves the claim behind. Taking it over is how a mirror
	// recovers from that, so it is on by default; the setting exists for
	// somebody who would rather two ends never fight over one terminal.
	t.Setenv(EnvTakeover, "")
	if !takeoverEnabled() {
		t.Error("takeover should be on when nothing says otherwise")
	}

	t.Setenv(EnvTakeover, "true")
	if !takeoverEnabled() {
		t.Error("takeover should be on when asked for")
	}

	t.Setenv(EnvTakeover, "false")
	if takeoverEnabled() {
		t.Error("takeover should be off when turned off")
	}

	// Only the exact word turns it off. Anything else means somebody wrote
	// something unexpected, and the default is the recovering one.
	for _, odd := range []string{"False", "FALSE", "0", "no", "off", " false"} {
		t.Setenv(EnvTakeover, odd)
		if !takeoverEnabled() {
			t.Errorf("%q turned takeover off; only \"false\" should", odd)
		}
	}
}

func TestDescribeCommandKeepsTheMachineWhenTheRemoteCommandHasASeparator(t *testing.T) {
	// ssh's own "--" is the one before the destination. Taking the last one
	// instead dropped the destination and everything up to whatever separator
	// the remote command happened to use -- and the destination is the one
	// thing this function exists to show.
	argv := []string{
		"ssh", "-o", "ControlMaster=auto", "-o", "ControlPersist=60", "-tt",
		"--", "bot",
		"sh", "-c", "runner", "--", "inner-argument",
	}
	got := describeCommand(argv)
	if !strings.Contains(got, "bot") {
		t.Errorf("describeCommand = %q, want the machine in it", got)
	}
	if strings.Contains(got, "ControlMaster") {
		t.Errorf("describeCommand = %q, want the ssh options left out", got)
	}
	if !strings.Contains(got, "inner-argument") {
		t.Errorf("describeCommand = %q, want what is run on the machine kept", got)
	}
}

func TestClosingAPaneIsRememberedAsDeliberate(t *testing.T) {
	// Closing a pane signals this process, which makes the bridge exit with an
	// error too, so the error alone cannot tell a deliberate close from a
	// dropped link. What tells them apart is this flag being set on the way
	// out -- and the decision that reads it has been tested all along while
	// nothing tested that anything ever sets it. Getting this backwards means
	// either reopening a terminal somebody just closed, or never recovering
	// from a link that dropped.
	restore := stopped.Load()
	defer stopped.Store(restore)
	stopped.Store(false)

	stop := watchForStop(nil)
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signalling this process: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for !stopped.Load() {
		if time.Now().After(deadline) {
			t.Fatal("a termination signal did not record that the exit was asked for")
		}
		time.Sleep(time.Millisecond)
	}

	// And with it set, the exit is not reported as a failure, so the daemon
	// leaves the terminal closed.
	if shouldReportFailure(errors.New("signal: terminated"), stopped.Load()) {
		t.Error("a pane closed by hand was recorded as a dropped connection")
	}
}

func TestClosingAPaneTakesTheSSHWithIt(t *testing.T) {
	// The bridge is the parent of an ssh that holds a connection open. Leaving
	// it running when the pane closes leaks a connection per pane closed, and
	// on the machine a session nobody can see.
	restore := stopped.Load()
	defer stopped.Store(restore)
	stopped.Store(false)

	child := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	stop := watchForStop(child.Process)
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signalling this process: %v", err)
	}

	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()

	select {
	case err := <-waited:
		if err == nil {
			t.Error("the child exited cleanly; it was not signalled")
		}
	case <-time.After(10 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("the ssh was left running after the pane was closed")
	}
}

func TestTakeoverEvictsAStaleAttachUnlessTurnedOff(t *testing.T) {
	// A killed mirror pane can leave `herdr terminal attach` running on the
	// machine, and every later attempt to mirror that terminal then fails with
	// "already has an attached client". Taking over evicts it.
	//
	// The default is on, and the default matters more than usual here: off
	// means a terminal that stops being mirrorable until somebody logs into the
	// machine and finds the stale client themselves.
	t.Run("on unless said otherwise", func(t *testing.T) {
		t.Setenv(EnvTakeover, "")
		if !takeoverEnabled() {
			t.Error("takeover is off with nothing set; a stale attach would block the terminal")
		}
	})

	t.Run("on when set to anything else", func(t *testing.T) {
		// Only the exact word turns it off, so a setting written as "0" or
		// "no" does not quietly disable it.
		for _, value := range []string{"true", "yes", "1", "0", "no"} {
			t.Setenv(EnvTakeover, value)
			if !takeoverEnabled() {
				t.Errorf("takeover is off for %q, which is not how it is turned off", value)
			}
		}
	})

	t.Run("off when asked", func(t *testing.T) {
		t.Setenv(EnvTakeover, "false")
		if takeoverEnabled() {
			t.Error("takeover is still on after being turned off")
		}
	})
}

func TestAFailedTerminalRecordsWhatSSHSaid(t *testing.T) {
	// mirror.log is what the README points at for why a terminal would not
	// open, and it used to hold "exit status 255 running: ssh prod" — which is
	// the number for "ssh could not connect" and not a reason. ssh writes the
	// reason to standard error, which is the pane: somebody watching saw it and
	// somebody reading afterwards did not.
	said := &tail{max: maxSaid}
	// What ssh actually writes for the commonest of these: sixty characters of
	// warning, a paragraph, and the sentence that names the problem last.
	_, _ = said.Write([]byte(strings.Repeat("@", 60) + "\n" +
		"WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\n" +
		strings.Repeat("@", 60) + "\n" +
		"Someone could be eavesdropping on you right now.\n" +
		"Host key verification failed.\n"))

	err := failed(errors.New("exit status 255"), []string{"ssh", "prod"}, said)

	if !strings.Contains(err.Error(), "Host key verification failed") {
		t.Errorf("the record does not say why: %q", err)
	}
	// The last line rather than the first: the first is a row of "@".
	if strings.Contains(err.Error(), "@@@@") {
		t.Errorf("the record kept the warning banner instead of the reason: %q", err)
	}
	// And still says what it was doing.
	if !strings.Contains(err.Error(), "ssh prod") || !strings.Contains(err.Error(), "255") {
		t.Errorf("the record lost the command or the status: %q", err)
	}
}

func TestACommandThatSaidNothingIsRecordedAnyway(t *testing.T) {
	// Nothing on standard error is ordinary — a connection that simply drops
	// says nothing at all — and the line has to read properly without it
	// rather than trailing a colon.
	said := &tail{max: maxSaid}
	err := failed(errors.New("exit status 255"), []string{"ssh", "bot"}, said)

	if got := err.Error(); !strings.HasSuffix(got, "ssh bot") {
		t.Errorf("with nothing said the record reads %q", got)
	}
}

func TestWhatAMachineSaysCannotRunAwayWithTheLog(t *testing.T) {
	// Standard error belongs to the far machine, and it can write as much of
	// it as it likes for as long as the session lasts. Only the end of it is
	// kept, and only one line of that goes in the record.
	said := &tail{max: maxSaid}
	for i := 0; i < 200; i++ {
		_, _ = said.Write([]byte(strings.Repeat("x", 500) + "\n"))
	}
	_, _ = said.Write([]byte("the last thing it said\n"))

	if len(said.seen) > maxSaid {
		t.Errorf("kept %d bytes of what the machine said, want at most %d", len(said.seen), maxSaid)
	}
	line := said.lastLine()
	if line != "the last thing it said" {
		t.Errorf("the last line is %q", line)
	}
	// And a single enormous line is cut rather than written whole.
	long := &tail{max: maxSaid}
	_, _ = long.Write([]byte(strings.Repeat("y", 2000)))
	if got := len([]rune(long.lastLine())); got > maxSaidWidth {
		t.Errorf("one line of %d characters went in the record", got)
	}
}

func TestWhatAMachineSaysCannotDrawOnTheTerminal(t *testing.T) {
	// It is written to a file somebody will read in a terminal, and it is
	// whatever the far side chose to print.
	said := &tail{max: maxSaid}
	_, _ = said.Write([]byte("\x1b[2J\x1b[Hcleared your screen\n"))

	if line := said.lastLine(); strings.Contains(line, "\x1b") {
		t.Errorf("an escape sequence went into the record: %q", line)
	}
}

func TestTheReasonReachesTheRecordFromARealFailure(t *testing.T) {
	// The tests above are about the wording. This is about the wiring: ssh's
	// standard error is the pane, and keeping a copy of it is the whole of the
	// change. If that copy is not actually being taken, every one of them
	// still passes.
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\n" +
		"echo 'Host key verification failed.' >&2\n" +
		"exit 255\n"
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := shell(remote.NewWithBin("prod", "", ""))
	if err == nil {
		t.Fatal("an ssh that exited 255 was treated as a session that ended")
	}
	if !strings.Contains(err.Error(), "Host key verification failed") {
		t.Errorf("what ssh said did not reach the record: %q", err)
	}
}

// withStty puts an stty on PATH that records how it was called and succeeds or
// fails as asked, and hands back what has been asked of it.
func withStty(t *testing.T, works bool) func() []string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "asked")
	status := 0
	if !works {
		status = 1
	}
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\nexit %d\n", log, status)
	if err := os.WriteFile(filepath.Join(dir, "stty"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() []string {
		raw, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
}

func TestAMirroredPaneGetsItsTerminalBack(t *testing.T) {
	// A read-only mirror stops the local pty echoing what you type, since what
	// you type is not going anywhere. That has to be undone when the mirror
	// ends: left raw, the pane echoes nothing and reads nothing line by line,
	// and what is left behind in it is a shell that appears to have stopped
	// working.
	asked := withStty(t, true)

	restore := rawMode()
	if got := asked(); len(got) != 1 || !strings.Contains(got[0], "raw") {
		t.Errorf("stty was asked %v, want raw mode once", got)
	}

	restore()
	if got := asked(); len(got) != 2 || !strings.Contains(got[1], "sane") {
		t.Errorf("stty was asked %v, want the pane put back", got)
	}
}

func TestAPaneThatWillNotGoRawIsLeftAsItIs(t *testing.T) {
	// Nothing was changed, so there is nothing to put back -- and asking a
	// terminal that refused raw mode to go "sane" would change settings this
	// never touched.
	asked := withStty(t, false)

	rawMode()()

	for _, line := range asked() {
		if strings.Contains(line, "sane") {
			t.Errorf("stty was asked to put back a pane it never changed: %v", asked())
		}
	}
}
