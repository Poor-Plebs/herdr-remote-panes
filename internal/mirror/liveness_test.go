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

	clear := markLive("w1:p2")
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
	markLive("w1:p3")
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
