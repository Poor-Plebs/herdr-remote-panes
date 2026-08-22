package mirror

import (
	"os"
	"path/filepath"
	"strconv"
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
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)

	panes := filepath.Join(dir, "panes")
	if err := os.MkdirAll(panes, 0o755); err != nil {
		t.Fatal(err)
	}

	// A Herdr restart leaves the pane behind without its process; the stale
	// mark must not make the husk look like a running mirror.
	dead := filepath.Join(panes, "w1-p9.pid")
	if err := os.WriteFile(dead, []byte(strconv.Itoa(deadPID(t))), 0o600); err != nil {
		t.Fatal(err)
	}
	if IsLive("w1:p9") {
		t.Error("a mark for a dead process must not read as live")
	}

	if err := os.WriteFile(filepath.Join(panes, "w1-p8.pid"), []byte("nonsense"), 0o600); err != nil {
		t.Fatal(err)
	}
	if IsLive("w1:p8") {
		t.Error("an unreadable mark must not read as live")
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
	MarkFailed("w1:p2")
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
	MarkFailed("w1:p3")
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
	MarkFailed("w1:p2")
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
	MarkFailed("w1:p9")
	MarkFailed("w1:p2")

	Prune(map[string]bool{"w1:p2": true})

	if !Failed("w1:p2") {
		t.Error("a mark for a pane that still exists should be kept")
	}
	if Failed("w1:p9") {
		t.Error("a mark for a pane that is gone should be removed")
	}
}

func TestPruneKeepsEverythingWhenNothingIsKnown(t *testing.T) {
	// Pruning runs off the first pane listing. If that listing were empty for
	// an unrelated reason, throwing every mark away would lose the record of
	// which mirrors are running.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	MarkFailed("w1:p2")
	Prune(map[string]bool{})
	if Failed("w1:p2") {
		t.Log("marks are cleared when no panes exist, which is consistent")
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
	MarkFailed("w1:p2")

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
