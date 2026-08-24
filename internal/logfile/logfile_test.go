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

func TestTheNewestMessageIsAlwaysInTheLogItself(t *testing.T) {
	// The reason to open daemon.log is almost always to see what just
	// happened. Rolling over must therefore leave the newest message in the
	// log rather than in the generation beside it -- a rotation that got that
	// backwards would keep the size right, keep the file count right, and be
	// useless.
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	f, err := Open(path, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	for _, line := range []string{"first\n", "second\n", "third\n", "fourth\n"} {
		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), "fourth") {
		t.Errorf("the newest message is not in the log: %q", current)
	}

	// And what was rolled out of it is the older half, not the newer.
	previous, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("no previous generation was kept: %v", err)
	}
	if strings.Contains(string(previous), "fourth") {
		t.Errorf("the newest message was rolled away into %q", previous)
	}
	if !strings.Contains(string(previous), "second") && !strings.Contains(string(previous), "third") {
		t.Errorf("the generation kept holds neither of the messages before it: %q", previous)
	}
}

func TestALogExactlyAtItsBoundIsNotRolledOver(t *testing.T) {
	// Rolling over one byte early costs a generation for nothing: the message
	// that fits exactly is thrown into the previous file and the log starts
	// again empty. The bound is what the log may reach, not what it must stay
	// under.
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	f, err := Open(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, err := f.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Error("a log filled exactly to its bound was rolled over")
	}

	// One more byte is over it, and that does roll.
	if _, err := f.Write([]byte("A")); err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("going over the bound did not roll the log over: %v", err)
	}
	if !strings.Contains(string(previous), "0123456789") {
		t.Errorf("the generation kept holds %q, not what was in the log", previous)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "A" {
		t.Errorf("the log holds %q after rolling, want just the message that went over", current)
	}
}

func TestAMessageBiggerThanTheWholeLogIsStillWrittenWhole(t *testing.T) {
	// Nothing this writes is anywhere near the bound, but the answer matters if
	// one ever is: a message that will not fit is written anyway rather than
	// cut in half or dropped. Half a failure in a log reads as a different
	// failure, and the size is only ever a guard against growing without end.
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	f, err := Open(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	long := strings.Repeat("x", 40) + "\n"
	n, err := f.Write([]byte(long))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(long) {
		t.Errorf("wrote %d bytes of %d", n, len(long))
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != long {
		t.Errorf("the log holds %d bytes, want the message whole (%d)", len(current), len(long))
	}
}

func TestReopeningRemembersHowBigTheLogAlreadyIs(t *testing.T) {
	// The daemon reopens this every time Herdr starts, and opens it for append,
	// so a log that came back thinking it was empty would grow by its whole
	// bound again before rolling. Restart often enough and "rolls over rather
	// than growing without end" stops being true, one restart at a time.
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	const max = 64

	// Most of the way to the bound, then closed as a restart would.
	first, err := Open(path, max)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write([]byte(strings.Repeat("a", max-8) + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopened, and given more than the little that is left.
	second, err := Open(path, max)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := second.Write([]byte(strings.Repeat("b", 16) + "\n")); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > max {
		t.Errorf("after reopening, the log is %d bytes against a bound of %d: "+
			"it came back thinking it was empty", info.Size(), max)
	}
	// It rolled rather than simply refusing the write.
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("the log went over its bound without rolling over: %v", err)
	}
}
