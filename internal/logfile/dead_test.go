package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheLogComesBackAfterTheDiskMisbehaves(t *testing.T) {
	// This package exists so the log does not stop the first time something
	// goes wrong -- its own comments say so. But rotating closes the file
	// before renaming it, and open() only assigns on success: when the rename
	// fails and the reopen fails too, the handle left behind is the closed one.
	// The nil guard passes, every later write goes to a closed file, and the
	// log is gone for the rest of the session with nothing said.
	if os.Geteuid() == 0 {
		t.Skip("needs a directory the running user cannot write")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "log")

	f, err := Open(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("before\n")); err != nil {
		t.Fatal(err)
	}

	// The disk goes. Both halves have to fail to reach this: the rename, which
	// needs the directory, and the reopen, which needs the file -- reopening
	// something that already exists does not touch the directory at all, so
	// making only the directory read-only leaves the log recovering by itself.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	// Enough to want a rotation, which fails both ways.
	_, _ = f.Write([]byte(strings.Repeat("x", 100) + "\n"))

	// And comes back.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("after\n")); err != nil {
		t.Errorf("writing after the disk recovered: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "after") {
		t.Errorf("nothing written after the disk recovered; the log is dead for "+
			"the rest of the session. It holds: %q", raw)
	}
}

func TestALogClosedOnPurposeStaysClosed(t *testing.T) {
	// The retry above must not resurrect a log somebody shut. Closing is how
	// the daemon lets go of the file when it stops; reopening it on the next
	// line written would keep a handle on a file nothing is meant to be
	// holding, and write to it after the process said it was done.
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	f, err := Open(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("before\n")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Accepted and dropped: whoever writes to a log should not have to know
	// whether it is still open.
	if _, err := f.Write([]byte("after\n")); err != nil {
		t.Errorf("writing to a closed log returned %v, want it quietly dropped", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "after") {
		t.Errorf("a log closed on purpose reopened itself: %q", raw)
	}
}
