// Package remote talks to a Herdr server on another machine over SSH.
//
// Every connection is multiplexed through an OpenSSH ControlMaster socket, so
// polling a host costs one round trip rather than a full handshake.
package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
)

// Client runs Herdr commands on a remote host.
type Client struct {
	Target  string
	Session string

	controlPath string
}

// New builds a client for an SSH target and optional remote session name.
func New(target, session string) *Client {
	sum := sha256.Sum256([]byte(target + "\x00" + session))
	// Keep the control socket short: Unix socket paths cap out near 104 bytes
	// and HERDR_PLUGIN_STATE_DIR can already be deep.
	name := "hrp-" + hex.EncodeToString(sum[:6]) + ".sock"
	return &Client{
		Target:      target,
		Session:     session,
		controlPath: filepath.Join(os.TempDir(), name),
	}
}

// SSHArgs returns the ssh argv prefix shared by every invocation.
//
// tty requests a remote pty and allows interactive auth prompts, which is what
// an interactive attach needs. Polling stays non-interactive so a host that
// cannot authenticate fails fast instead of blocking the daemon.
func (c *Client) SSHArgs(tty bool) []string {
	args := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + c.controlPath,
		"-o", "ControlPersist=120",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
	if tty {
		// The pane already owns a pty; force one on the far side too.
		args = append(args, "-tt", "-o", "BatchMode=no")
	} else {
		args = append(args, "-o", "BatchMode=yes")
	}
	return append(args, c.Target)
}

// remoteCommand renders a Herdr invocation as a single shell command string,
// because ssh joins its trailing arguments and hands them to a login shell.
func (c *Client) remoteCommand(args []string) string {
	// Clear socket overrides before invoking the remote Herdr. They take
	// priority over HERDR_SESSION, so a stray HERDR_SOCKET_PATH — forwarded by
	// an ssh SendEnv rule, or set in the remote shell profile — would silently
	// point these commands at the wrong session.
	parts := []string{"env", "-u", "HERDR_SOCKET_PATH", "-u", "HERDR_CLIENT_SOCKET_PATH"}
	if c.Session != "" {
		parts = append(parts, "HERDR_SESSION="+shellQuote(c.Session))
	}
	parts = append(parts, "herdr")
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// Argv builds a full ssh argv that runs a Herdr command on the remote host.
func (c *Client) Argv(tty bool, args ...string) []string {
	argv := []string{"ssh"}
	argv = append(argv, c.SSHArgs(tty)...)
	return append(argv, c.remoteCommand(args))
}

// Run executes a remote Herdr command and decodes its JSON envelope.
func (c *Client) Run(args ...string) (json.RawMessage, error) {
	argv := c.Argv(false, args...)
	out, err := runCommand(argv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Target, err)
	}
	return herdrcli.Decode(out, args)
}

// PaneList returns every pane on the remote host.
func (c *Client) PaneList() ([]herdrcli.Pane, error) {
	result, err := c.Run("pane", "list")
	if err != nil {
		return nil, err
	}
	return herdrcli.ParsePaneList(result)
}

// Ping verifies the host is reachable and running a compatible Herdr.
func (c *Client) Ping() error {
	_, err := c.PaneList()
	return err
}

// Close tears down the shared ControlMaster connection.
func (c *Client) Close() {
	argv := []string{"ssh", "-o", "ControlPath=" + c.controlPath, "-O", "exit", c.Target}
	_, _ = runCommand(argv)
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '=' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
