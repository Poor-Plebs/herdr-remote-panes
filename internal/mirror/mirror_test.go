package mirror

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// theLogLimit is what maxLogBytes is expected to be, written out rather than
// read from it. The test above fills the log to the constant and appends,
// which rolls over for whatever the constant says: it proves the log is rolled
// over, and nothing about where. Where is the part that matters on disk --
// one generation is kept, so a laptop gives up twice this and no more.
//
// maxDaemonLog is the same size for the daemon's own log, and deliberately not
// shared with this one. What the two share is the logfile package that does
// the rolling; the sizes are two logs' worth of judgement about two different
// logs, and coincide today.
const theLogLimit = 256 * 1024

func TestTheFailureLogRollsOverAtTwoHundredAndFiftySixKilobytes(t *testing.T) {
	// Both sides of the bound, so the number cannot move in either direction
	// without this saying so: at the limit it rolls over, comfortably under it
	// is left alone. Filled from the written-out number rather than from
	// maxLogBytes, so raising the constant does not raise what the test writes.
	for _, tt := range []struct {
		what   string
		fill   int
		rolled bool
	}{
		{"exactly the limit", theLogLimit, true},
		{"a kilobyte under it", theLogLimit - 1024, false},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "mirror.log")
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), tt.fill), 0o600); err != nil {
			t.Fatal(err)
		}
		appendToLog(path, "one more failure")

		_, err := os.Stat(path + ".1")
		rolled := err == nil
		if rolled != tt.rolled {
			info, _ := os.Stat(path)
			t.Errorf("%s: filled to %d bytes and appended: rolled over = %v, want %v "+
				"(the log is now %d bytes)", tt.what, tt.fill, rolled, tt.rolled, info.Size())
		}
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

// theSaidLimit is what maxSaid is expected to be, written out rather than read
// from it. Standard error belongs to the far machine and it may write as much
// as it likes for as long as the session lasts, so how much of it is held here
// -- once per failed pane, in a daemon that stays open all day -- is the
// decision. maxSaidWidth next to it is one line of the record; this is the
// whole buffer behind it.
const theSaidLimit = 4 << 10

func TestOnlyFourKilobytesOfWhatAMachineSaidIsKept(t *testing.T) {
	// The test above asks whether what is kept is at most maxSaid, which is
	// true of any maxSaid at all -- a megabyte of a machine's standard error
	// per failed pane included. Asked as an equality against a number written
	// out, so the bound cannot move in either direction without this failing.
	said := &tail{max: maxSaid}
	_, _ = said.Write(bytes.Repeat([]byte("x"), theSaidLimit*2))
	if len(said.seen) != theSaidLimit {
		t.Errorf("a machine that said %d bytes left %d of them here, want %d",
			theSaidLimit*2, len(said.seen), theSaidLimit)
	}

	// The other side of it: what fits is kept whole rather than trimmed to
	// something shorter.
	fits := &tail{max: maxSaid}
	_, _ = fits.Write(bytes.Repeat([]byte("y"), theSaidLimit-1))
	if len(fits.seen) != theSaidLimit-1 {
		t.Errorf("a machine that said %d bytes left %d of them here, want all of it",
			theSaidLimit-1, len(fits.seen))
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

func TestTheSizeAMirrorOpensAt(t *testing.T) {
	// The remote stream is opened at whatever size is asked for, and the far
	// end wraps its output to that. Ask for the wrong one and every line in a
	// mirrored pane wraps somewhere other than where the pane ends, for as
	// long as the mirror lasts.
	//
	// stty is the only thing that knows, and a pane that cannot be measured is
	// an ordinary case rather than a failure -- so the defaults have to be
	// used then, and not otherwise.
	t.Run("what the pane says", func(t *testing.T) {
		withSttySaying(t, "40 100")
		if cols, rows := windowSize(); cols != 100 || rows != 40 {
			t.Errorf("the mirror opens at %dx%d, want 100x40", cols, rows)
		}
	})

	t.Run("a pane that cannot be measured", func(t *testing.T) {
		withStty(t, false)
		cols, rows := windowSize()
		if cols != defaultCols || rows != defaultRows {
			t.Errorf("an unmeasurable pane opens at %dx%d, want the defaults %dx%d",
				cols, rows, defaultCols, defaultRows)
		}
	})

	t.Run("a pane that answers with nonsense", func(t *testing.T) {
		// Answering is not the same as answering usefully, and the defaults
		// are right here for the same reason.
		withSttySaying(t, "not a size at all")
		if cols, rows := windowSize(); cols != defaultCols || rows != defaultRows {
			t.Errorf("a nonsense answer opened the mirror at %dx%d, want the defaults", cols, rows)
		}
	})
}

// withSttySaying puts an stty on PATH that answers with the line given.
func withSttySaying(t *testing.T, answer string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\necho %q\n", answer)
	if err := os.WriteFile(filepath.Join(dir, "stty"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAPaneIsNotLeftReportingMouseEvents(t *testing.T) {
	// A mirrored pane is handed to a program on another machine, and Herdr
	// keeps the pane after that program has gone. Anything it turned on is
	// still on: mouse reporting most visibly, because a terminal that reports
	// mouse events does not select text with them — every drag goes to an
	// application that is no longer there, and the pane cannot be copied from.
	//
	// stty does not reach these. It puts the line discipline back; these are
	// the emulator's own modes.
	var pane strings.Builder
	restorePane(&pane)
	said := pane.String()

	for _, mode := range []struct{ sequence, what string }{
		{"\x1b[?1000l", "mouse clicks"},
		{"\x1b[?1002l", "mouse drags"},
		{"\x1b[?1003l", "mouse movement"},
		{"\x1b[?1006l", "SGR mouse encoding"},
		{"\x1b[?2004l", "bracketed paste"},
		{"\x1b[?25h", "the cursor"},
		{"\x1b[0m", "colours and attributes"},
	} {
		if !strings.Contains(said, mode.sequence) {
			t.Errorf("a pane can be left with %s on: %q is not put back", mode.what, mode.sequence)
		}
	}

	// Nothing that moves what is on the screen. The last thing the far side
	// printed is what somebody is looking at, and often why the mirror ended.
	for _, mode := range []struct{ sequence, what string }{
		{"\x1b[2J", "clearing the screen"},
		{"\x1b[?1049l", "leaving the alternate screen"},
		{"\x1bc", "a full reset"},
	} {
		if strings.Contains(said, mode.sequence) {
			t.Errorf("putting a pane back should not take %s with it: %q", mode.what, mode.sequence)
		}
	}
}

func TestPuttingAPaneBackHappensOnEveryWayOut(t *testing.T) {
	// The three things a mirror can be -- attached, observing, a plain SSH
	// shell -- all hand the pane to another machine, and all of them end. The
	// restore sits in the one place they are chosen from, so a fourth would
	// get it too.
	body, err := os.ReadFile("mirror.go")
	if err != nil {
		t.Fatal(err)
	}
	bridgeAt := strings.Index(string(body), "func bridge() error {")
	if bridgeAt < 0 {
		t.Fatal("bridge has been renamed; this test is checking nothing")
	}
	rest := string(body)[bridgeAt:]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		t.Fatal("could not find the end of bridge")
	}
	if !strings.Contains(rest[:end], "defer restorePane(") {
		t.Error("bridge does not put the pane back, so whichever way a mirror " +
			"ends the modes the far side set are left on")
	}
}

func TestAReconnectDoesNotFinishTheLastStreamsSentence(t *testing.T) {
	// A connection that drops ends wherever it happened to be. If that is
	// inside an escape sequence the terminal waits for the rest, and the first
	// bytes of the next stream complete it: two halves of different sentences,
	// read as one instruction.
	//
	// CAN is the one character that means "that sequence is over" and means
	// nothing at all when there is no sequence, which is every other time this
	// runs.
	if cancelPending != "\x18" {
		t.Errorf("cancelPending is %q; CAN is 0x18", cancelPending)
	}
	// Not SUB: it means the same thing to the parser and some terminals draw a
	// character where it lands, which would put a mark in the pane on every
	// reconnect.
	if cancelPending == "\x1a" {
		t.Error("SUB is drawn by some terminals; CAN is not")
	}

	// And it happens where a stream is started, rather than where one is
	// known to have failed: a stream that ended cleanly can also have ended
	// mid-sequence, and the cost of sending it when nothing is pending is
	// nothing.
	body, err := os.ReadFile("mirror.go")
	if err != nil {
		t.Fatal(err)
	}
	observeAt := strings.Index(string(body), "func observe(")
	if observeAt < 0 {
		t.Fatal("observe has been renamed; this test is checking nothing")
	}
	rest := string(body)[observeAt:]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		t.Fatal("could not find the end of observe")
	}
	loop := rest[:end]
	if !strings.Contains(loop, "cancelPending") {
		t.Error("observe reconnects without abandoning what the last stream left half-written")
	}
	if strings.Index(loop, "cancelPending") > strings.Index(loop, "streamOnce(") {
		t.Error("the abandon happens after the stream it is meant to precede")
	}
}

// TestAMirrorThatCouldNotStartSaysSoWhereItCanBeRead holds the one decision in
// Run.
//
// Run is the mirror's entire entry point and had no coverage at all, so what a
// pane does when its bridge fails was settled by code no test ran.
// shouldReportFailure, which decides whether an exit is worth reporting, is
// held three separate ways in this package -- a table of errors, a case in
// bridge_test.go, and the deliberate-close case. None of them reaches the call
// it guards.
//
// The reporting is not a nicety. Herdr does not capture a pane process's
// stderr and closes the pane the moment its command exits, so a mirror that
// cannot start leaves nothing behind but an exit status nobody sees. The log
// written here is what the troubleshooting page tells people to read.
func TestAMirrorThatCouldNotStartSaysSoWhereItCanBeRead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	t.Setenv(EnvTarget, "bot")
	t.Setenv(EnvTerminal, "build")
	// Nothing on the path, so there is no ssh and the bridge fails at once
	// rather than waiting out a connection.
	t.Setenv("PATH", dir)

	// The pane is held open afterwards so somebody can read the message. A
	// test is not somebody, and five seconds of the suite is a real cost.
	saved := holdOpen
	holdOpen = time.Millisecond
	t.Cleanup(func() { holdOpen = saved })

	// The message also goes to the pane, which here is the test's own output.
	quiet, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer quiet.Close()
	savedOut := os.Stdout
	os.Stdout = quiet
	runErr := Run()
	os.Stdout = savedOut

	// The bridge really did fail, or what follows is about a mirror that
	// worked and had nothing to report.
	if runErr == nil {
		t.Fatal("a bridge with no ssh on the path should have failed")
	}

	raw, err := os.ReadFile(filepath.Join(dir, "mirror.log"))
	if err != nil {
		t.Fatalf("a mirror that could not start wrote nothing to mirror.log, "+
			"which is where somebody is sent to find out why: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "build") {
		t.Errorf("the entry does not say which terminal it was: %q", got)
	}
	if !strings.Contains(got, "not reachable") {
		t.Errorf("the entry does not say what went wrong: %q", got)
	}
}

// envStopWatch tells a child of the test below which half to run, and
// envStopSignal which signal it sends itself.
const (
	envStopWatch  = "HRP_TEST_STOP_WATCH"
	envStopSignal = "HRP_TEST_STOP_SIGNAL"
)

// stopSignals are the signals watchForStop registers, under the name a child
// is given one by. Each is a pane ending, and each is a separate argument to
// one call, which is why each needs asking about separately.
var stopSignals = map[string]syscall.Signal{
	"terminated": syscall.SIGTERM,
	"hangup":     syscall.SIGHUP,
	"interrupt":  syscall.SIGINT,
}

// TestAStopSignalEndsThisProcessAgainOnceTheWatchIsOver holds that watching
// for a stop is undone when the watch ends, and that each of the three signals
// the watch registers is really registered.
//
// signal.Notify takes SIGTERM away from the runtime: while a channel is
// registered the signal is delivered there instead of ending the process, and
// the cleanup has to hand it back. Without signal.Stop the registration
// outlives the goroutine that was reading it, so a termination signal goes to
// a channel nobody reads and the process ignores it entirely -- measured, not
// argued: a child that does the same thing without that line prints its way
// past a SIGTERM it was sent.
//
// The watch is per stream and not per process, which is what makes the gap
// matter. observe picks a dropped stream up again after one, two, three and
// four seconds, and stop() has already run in every one of those waits. A pane
// closed during a reconnect would then not close: the bridge would carry on
// through the whole retry sequence, and the ssh underneath it with it.
//
// THREE SIGNALS AND ONE STATEMENT. `signal.Notify(signals, syscall.SIGTERM,
// syscall.SIGHUP, syscall.SIGINT)` is a single line, so a deletion sweep only
// ever offers the whole of it -- and with SIGTERM held the line read as
// settled while two thirds of it was held by nothing. Measured against the
// whole tree rather than this package: dropping SIGHUP survived, and so did
// dropping SIGINT. What differs per signal is only whether it reaches the
// channel at all; everything past that is the one goroutine, and its
// forwarding to the ssh is held by TestClosingAPaneTakesTheSSHWithIt. So each
// row below asks the one question that is per-signal -- did this process take
// it, or did the runtime.
//
// It runs in a child because what passing looks like here is the process
// dying, and because for the rows below it is what FAILING looks like. A
// signal nothing has registered ends the test binary: measured, dropping
// SIGTERM takes the package down inside whichever test was running and reports
// "signal: terminated" against that one, naming no claim at all, while
// dropping SIGHUP or SIGINT here fails the row it belongs to and nothing else.
func TestAStopSignalEndsThisProcessAgainOnceTheWatchIsOver(t *testing.T) {
	if half := os.Getenv(envStopWatch); half != "" {
		sig, named := stopSignals[os.Getenv(envStopSignal)]
		if !named {
			fmt.Printf("no signal named %q\n", os.Getenv(envStopSignal))
			os.Exit(1)
		}
		switch half {
		case "after the watch":
			stop := watchForStop(nil)
			stop()
			_ = syscall.Kill(os.Getpid(), sig)
			time.Sleep(2 * time.Second)
			fmt.Println("still here")
			os.Exit(0)
		case "during the watch":
			stop := watchForStop(nil)
			defer stop()
			_ = syscall.Kill(os.Getpid(), sig)
			deadline := time.Now().Add(10 * time.Second)
			for !stopped.Load() {
				if time.Now().After(deadline) {
					fmt.Println("nothing recorded it")
					os.Exit(1)
				}
				time.Sleep(time.Millisecond)
			}
			fmt.Println("recorded")
			os.Exit(0)
		default:
			// Rather than falling through to the parent's own path, which
			// would have this child run the whole table again and start a
			// child of its own for every row of it.
			fmt.Printf("no half named %q\n", half)
			os.Exit(1)
		}
	}

	tests := []struct {
		name       string
		half       string
		signal     string
		wantKilled bool
		wantSaid   string
	}{
		{
			name:       "the signal ends the process once the watch has gone",
			half:       "after the watch",
			signal:     "terminated",
			wantKilled: true,
		},
		{
			// The control. Without it a build that registered nothing at all
			// would pass the case above, since a SIGTERM nobody has asked for
			// ends the process too -- so the case above says nothing on its
			// own about the watch ever having been in place.
			name:     "and it does not while the watch is up",
			half:     "during the watch",
			signal:   "terminated",
			wantSaid: "recorded",
		},
		{
			// A pane whose terminal goes away hangs this process up rather
			// than terminating it, and that is a pane closing just as much.
			name:     "nor does a hangup",
			half:     "during the watch",
			signal:   "hangup",
			wantSaid: "recorded",
		},
		{
			name:     "nor does an interrupt",
			half:     "during the watch",
			signal:   "interrupt",
			wantSaid: "recorded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			child := exec.Command(os.Args[0],
				"-test.run=^TestAStopSignalEndsThisProcessAgainOnceTheWatchIsOver$")
			child.Env = append(os.Environ(),
				envStopWatch+"="+tt.half, envStopSignal+"="+tt.signal)
			said, err := child.CombinedOutput()

			killed := false
			var died *exec.ExitError
			if errors.As(err, &died) {
				if status, ok := died.Sys().(syscall.WaitStatus); ok {
					killed = status.Signaled() && status.Signal() == stopSignals[tt.signal]
				}
			}
			if killed != tt.wantKilled {
				t.Fatalf("a child sent %s %s: ended by that signal = %v, want %v "+
					"(exit %v, said %q)", tt.signal, tt.half, killed, tt.wantKilled, err, said)
			}
			if tt.wantSaid != "" && !strings.Contains(string(said), tt.wantSaid) {
				t.Fatalf("a child sent %s %s said %q, want it to say %q (exit %v)",
					tt.signal, tt.half, said, tt.wantSaid, err)
			}
		})
	}
}

// goroutinesSettled waits for the goroutine count to stop moving and returns
// it.
//
// A goroutine ends when the runtime next schedules it, not when the channel it
// is waiting on closes, so a count read straight after a cleanup is read too
// early. Waiting for a few identical readings costs milliseconds and answers
// the same way whether the count came back down or stayed up.
func goroutinesSettled() int {
	deadline := time.Now().Add(5 * time.Second)
	last := runtime.NumGoroutine()
	same := 0
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
		n := runtime.NumGoroutine()
		if n != last {
			last, same = n, 0
			continue
		}
		same++
		if same == 5 {
			return n
		}
	}
	return runtime.NumGoroutine()
}

// TestTheWatchLetsGoOfItsGoroutineAndNotOnlyOfTheSignal holds that the cleanup
// ends the goroutine watchForStop started.
//
// signal.Stop beside it takes back the registration; close(done) is what tells
// the goroutine to stop waiting. Without it the goroutine blocks on that select
// for ever -- no signal can arrive, because the registration has just gone, and
// nothing else closes the channel -- and it holds the channel and the process
// handle with it. Measured at 200 cleanups: with the line the count comes back
// to where it started, without it 200 goroutines stay.
//
// The number of watches is not bounded by the retry limit, which is what makes
// this worth holding. A resize ends a stream with errResized, planObserveNext
// answers observeAgainNow and resets the attempt count to nothing, so observe
// starts another stream -- and another watch -- for every drag of a window
// edge, for as long as the pane is open.
func TestTheWatchLetsGoOfItsGoroutineAndNotOnlyOfTheSignal(t *testing.T) {
	// os/signal starts a receive loop of its own on the first Notify and never
	// ends it. That is one goroutine for the process, not one per watch, so it
	// belongs in the baseline rather than in the count under test.
	warm := watchForStop(nil)
	warm()
	base := goroutinesSettled()

	const watches = 50
	stops := make([]func(), 0, watches)
	for i := 0; i < watches; i++ {
		stops = append(stops, watchForStop(nil))
	}

	// The control: each watch really is a goroutine while it is up, so what
	// follows is about them ending rather than about their never having been
	// started. Without this a build whose watchForStop ran nothing at all
	// would pass the assertion below by having nothing to leak.
	if up := runtime.NumGoroutine(); up < base+watches {
		t.Fatalf("%d watches took the goroutine count from %d only to %d; "+
			"a watch that runs nothing cannot be leaking anything", watches, base, up)
	}

	for _, stop := range stops {
		stop()
	}

	// Two spare, for anything this test did not start; the leak being caught
	// is fifty.
	if after := goroutinesSettled(); after > base+2 {
		t.Errorf("%d cleanups left the goroutine count at %d, up from %d: "+
			"the watches were stopped but their goroutines are still waiting",
			watches, after, base)
	}
}
