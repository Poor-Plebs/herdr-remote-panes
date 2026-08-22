package mirror

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// Liveness marks which panes currently have a mirror process running.
//
// A restarted Herdr server restores a plugin pane as an ordinary shell in the
// plugin's directory without re-running its command: the pane id survives, the
// mirror does not. "The pane still exists" is therefore not enough to conclude
// a mirror is live, and adopting such a husk would leave a dead local shell
// wearing a name@host label forever. Each mirror records its pid instead.

// livenessDir holds one file per pane, named for the pane, containing the pid.
func livenessDir() string {
	dir := os.Getenv("HERDR_PLUGIN_STATE_DIR")
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "panes")
}

func livenessPath(paneID string) string {
	dir := livenessDir()
	if dir == "" || paneID == "" {
		return ""
	}
	return filepath.Join(dir, sanitizePaneID(paneID)+".pid")
}

// markLive records that this process is mirroring into the given pane, and
// returns a function that clears the mark.
func markLive(paneID string) func() {
	path := livenessPath(paneID)
	if path == "" {
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return func() {}
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return func() {}
	}
	return func() { _ = os.Remove(path) }
}

// IsLive reports whether a mirror process is currently running for a pane.
func IsLive(paneID string) bool {
	path := livenessPath(paneID)
	if path == "" {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil || pid <= 0 {
		return false
	}
	// Signal 0 checks for existence without delivering anything.
	return syscall.Kill(pid, 0) == nil
}

// failurePath marks a pane whose bridge exited with an error, as opposed to
// one closed deliberately. A pane closes either way, so without this the daemon
// cannot tell a dropped connection from someone shutting a terminal, and would
// have to choose between never recovering and reopening what was just closed.
func failurePath(paneID string) string {
	dir := livenessDir()
	if dir == "" || paneID == "" {
		return ""
	}
	return filepath.Join(dir, sanitizePaneID(paneID)+".failed")
}

// MarkFailed records that a pane's bridge died of an error.
func MarkFailed(paneID string) {
	path := failurePath(paneID)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600)
}

// Failed reports whether a pane's bridge died of an error.
func Failed(paneID string) bool {
	path := failurePath(paneID)
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// ClearFailed forgets a recorded failure, once it has been acted on.
func ClearFailed(paneID string) {
	if path := failurePath(paneID); path != "" {
		_ = os.Remove(path)
	}
}

// ClearLive removes a stale mark, for panes the daemon has given up on.
func ClearLive(paneID string) {
	if path := livenessPath(paneID); path != "" {
		_ = os.Remove(path)
	}
}

// sanitizePaneID keeps a pane id usable as a filename; ids look like "w1:p2".
func sanitizePaneID(paneID string) string {
	out := []rune(paneID)
	for i, r := range out {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			out[i] = '-'
		}
	}
	return string(out)
}
