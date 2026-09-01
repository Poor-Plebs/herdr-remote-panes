package mirror

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
)

// bridge is the pane's entrypoint: it reads what the daemon told it through the
// environment and decides which of the three things to be. Getting that wrong
// is not a crash but a pane doing the wrong thing quietly -- a plain SSH
// session where a mirror was meant, or a mirror of nothing.

// recordingSSH puts an ssh on PATH that writes down how it was called and
// exits, so a test can see which of the three was run and with what.
func recordingSSH(t *testing.T) fakeSSH {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	state := t.TempDir()
	// Whether the pane reads as live at the moment the bridge is running,
	// which is the only moment it can be asked: the mark is put down when the
	// bridge starts and taken up when it returns.
	liveDuring := filepath.Join(dir, "was-live")
	script := "#!/bin/sh\n" +
		"last=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in\n" +
		"  *command\\ -v\\ herdr*) echo /usr/bin/herdr; exit 0;;\n" +
		"esac\n" +
		"[ -f " + filepath.Join(state, "panes", "hub", "w1-p2.pid") + " ] && echo yes > " + liveDuring + "\n" +
		"echo \"$last\" >> " + log + "\n" +
		"echo \" $* \" >> " + log + ".argv\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)
	t.Setenv("HERDR_SESSION", "hub")
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	read := func(path string) string {
		raw, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return string(raw)
	}
	return fakeSSH{
		ranOnMachine: func() string { return read(log) },
		wasLiveDuring: func() bool {
			_, err := os.Stat(liveDuring)
			return err == nil
		},
		argv: func() string {
			t.Helper()
			lines := strings.Split(strings.TrimSpace(read(log+".argv")), "\n")
			if lines[0] == "" {
				t.Fatal("ssh was never called")
			}
			return lines[len(lines)-1]
		},
	}
}

// fakeSSH is the three ways a test can ask what the pane did: what it ran on
// the machine, whether the pane read as live while it ran, and the whole argv
// for the parts that are ssh's own flags.
type fakeSSH struct {
	ranOnMachine  func() string
	wasLiveDuring func() bool
	argv          func() string
}

func TestThePaneBecomesWhatTheDaemonToldItToBe(t *testing.T) {
	for _, tt := range []struct {
		mode     string
		terminal string
		wants    string
		what     string
	}{
		{"ssh", "", "", "a login shell, with nothing asked of the far side"},
		{"attach", "term_7", "terminal attach term_7", "a live attach to one terminal"},
		{"observe", "term_7", "terminal session observe term_7", "a read-only stream of it"},
	} {
		t.Run(tt.mode+": "+tt.what, func(t *testing.T) {
			ssh := recordingSSH(t)
			t.Setenv(EnvTarget, "bot")
			t.Setenv(EnvMode, tt.mode)
			t.Setenv(EnvTerminal, tt.terminal)

			if err := bridge(); err != nil {
				t.Fatalf("bridge: %v", err)
			}

			got := ssh.ranOnMachine()
			if tt.wants == "" {
				// A plain SSH pane runs no command on the machine at all,
				// which is what lets it work against a machine with no Herdr.
				if strings.Contains(got, "terminal") {
					t.Errorf("the pane ran %q on the machine; a login shell runs nothing", got)
				}
				return
			}
			if !strings.Contains(got, tt.wants) {
				t.Errorf("the pane ran %q, want it to have run %q", got, tt.wants)
			}
		})
	}
}

func TestAPaneToldNothingUsefulSaysSo(t *testing.T) {
	// These come from the daemon, so a pane missing one of them means a bug
	// here rather than anything a user did -- and a pane that exits without
	// saying why is a pane nobody can debug.
	//
	// It is also five seconds of a terminal somebody is looking at, which is
	// how this was noticed: a real session has two of these in its log, and
	// what they said was the name of an environment variable. So both halves
	// are held here. The name, for whoever comes to debug it; and what
	// happened and what to do instead, for whoever is looking at the pane and
	// has never heard of HRP_TARGET.
	t.Run("no machine", func(t *testing.T) {
		recordingSSH(t)
		t.Setenv(EnvTarget, "")
		t.Setenv(EnvMode, "ssh")

		err := bridge()
		if err == nil {
			t.Fatal("a pane with no machine to connect to ran anyway")
		}
		if !strings.Contains(err.Error(), EnvTarget) {
			t.Errorf("the error is %q, which does not name what was missing", err)
		}
		if !strings.Contains(err.Error(), "from the menu") {
			t.Errorf("the error is %q, which tells somebody watching the pane "+
				"what is wrong and not what to do about it", err)
		}
	})

	t.Run("no terminal to mirror", func(t *testing.T) {
		// Only mirroring needs one: a plain SSH pane has no remote terminal.
		recordingSSH(t)
		t.Setenv(EnvTarget, "bot")
		t.Setenv(EnvMode, "attach")
		t.Setenv(EnvTerminal, "")

		err := bridge()
		if err == nil {
			t.Fatal("a mirror with no terminal to mirror ran anyway")
		}
		if !strings.Contains(err.Error(), EnvTerminal) {
			t.Errorf("the error is %q, which does not name what was missing", err)
		}
		if !strings.Contains(err.Error(), "from the menu") {
			t.Errorf("the error is %q, which tells somebody watching the pane "+
				"what is wrong and not what to do about it", err)
		}
	})
}

func TestABridgeSaysItIsRunningWhileItRuns(t *testing.T) {
	// The daemon tells a live mirror from a pane Herdr restored with nothing
	// behind it by this mark, and that decides whether the pane is left alone
	// or replaced.
	ssh := recordingSSH(t)
	t.Setenv(EnvTarget, "bot")
	t.Setenv(EnvMode, "ssh")

	if IsLive("w1:p2") {
		t.Fatal("the pane reads as live before anything has run")
	}
	if err := bridge(); err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if !ssh.wasLiveDuring() {
		t.Error("the pane did not read as live while its bridge was running, " +
			"so the daemon would take it for a pane with nothing behind it and replace it")
	}

	// And stops saying so once it is over, or a pane that has gone would keep
	// looking like somebody's session.
	if IsLive("w1:p2") {
		t.Error("the pane still reads as live after the bridge returned")
	}
}

func TestLeavingASessionClosesThePaneWhateverTheShellSaid(t *testing.T) {
	// ssh reports its own failures as 255 and passes through anything else, so
	// a different status is the session on the machine ending rather than the
	// connection to it failing.
	//
	// It matters because `exit` with no argument returns the last command's
	// status. Run something that fails, type exit, and the session ends with
	// that status -- which was read as a dropped connection, so the pane came
	// back a moment after you had finished with it. Ctrl-C does the same.
	for _, tt := range []struct {
		code   string
		reopen bool
		what   string
	}{
		{"0", false, "a session ended cleanly"},
		{"1", false, "a session ended after something failed"},
		{"130", false, "a session ended with ctrl-C"},
		{"255", true, "ssh could not connect, or the connection broke"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			exitingSSH(t, tt.code)
			t.Setenv(EnvTarget, "bot")
			t.Setenv(EnvMode, "ssh")
			stopped.Store(false)

			err := bridge()
			if got := shouldReportFailure(err, stopped.Load()); got != tt.reopen {
				verb := "left alone"
				if tt.reopen {
					verb = "opened again"
				}
				t.Errorf("ssh exiting %s: the pane is %v, want it %s",
					tt.code, map[bool]string{true: "opened again", false: "left alone"}[got], verb)
			}
		})
	}
}

// exitingSSH puts an ssh on PATH that answers the probe and then exits with a
// given status, as a session ending does.
func exitingSSH(t *testing.T, code string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nlast=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in *command\\ -v\\ herdr*) echo /usr/bin/herdr; exit 0;; esac\n" +
		"exit " + code + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")
	t.Setenv("HERDR_PANE_ID", "w1:p2")
}

func TestOnlyTheModesThatNeedATerminalAskForOne(t *testing.T) {
	// A pane that lets you type needs a pty on the far side, and one that only
	// watches must not take one -- an observe holding a pty would be claiming
	// a terminal it never uses. Losing either is invisible from here and
	// obvious to whoever is sitting in the pane: keystrokes that go nowhere.
	//
	// It is also what decides whether ssh may ask for anything. A pane can
	// answer a passphrase prompt; a poll cannot, and one that waits for an
	// answer nobody can give hangs the machine's turn.
	for _, tt := range []struct {
		mode    string
		wantTTY bool
		what    string
	}{
		{"ssh", true, "a login shell is typed into"},
		{"attach", true, "an attached terminal is typed into"},
		{"observe", false, "a read-only stream is not"},
	} {
		t.Run(tt.mode+": "+tt.what, func(t *testing.T) {
			ssh := recordingSSH(t)
			t.Setenv(EnvTarget, "bot")
			t.Setenv(EnvMode, tt.mode)
			t.Setenv(EnvTerminal, "term_1")

			if err := bridge(); err != nil {
				t.Fatalf("bridge: %v", err)
			}
			// The whole argv, since the flag is on ssh rather than in the
			// command it runs.
			argv := ssh.argv()
			gotTTY := strings.Contains(argv, " -tt ")
			if gotTTY != tt.wantTTY {
				t.Errorf("%s mode ran ssh %s a terminal:\n  %s",
					tt.mode, map[bool]string{true: "with", false: "without"}[gotTTY], argv)
			}
			// And the two go together: asking for a terminal means being able
			// to answer a prompt on it.
			wantBatch := "BatchMode=yes"
			if tt.wantTTY {
				wantBatch = "BatchMode=no"
			}
			if !strings.Contains(argv, wantBatch) {
				t.Errorf("%s mode ran ssh without %s:\n  %s", tt.mode, wantBatch, argv)
			}
		})
	}
}

func TestAPaneThatFailedLeavesATraceInThreePlaces(t *testing.T) {
	// A pane about to close is the worst place to put the only copy of
	// something, so this leaves it in three: on the pane, where somebody is
	// looking; in the log, where they can find it afterwards; and in the record
	// the daemon reads, which decides whether the terminal is opened again.
	restore := holdOpen
	holdOpen = time.Millisecond
	defer func() { holdOpen = restore }()

	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)
	t.Setenv("HERDR_SESSION", "hub")
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	t.Setenv(EnvTarget, "bot")
	t.Setenv(EnvName, "shell@bot")
	t.Setenv(EnvTerminal, "")

	drawn := captureStdout(t, func() {
		reportFailure(errors.New("exit status 255 running: ssh bot"))
	})

	if !strings.Contains(drawn, "shell@bot") || !strings.Contains(drawn, "exit status 255") {
		t.Errorf("the pane shows %q, want it to name the terminal and say what happened", drawn)
	}

	log, err := os.ReadFile(filepath.Join(state, "mirror.log"))
	if err != nil {
		t.Fatalf("nothing was written where somebody could find it later: %v", err)
	}
	if !strings.Contains(string(log), "shell@bot") {
		t.Errorf("the log says %q, without naming the terminal", log)
	}

	if !Failed("w1:p2") {
		t.Error("nothing recorded that the pane failed, so the daemon reads it as a tab somebody shut")
	}
	if got := FailureReason("w1:p2"); !strings.Contains(got, "exit status 255") {
		t.Errorf("the record says %q, without why", got)
	}
}

func TestAPlainSSHPaneIsNamedByItsMachine(t *testing.T) {
	// A plain SSH pane has neither a name nor a remote terminal, and this read
	// as "[herdr-remote-panes] : exit status 255" -- a colon introducing
	// nothing. The machine is what identifies it in that case.
	restore := holdOpen
	holdOpen = time.Millisecond
	defer func() { holdOpen = restore }()

	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	t.Setenv(EnvTarget, "bot")
	t.Setenv(EnvName, "")
	t.Setenv(EnvTerminal, "")

	drawn := captureStdout(t, func() {
		reportFailure(errors.New("exit status 255"))
	})

	if !strings.Contains(drawn, "bot") {
		t.Errorf("the pane shows %q, which does not say which machine went", drawn)
	}
	if strings.Contains(drawn, "] :") {
		t.Errorf("the pane shows %q, with a colon introducing nothing", drawn)
	}
}

// captureStdout returns what something wrote to the pane.
func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = saved }()

	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(read)
		done <- string(out)
	}()
	run()
	write.Close()
	return <-done
}

func TestWhatAMachineSaysCannotRepaintThePaneOrTheLog(t *testing.T) {
	// The error a failure carries holds the far side's standard error as it
	// was written -- runCommand puts it there verbatim -- and a machine's
	// banner is that machine's to choose. This wrote it to the pane and to
	// mirror.log untouched, and the troubleshooting page tells people to cat
	// that file, so the escapes would run twice: once where the pane is, and
	// again in whatever terminal went looking for why.
	restore := holdOpen
	holdOpen = time.Millisecond
	defer func() { holdOpen = restore }()

	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)
	t.Setenv("HERDR_SESSION", "hub")
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	t.Setenv(EnvTarget, "bot")
	t.Setenv(EnvName, "shell@bot")
	t.Setenv(EnvTerminal, "")

	banner := errors.New("exit status 255: \x1b[2J\x1b[H\x1b]0;renamed\x07\nsecond line")
	drawn := captureStdout(t, func() { reportFailure(banner) })

	logged, err := os.ReadFile(filepath.Join(state, "mirror.log"))
	if err != nil {
		t.Fatal(err)
	}
	for what, got := range map[string]string{"the pane": drawn, "the log": string(logged)} {
		if strings.ContainsAny(got, "\x1b\x07") {
			t.Errorf("%s takes an escape from the machine: %s", what, strconv.Quote(got))
		}
		// The words either side of a newline must not be run together. Dropping
		// the newline outright is what a plain sanitise does, and it turns two
		// readable lines into one unreadable one.
		if !strings.Contains(got, "renamed second line") {
			t.Errorf("%s ran the lines together: %s", what, strconv.Quote(got))
		}
	}

	// One failure is one entry: the log has the time at the front of each, so
	// a message that kept its newlines put two lines in it with no time on the
	// second, which reads as an entry from an unknown moment.
	if n := strings.Count(strings.TrimRight(string(logged), "\n"), "\n"); n != 0 {
		t.Errorf("one failure wrote %d extra lines into the log: %s", n, strconv.Quote(string(logged)))
	}

	// And what a machine can spend: a command's output is read up to
	// capped.Max, which is eight megabytes, and all of it used to arrive on
	// one line of a pane and a file that is kept.
	flood := errors.New(strings.Repeat("bot said something. ", 4000))
	captureStdout(t, func() { reportFailure(flood) })
	after, err := os.ReadFile(filepath.Join(state, "mirror.log"))
	if err != nil {
		t.Fatal(err)
	}
	last := strings.Split(strings.TrimRight(string(after), "\n"), "\n")
	if grew := len(last[len(last)-1]); grew > 400 {
		t.Errorf("a machine wrote %d characters into one log entry", grew)
	}
}

// failingSSH puts an ssh on PATH that writes a reason to standard error and
// exits the way ssh does when it could not connect.
func failingSSH(t *testing.T, reason string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"last=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in\n" +
		"  *command\\ -v\\ herdr*) echo /usr/bin/herdr; exit 0;;\n" +
		"esac\n" +
		"echo \"" + reason + "\" >&2\n" +
		"exit 255\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAMirrorThatCouldNotConnectRecordsWhy(t *testing.T) {
	// ssh writes why it could not connect to standard error, and for a pane
	// that is the pane -- so somebody watching sees "Host key verification
	// failed." and the file the troubleshooting page tells them to read held
	// "exit status 255", which is the number for "ssh could not connect" and
	// not a reason at all.
	//
	// shell was given the fix. attach was not, and attach is what a mirrored
	// machine uses: one real mirror.log has a hundred and forty-one of those
	// and not one reason among them.
	failingSSH(t, "Host key verification failed.")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	client := remote.NewWithBin("bot", "hub", "/usr/bin/herdr")
	err := attach(client, "term_1")
	if err == nil {
		t.Fatal("attaching to a machine that refused the connection reported no error")
	}
	if !strings.Contains(err.Error(), "Host key verification failed") {
		t.Errorf("the failure reads %q, and ssh said why on standard error", err)
	}
	// And the command is still named, which is what says which machine and
	// which terminal it was.
	if !strings.Contains(err.Error(), "ssh") {
		t.Errorf("the failure does not say what was run: %q", err)
	}
}

func TestAnObservedTerminalThatCouldNotConnectRecordsWhy(t *testing.T) {
	// The third mode. shell kept ssh's reason, attach was given it, and this
	// one returned the bare exit status with neither the reason nor the
	// command in it -- for the mode whose whole job is showing what a machine
	// is doing.
	failingSSH(t, "Permission denied (publickey).")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	client := remote.NewWithBin("bot", "hub", "/usr/bin/herdr")
	err := streamOnce(client, "term_1", 80, 24, make(chan os.Signal))
	if err == nil {
		t.Fatal("observing a machine that refused the connection reported no error")
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("the failure reads %q, and ssh said why on standard error", err)
	}
	if !strings.Contains(err.Error(), "terminal session observe") {
		t.Errorf("the failure does not say what was run: %q", err)
	}

	// And it is still an ssh failure as far as the retry decides: wrapping it
	// must not turn a dropped connection into something that stops trying.
	if got := exitStatus(err); got != 255 {
		t.Errorf("the wrapped failure reports exit status %d, and ssh exited 255", got)
	}
	if next, _ := planObserveNext(err, 0); next != observeAgainSoon {
		t.Errorf("a refused connection now plans %v rather than trying again", next)
	}
}

// refusingHerdr puts an ssh on PATH that connects fine and lets the far side
// refuse: the message comes back the way a pty carries it, on standard output.
func refusingHerdr(t *testing.T, reason string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"last=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in\n" +
		"  *command\\ -v\\ herdr*) echo /usr/bin/herdr; exit 0;;\n" +
		"esac\n" +
		"echo \"" + reason + "\"\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAMirrorTheFarSideRefusedRecordsWhatItSaid(t *testing.T) {
	// ssh connected; the Herdr on the machine refused. That message comes back
	// through the pty, which is standard output here, so standard error is
	// empty and the record said "exit status 1" -- sixty-six times in one real
	// mirror.log, for a machine that was answering perfectly well.
	refusingHerdr(t, "no terminal with id term_1")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	client := remote.NewWithBin("bot", "hub", "/usr/bin/herdr")
	err := attach(client, "term_1")
	if err == nil {
		t.Fatal("the far side refused and the pane reported no error")
	}
	if !strings.Contains(err.Error(), "no terminal with id term_1") {
		t.Errorf("the failure reads %q, and the machine said why", err)
	}
}

// noisySSH puts an ssh on PATH that writes to both places and fails the way
// ssh does: something on the screen, and its own reason on standard error.
func noisySSH(t *testing.T, onScreen, onStderr string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"last=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in\n" +
		"  *command\\ -v\\ herdr*) echo /usr/bin/herdr; exit 0;;\n" +
		"esac\n" +
		"echo \"" + onScreen + "\"\n" +
		"echo \"" + onStderr + "\" >&2\n" +
		"exit 255\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAMirrorWithBothPlacesFilledSaysTheReason(t *testing.T) {
	// The same as the unit below, through the pane rather than through the
	// helper: a connection that drops under a working terminal leaves the
	// screen's last line in one tail and ssh's complaint in the other, and
	// which one the record gets is decided at the call rather than in failed.
	noisySSH(t, "user@bot:~$ ls -la", "Host key verification failed.")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	client := remote.NewWithBin("bot", "hub", "/usr/bin/herdr")
	err := attach(client, "term_1")
	if err == nil {
		t.Fatal("the connection failed and the pane reported no error")
	}
	if !strings.Contains(err.Error(), "Host key verification failed") {
		t.Errorf("the failure reads %q, and ssh said why on standard error", err)
	}
	if strings.Contains(err.Error(), "ls -la") {
		t.Errorf("the failure carries the screen rather than the reason: %q", err)
	}
}

func TestSshsOwnReasonIsPreferredToWhatWasOnTheScreen(t *testing.T) {
	// Both places can have something in them: a connection that drops under a
	// working terminal leaves the screen's last line in one and ssh's
	// complaint in the other. The complaint is a reason by construction; the
	// screen is whatever the terminal happened to be showing.
	said := &tail{max: maxSaid}
	said.Write([]byte("Host key verification failed.\n"))
	screen := &tail{max: maxSaid}
	screen.Write([]byte("$ ls -la\n"))

	err := failed(errors.New("exit status 255"), []string{"ssh", "bot"}, said, screen)
	if !strings.Contains(err.Error(), "Host key verification failed") {
		t.Errorf("the failure reads %q, and ssh said why", err)
	}
	if strings.Contains(err.Error(), "ls -la") {
		t.Errorf("the failure carries the screen rather than the reason: %q", err)
	}
}

func TestThePaneSaysWhenTheDaemonWillNotLearnItFailed(t *testing.T) {
	// The failure goes to the pane and to mirror.log. The mark that tells the
	// daemon it was a failure goes to a file of its own -- and when that
	// cannot be written, what is left is a pane that vanished with no reason
	// beside it, which is the description of a terminal somebody shut. With
	// close_propagates on, that closes the terminal on the machine.
	//
	// So the log says both: what went wrong, and that the plugin could not
	// record it.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SESSION", "hub")
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	t.Setenv(EnvTarget, "bot")

	// Something in the way of the directory the marks live in.
	marks := filepath.Dir(failurePath("w1:p2"))
	if err := os.MkdirAll(filepath.Dir(marks), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marks, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}

	reportFailure(errors.New("the machine went away"))

	raw, err := os.ReadFile(filepath.Join(dir, "mirror.log"))
	if err != nil {
		t.Fatalf("nothing was written to mirror.log at all: %v", err)
	}
	said := string(raw)
	if !strings.Contains(said, "the machine went away") {
		t.Errorf("the log does not say what went wrong:\n%s", said)
	}
	if !strings.Contains(said, "could not be recorded") {
		t.Errorf("the log does not say the daemon will not learn of it:\n%s", said)
	}
	if !strings.Contains(said, "read this pane as one you closed") {
		t.Errorf("the log does not say what that means:\n%s", said)
	}
}

// streamingSSH puts an ssh on PATH that sends observe frames and then keeps
// the connection open, the way a machine with a live terminal does.
func streamingSSH(t *testing.T, frames int) {
	t.Helper()
	dir := t.TempDir()
	// One frame per line, base64 as the far side sends it, and then something
	// left running that outlives the shell and holds the pipes open.
	//
	// The background child is the point. Killing what was started is not the
	// same as being done waiting for it: Wait returns once nothing holds the
	// other end of the pipes, so a child that outlived its parent can keep a
	// stream that has been given up on waiting as long as it lives. Written as
	// a foreground command this passed on Linux, where the shell replaces
	// itself with its last command and dies with it, and hung on macOS, where
	// it does not. Backgrounded, both behave the way the far side might.
	script := "#!/bin/sh\n" +
		"last=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in\n" +
		"  *command\\ -v\\ herdr*) echo /usr/bin/herdr; exit 0;;\n" +
		"esac\n" +
		"i=0\n" +
		"while [ $i -lt " + strconv.Itoa(frames) + " ]; do\n" +
		"  printf '{\"bytes\":\"aGVsbG8=\"}\\n'\n" +
		"  i=$((i+1))\n" +
		"done\n" +
		"sleep 30 &\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAnObservedStreamWithNowhereToGoIsGivenUpOnRatherThanWaitedFor(t *testing.T) {
	// The pane has gone, so writing what the machine sends fails. Waiting on a
	// stream this has stopped reading never returns: the far side is still
	// sending, the pipe fills, the process never exits and the wait never
	// comes back. What is left is a pane with no output, no reconnect, and
	// alive as far as anything watching it can tell.
	//
	// So giving up on a stream has to mean ending it, not waiting politely for
	// it to finish -- and that had no test, in the one mode whose whole job is
	// to keep showing what a machine is doing.
	streamingSSH(t, 4)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	// Somewhere to write that is already closed, which is the pane going.
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	read.Close()
	saved := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = saved; write.Close() }()

	client := remote.NewWithBin("bot", "hub", "/usr/bin/herdr")
	done := make(chan error, 1)
	go func() {
		done <- streamOnce(client, "term_1", 80, 24, make(chan os.Signal))
	}()

	select {
	case err := <-done:
		os.Stdout = saved
		if !errors.Is(err, errStreamAbandoned) {
			t.Errorf("a stream with nowhere to write gave %v, want it abandoned", err)
		}
	case <-time.After(20 * time.Second):
		os.Stdout = saved
		t.Fatal("streamOnce never returned: the far side is still sending and " +
			"this waited for it, which is the pane that sits there for ever")
	}
}
