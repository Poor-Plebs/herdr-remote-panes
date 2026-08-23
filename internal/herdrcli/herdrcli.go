// Package herdrcli wraps the local Herdr CLI, which is the supported plugin API.
package herdrcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"

	"context"
	"errors"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
	"time"
)

// Bin resolves the Herdr binary, preferring the path Herdr injects.
func Bin() string {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p
	}
	return "herdr"
}

// Pane is the subset of Herdr's pane_info shape this plugin needs.
type Pane struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	TerminalID  string `json:"terminal_id"`
	Label       string `json:"label"`
	Title       string `json:"terminal_title_stripped"`
	Agent       string `json:"agent"`
	AgentStatus string `json:"agent_status"`
	Cwd         string `json:"cwd"`
}

// DisplayName is the best human name for a pane: its explicit label, else the
// detected agent, else the terminal title, else the working directory.
//
// Shell titles are usually of the form "user@host:~", which would produce a
// second "@" once the host suffix is appended, so those are skipped in favour
// of the directory name.
// maxAgentName bounds an agent's name. An unbounded one crowds out everything
// beside it in a sidebar.
const maxAgentName = 28

// SafeAgent is the agent's name, made safe to pass on.
//
// It is set by whatever runs in the pane, which for a remote pane is something
// at the other end of an SSH connection, and it reaches a sidebar by two
// separate routes: as part of a pane's name, and through report-agent. Cleaning
// it at each route is how one of them came to be missed, so it is cleaned once
// here instead.
func (p Pane) SafeAgent() string {
	return text.Truncate(text.Sanitize(p.Agent), maxAgentName)
}

func (p Pane) DisplayName() string {
	if s := strings.TrimSpace(p.Label); s != "" {
		return s
	}
	if s := strings.TrimSpace(p.Agent); s != "" {
		return s
	}
	if s := strings.TrimSpace(p.Title); s != "" && !looksLikeShellTitle(s) {
		return s
	}
	if s := strings.TrimSpace(p.Cwd); s != "" {
		if base := path.Base(s); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return p.PaneID
}

// looksLikeShellTitle reports whether a terminal title is just the shell's
// default "user@host:dir" banner rather than a meaningful process name.
//
// This used to treat any title containing "@" or ":" as a banner, which threw
// away most of the useful ones: "npm run build:prod", "vim: notes.md",
// "make test:unit" and "ssh user@server" all became the name of the directory
// the pane happened to be in. The shape is what identifies a banner, not the
// punctuation -- the host part of "user@host:dir" runs up to the colon with no
// spaces in it, which is exactly what a command line does not do.
func looksLikeShellTitle(title string) bool {
	// "user@host" and "user@host:~/dir".
	if at := strings.IndexByte(title, '@'); at > 0 && !hasSpace(title[:at]) {
		host := title[at+1:]
		end := strings.IndexAny(host, " \t:")
		if end < 0 {
			return true // Nothing after the host at all.
		}
		if host[end] == ':' {
			return true // A path follows, as in a prompt.
		}
	}
	// "host:~/dir" and "host:/path", the same banner without the user part.
	if colon := strings.IndexByte(title, ':'); colon > 0 && !hasSpace(title[:colon]) {
		switch rest := strings.TrimLeft(title[colon+1:], " \t"); {
		case strings.HasPrefix(rest, "~"), strings.HasPrefix(rest, "/"):
			return true
		}
	}
	return false
}

func hasSpace(s string) bool {
	return strings.ContainsAny(s, " \t")
}

type envelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// RunError makes sense of a Herdr command that exited non-zero.
//
// Herdr signals a refusal by exiting non-zero and printing the error envelope,
// so returning the exit status alone threw away the code it had just given --
// which is the part a caller can act on. Callers were left matching on the
// words, or more often not noticing at all.
func RunError(runErr error, args []string, outputs ...[]byte) error {
	for _, out := range outputs {
		if _, err := Decode(out, args); err != nil {
			var api *APIError
			if errors.As(err, &api) {
				return api
			}
		}
	}
	joined := ""
	for _, out := range outputs {
		if s := strings.TrimSpace(string(out)); s != "" {
			joined = s
			break
		}
	}
	return fmt.Errorf("herdr %s: %w: %s", strings.Join(args, " "), runErr, joined)
}

// APIError is a refusal from Herdr, carrying the code it gave.
//
// The code matters as well as the words: a caller that has just been told the
// thing it was working on is gone should stop asking after it, rather than
// repeating the request for as long as it runs.
type APIError struct {
	Command string
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("herdr %s: %s: %s", e.Command, e.Code, e.Message)
}

// ignoreNotFound treats "it is not there" as success for the operations that
// were asking for exactly that.
//
// Closing a pane is asked for when the pane should not exist, and a reconciling
// daemon races with Herdr over which of them removes a thing first. Reporting
// the loser of that race as a failure filled the log with the daemon
// complaining that something it wanted gone was gone.
func ignoreNotFound(err error) error {
	if IsNotFound(err) {
		return nil
	}
	return err
}

// IsNotFound reports whether Herdr refused because the thing asked about does
// not exist -- a workspace closed, a pane already gone.
func IsNotFound(err error) bool {
	var api *APIError
	return errors.As(err, &api) && strings.HasSuffix(api.Code, "_not_found")
}

// commandTimeout bounds any single call to the Herdr CLI.
//
// These go to a socket on this machine and answer in milliseconds, so this is
// far beyond anything ordinary -- it is there because the reconcile loop holds
// the daemon's lock while it runs, and one call that never returns would take
// the status listing, the menu and every machine down with it. The calls to
// other machines have been bounded since the day one of them froze everything;
// the calls to this one were not, and they run far more often.
var commandTimeout = 30 * time.Second

// Run executes a Herdr CLI command and returns its decoded `result` object.
func Run(args ...string) (json.RawMessage, error) {
	return RunWith(nil, args...)
}

// RunWith executes a Herdr CLI command with extra environment variables,
// which is how remote invocations select a non-default HERDR_SESSION.
func RunWith(env []string, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, Bin(), args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("herdr %s: timed out after %s",
			strings.Join(args, " "), commandTimeout)
	}
	if err != nil {
		return nil, RunError(err, args, stderr.Bytes(), stdout.Bytes())
	}
	return Decode(stdout.Bytes(), args)
}

// Decode unwraps a Herdr CLI JSON envelope, surfacing API errors as errors.
//
// The response is found line by line rather than by parsing the whole output.
// Herdr prints the occasional notice — an available update, a banner — around
// its JSON, and treating the output as a single document made any such line
// fail every command with "unreadable response".
func Decode(out []byte, args []string) (json.RawMessage, error) {
	var (
		found bool
		env   envelope
	)
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var candidate envelope
		if err := json.Unmarshal(line, &candidate); err != nil {
			continue
		}
		if candidate.Result == nil && candidate.Error == nil {
			continue
		}
		// The last envelope wins: a command that reports progress before its
		// result should be read by its result.
		env, found = candidate, true
	}

	if !found {
		trimmed := bytes.TrimSpace(out)
		if len(trimmed) == 0 {
			return nil, nil
		}
		// Nothing on a line of its own parsed, so try the whole output: a
		// response printed across several lines is still a valid response.
		var whole envelope
		if err := json.Unmarshal(trimmed, &whole); err == nil &&
			(whole.Result != nil || whole.Error != nil) {
			env, found = whole, true
		}
	}
	if !found {
		return nil, fmt.Errorf("herdr %s: unreadable response: %s",
			strings.Join(args, " "), truncate(string(out)))
	}
	if env.Error != nil {
		return nil, &APIError{
			Command: strings.Join(args, " "),
			Code:    env.Error.Code,
			Message: env.Error.Message,
		}
	}
	return env.Result, nil
}

// truncate shortens a response for an error message. It counts characters, not
// bytes: a response can carry non-ASCII text, and cutting mid-character would
// put a broken rune into the error.
func truncate(s string) string {
	const max = 200
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

// PaneList returns every pane in the local session.
func PaneList() ([]Pane, error) {
	result, err := Run("pane", "list")
	if err != nil {
		return nil, err
	}
	return ParsePaneList(result)
}

// ParsePaneList decodes the `pane_list` result payload.
func ParsePaneList(result json.RawMessage) ([]Pane, error) {
	var body struct {
		Panes []Pane `json:"panes"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return nil, fmt.Errorf("parse pane list: %w", err)
	}
	return body.Panes, nil
}

// OpenOptions describes a plugin pane to open.
type OpenOptions struct {
	PluginID   string
	Entrypoint string
	Placement  string
	Workspace  string
	TargetPane string
	Direction  string
	Cwd        string
	Env        map[string]string
	Focus      bool
}

// OpenPane opens a plugin-owned pane and returns the pane Herdr created.
func OpenPane(opts OpenOptions) (Pane, error) {
	result, err := Run(openPaneArgs(opts)...)
	if err != nil {
		return Pane{}, err
	}
	return parseOpenedPane(result)
}

// parseOpenedPane pulls the new pane out of a plugin.pane.open result.
//
// Herdr nests it under `plugin_pane`; a bare `pane` is accepted too so a future
// response shape does not silently break mirroring. Returning an error here
// matters: a caller that cannot learn the pane id cannot track the pane, and
// would otherwise reopen it on every reconcile.
func parseOpenedPane(result json.RawMessage) (Pane, error) {
	var body struct {
		PluginPane struct {
			Pane Pane `json:"pane"`
		} `json:"plugin_pane"`
		Pane Pane `json:"pane"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return Pane{}, fmt.Errorf("parse plugin pane open: %w", err)
	}
	pane := body.PluginPane.Pane
	if pane.PaneID == "" {
		pane = body.Pane
	}
	if pane.PaneID == "" {
		return Pane{}, fmt.Errorf("plugin pane open returned no pane id: %s", truncate(string(result)))
	}
	return pane, nil
}

// openPaneArgs renders a plugin pane request as CLI arguments. Kept separate
// so the flag names, which Herdr rejects silently when wrong, can be tested.
func openPaneArgs(opts OpenOptions) []string {
	args := []string{"plugin", "pane", "open",
		"--plugin", opts.PluginID,
		"--entrypoint", opts.Entrypoint,
	}
	if opts.Placement != "" {
		args = append(args, "--placement", opts.Placement)
	}
	if opts.Workspace != "" {
		args = append(args, "--workspace", opts.Workspace)
	}
	if opts.TargetPane != "" {
		args = append(args, "--target-pane", opts.TargetPane)
	}
	if opts.Direction != "" {
		args = append(args, "--direction", opts.Direction)
	}
	if opts.Cwd != "" {
		args = append(args, "--cwd", opts.Cwd)
	}
	// Sorted so the command is stable, which keeps logs and tests readable.
	keys := make([]string, 0, len(opts.Env))
	for k := range opts.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+opts.Env[k])
	}
	if opts.Focus {
		args = append(args, "--focus")
	} else {
		args = append(args, "--no-focus")
	}
	return args
}

// RenamePane sets a pane's display label.
func RenamePane(paneID, label string) error {
	_, err := Run("pane", "rename", paneID, label)
	return err
}

// ClosePane closes a plugin-owned pane.
func ClosePane(paneID string) error {
	_, err := Run("plugin", "pane", "close", paneID)
	return ignoreNotFound(err)
}

// Notify shows a Herdr notification.
func Notify(message string) {
	_, _ = Run("notification", "show", message)
}

// ReportAgent declares which agent a pane is running and what it is doing.
//
// Herdr detects agents from the local process and its output. A mirror pane
// runs ssh, so nothing local looks like an agent and the pane shows up bare in
// the sidebar. The remote Herdr has already done the detection, so the result
// is reported here instead.
func ReportAgent(paneID, source, agent, state string) error {
	_, err := Run("pane", "report-agent", paneID,
		"--source", source, "--agent", agent, "--state", state)
	return err
}

// ReleaseAgent gives up agent authority for a pane, for when the remote pane
// stops running an agent.
func ReleaseAgent(paneID, source, agent string) error {
	_, err := Run("pane", "release-agent", paneID, "--source", source, "--agent", agent)
	return ignoreNotFound(err)
}

// AgentState maps a remote pane's agent status onto the states pane
// report-agent accepts. Herdr reports "done" but only accepts idle, working,
// blocked and unknown, so a finished agent is reported as idle.
func AgentState(status string) string {
	switch status {
	case "idle", "working", "blocked", "unknown":
		return status
	case "done":
		return "idle"
	default:
		return "unknown"
	}
}

// FocusWorkspace brings a workspace to the front.
//
// A space that has gone counts as focused: it is asked for after connecting,
// and a machine whose space closed in between is not a failure worth reporting
// over the connection having worked.
func FocusWorkspace(workspaceID string) error {
	_, err := Run("workspace", "focus", workspaceID)
	return ignoreNotFound(err)
}

// ClosePaneByID closes any pane, plugin-owned or not. Plugin panes are closed
// through ClosePane; this is for ordinary panes such as the shell Herdr creates
// alongside a new workspace.
func ClosePaneByID(paneID string) error {
	_, err := Run("pane", "close", paneID)
	return ignoreNotFound(err)
}

// WorkspaceLabel returns the label of a workspace, or "" when it is unknown.
func WorkspaceLabel(workspaceID string) string {
	result, err := Run("workspace", "list")
	if err != nil {
		return ""
	}
	var body struct {
		Workspaces []struct {
			WorkspaceID string `json:"workspace_id"`
			Label       string `json:"label"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return ""
	}
	for _, ws := range body.Workspaces {
		if ws.WorkspaceID == workspaceID {
			return ws.Label
		}
	}
	return ""
}

// SplitPane opens an ordinary local pane next to the focused one.
func SplitPane(direction string) error {
	_, err := Run("pane", "split", "--direction", direction, "--focus")
	return err
}

// ReportWorkspaceToken attaches display-only metadata to a workspace. Whether
// it is shown depends on the user's sidebar template, so it complements the
// marker in the workspace name rather than replacing it.
func ReportWorkspaceToken(workspaceID, source, name, value string) error {
	_, err := Run("workspace", "report-metadata", workspaceID,
		"--source", source, "--token", name+"="+value)
	return err
}
