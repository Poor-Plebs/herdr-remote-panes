package syncd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StateDir is where Herdr lets a plugin keep runtime state.
func StateDir() (string, error) {
	dir := os.Getenv("HERDR_PLUGIN_STATE_DIR")
	if dir == "" {
		return "", fmt.Errorf("HERDR_PLUGIN_STATE_DIR is not set; run this through Herdr")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ControlSocket is the address actions use to reach a running daemon.
//
// Herdr's plugin state directory is shared by every session, but each session
// runs its own daemon, so the socket is named per session. Without this a
// second session's daemon would find the first one's socket and exit.
func ControlSocket() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	session := os.Getenv("HERDR_SESSION")
	if session == "" {
		session = "default"
	}
	return filepath.Join(dir, "control-"+sanitize(session)+".sock"), nil
}

// sanitize keeps a session name usable as a filename.
func sanitize(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

// Command is a request from a plugin action to the daemon.
type Command struct {
	Cmd string `json:"cmd"`
	// Host names an SSH target explicitly.
	Host string `json:"host,omitempty"`
	// Workspace is the workspace the action was invoked from. When no host is
	// named, it decides which machine a new pane belongs to.
	Workspace string `json:"workspace,omitempty"`
}

// Reply is the daemon's answer to a Command.
type Reply struct {
	OK      bool       `json:"ok"`
	Message string     `json:"message,omitempty"`
	Hosts   []HostInfo `json:"hosts,omitempty"`
}

// HostInfo summarises one connected host for the status action.
type HostInfo struct {
	Target    string `json:"target"`
	Label     string `json:"label"`
	Connected bool   `json:"connected"`
	Mirrors   int    `json:"mirrors"`
	// SSHOnly marks a host used through plain SSH panes, with no Herdr on it.
	SSHOnly   bool   `json:"ssh_only,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// snapshot is the daemon's mirror bookkeeping, persisted so a restarted daemon
// adopts the panes it already opened instead of opening a second set.
type snapshot struct {
	Hosts map[string]hostSnapshot `json:"hosts"`
}

type hostSnapshot struct {
	// Mirrors maps a remote terminal id to the local pane showing it.
	Mirrors map[string]string `json:"mirrors"`
	// Dismissed are mirrors the user closed by hand.
	Dismissed []string `json:"dismissed,omitempty"`
}

// snapshotPath is per session, matching the control socket.
func snapshotPath() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	session := os.Getenv("HERDR_SESSION")
	if session == "" {
		session = "default"
	}
	return filepath.Join(dir, "mirrors-"+sanitize(session)+".json"), nil
}

// loadSnapshot reads the previous bookkeeping. A missing or unreadable file is
// not an error: the daemon simply starts with no adopted mirrors.
func loadSnapshot() snapshot {
	empty := snapshot{Hosts: map[string]hostSnapshot{}}

	path, err := snapshotPath()
	if err != nil {
		return empty
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var loaded snapshot
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return empty
	}
	if loaded.Hosts == nil {
		loaded.Hosts = map[string]hostSnapshot{}
	}
	return loaded
}

// saveSnapshot writes the bookkeeping, replacing the file atomically so a
// crash mid-write cannot leave a truncated snapshot behind.
func saveSnapshot(s snapshot) error {
	path, err := snapshotPath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
