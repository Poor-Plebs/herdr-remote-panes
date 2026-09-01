package mirror

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLivenessLifecycle(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	if IsLive("w1:p2") {
		t.Fatal("a pane with no mark must not read as live")
	}

	clear := markLive("w1:p2", "term_x")
	if !IsLive("w1:p2") {
		t.Error("a marked pane should read as live")
	}

	clear()
	if IsLive("w1:p2") {
		t.Error("a cleared pane must not read as live")
	}
}

func TestLivenessRejectsDeadAndCorruptMarks(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	// Written where the code looks, taken from the code. These marks used to be
	// put in panes/ while the layout has a session directory below that, so
	// nothing here was ever read: the test passed because the file was missing,
	// and went on passing with the checks it is about removed altogether.
	write := func(paneID, contents string) {
		t.Helper()
		path := livenessPath(paneID)
		if path == "" {
			t.Fatal("no liveness path")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// A Herdr restart leaves the pane behind without its process; the stale
	// mark must not make the husk look like a running mirror.
	write("w1:p9", strconv.Itoa(deadPID(t)))
	if IsLive("w1:p9") {
		t.Error("a mark for a dead process must not read as live")
	}

	write("w1:p8", "nonsense")
	if IsLive("w1:p8") {
		t.Error("an unreadable mark must not read as live")
	}

	// A number that parses and is not a process. These matter more than they
	// look: the check for whether the process is still there is kill(2) with
	// signal 0, and to kill(2) a pid of 0 means every process in this group
	// and a negative one means a group of its own choosing. Nothing is
	// delivered with signal 0, so today this is only a wrong answer.
	//
	// The last of them does not look like a pid at all, and Atoi does not
	// reject it: out of range comes back as an error *and* the largest int,
	// which is the one bad-input shape that arrives as a plausible number.
	// Truncated to the kernel's 32-bit pid it can land on -1, which is the
	// value kill(2) reads as every process the caller may signal.
	//
	// Two things stop all of this, and only one of them is portable: the guard
	// above, and -- on Linux -- reading /proc/<pid>/comm, which no pid here
	// has. On a system with no /proc that check passes everything, so the
	// guard is the whole of it.
	for _, pid := range []string{"0", "-1", "-12345", "99999999999999999999"} {
		write("w1:p7", pid)
		if IsLive("w1:p7") {
			t.Errorf("a mark holding the pid %s read as live", pid)
		}
	}
}

func TestAMarkNeedsAPaneToBelongTo(t *testing.T) {
	// Without a pane id there is no file to write, and the join would make one
	// anyway: a ".pid" with nothing in front of it, in the session directory,
	// which every pane without an id would then share as though it were theirs.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	if got := livenessPath(""); got != "" {
		t.Errorf("a pane with no id was given the mark file %q", got)
	}
	if got := livenessPath("w1:p1"); got == "" {
		t.Error("a pane with an id was given no mark file at all")
	}

	// The same for the mark that says a mirror failed, which is a second
	// function with the same guard: a shared ".failed" would have every pane
	// without an id inheriting the last one's failure.
	if got := failurePath(""); got != "" {
		t.Errorf("a pane with no id was given the failure file %q", got)
	}
	if got := failurePath("w1:p1"); got == "" {
		t.Error("a pane with an id was given no failure file at all")
	}
}

func TestClearLive(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	markLive("w1:p3", "term_x")
	ClearLive("w1:p3")
	if IsLive("w1:p3") {
		t.Error("ClearLive should remove the mark")
	}
}

func TestSanitizePaneID(t *testing.T) {
	// Pane ids contain a colon, which must not become a path separator or an
	// awkward filename.
	if got := sanitizePaneID("w1:p2"); got != "w1-p2" {
		t.Errorf("sanitizePaneID(w1:p2) = %q, want w1-p2", got)
	}
	if got := sanitizePaneID("../evil"); got != "---evil" {
		t.Errorf("sanitizePaneID(../evil) = %q, want ---evil", got)
	}
}

// deadPID returns a pid that has certainly exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := startTrueCommand(t)
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	return pid
}

func TestFailureMarks(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	// A pane closes whether its bridge dropped or someone shut the terminal.
	// Only the first records a failure, which is what tells them apart.
	if Failed("w1:p2") {
		t.Error("a pane with no record must not read as failed")
	}
	MarkFailed("w1:p2", "ssh: connect to host bot port 22: Connection refused")
	if !Failed("w1:p2") {
		t.Error("a recorded failure should read as failed")
	}
	ClearFailed("w1:p2")
	if Failed("w1:p2") {
		t.Error("a cleared failure must not read as failed")
	}
}

func TestFailureAndLivenessAreSeparate(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	clear := markLive("w1:p3", "term_x")
	MarkFailed("w1:p3", "ssh: connect to host bot port 22: Connection refused")
	clear()

	// Clearing liveness on exit must not erase why the pane went away.
	if IsLive("w1:p3") {
		t.Error("liveness should be cleared")
	}
	if !Failed("w1:p3") {
		t.Error("the failure record should survive the liveness mark being cleared")
	}
}

func TestMarksArePerSession(t *testing.T) {
	// Pane ids repeat across Herdr sessions, so sharing the marks would let one
	// session decide another's panes were alive or dead.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	t.Setenv("HERDR_SESSION", "hub")
	clearHub := markLive("w1:p2", "term_x")
	defer clearHub()
	if !IsLive("w1:p2") {
		t.Fatal("the mark should be live in the session that made it")
	}

	t.Setenv("HERDR_SESSION", "other")
	if IsLive("w1:p2") {
		t.Error("another session's pane should not read as live here")
	}

	// And a failure in one session must not be seen by the other.
	MarkFailed("w1:p2", "ssh: connect to host bot port 22: Connection refused")
	t.Setenv("HERDR_SESSION", "hub")
	if Failed("w1:p2") {
		t.Error("another session's failure should not be seen here")
	}
}

func TestPruneRemovesMarksForPanesThatAreGone(t *testing.T) {
	// A mark is left behind whenever a pane goes without the daemon noticing.
	// Herdr reuses pane ids, so a stale failure would make the next pane on
	// that id look like a dropped connection and be reopened after someone
	// deliberately closed it.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	markLive("w1:p2", "term_x")()
	MarkFailed("w1:p9", "ssh: connect to host bot port 22: Connection refused")
	MarkFailed("w1:p2", "ssh: connect to host bot port 22: Connection refused")

	Prune(map[string]bool{"w1:p2": true})

	if !Failed("w1:p2") {
		t.Error("a mark for a pane that still exists should be kept")
	}
	if Failed("w1:p9") {
		t.Error("a mark for a pane that is gone should be removed")
	}
}

func TestPruningWithNoPanesClearsEverything(t *testing.T) {
	// This used to be called "keeps everything when nothing is known", after a
	// hazard its author had in mind: a pane listing that came back empty for
	// some unrelated reason, taking every mark with it. It never asserted
	// anything -- the check was a t.Log inside an if, so it passed whichever
	// way the answer went -- and the answer is the opposite of its name.
	//
	// Clearing is right. Prune is handed the panes Herdr says it has, and an
	// empty listing means Herdr has no panes, which means no mirror is running
	// and no mark should survive. The hazard is real but it is not here: it is
	// in never calling this with a listing that failed, which is the caller's
	// job and has a test of its own beside the caller.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	MarkFailed("w1:p2", "ssh: connect to host bot port 22: Connection refused")
	clear := markLive("w1:p3", "term_x")
	defer clear()
	if !Failed("w1:p2") || !IsLive("w1:p3") {
		t.Fatal("the marks were not written, so nothing below is being pruned")
	}

	Prune(map[string]bool{})

	if Failed("w1:p2") {
		t.Error("a failure mark survived a listing with no panes in it")
	}
	if IsLive("w1:p3") {
		t.Error("a liveness mark survived a listing with no panes in it")
	}
}

func TestPruningKeepsTheMarksForPanesThatAreThere(t *testing.T) {
	// The other half, and the one that matters more: a pane Herdr still has
	// must keep its mark, or the daemon reads a running mirror as a husk and
	// replaces it.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	clear := markLive("w1:p3", "term_x")
	defer clear()
	MarkFailed("w1:p4", "connection refused")

	Prune(map[string]bool{"w1:p3": true, "w1:p4": true})

	if !IsLive("w1:p3") {
		t.Error("a live pane's mark was pruned while the pane is still there")
	}
	if !Failed("w1:p4") {
		t.Error("a failure was forgotten while the pane it is about is still there")
	}
}

func TestPruneClearsTheOldSharedLayout(t *testing.T) {
	// Marks used to live directly in the parent, before they were separated by
	// session. Nothing reads those any more, so an upgrade would leave them
	// behind for good.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SESSION", "hub")

	legacy := filepath.Join(dir, "panes")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"w1-p2.failed", "w9-p1.pid"} {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte("1"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A mark in this session's own directory that is still claimed.
	MarkFailed("w1:p2", "ssh: connect to host bot port 22: Connection refused")

	Prune(map[string]bool{"w1:p2": true})

	left, err := os.ReadDir(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range left {
		if !entry.IsDir() {
			t.Errorf("a mark from the old layout survived: %s", entry.Name())
		}
	}
	if !Failed("w1:p2") {
		t.Error("this session's claimed mark should have been kept")
	}
}

func TestLiveTerminalIdentifiesWhatIsInThePane(t *testing.T) {
	// A pane id alone does not identify a mirror: Herdr reuses them, so
	// bookkeeping saying "terminal t1 is mirrored at w1:p2" can survive to meet
	// a pane that is now mirroring something else entirely.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	clear := markLive("w1:p2", "term_a")
	defer clear()

	terminal, live := LiveTerminal("w1:p2")
	if !live {
		t.Fatal("a marked pane should read as live")
	}
	if terminal != "term_a" {
		t.Errorf("terminal = %q, want term_a", terminal)
	}

	if _, live := LiveTerminal("w1:p9"); live {
		t.Error("an unmarked pane should not read as live")
	}
}

func TestLiveTerminalAcceptsMarksWithoutATerminal(t *testing.T) {
	// A mark written before the terminal was recorded must not be read as a
	// dead mirror, or upgrading would close every working pane.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SESSION", "hub")

	path := livenessPath("w1:p2")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	terminal, live := LiveTerminal("w1:p2")
	if !live {
		t.Error("an older mark should still read as live")
	}
	if terminal != "" {
		t.Errorf("terminal = %q, want empty for an older mark", terminal)
	}
}

func TestSameProgramRejectsARecycledProcessID(t *testing.T) {
	// A mark holds the mirror's process id, and signal 0 only proves something
	// with that id exists. Process ids are reused, so after a mirror dies its
	// id can be handed to something unrelated and the mark would read as live
	// for as long as that process lives, leaving a pane nothing ever repairs.
	if _, err := os.ReadFile("/proc/self/comm"); err != nil {
		t.Skip("no /proc to check against; the id is taken at face value here")
	}

	if !sameProgram(os.Getpid()) {
		t.Error("this process is not recognised as itself")
	}

	// Process 1 exists on every Unix and is emphatically not a mirror, which is
	// what a recycled id looks like from here.
	if sameProgram(1) {
		t.Error("process 1 was accepted as a mirror of ours")
	}

	// An id that does not exist at all.
	if sameProgram(0x7FFFFFFF) {
		t.Error("a process id that cannot exist was accepted")
	}
}

func TestAFailureRecordsWhatKilledIt(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	const reason = "bot is not reachable over ssh: exit status 255: Host key verification failed."
	MarkFailed("w1:p2", reason)

	if !Failed("w1:p2") {
		t.Fatal("the pane should be marked as failed")
	}
	if got := FailureReason("w1:p2"); got != reason {
		t.Errorf("FailureReason = %q, want %q", got, reason)
	}

	// Cleared along with the mark, so a machine that comes back does not read
	// as still broken.
	ClearFailed("w1:p2")
	if got := FailureReason("w1:p2"); got != "" {
		t.Errorf("a cleared failure still reports %q", got)
	}
}

func TestAFailureWithNothingToSayIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SESSION", "hub")

	// What an older build wrote: the timestamp alone, no reason. It must read
	// as "failed, cause unknown" rather than as a corrupt file, because the
	// daemon's fallback for an unknown cause is the behaviour that build had.
	MarkFailed("w1:p2", "")
	if !Failed("w1:p2") {
		t.Fatal("a failure with no reason is still a failure")
	}
	if got := FailureReason("w1:p2"); got != "" {
		t.Errorf("FailureReason = %q, want empty", got)
	}

	// And a pane that was never marked at all.
	if got := FailureReason("w9:p9"); got != "" {
		t.Errorf("an unmarked pane reports %q", got)
	}
}

func TestALongFailureIsNotKeptWhole(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	// This file is read only to decide whether reopening could help, and the
	// phrase that says so is in the first line or two. A remote that prints a
	// megabyte of banner should not have it written to disk on every attempt.
	//
	// A megabyte written out, and a number to compare against. Both used to be
	// written in terms of maxFailureReason, so raising the bound raised the
	// input, the threshold and the result together and the test passed for any
	// value it could take -- including one that puts the whole banner back on
	// disk. Eight kilobytes is loose enough that tuning the bound does not
	// break this, and tight enough that losing it does.
	MarkFailed("w1:p2", strings.Repeat("x", 1<<20))
	if got := len(FailureReason("w1:p2")); got > 8192 {
		t.Errorf("kept %d bytes of a megabyte of banner, which is meant to be "+
			"cut to the couple of lines that say whether retrying helps", got)
	}
}

func TestAPaneIdKeepsItsIdentityWhenItBecomesAFilename(t *testing.T) {
	// The mark for a pane is a file named after it, and whether a mirror is
	// running is read back from that file. Two panes whose names come out the
	// same share one mark, and then one pane's liveness is answered from the
	// other's -- a husk read as a running mirror, or a live pane replaced.
	//
	// Every edge of every range, because an off-by-one at any of them turns an
	// ordinary character into the same dash everything else becomes.
	for _, tt := range []struct{ in, want string }{
		{"w1:p2", "w1-p2"},
		{"az", "az"},
		{"AZ", "AZ"},
		{"09", "09"},
		{"a-z_A-Z_0-9", "a-z_A-Z_0-9"},
		// Just outside each range, in ASCII order.
		{"`", "-"}, // before 'a'
		{"{", "-"}, // after 'z'
		{"@", "-"}, // before 'A'
		{"[", "-"}, // after 'Z'
		{"/", "-"}, // before '0'
		{":", "-"}, // after '9'
		{"../evil", "---evil"},
	} {
		if got := sanitizePaneID(tt.in); got != tt.want {
			t.Errorf("sanitizePaneID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	// The property behind the cases, over ids shaped the way Herdr writes them:
	// panes that differ still differ afterwards.
	seen := map[string]string{}
	for _, paneID := range []string{
		"w1:p1", "w1:p2", "w1:p9", "w1:p10", "w2:p1", "w10:p1", "w4A:p2", "w4a:p2", "wZ:p0",
	} {
		got := sanitizePaneID(paneID)
		if other, clash := seen[got]; clash {
			t.Errorf("panes %q and %q both become %q, so they share one mark", other, paneID, got)
		}
		seen[got] = paneID
	}
}

func TestAMarkWithNoRealProcessInItIsNotAlive(t *testing.T) {
	// A mark is a pid written to a file, and the file can be anything: a write
	// interrupted partway, a leftover from an older layout, a zero.
	//
	// Zero especially. Signal 0 to pid 0 goes to the caller's own process
	// group, which exists, so a mark that has lost its number would report the
	// pane as live -- and a husk that reads as a running mirror is never
	// replaced.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	// Written where the code looks for it, taken from the code rather than
	// rebuilt here: the layout has a session directory in it, and a mark put
	// beside that directory is simply never read.
	mark := livenessPath("w1:p7")
	if mark == "" {
		t.Fatal("no liveness path")
	}
	if err := os.MkdirAll(filepath.Dir(mark), 0o755); err != nil {
		t.Fatal(err)
	}

	// First: a mark this test writes is a mark the code reads. Without this the
	// cases below would pass for a file nobody opens, which is exactly how the
	// test beside this one came to assert nothing for as long as it existed.
	if err := os.WriteFile(mark, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if !IsLive("w1:p7") {
		t.Fatal("a mark holding this test's own pid does not read as live, so nothing below is being read")
	}

	for _, tt := range []struct{ what, contents string }{
		{"a zero, which would signal our own process group", "0"},
		{"a negative pid, which would signal a whole group", "-1"},
		{"the group of the process that wrote it", "-12345"},
		{"an empty file, from a write that never happened", ""},
		{"nothing but space", "   \n"},
		{"a number with something after it", "123x"},
		{"something enormous", "999999999999999999999999"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			const paneID = "w1:p7"
			if err := os.WriteFile(mark, []byte(tt.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if IsLive(paneID) {
				t.Errorf("a mark holding %q read as a live mirror", tt.contents)
			}
			if _, live := LiveTerminal(paneID); live {
				t.Errorf("a mark holding %q named a live terminal", tt.contents)
			}
		})
	}
}

func TestWithoutAStateDirectoryNothingIsWrittenAnywhere(t *testing.T) {
	// Every mark goes under the state directory Herdr provides, and everything
	// in the suite provides one. Without it there is nowhere marks belong, and
	// the answer has to be to do nothing -- not to fall back to a relative path
	// and start writing pid files into whatever directory the plugin was
	// started from.
	//
	// That is not a hypothetical environment: it is this binary run by hand,
	// which is how somebody checks whether it works at all.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")

	// Somewhere to notice anything written by mistake.
	dir := t.TempDir()
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(before) })

	MarkFailed("w1:p1", "ssh: connect to host bot port 22: Connection refused")
	ClearFailed("w1:p1")
	ClearLive("w1:p1")
	Prune(map[string]bool{"w1:p1": true})

	// Nothing is marked, because nothing could be.
	if IsLive("w1:p1") {
		t.Error("a pane read as live with nowhere for a mark to have been written")
	}
	if Failed("w1:p1") {
		t.Error("a pane read as failed with nowhere for a mark to have been written")
	}
	if _, live := LiveTerminal("w1:p1"); live {
		t.Error("a terminal read as bridged with nowhere for a mark to have been written")
	}
	if reason := FailureReason("w1:p1"); reason != "" {
		t.Errorf("a failure came back as %q with nowhere for it to have been written", reason)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("with no state directory, %v was written into the working directory", names)
	}
}

func TestAPaneStartingIsNotAPaneThatFailed(t *testing.T) {
	// Herdr hands out pane ids afresh each time it starts, so the third pane
	// of the next session is w1:p3 again. A mark left behind by a pane that
	// went without the daemon noticing -- a crash, a machine losing power, a
	// session that never came back -- is then wearing the id of something
	// else entirely.
	//
	// Prune clears marks no live pane claims, which covers every stale one
	// except this: a reused id *is* claimed, so its mark survives. Then the
	// pane is closed on purpose, the daemon reads a mark left by a different
	// pane in a different session, and opens it again -- which is the one
	// thing closing a pane is meant to stop.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	MarkFailed("w1:p3", "ssh: connection reset by peer")
	if !Failed("w1:p3") {
		t.Fatal("the fixture did not leave a mark, so this proves nothing")
	}

	// A bridge starting on that id.
	done := markLive("w1:p3", "term_abc")
	if Failed("w1:p3") {
		t.Error("a pane that has just started is still marked as having failed, " +
			"so closing it will bring it back")
	}
	if reason := FailureReason("w1:p3"); reason != "" {
		t.Errorf("the reason left by something else survives as %q", reason)
	}
	done()

	// And the mark this run writes is its own business: reportFailure runs
	// after the bridge returns, which is after the clean-up above, so nothing
	// here erases it.
	MarkFailed("w1:p3", "host key changed")
	if !Failed("w1:p3") {
		t.Error("a pane that failed after running is not marked as having failed")
	}
}

func TestAMarkThatCannotBeWrittenIsReported(t *testing.T) {
	// The mark is how the daemon tells a bridge that died from a terminal
	// somebody shut. Getting that wrong is not a small thing: with
	// close_propagates on, a pane read as deliberately closed closes the
	// terminal on the machine -- so a disk that will not take a hundred bytes
	// ends in work on another machine being closed, and every part of it used
	// to happen in silence.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SESSION", "hub")

	// A file where the marks directory has to go, so writing one cannot work.
	if err := MarkFailed("w1:p2", "the machine went away"); err != nil {
		t.Fatalf("a mark that should have been written was not: %v", err)
	}
	marks := filepath.Dir(failurePath("w1:p2"))
	if err := os.RemoveAll(marks); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marks, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := MarkFailed("w1:p2", "the machine went away")
	if err == nil {
		t.Fatal("the mark could not have been written and MarkFailed said nothing")
	}

	// And the pane reads as one that was not marked, which is the state the
	// daemon would act on.
	if Failed("w1:p2") {
		t.Error("a pane whose mark could not be written reads as marked")
	}

	// The other way it fails is the write rather than the directory, and both
	// are silent in the same way -- a test meeting only the first says nothing
	// about the second, which is what a mutation swallowing the write showed.
	// The directory back, and a directory where the mark itself goes: the path
	// is fine and the write is impossible.
	if err := os.Remove(marks); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(failurePath("w1:p2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MarkFailed("w1:p2", "the machine went away"); err == nil {
		t.Error("the mark's own path is a directory and MarkFailed said nothing")
	}
}
