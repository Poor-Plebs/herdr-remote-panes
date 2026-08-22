// Package config loads the user-editable plugin configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Mode selects how a remote pane is bridged into a local mirror pane.
type Mode string

const (
	// DefaultSession is the remote Herdr session mirrored unless configured
	// otherwise. Herdr's own default session is left alone so mirroring never
	// competes with the work already running there.
	DefaultSession = "remote"
	// DefaultSessionName opts a host back into the remote's unnamed default
	// session.
	DefaultSessionName = "default"
)

const (
	// ModeAttach runs `herdr terminal attach` over SSH: fully interactive, but
	// exclusive per remote terminal and it pins the remote terminal size.
	ModeAttach Mode = "attach"
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
	// Session is the remote HERDR_SESSION mirrored by default. It defaults to
	// "remote" so the hub never disturbs whatever the user is doing in the
	// remote machine's own default session.
	Session string `json:"session,omitempty"`
	// Mode is the default bridge mode for hosts that do not override it.
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
	// gives each host its own workspace named after it.
	Workspace string `json:"workspace,omitempty"`
	// Takeover evicts a stale remote attach when mirroring in attach mode. A
	// direct attach is exclusive and can outlive the pane that started it, so
	// without this a killed mirror blocks its terminal until that process dies.
	Takeover *bool `json:"takeover,omitempty"`
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
		PollInterval: "2s",
		Session:      DefaultSession,
		Mode:         ModeAttach,
		Placement:    "split",
		LabelFormat:  "{name}@{host}",
		Takeover:     boolPtr(true),
		AutoStart:    boolPtr(true),
		MaxMirrors:   32,
		Hosts:        []Host{},
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

// SessionFor resolves which remote Herdr session a host is mirrored from.
//
// Mirroring a dedicated session by default keeps the hub out of the remote's
// default session. Naming the session DefaultSessionName opts back into the
// remote's unnamed default session, which Herdr addresses with an empty
// HERDR_SESSION.
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
	return h.DisplayLabel()
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
