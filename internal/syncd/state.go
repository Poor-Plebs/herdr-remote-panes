package syncd

import (
	"fmt"
	"os"
	"path/filepath"
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
func ControlSocket() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "control.sock"), nil
}

// Command is a request from a plugin action to the daemon.
type Command struct {
	Cmd  string `json:"cmd"`
	Host string `json:"host,omitempty"`
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
	LastError string `json:"last_error,omitempty"`
}
