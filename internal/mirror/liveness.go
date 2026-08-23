package mirror

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
//
// It is per session: pane ids repeat across Herdr sessions, so two sessions
// running this plugin would otherwise decide each other's panes were alive or
// dead from marks that belong to the other.
func livenessDir() string {
	dir := os.Getenv("HERDR_PLUGIN_STATE_DIR")
	if dir == "" {
		return ""
	}
	session := os.Getenv("HERDR_SESSION")
	if session == "" {
		session = "default"
	}
	return filepath.Join(dir, "panes", sanitizePaneID(session))
}

// Prune removes marks for panes that no longer exist.
//
// A mark is left behind whenever a pane goes without the daemon noticing — a
// crash, a session that never came back — and Herdr reuses pane ids, so a stale
// failure mark would make the next pane on that id look like a dropped
// connection and be reopened after someone deliberately closed it.
func Prune(known map[string]bool) {
	dir := livenessDir()
	if dir == "" {
		return
	}
	pruneDir(dir, known)

	// Marks used to live directly in the parent, before they were separated by
	// session. Nothing reads those any more, so an upgrade would leave them
	// behind for good.
	pruneDir(filepath.Dir(dir), nil)
}

// pruneDir removes mark files in one directory that no live pane claims. A nil
// set means none of them are claimed.
func pruneDir(dir string, known map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".pid"), ".failed")
		if base == name {
			// Not a mark; leave whatever it is alone.
			continue
		}
		if knownPane(known, base) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// knownPane reports whether a sanitised file name belongs to a live pane.
func knownPane(known map[string]bool, sanitized string) bool {
	for paneID := range known {
		if sanitizePaneID(paneID) == sanitized {
			return true
		}
	}
	return false
}

func livenessPath(paneID string) string {
	dir := livenessDir()
	if dir == "" || paneID == "" {
		return ""
	}
	return filepath.Join(dir, sanitizePaneID(paneID)+".pid")
}

// markLive records that this process is bridging the given terminal into the
// given pane, and returns a function that clears the mark.
//
// The terminal is recorded alongside the pid because a pane id alone does not
// identify a mirror: Herdr reuses pane ids, so bookkeeping that says "terminal
// t1 is mirrored at w1:p2" can survive to meet a pane that is now mirroring
// something else entirely.
func markLive(paneID, terminalID string) func() {
	path := livenessPath(paneID)
	if path == "" {
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return func() {}
	}
	body := strconv.Itoa(os.Getpid()) + "\n" + terminalID
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return func() {}
	}
	return func() { _ = os.Remove(path) }
}

// IsLive reports whether a mirror process is currently running for a pane.
func IsLive(paneID string) bool {
	_, live := readMark(paneID)
	return live
}

// LiveTerminal reports which terminal a running mirror is bridging, so
// bookkeeping can be checked against what is actually in the pane rather than
// trusted because the pane id matches.
//
// An empty terminal with live true means the mark predates this being recorded;
// callers should accept it rather than discard a working mirror.
func LiveTerminal(paneID string) (terminalID string, live bool) {
	return readMark(paneID)
}

func readMark(paneID string) (terminalID string, live bool) {
	path := livenessPath(paneID)
	if path == "" {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}

	pidText, terminal, _ := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(pidText))
	if err != nil || pid <= 0 {
		return "", false
	}
	// Signal 0 checks for existence without delivering anything.
	if syscall.Kill(pid, 0) != nil {
		return "", false
	}
	if !sameProgram(pid) {
		return "", false
	}
	return strings.TrimSpace(terminal), true
}

// sameProgram reports whether a process id belongs to this same program, as far
// as the platform will say.
//
// Signal 0 only proves that something with that id exists. Process ids are
// reused, so once a mirror has died its id can be handed to something
// unrelated, and the mark would then read as live for as long as that process
// lives -- leaving a pane nothing ever repairs, because everything downstream
// believes a mirror is already running in it.
//
// Linux exposes the name through /proc. Comparing against this process rather
// than a name written down here keeps the two in step, including the fifteen
// characters /proc truncates to. Where /proc is not available, macOS included,
// there is nothing cheap to check and the id is taken at face value, which is
// what it was before.
func sameProgram(pid int) bool {
	self, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		return true
	}
	other, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		// It answered signal 0 a moment ago and cannot be read now: either it
		// has just gone, or it belongs to somebody else. Neither is our mirror.
		return false
	}
	return strings.TrimSpace(string(self)) == strings.TrimSpace(string(other))
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

// maxFailureReason bounds what is kept of a failure. SSH prints fifteen lines
// of banner for a changed host key, and this file is only ever read to decide
// whether reopening the pane could help -- the phrase that says so is in the
// first line or two.
const maxFailureReason = 2048

// MarkFailed records that a pane's bridge died, and of what.
//
// The reason is kept because the daemon's only other option is to reopen the
// pane and see. That is the right guess for a dropped connection and the wrong
// one for a changed host key, where the second pane fails exactly as the first
// did -- two terminals flashing open and shut, and two more copies of the
// banner in the log, before anything says what is actually wrong.
func MarkFailed(paneID, reason string) {
	path := failurePath(paneID)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if len(reason) > maxFailureReason {
		reason = reason[:maxFailureReason]
	}
	body := strconv.FormatInt(time.Now().Unix(), 10) + "\n" + reason
	_ = os.WriteFile(path, []byte(body), 0o600)
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

// FailureReason is what killed a pane's bridge, or empty if that is not known.
//
// Empty is a real answer, not only an error: a pane marked by an older build
// wrote the timestamp alone, and one killed rather than failed leaves no file.
// Both mean the same thing here -- decide by reopening.
func FailureReason(paneID string) string {
	path := failurePath(paneID)
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	_, reason, found := strings.Cut(string(raw), "\n")
	if !found {
		return ""
	}
	return strings.TrimSpace(reason)
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
