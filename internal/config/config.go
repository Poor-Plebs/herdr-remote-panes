// Package config loads the user-editable plugin configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	if h.Label != "" {
		return h.Label
	}
	return h.Target
}

// Config is the whole plugin configuration.
type Config struct {
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

	cfg := Defaults()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg.normalized(), nil
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
	raw, err := json.MarshalIndent(cfg.normalized(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
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
	if c.Hosts == nil {
		c.Hosts = []Host{}
	}
	return c
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

// RemoteWorkspaceLabel is the workspace label used on the remote machine.
func (c Config) RemoteWorkspaceLabel() string {
	hub, err := os.Hostname()
	if err != nil || hub == "" {
		hub = "herdr"
	}
	return strings.ReplaceAll(c.RemoteWorkspaceFormat, "{hub}", hub)
}

// Mirrors reports whether a host's terminals are kept in step with the
// machine, rather than being a plain SSH session.
func (c Config) Mirrors(h Host) bool {
	return c.ModeFor(h) != ModeSSH
}

// SetHostMode records a machine's mode on disk, adding the host when it is not
// configured yet, and returns the updated configuration.
func SetHostMode(target string, mode Mode) (Config, error) {
	cfg, err := Load()
	if err != nil {
		return cfg, err
	}
	for i := range cfg.Hosts {
		if cfg.Hosts[i].Target == target {
			cfg.Hosts[i].Mode = mode
			return cfg, Save(cfg)
		}
	}
	cfg.Hosts = append(cfg.Hosts, Host{Target: target, Mode: mode})
	return cfg, Save(cfg)
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
