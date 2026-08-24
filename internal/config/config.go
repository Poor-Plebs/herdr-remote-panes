// Package config loads the user-editable plugin configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
)

// Mode selects how a remote pane is bridged into a local mirror pane.
type Mode string

const (
	// ScopeShared mirrors only this machine's own space on the remote, keeping
	// both ends identical. ScopeAll mirrors every pane the machine has.
	ScopeShared = "shared"
	ScopeAll    = "all"
)

const (
	// DefaultSessionName means the machine's own default session: the one
	// plain `herdr` opens there. Mirroring into it is the default, because a
	// dedicated session is invisible unless you know to ask for it by name.
	DefaultSessionName = "default"
)

const (
	// ModeAttach mirrors the machine's terminals with `herdr terminal attach`
	// over SSH. Experimental: it needs Herdr on the machine, is exclusive per
	// remote terminal, and pins that terminal's size.
	ModeAttach Mode = "attach"
	// ModeSSH opens plain SSH panes and is the default. The machine needs
	// nothing but an SSH login: no Herdr, no session, nothing to keep in step.
	ModeSSH Mode = "ssh"
	// ModeObserve decodes `herdr terminal session observe` frames: read-only,
	// safe to run concurrently with other viewers, and it never locks resize.
	ModeObserve Mode = "observe"
)

// Host is a single SSH target whose panes are mirrored into this session.
type Host struct {
	// Target is the SSH destination, as accepted by ssh(1) and ~/.ssh/config.
	Target string `json:"target"`
	// Label overrides the "@suffix" appended to mirrored pane names.
	// Defaults to Target.
	Label string `json:"label,omitempty"`
	// Session overrides the remote HERDR_SESSION name for this host. Empty
	// falls back to the top-level Session. Use DefaultSessionName to target
	// the remote's unnamed default session instead.
	Session string `json:"session,omitempty"`
	// Mode overrides the global mode for this host.
	Mode Mode `json:"mode,omitempty"`
	// Placement overrides the global placement for this host.
	Placement string `json:"placement,omitempty"`
	// Workspace is the local workspace label mirrors from this host land in.
	// It is created when missing. Empty falls back to the top-level Workspace.
	Workspace string `json:"workspace,omitempty"`
	// HerdrBin overrides the remote Herdr binary path for this host.
	HerdrBin string `json:"herdr_bin,omitempty"`
	// Disabled skips the host without removing it from the config.
	Disabled bool `json:"disabled,omitempty"`
}

// DisplayLabel is the suffix used when naming panes from this host.
func (h Host) DisplayLabel() string {
	// Made safe here rather than at each place it is drawn. It ends up in a
	// pane's name, in the name of the machine's space, and in the suffix those
	// are matched against -- and only the first of those was cleaning it, so a
	// stray escape in a hand-edited label reached the sidebar by the other two.
	// Doing it once also keeps the three agreeing, which is what lets a pane be
	// recognised as belonging to its machine.
	if h.Label != "" {
		return text.Sanitize(h.Label)
	}
	return text.Sanitize(h.Target)
}

// Config is the whole plugin configuration.
type Config struct {
	// unknown holds settings read from the file that mean nothing here. Not a
	// setting itself, so it is neither read from nor written back to JSON.
	unknown []string
	// dropped names the machines in the file that could not be used, in the
	// words of the file rather than the index of a slice nobody can see.
	dropped []string

	// PollInterval is how often each host is polled for pane changes.
	PollInterval string `json:"poll_interval,omitempty"`
	// Session is the Herdr session mirrored on each machine. It defaults to
	// the machine's own default session, so plain `herdr` there shows the
	// shared terminals. Name a session to keep them separate instead.
	Session string `json:"session,omitempty"`
	// Mode is how a machine's terminals are reached, for hosts that do not
	// override it. Defaults to plain SSH panes; mirroring is opt-in.
	Mode Mode `json:"mode,omitempty"`
	// Placement is the default plugin pane placement: split, tab or zoomed.
	Placement string `json:"placement,omitempty"`
	// LabelFormat builds the mirrored pane name. {name} is the remote pane's
	// own label or terminal title, {host} is the host's display label.
	LabelFormat string `json:"label_format,omitempty"`
	// HerdrBin is the remote Herdr binary path used for hosts that do not
	// override it. Empty probes the usual install locations, which is needed
	// because `ssh host <command>` does not run a login shell.
	HerdrBin string `json:"herdr_bin,omitempty"`
	// Workspace is the local workspace label every host's mirrors share. Empty
	// gives each host its own workspace named by WorkspaceFormat.
	Workspace string `json:"workspace,omitempty"`
	// WorkspaceFormat names a host's own workspace and marks it as remote, so
	// it is distinguishable from local workspaces in the sidebar. {host} is the
	// host's display label. A shared Workspace is used verbatim instead.
	//
	// Two spaces after the glyph: a cloud is ambiguous-width, so terminals that
	// draw it in two cells would otherwise crowd the name.
	WorkspaceFormat string `json:"workspace_format,omitempty"`
	// Takeover evicts a stale remote attach when mirroring in attach mode. A
	// direct attach is exclusive and can outlive the pane that started it, so
	// without this a killed mirror blocks its terminal until that process dies.
	Takeover *bool `json:"takeover,omitempty"`
	// WorkspaceFormatDown names a machine's space while it cannot be reached.
	// Herdr joins sidebar tokens with " · ", so a separate marker token always
	// sits a dot away from the name; carrying the marker in the name itself is
	// the only way to have it directly beside it.
	WorkspaceFormatDown string `json:"workspace_format_down,omitempty"`
	// RemoteWorkspaceFormat names the workspace this plugin creates on the
	// remote machine. {hub} is this machine's hostname, so that sitting on the
	// remote you can tell which machine those panes are shared with.
	RemoteWorkspaceFormat string `json:"remote_workspace_format,omitempty"`
	// Scope decides which of a machine's terminals are shared. "shared" mirrors
	// only the space this plugin owns there, so both ends always show the same
	// tabs in the same order. "all" mirrors every pane on the machine, which
	// also surfaces work started there but makes the two sides differ.
	Scope string `json:"scope,omitempty"`
	// ClosePropagates closes the terminal on the machine when its mirror is
	// closed here. Mirroring is otherwise two-way for everything except
	// closing, which is surprising: the tab goes but the work carries on.
	ClosePropagates *bool `json:"close_propagates,omitempty"`
	// CaptureNewPanes replaces a local pane opened inside a machine's space
	// with a terminal on that machine. Herdr's new-tab keybinding and the plus
	// icon in the tab bar both create a local shell, and neither can be
	// intercepted by a plugin, so they are corrected after the fact.
	CaptureNewPanes *bool `json:"capture_new_panes,omitempty"`
	// AutoStart launches the remote Herdr session when it is not running, so
	// a host only needs the herdr binary installed and reachable over SSH.
	AutoStart *bool `json:"auto_start,omitempty"`
	// MaxMirrors caps how many panes one host may mirror, so a remote with a
	// runaway pane count cannot flood the local session.
	MaxMirrors int `json:"max_mirrors,omitempty"`
	// Hosts are the SSH targets to mirror.
	Hosts []Host `json:"hosts"`
}

// Defaults returns a configuration with every optional field populated.
func Defaults() Config {
	return Config{
		PollInterval:          "2s",
		Session:               DefaultSessionName,
		Mode:                  ModeSSH,
		Scope:                 ScopeShared,
		Placement:             "split",
		LabelFormat:           "{name}@{host}",
		WorkspaceFormat:       "☁  {host}",
		WorkspaceFormatDown:   "⚠  {host}",
		RemoteWorkspaceFormat: "☁  {hub}",
		CaptureNewPanes:       boolPtr(true),
		ClosePropagates:       boolPtr(true),
		Takeover:              boolPtr(true),
		AutoStart:             boolPtr(true),
		MaxMirrors:            32,
		Hosts:                 []Host{},
	}
}

// Path returns the config file location inside the plugin config directory.
func Path() (string, error) {
	dir := os.Getenv("HERDR_PLUGIN_CONFIG_DIR")
	if dir == "" {
		return "", errors.New("HERDR_PLUGIN_CONFIG_DIR is not set; run this through Herdr")
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config file, writing a commented default when it is absent.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		cfg := Defaults()
		if err := Save(cfg); err != nil {
			return cfg, err
		}
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	// A byte-order mark, which several editors write and JSON does not allow.
	// Dropped rather than reported: the decoder's complaint names a character
	// that does not appear anywhere in the file as the file's author sees it,
	// and there is nothing to fix in a file that is otherwise correct. Dropped
	// here, before anything reads offsets out of raw, so the line numbers in
	// any later complaint still match the file.
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))

	cfg := Defaults()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, &ParseError{Path: path, Detail: describeJSONError(raw, err)}
	}
	cfg = cfg.normalized()
	cfg.unknown = unknownKeys(raw)
	return cfg, nil
}

// ParseError is a config file that could not be read, holding which file it
// was and what is wrong with it separately.
//
// They are kept apart because the two are worth different amounts depending on
// where the error is shown. In the daemon's log the path matters -- it says
// which of several files to open. In the menu there is room for two lines and
// only ever one plugin config, so a full path spends that room saying something
// the reader already knows, and pushes the part that says what to fix off the
// end.
type ParseError struct {
	Path   string
	Detail string
}

func (e *ParseError) Error() string { return e.Path + ": " + e.Detail }

// describeJSONError says what is wrong with the file in the terms somebody
// editing it is thinking in.
//
// The decoder's own wording is accurate and about Go: "cannot unmarshal string
// into Go struct field Config.max_mirrors of type int". That sentence ends up
// in the menu and in the status listing, where nobody is thinking about Go
// structs -- they are looking at a file they just edited and want to know which
// line to change.
func describeJSONError(raw []byte, err error) string {
	// "unexpected end of JSON input" is what the decoder says about a file with
	// nothing in it, and it reads as though something was cut off partway. An
	// empty config is a state a file gets into by itself -- a truncated write,
	// a redirect that clobbered it -- and the way out is worth saying, since
	// the plugin writes a fresh one whenever the file is missing entirely.
	if len(bytes.TrimSpace(raw)) == 0 {
		return "the file is empty; delete it and a fresh one will be written with the defaults in it"
	}

	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return fmt.Sprintf("%s should be %s, not %s%s",
			plainField(typeErr.Field), plainType(typeErr.Type), plainValue(typeErr.Value),
			atLine(raw, typeErr.Offset))
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Sprintf("%s%s", syntaxErr, atLine(raw, syntaxErr.Offset))
	}
	return err.Error()
}

// plainField names the setting at fault, without the position of the machine
// it is in.
//
// The decoder spells that position differently depending on which Go built the
// plugin -- "hosts.disabled" on some, "hosts.0.disabled" on others -- and this
// is built from source on whatever toolchain the machine has, so leaving it as
// given makes the same broken file produce different wording on two machines.
// The index is no loss: it counts from zero, which does not match how anyone
// reads the file, and the line number that follows says exactly which entry
// anyway.
func plainField(field string) string {
	parts := strings.Split(field, ".")
	kept := parts[:0]
	for _, part := range parts {
		if part == "" || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		return field
	}
	return strings.Join(kept, ".")
}

// atLine says which line an offset falls on, since that is what an editor shows.
func atLine(raw []byte, offset int64) string {
	if offset <= 0 || int(offset) > len(raw) {
		return ""
	}
	return fmt.Sprintf(" (line %d)", 1+bytes.Count(raw[:offset], []byte("\n")))
}

// plainType names a Go type the way the setting reads in the file.
func plainType(t reflect.Type) string {
	if t == nil {
		return "something else"
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "text"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Map, reflect.Struct:
		return "a set of settings"
	default:
		return t.String()
	}
}

// plainValue names what the decoder found, which it reports as a JSON word.
func plainValue(found string) string {
	switch found {
	case "string":
		return "text"
	case "number":
		return "a number"
	case "bool":
		return "true or false"
	case "array":
		return "a list"
	case "object":
		return "a set of settings"
	default:
		return found
	}
}

// unknownKeys lists settings in the file that mean nothing here.
//
// Anything not recognised is dropped in silence by the decoder, so a setting
// spelled wrong, or a per-machine one written at the top level, looks exactly
// like one that is being obeyed. That is the same trouble as a value spelled
// wrong, which is already reported.
func unknownKeys(raw []byte) []string {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}

	var out []string
	known := jsonNames(reflect.TypeOf(Config{}))
	for name := range top {
		if !known[name] {
			out = append(out, name)
		}
	}

	if hosts, ok := top["hosts"]; ok {
		var entries []map[string]json.RawMessage
		if json.Unmarshal(hosts, &entries) == nil {
			knownHost := jsonNames(reflect.TypeOf(Host{}))
			seen := map[string]bool{}
			for _, entry := range entries {
				for name := range entry {
					if !knownHost[name] && !seen[name] {
						seen[name] = true
						out = append(out, "hosts[]."+name)
					}
				}
			}
		}
	}

	sort.Strings(out)
	return out
}

// jsonNames is the set of field names a struct accepts from JSON.
func jsonNames(t reflect.Type) map[string]bool {
	names := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		names[strings.Split(tag, ",")[0]] = true
	}
	return names
}

// Save writes the config back to disk.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return saveRaw(cfg.normalized())
}

// saveRaw writes a configuration exactly as given, without filling anything in.
func saveRaw(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomically(path, append(raw, '\n'))
}

// loadRaw reads the file as written, without filling in defaults.
//
// Changing one setting used to go through Load and Save, both of which fill in
// what is missing, so toggling mirroring wrote back every setting somebody had
// left out, pinned to whatever it defaulted to that day. Nothing changed at the
// time; it did mean those settings quietly stopped following the default.
func loadRaw() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, &ParseError{Path: path, Detail: describeJSONError(raw, err)}
	}
	// Carried like Load does. Without it, toggling mirroring handed the daemon
	// a configuration that had forgotten which of its settings mean nothing,
	// and the warning about them vanished until Herdr was restarted -- with the
	// file unchanged and still wrong.
	cfg.unknown = unknownKeys(raw)
	return cfg, nil
}

// writeFileAtomically replaces a file's contents in one step.
//
// Writing in place truncates first, so an interruption — a crash, a full disk,
// the machine losing power — leaves the file empty or half written. This file
// is edited by hand and holds the list of machines, and it is rewritten
// whenever mirroring is toggled from the menu, so losing it that way is a real
// prospect rather than a theoretical one.
func writeFileAtomically(path string, data []byte) error {
	// A config symlinked into a dotfiles repo has to be written through rather
	// than replaced: renaming onto the link itself would swap it for a regular
	// file and quietly detach it from the repo it belongs to. Resolving first
	// also keeps the temporary file on the same filesystem as its target, which
	// is what lets the rename be atomic at all.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}

	// Keep whatever permissions the file already had; only a file being created
	// for the first time gets the private default.
	perm := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}

	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName) // No-op once the rename below succeeds.

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	// Flush to disk before the rename, so a power loss cannot leave the new
	// name pointing at a file whose contents never arrived.
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempName, perm); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func (c Config) normalized() Config {
	d := Defaults()
	if c.PollInterval == "" {
		c.PollInterval = d.PollInterval
	}
	if c.Session == "" {
		c.Session = d.Session
	}
	if c.Mode == "" {
		c.Mode = d.Mode
	}
	if c.Placement == "" {
		c.Placement = d.Placement
	}
	if c.LabelFormat == "" {
		c.LabelFormat = d.LabelFormat
	}
	if c.WorkspaceFormat == "" {
		c.WorkspaceFormat = d.WorkspaceFormat
	}
	if c.WorkspaceFormatDown == "" {
		c.WorkspaceFormatDown = d.WorkspaceFormatDown
	}
	if c.RemoteWorkspaceFormat == "" {
		c.RemoteWorkspaceFormat = d.RemoteWorkspaceFormat
	}
	if c.Scope == "" {
		c.Scope = d.Scope
	}
	if c.CaptureNewPanes == nil {
		c.CaptureNewPanes = d.CaptureNewPanes
	}
	if c.ClosePropagates == nil {
		c.ClosePropagates = d.ClosePropagates
	}
	if c.Takeover == nil {
		c.Takeover = d.Takeover
	}
	if c.AutoStart == nil {
		c.AutoStart = d.AutoStart
	}
	if c.MaxMirrors <= 0 {
		c.MaxMirrors = d.MaxMirrors
	}
	// A host with no target cannot be reached, and leaving it in produces a
	// space named after nothing and an ssh command with no destination. Which
	// ones went is remembered: dropping an entry somebody deliberately wrote
	// and saying nothing is how a mistyped "targt" turns into a machine that
	// is simply missing from the menu, with nowhere to look for why.
	// A new slice, not a filter in place. Config is taken by value but its
	// Hosts share the caller's backing array, so compacting into it rewrites
	// what the caller is still holding -- and the caller here is whoever asked
	// for a normalized copy, which is not the same as asking for theirs to be
	// changed.
	kept := make([]Host, 0, len(c.Hosts))
	for i, host := range c.Hosts {
		if host.Target != "" {
			kept = append(kept, host)
			continue
		}
		c.dropped = append(c.dropped, describeDropped(i, host))
	}
	c.Hosts = kept
	if c.Hosts == nil {
		c.Hosts = []Host{}
	}
	return c
}

// describeDropped points at a machine entry in terms someone can find it by.
func describeDropped(index int, h Host) string {
	// Its label if it has one -- that is what its author would recognise --
	// and otherwise its position, counted the way the file reads.
	if h.Label != "" {
		return fmt.Sprintf("the machine labelled %q has no target and is ignored", h.Label)
	}
	return fmt.Sprintf("machine %d under hosts has no target and is ignored", index+1)
}

// Interval parses PollInterval, clamping it to something a remote can sustain.
func (c Config) Interval() time.Duration {
	d, err := time.ParseDuration(c.PollInterval)
	if err != nil || d < 500*time.Millisecond {
		return 2 * time.Second
	}
	return d
}

// SessionFor resolves which Herdr session on a machine is mirrored.
//
// The machine's own default session is addressed with an empty HERDR_SESSION,
// so DefaultSessionName maps to "". Naming any other session keeps the shared
// terminals separate from the machine's own work, at the cost of having to run
// `herdr --session <name>` there to see them.
func (c Config) SessionFor(h Host) string {
	name := h.Session
	if name == "" {
		name = c.Session
	}
	if name == DefaultSessionName {
		return ""
	}
	return name
}

func boolPtr(b bool) *bool { return &b }

// SharedOnly reports whether only this machine's own space is mirrored.
func (c Config) SharedOnly() bool {
	return c.Scope != ScopeAll
}

// ShouldClosePropagate reports whether closing a mirror closes the terminal on
// the machine too.
func (c Config) ShouldClosePropagate() bool {
	return c.ClosePropagates == nil || *c.ClosePropagates
}

// ShouldCaptureNewPanes reports whether a local pane opened in a machine's
// space should be replaced with a terminal on that machine.
func (c Config) ShouldCaptureNewPanes() bool {
	return c.CaptureNewPanes == nil || *c.CaptureNewPanes
}

// ShouldTakeover reports whether a stale remote attach may be evicted.
func (c Config) ShouldTakeover() bool {
	return c.Takeover == nil || *c.Takeover
}

// ShouldAutoStart reports whether a missing remote session may be started.
func (c Config) ShouldAutoStart() bool {
	return c.AutoStart == nil || *c.AutoStart
}

// WorkspaceFor resolves the local workspace label a host's mirrors belong in.
//
// Per-host wins, then a shared top-level workspace, then the host's own name.
// Pointing several hosts at one label puts panes from different machines in a
// single layout; the default keeps each machine separate.
func (c Config) WorkspaceFor(h Host) string {
	if h.Workspace != "" {
		return h.Workspace
	}
	if c.Workspace != "" {
		return c.Workspace
	}
	return strings.ReplaceAll(c.WorkspaceFormat, "{host}", h.DisplayLabel())
}

// SharesWorkspace reports whether a machine's terminals land in a space named
// outright rather than one of its own.
//
// Such a space can hold several machines at once, so nothing about one
// machine's state belongs on it: two machines in different states would each
// mark it as their own, every couple of seconds, for as long as both were
// connected.
func (c Config) SharesWorkspace(h Host) bool {
	return h.Workspace != "" || c.Workspace != ""
}

// WorkspaceLabelFor names a host's space for its current reachability.
func (c Config) WorkspaceLabelFor(h Host, reachable bool) string {
	if h.Workspace != "" || c.Workspace != "" {
		// An explicitly chosen name is used as given, marker and all.
		return c.WorkspaceFor(h)
	}
	format := c.WorkspaceFormat
	if !reachable {
		format = c.WorkspaceFormatDown
	}
	return strings.ReplaceAll(format, "{host}", h.DisplayLabel())
}

// RemoteWorkspaceLabel is the workspace label used on the remote machine.
func (c Config) RemoteWorkspaceLabel() string {
	return strings.ReplaceAll(c.RemoteWorkspaceFormat, "{hub}", HubName())
}

// HubName is what this machine is called in the space it creates on another
// one. Exposed because finding that space again needs the name without
// whatever the format puts around it.
func HubName() string {
	return hubName(os.Hostname())
}

// hubName is HubName with the answer handed to it, since os.Hostname cannot be
// made to fail or to come back empty from a test -- and coming back empty is
// the case worth having an answer for. A machine that does not know its own
// name would otherwise name the space it creates on every other machine after
// nothing, and none of those spaces could be told apart.
func hubName(hostname string, err error) string {
	if err != nil || hostname == "" {
		return "herdr"
	}
	return hostname
}

// EffectiveMode is how a machine is actually reached.
//
// A mode that is not recognised falls back to a plain SSH terminal rather than
// mirroring. Treating anything that is not "ssh" as mirroring meant a mode
// spelled wrong silently turned on the experimental feature, which is the
// opposite of what someone typing a typo wants.
func (c Config) EffectiveMode(h Host) Mode {
	switch mode := c.ModeFor(h); mode {
	case ModeAttach, ModeObserve, ModeSSH:
		return mode
	default:
		return ModeSSH
	}
}

// Mirrors reports whether a host's terminals are kept in step with the
// machine, rather than being a plain SSH session.
func (c Config) Mirrors(h Host) bool {
	return c.EffectiveMode(h) != ModeSSH
}

// SetHostMode records a machine's mode on disk, adding the host when it is not
// configured yet, and returns the updated configuration.
func SetHostMode(target string, mode Mode) (Config, error) {
	// The file as written, so that changing one setting does not write back
	// every other one that was being left to its default.
	cfg, err := loadRaw()
	if errors.Is(err, fs.ErrNotExist) {
		// No file yet: Load writes one from the defaults, which is the only
		// time filling them in is what somebody wants.
		if cfg, err = Load(); err != nil {
			return cfg, err
		}
	} else if err != nil {
		return Config{}, err
	}

	found := false
	for i := range cfg.Hosts {
		if cfg.Hosts[i].Target == target {
			cfg.Hosts[i].Mode = mode
			found = true
			break
		}
	}
	if !found {
		cfg.Hosts = append(cfg.Hosts, Host{Target: target, Mode: mode})
	}

	if err := saveRaw(cfg); err != nil {
		return Config{}, err
	}
	// The caller runs on this, so it gets the filled-in one; the file keeps
	// what it had.
	return cfg.normalized(), nil
}

// BinFor resolves the remote Herdr binary for a host. An empty result means
// the path is probed on the remote machine.
func (c Config) BinFor(h Host) string {
	if h.HerdrBin != "" {
		return h.HerdrBin
	}
	return c.HerdrBin
}

// ModeFor resolves the effective bridge mode for a host.
func (c Config) ModeFor(h Host) Mode {
	if h.Mode != "" {
		return h.Mode
	}
	return c.Mode
}

// PlacementFor resolves the effective pane placement for a host.
func (c Config) PlacementFor(h Host) string {
	if h.Placement != "" {
		return h.Placement
	}
	return c.Placement
}
