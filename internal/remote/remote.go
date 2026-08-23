// Package remote talks to a Herdr server on another machine over SSH.
//
// Every connection is multiplexed through an OpenSSH ControlMaster socket, so
// polling a host costs one round trip rather than a full handshake.
package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
)

// ErrNoHerdr means the host is reachable over SSH but has no Herdr binary.
// It is distinct from an unreachable host: a machine without Herdr can still
// be used through plain SSH panes.
var ErrNoHerdr = errors.New("no herdr on the remote host")

// Client runs Herdr commands on a remote host.
type Client struct {
	Target  string
	Session string

	controlPath string

	// configuredBin, when set, is used verbatim instead of probing.
	configuredBin string

	mu  sync.Mutex
	bin string
}

// New builds a client for an SSH target and optional remote session name.
func New(target, session string) *Client {
	return NewWithBin(target, session, "")
}

// NewWithBin builds a client that invokes an explicit remote Herdr binary.
// An empty bin means the remote path is probed on first use.
func NewWithBin(target, session, bin string) *Client {
	sum := sha256.Sum256([]byte(target + "\x00" + session))
	// Keep the control socket short: Unix socket paths cap out near 104 bytes
	// and HERDR_PLUGIN_STATE_DIR can already be deep.
	name := "hrp-" + hex.EncodeToString(sum[:6]) + ".sock"
	return &Client{
		Target:        target,
		Session:       session,
		configuredBin: bin,
		controlPath:   filepath.Join(os.TempDir(), name),
	}
}

// probeScript finds Herdr on a host whose non-interactive PATH is minimal.
//
// `ssh host <command>` does not run a login shell, so an install under
// ~/.local/bin — where Herdr's own installer puts it for a non-root user — is
// invisible even though an interactive login finds it. Probe the usual
// locations rather than failing with "herdr: command not found".
//
// -f as well as -x: a directory is executable in the sense -x tests for, so a
// directory named herdr would otherwise be handed back as the binary and every
// call after that would fail saying something else entirely.
const probeScript = `command -v herdr 2>/dev/null && exit 0
for p in "$HOME/.local/bin/herdr" /usr/local/bin/herdr /opt/homebrew/bin/herdr \
         "$HOME/.nix-profile/bin/herdr" "$HOME/.local/share/mise/shims/herdr"; do
  [ -f "$p" ] && [ -x "$p" ] && printf '%s\n' "$p" && exit 0
done
exit 1`

// Bin resolves the remote Herdr binary, caching the result per client.
func (c *Client) Bin() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.configuredBin != "" {
		return c.configuredBin, nil
	}
	if c.bin != "" {
		return c.bin, nil
	}

	argv := []string{"ssh"}
	argv = append(argv, c.SSHArgs(false)...)
	argv = append(argv, probeScript)
	out, err := runCommand(argv)
	if err != nil {
		if reachErr := c.Reachable(); reachErr != nil {
			return "", reachErr
		}
		return "", fmt.Errorf("%s: %w", c.Target, ErrNoHerdr)
	}
	path := strings.TrimSpace(string(out))
	if i := strings.IndexByte(path, '\n'); i >= 0 {
		path = strings.TrimSpace(path[:i])
	}
	if path == "" {
		return "", fmt.Errorf("%s: could not find herdr on the remote host", c.Target)
	}
	c.bin = path
	return path, nil
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
		"-o", fmt.Sprintf("ConnectTimeout=%d", connectTimeout),
	}
	if tty {
		// The pane already owns a pty; force one on the far side too.
		args = append(args, "-tt", "-o", "BatchMode=no")
	} else {
		args = append(args, "-o", "BatchMode=yes")
	}
	// "--" so a destination is never read as an option. ssh takes -o on the
	// command line, and -oProxyCommand=... runs a command, so a target that
	// begins with a dash is an instruction rather than a machine.
	return append(args, "--", c.Target)
}

// remoteCommand renders a Herdr invocation as a single shell command string,
// because ssh joins its trailing arguments and hands them to a login shell.
func (c *Client) remoteCommand(bin string, args []string) string {
	// Clear socket overrides before invoking the remote Herdr. They take
	// priority over HERDR_SESSION, so a stray HERDR_SOCKET_PATH — forwarded by
	// an ssh SendEnv rule, or set in the remote shell profile — would silently
	// point these commands at the wrong session.
	parts := []string{"env", "-u", "HERDR_SOCKET_PATH", "-u", "HERDR_CLIENT_SOCKET_PATH"}
	if c.Session != "" {
		parts = append(parts, "HERDR_SESSION="+shellQuote(c.Session))
	}
	parts = append(parts, shellQuote(bin))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// Argv builds a full ssh argv that runs a Herdr command on the remote host.
func (c *Client) Argv(tty bool, args ...string) ([]string, error) {
	bin, err := c.Bin()
	if err != nil {
		return nil, err
	}
	argv := []string{"ssh"}
	argv = append(argv, c.SSHArgs(tty)...)
	return append(argv, c.remoteCommand(bin, args)), nil
}

// Run executes a remote Herdr command and decodes its JSON envelope.
func (c *Client) Run(args ...string) (json.RawMessage, error) {
	argv, err := c.Argv(false, args...)
	if err != nil {
		return nil, err
	}
	out, err := runCommand(argv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Target, err)
	}
	return herdrcli.Decode(out, args)
}

// CheckHerdr verifies the resolved binary actually runs. A configured
// herdr_bin that does not exist, or a host without Herdr, both surface as
// ErrNoHerdr so the caller can fall back to plain SSH panes rather than fail.
func (c *Client) CheckHerdr() error {
	bin, err := c.Bin()
	if err != nil {
		return err
	}
	argv := []string{"ssh"}
	argv = append(argv, c.SSHArgs(false)...)
	argv = append(argv, shellQuote(bin)+" --version")
	if _, err := runCommand(argv); err != nil {
		if reachErr := c.Reachable(); reachErr != nil {
			return reachErr
		}
		return fmt.Errorf("%s: %s did not run: %w", c.Target, bin, ErrNoHerdr)
	}
	return nil
}

// Reachable reports whether plain SSH to the host works, independently of
// whether Herdr is installed there.
func (c *Client) Reachable() error {
	argv := []string{"ssh"}
	argv = append(argv, c.SSHArgs(false)...)
	argv = append(argv, "true")
	if _, err := runCommand(argv); err != nil {
		return fmt.Errorf("%s is not reachable over ssh: %w", c.Target, err)
	}
	return nil
}

// ShellArgv builds an ssh argv that opens an interactive login shell. It needs
// no Herdr on the far side.
func (c *Client) ShellArgv() []string {
	argv := []string{"ssh"}
	return append(argv, c.SSHArgs(true)...)
}

// PaneList returns every pane on the remote host.
func (c *Client) PaneList() ([]herdrcli.Pane, error) {
	result, err := c.Run("pane", "list")
	if err != nil {
		return nil, err
	}
	return herdrcli.ParsePaneList(result)
}

// TabOrder returns each tab id on the remote host with its display number, so
// panes can be mirrored in the order they appear there.
func (c *Client) TabOrder() (map[string]int, error) {
	result, err := c.Run("tab", "list")
	if err != nil {
		return nil, err
	}
	return herdrcli.ParseTabOrder(result)
}

// Ping verifies the host is reachable and its Herdr session is answering.
func (c *Client) Ping() error {
	_, err := c.PaneList()
	return err
}

// Start launches a headless Herdr server for this client's session on the
// remote host. It is a no-op when one is already running, because Herdr binds
// a per-session socket and a second server exits on its own.
func (c *Client) Start() error {
	bin, err := c.Bin()
	if err != nil {
		return err
	}

	argv := []string{"ssh"}
	argv = append(argv, c.SSHArgs(false)...)
	argv = append(argv, c.startCommand(bin))
	if _, err := runCommand(argv); err != nil {
		return fmt.Errorf("%s: could not start a remote Herdr session: %w", c.Target, err)
	}
	return nil
}

// SameSettings reports whether this client is already set up for exactly these
// settings.
//
// The session is part of the multiplexed connection's identity, so a client
// made for one session cannot stand in for another. Reconnecting a host whose
// settings have changed has to replace the client rather than keep talking to
// the machine the old way.
func (c *Client) SameSettings(target, session, bin string) bool {
	return c.Target == target && c.Session == session && c.configuredBin == bin
}

// startCommand renders the shell command that starts a Herdr session on the
// machine.
//
// Three parts of it are load-bearing. The socket variables are cleared because
// SSH forwards this machine's, and a Herdr started with those set would talk
// back to the session here instead of its own. Every stream is redirected
// because ssh waits for them to close, and a background process holding one
// open leaves the connection hanging until it exits -- which for a server is
// never. And nohup, so it outlives the connection that started it.
func (c *Client) startCommand(bin string) string {
	prefix := "env -u HERDR_SOCKET_PATH -u HERDR_CLIENT_SOCKET_PATH "
	if c.Session != "" {
		prefix += "HERDR_SESSION=" + shellQuote(c.Session) + " "
	}
	return prefix + "nohup " + shellQuote(bin) + " server >/dev/null 2>&1 </dev/null &"
}

// Close tears down the shared ControlMaster connection.
func (c *Client) Close() {
	argv := []string{"ssh", "-o", "ControlPath=" + c.controlPath, "-O", "exit", "--", c.Target}
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
