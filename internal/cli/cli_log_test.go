package cli

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/version"
)

// TestTheDaemonsLogReachesTheFileItNames holds the join rather than the parts.
//
// logfile is tested thoroughly on its own -- rotating, reopening after a
// failed rotate, coming back when the disk does. What none of that says is
// whether the daemon's log is connected to it, and that is the half somebody
// depends on: every instruction for finding out why something went wrong ends
// with reading this file.
//
// It would fail silently. The daemon carries on either way, nothing checks
// that a line arrived, and the file simply stays empty -- which reads exactly
// like a daemon with nothing to say.
func TestTheDaemonsLogReachesTheFileItNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	// Whatever else happens, the standard logger goes back where it was: it is
	// process-wide, and a test that leaves it pointing into its own temporary
	// directory takes every later test's output with it.
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// Standing in for the terminal, so the other half of the join can be read:
	// the daemon writes to both, and running it by hand and seeing nothing is
	// the same silence as a daemon that has stopped.
	//
	// Swapped before the call, because the writer is built from whatever
	// os.Stderr is at that moment.
	readable, terminal, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	wasStderr := os.Stderr
	os.Stderr = terminal
	t.Cleanup(func() { os.Stderr = wasStderr })

	closeLog := daemonLog()
	if closeLog == nil {
		t.Fatal("no log was opened, with a state directory that exists")
	}

	path := filepath.Join(dir, "daemon.log")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("daemon.log was not created: %v", err)
	}

	log.Printf("a line the daemon would write")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	said := string(raw)

	if !strings.Contains(said, "a line the daemon would write") {
		t.Errorf("what the daemon logged is not in daemon.log:\n%s", said)
	}
	// The starting line is what says which build is running, and is the first
	// thing worth knowing when a log is being read at all.
	if !strings.Contains(said, "starting") || !strings.Contains(said, version.Short()) {
		t.Errorf("daemon.log does not open by naming the build that started:\n%s", said)
	}

	// Read before closing anything, and bounded: the pipe holds what has been
	// written so far and reading past it would wait for a writer that is still
	// open.
	_ = terminal.Close()
	os.Stderr = wasStderr
	shown := make([]byte, 4096)
	n, _ := readable.Read(shown)
	if !strings.Contains(string(shown[:n]), "a line the daemon would write") {
		t.Errorf("the daemon's log did not reach the terminal as well as the "+
			"file, so running it by hand shows nothing:\n%s", shown[:n])
	}

	closeLog()
	log.Printf("after the daemon has stopped")
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "after the daemon has stopped") {
		t.Error("the log is still being written to after it was closed")
	}
}

// TestNoStateDirectoryMeansNoLogRatherThanACrash holds the other half of what
// daemonLog returns. Its caller writes `if closeLog := daemonLog(); closeLog
// != nil` -- calling a nil func is a panic, and this is the case that returns
// one.
func TestNoStateDirectoryMeansNoLogRatherThanACrash(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	if closeLog := daemonLog(); closeLog != nil {
		closeLog()
		t.Error("a log was opened with nowhere to put it")
	}
}

func TestTheDaemonLogSaysWhichDaySomethingHappened(t *testing.T) {
	// daemon.log is kept until it rolls at a quarter of a megabyte, which for a
	// healthy daemon is days. With the time alone, the page runs backwards
	// every time Herdr is restarted on a later day -- a real one here has
	// "stopping on terminated" at 21:29 directly above "starting" at 12:24 --
	// and placing an entry means counting restarts.
	//
	// mirror.log, written beside it, has carried a full timestamp all along.
	var written strings.Builder
	restore := log.Flags()
	prefix := log.Prefix()
	out := log.Writer()
	t.Cleanup(func() {
		log.SetFlags(restore)
		log.SetPrefix(prefix)
		log.SetOutput(out)
	})

	// Set the way Main sets them, so this is about what a daemon writes rather
	// than about the defaults.
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(&written)
	log.Print("listening on /somewhere/control-hub.sock")

	line := written.String()
	if !regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}`).MatchString(line) {
		t.Errorf("a log line reads %q, without a date on it", strings.TrimSpace(line))
	}
}

func TestMainAsksForTheDateOnEveryLine(t *testing.T) {
	// The flags above are set once, in Main, and the test beside this one sets
	// them itself -- so it would pass with the line in Main deleted. Read
	// instead: Main cannot be called from a test without it taking over the
	// process's logger and arguments for the rest of the run.
	source, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "log.SetFlags(log.Ldate | log.Ltime)") {
		t.Error("the daemon no longer asks for the date, so daemon.log covers days " +
			"of restarts with nothing saying which day any of them was")
	}
}

func TestVersionDoesNotClaimTheDaemonIsDownWhenItCannotAsk(t *testing.T) {
	// This command exists to compare the build installed against the one
	// running, which is the question after an upgrade. Run from an ordinary
	// shell rather than through Herdr there is no state directory, so no
	// socket to knock on -- and every failure read as "not running", which is
	// a definite answer to a question that was never put. The daemon may be up
	// and mirroring; this process cannot see it either way.
	//
	// Believing it costs a restart of Herdr to no purpose, or the opposite
	// conclusion: that the new build is the one running.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")
	t.Setenv("HERDR_SESSION", "")

	var out, warn strings.Builder
	if err := reportVersion(&out, &warn, "abc1234"); err != nil {
		t.Fatal(err)
	}
	said := out.String()
	if strings.Contains(said, "not running") {
		t.Errorf("with no way to ask, the report says the daemon is not running:\n%s", said)
	}
	if !strings.Contains(said, "cannot tell") {
		t.Errorf("the report does not say it could not ask:\n%s", said)
	}
	// And no stale-build warning either: comparing against a build nothing
	// reported is comparing against nothing.
	if warn.Len() != 0 {
		t.Errorf("a warning was given about a daemon that was never reached: %q", warn.String())
	}

	// With somewhere to look and nothing there, "not running" is the right
	// answer and still gets given.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "nobody")
	out.Reset()
	warn.Reset()
	if err := reportVersion(&out, &warn, "abc1234"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Errorf("with a socket to knock on and no answer, the report says:\n%s", out.String())
	}
}
