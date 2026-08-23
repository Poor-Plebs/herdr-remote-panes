package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRollsOverRatherThanGrowingForever(t *testing.T) {
	// The daemon runs for as long as Herdr does, so anything it writes has to
	// have an end to it.
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")

	f, err := Open(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	for i := 0; i < 50; i++ {
		if _, err := f.Write([]byte(strings.Repeat("x", 20) + "\n")); err != nil {
			t.Fatal(err)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 200 {
		t.Errorf("the log is %d bytes, over its bound of 200", info.Size())
	}

	// One generation is kept, and no more: the space used stays bounded.
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

func TestContinuesAnExistingLog(t *testing.T) {
	// Restarting should not throw away what the last run had to say.
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte("from before\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("from now\n")); err != nil {
		t.Fatal(err)
	}
	f.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"from before", "from now"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("log = %q, want it to contain %q", raw, want)
		}
	}
}

func TestIsPrivate(t *testing.T) {
	// It records which machines were reached and what went wrong.
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	f, err := Open(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions are %o, want 600", perm)
	}
}

func TestWritesFromSeveralGoroutines(t *testing.T) {
	// The daemon reconciles machines in parallel and each can log.
	f, err := Open(filepath.Join(t.TempDir(), "daemon.log"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = f.Write([]byte("a line of diagnostics\n"))
			}
		}()
	}
	wg.Wait()
}

func TestWritingAfterCloseIsHarmless(t *testing.T) {
	// The daemon stops on a signal, and something logging on the way out must
	// not bring it down.
	f, err := Open(filepath.Join(t.TempDir(), "daemon.log"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("closing twice: %v", err)
	}
	if _, err := f.Write([]byte("after the end\n")); err != nil {
		t.Errorf("writing after close: %v", err)
	}
}
