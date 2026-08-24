package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
