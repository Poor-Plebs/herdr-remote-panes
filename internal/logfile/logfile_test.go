package logfile

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
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
	path := filepath.Join(t.TempDir(), "daemon.log")
	f, err := Open(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("while it was open\n")); err != nil {
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

	// Harmless means the write was dropped, not that it was quietly let
	// through. Checking only that nothing returned an error is satisfied by a
	// Close that does nothing at all -- the file stays open, the writes keep
	// landing, and the descriptor is never given back.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "while it was open") {
		t.Errorf("the log lost what was written before the close: %q", raw)
	}
	if strings.Contains(string(raw), "after the end") {
		t.Errorf("a write after the close reached the file, so the close did not "+
			"close anything: %q", raw)
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

func TestTheLogSurvivesACloseThatComplains(t *testing.T) {
	// Rolling over closes the file, renames it and opens a new one. Close can
	// report a problem -- a flush failing on a disk that is misbehaving -- and
	// the descriptor is gone either way, so a rotate that gave up there left
	// nothing to write to. Every line after that was lost, silently and for
	// as long as the daemon ran, which is exactly the failure this package
	// was written to prevent.
	//
	// A file closed behind its back is what that looks like from in here: the
	// next Close reports one, and the one after it fails the same way a flush
	// error would.
	path := filepath.Join(t.TempDir(), "daemon.log")
	// Room for the two lines below to land in one generation, so that what is
	// checked is the rotation whose close failed and not a later one.
	f, err := Open(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Most of the way to the bound, so the next line is the one that rolls it.
	if _, err := f.Write([]byte(strings.Repeat("x", 49) + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = f.file.Close()

	// Long enough to be over the bound, so this write is the one that rotates.
	line := []byte("the line that rolls the log over\n")
	if n, err := f.Write(line); err != nil || n != len(line) {
		t.Errorf("the line that rotated the log was lost: n=%d err=%v", n, err)
	}
	// And the log keeps working afterwards, rather than every later line
	// meeting the same closed file.
	after := []byte("and the next one\n")
	if n, err := f.Write(after); err != nil || n != len(after) {
		t.Errorf("the log stopped working after a close complained: n=%d err=%v", n, err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rolls the log over", "and the next one"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the log does not hold %q:\n%s", want, body)
		}
	}
}

func TestTheLogKeepsWritingWhenItCannotRollOver(t *testing.T) {
	// The other half of the rotation going wrong: the file closes cleanly and
	// the rename is what fails. Something is sitting where the previous
	// generation goes, or the directory has stopped accepting renames.
	//
	// The bound is the thing given up, not the log. Outgrowing it is a
	// nuisance; stopping is the daemon losing its account of what it did, and
	// there is nowhere else that account exists.
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")

	// A non-empty directory where the kept generation goes, which is a rename
	// the operating system will refuse.
	if err := os.MkdirAll(path+".1", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path+".1", "in-the-way"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, err := f.Write([]byte(strings.Repeat("x", 49) + "\n")); err != nil {
		t.Fatal(err)
	}
	line := []byte("the line that could not roll the log over\n")
	if n, err := f.Write(line); err != nil || n != len(line) {
		t.Errorf("a line was lost because the log could not roll over: n=%d err=%v", n, err)
	}
	after := []byte("and the next one\n")
	if n, err := f.Write(after); err != nil || n != len(after) {
		t.Errorf("the log stopped after a rename it could not make: n=%d err=%v", n, err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"could not roll the log over", "and the next one"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the log does not hold %q:\n%s", want, body)
		}
	}

	// And it stops trying for another bound's worth. The count is what decides
	// when to roll over, so leaving it at the file's real size means a rename
	// attempted for every line written from here on -- a syscall per line, on
	// a rename already known to fail. Giving the bound up is the point;
	// hammering the thing that refused it is not.
	if f.written >= f.max {
		t.Errorf("the log holds %d bytes against a bound of %d, so the next line "+
			"tries the rename again", f.written, f.max)
	}
}

func TestALogDoesNotCarryWhatATerminalWouldActOn(t *testing.T) {
	// Both files this package writes are named in the troubleshooting page
	// with `cat` in front of them, and what goes into them is not all ours: an
	// error from a machine carries that machine's standard error as it was
	// written. So the escape that clears the screen or renames the window
	// would run in the terminal of somebody looking for why something failed.
	//
	// Held here rather than at each place that logs an error, because that is
	// a list of twenty-odd that nobody can finish.
	path := filepath.Join(t.TempDir(), "daemon.log")
	f, err := Open(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	logger := log.New(f, "", log.LstdFlags)
	logger.Printf("bot: %s", "could not connect: \x1b[2J\x1b]0;renamed\x07")
	logger.Printf("bot: second entry")
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(string(written), "\x1b\x07") {
		t.Errorf("the log carries an escape from a machine: %s", strconv.Quote(string(written)))
	}
	// The text still has to be there. Taking the escape out is only worth
	// doing if what it was wrapped around survives.
	if !strings.Contains(string(written), "could not connect") {
		t.Errorf("the log lost what it was reporting: %s", strconv.Quote(string(written)))
	}
	// And the newline that divides one entry from the next: Sanitize drops it
	// along with the other control characters, and an entry per line with the
	// time at the front is the whole shape of the file.
	if n := len(strings.Split(strings.TrimRight(string(written), "\n"), "\n")); n != 2 {
		t.Errorf("two entries came out as %d lines: %s", n, strconv.Quote(string(written)))
	}
}

func TestAWriteReportsEverythingItWasGiven(t *testing.T) {
	// Once anything is taken out, what reaches the file is shorter than what
	// arrived -- and an io.Writer that returns fewer bytes than it was given
	// is reporting a short write to everything that checks, which for a log is
	// a failure invented out of a message that was written correctly.
	f, err := Open(filepath.Join(t.TempDir(), "daemon.log"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	line := []byte("bot: \x1b[2Jcould not connect\n")
	n, err := f.Write(line)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(line) {
		t.Errorf("Write was given %d bytes and reported %d", len(line), n)
	}
}

func TestSanitizedGuardsAWriterThatIsNotAFile(t *testing.T) {
	// The log file is only half of where the daemon's diagnostics go: they
	// reach standard error too, which Herdr collects and shows, and every
	// command's final error goes there through the same logger. Sanitizing
	// only the file would leave the half that a terminal reads first.
	var out strings.Builder
	w := Sanitized(&out)

	line := []byte("bot: could not connect: \x1b[2J\x1b]0;renamed\x07\n")
	n, err := w.Write(line)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(line) {
		t.Errorf("Write was given %d bytes and reported %d", len(line), n)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Errorf("an escape reached the writer: %s", strconv.Quote(out.String()))
	}
	if !strings.Contains(out.String(), "could not connect") {
		t.Errorf("what was being reported was lost: %s", strconv.Quote(out.String()))
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Errorf("the line no longer ends: %s", strconv.Quote(out.String()))
	}
}
