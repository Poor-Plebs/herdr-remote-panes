package cli

import (
	"log"
	"os"
	"path/filepath"
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
