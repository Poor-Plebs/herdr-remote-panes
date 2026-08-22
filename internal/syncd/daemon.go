// Package syncd runs the long-lived reconciler that keeps local mirror panes
// in step with the panes on each configured remote host.
package syncd

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/mirror"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
)

// PluginID must match the id in herdr-plugin.toml.
const PluginID = "poorplebs.remote-panes"

// paneEntrypoint must match the [[panes]] id in herdr-plugin.toml.
const paneEntrypoint = "mirror"

// Daemon reconciles remote panes into local mirror panes.
type Daemon struct {
	cfg config.Config

	mu    sync.Mutex
	hosts map[string]*hostSync
}

// hostSync is the live state for one SSH target.
type hostSync struct {
	host   config.Host
	client *remote.Client

	// mirrors maps a remote terminal id to the local pane showing it.
	mirrors map[string]string
	// dismissed remembers mirrors the user closed by hand, so the next poll
	// does not immediately reopen them.
	dismissed map[string]bool
	// failures and retryAt back off a terminal that will not mirror, so a
	// persistent error cannot spawn a new pane on every tick.
	failures map[string]int
	retryAt  map[string]time.Time

	lastErr error
}

// paneIndex is the local pane snapshot used for one reconcile pass. It also
// records panes created during the pass, so a split mirror can target a
// sibling that did not exist when the snapshot was taken.
type paneIndex struct {
	alive          map[string]bool
	anyInWorkspace map[string]string
}

func newPaneIndex(panes []herdrcli.Pane) *paneIndex {
	index := &paneIndex{
		alive:          make(map[string]bool, len(panes)),
		anyInWorkspace: map[string]string{},
	}
	for _, p := range panes {
		index.add(p)
	}
	return index
}

func (p *paneIndex) add(pane herdrcli.Pane) {
	p.alive[pane.PaneID] = true
	if pane.WorkspaceID != "" {
		p.anyInWorkspace[pane.WorkspaceID] = pane.PaneID
	}
}

// New builds a daemon from the on-disk configuration.
func New(cfg config.Config) *Daemon {
	return &Daemon{cfg: cfg, hosts: map[string]*hostSync{}}
}

// Run starts the control listener and blocks, polling every connected host.
func (d *Daemon) Run() error {
	socket, err := ControlSocket()
	if err != nil {
		return err
	}
	listener, err := listenControl(socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)

	go d.serveControl(listener)

	for _, h := range d.cfg.Hosts {
		if h.Disabled {
			continue
		}
		if err := d.connect(h); err != nil {
			log.Printf("connect %s: %v", h.Target, err)
		}
	}

	ticker := time.NewTicker(d.cfg.Interval())
	defer ticker.Stop()
	for range ticker.C {
		d.reconcileAll()
	}
	return nil
}

// listenControl binds the control socket, clearing a socket left behind by a
// daemon that did not shut down cleanly.
func listenControl(socket string) (net.Listener, error) {
	listener, err := net.Listen("unix", socket)
	if err == nil {
		return listener, nil
	}
	if conn, dialErr := net.DialTimeout("unix", socket, time.Second); dialErr == nil {
		conn.Close()
		return nil, fmt.Errorf("another herdr-remote-panes daemon is already running")
	}
	if rmErr := os.Remove(socket); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return nil, err
	}
	return net.Listen("unix", socket)
}

func (d *Daemon) serveControl(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go d.handleControl(conn)
	}
}

func (d *Daemon) handleControl(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	var cmd Command
	if err := json.NewDecoder(conn).Decode(&cmd); err != nil {
		_ = json.NewEncoder(conn).Encode(Reply{Message: "bad request"})
		return
	}
	_ = json.NewEncoder(conn).Encode(d.dispatch(cmd))
}

func (d *Daemon) dispatch(cmd Command) Reply {
	switch cmd.Cmd {
	case "connect":
		host, ok := d.hostConfig(cmd.Host)
		if !ok {
			return Reply{Message: fmt.Sprintf("%s is not in the plugin config", cmd.Host)}
		}
		if err := d.connect(host); err != nil {
			return Reply{Message: err.Error()}
		}
		d.reconcileAll()
		return Reply{OK: true, Message: "connected to " + host.Target}

	case "disconnect":
		if err := d.disconnect(cmd.Host); err != nil {
			return Reply{Message: err.Error()}
		}
		return Reply{OK: true, Message: "disconnected from " + cmd.Host}

	case "open":
		host, ok := d.hostConfig(cmd.Host)
		if !ok {
			return Reply{Message: fmt.Sprintf("%s is not in the plugin config", cmd.Host)}
		}
		if err := d.connect(host); err != nil {
			return Reply{Message: err.Error()}
		}
		if err := d.openRemotePane(host); err != nil {
			return Reply{Message: err.Error()}
		}
		d.reconcileAll()
		return Reply{OK: true, Message: "opened a pane on " + host.Target}

	case "refresh":
		d.reconcileAll()
		return Reply{OK: true, Message: "refreshed"}

	case "status":
		return Reply{OK: true, Hosts: d.status()}

	default:
		return Reply{Message: "unknown command " + cmd.Cmd}
	}
}

// openRemotePane creates a new pane on the remote host. The reconcile that
// follows mirrors it back, so opening a pane "on a machine" is one action.
func (d *Daemon) openRemotePane(host config.Host) error {
	d.mu.Lock()
	state, ok := d.hosts[host.Target]
	d.mu.Unlock()
	if !ok {
		return fmt.Errorf("%s is not connected", host.Target)
	}

	// A freshly started session has no workspace to put a tab in.
	result, err := state.client.Run("workspace", "list")
	if err != nil {
		return err
	}
	var body struct {
		Workspaces []struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return fmt.Errorf("parse remote workspace list: %w", err)
	}
	if len(body.Workspaces) == 0 {
		_, err = state.client.Run("workspace", "create", "--label", host.DisplayLabel())
		return err
	}
	_, err = state.client.Run("tab", "create")
	return err
}

// hostConfig finds a configured host by target or label.
func (d *Daemon) hostConfig(name string) (config.Host, bool) {
	for _, h := range d.cfg.Hosts {
		if h.Target == name || h.Label == name {
			return h, true
		}
	}
	// An unconfigured target is still usable: treat it as an ad-hoc host so
	// `connect` works against anything in the user's ssh config.
	if name != "" {
		return config.Host{Target: name}, true
	}
	return config.Host{}, false
}

func (d *Daemon) connect(host config.Host) error {
	client := remote.NewWithBin(host.Target, d.cfg.SessionFor(host), d.cfg.BinFor(host))
	if err := client.Ping(); err != nil {
		// The host is reachable but its session is not up yet. Start one and
		// retry before treating this as a failure.
		if !d.cfg.ShouldAutoStart() {
			return err
		}
		if startErr := client.Start(); startErr != nil {
			return startErr
		}
		if retryErr := client.Ping(); retryErr != nil {
			return retryErr
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, ok := d.hosts[host.Target]; ok {
		existing.host = host
		return nil
	}
	d.hosts[host.Target] = &hostSync{
		host:      host,
		client:    client,
		mirrors:   map[string]string{},
		dismissed: map[string]bool{},
		failures:  map[string]int{},
		retryAt:   map[string]time.Time{},
	}
	return nil
}

func (d *Daemon) disconnect(target string) error {
	d.mu.Lock()
	state, ok := d.hosts[target]
	if ok {
		delete(d.hosts, target)
	}
	d.mu.Unlock()

	if !ok {
		return fmt.Errorf("%s is not connected", target)
	}
	for _, paneID := range state.mirrors {
		if err := herdrcli.ClosePane(paneID); err != nil {
			log.Printf("close mirror %s: %v", paneID, err)
		}
	}
	state.client.Close()
	return nil
}

func (d *Daemon) status() []HostInfo {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]HostInfo, 0, len(d.hosts))
	for _, state := range d.hosts {
		info := HostInfo{
			Target:    state.host.Target,
			Label:     state.host.DisplayLabel(),
			Connected: state.lastErr == nil,
			Mirrors:   len(state.mirrors),
		}
		if state.lastErr != nil {
			info.LastError = state.lastErr.Error()
		}
		out = append(out, info)
	}
	return out
}

func (d *Daemon) reconcileAll() {
	d.mu.Lock()
	states := make([]*hostSync, 0, len(d.hosts))
	for _, state := range d.hosts {
		states = append(states, state)
	}
	d.mu.Unlock()

	if len(states) == 0 {
		return
	}

	local, err := herdrcli.PaneList()
	if err != nil {
		log.Printf("local pane list: %v", err)
		return
	}
	index := newPaneIndex(local)

	var wg sync.WaitGroup
	for _, state := range states {
		wg.Add(1)
		go func(state *hostSync) {
			defer wg.Done()
			d.mu.Lock()
			defer d.mu.Unlock()
			if err := d.reconcileHost(state, index); err != nil {
				if state.lastErr == nil || state.lastErr.Error() != err.Error() {
					log.Printf("reconcile %s: %v", state.host.Target, err)
				}
				state.lastErr = err
			} else {
				state.lastErr = nil
			}
		}(state)
	}
	wg.Wait()
}

// reconcileHost brings one host's mirrors in line with its remote panes.
func (d *Daemon) reconcileHost(state *hostSync, index *paneIndex) error {
	// Drop mirrors the user closed by hand and do not reopen them.
	for terminalID, paneID := range state.mirrors {
		if !index.alive[paneID] {
			delete(state.mirrors, terminalID)
			state.dismissed[terminalID] = true
		}
	}

	remotePanes, err := state.client.PaneList()
	if err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, rp := range remotePanes {
		if rp.TerminalID == "" {
			continue
		}
		seen[rp.TerminalID] = true

		label := d.label(state.host, rp)
		if paneID, ok := state.mirrors[rp.TerminalID]; ok {
			d.retitle(paneID, label)
			continue
		}
		if state.dismissed[rp.TerminalID] {
			continue
		}
		if until, ok := state.retryAt[rp.TerminalID]; ok && time.Now().Before(until) {
			continue
		}
		if len(state.mirrors) >= d.cfg.MaxMirrors {
			log.Printf("%s: mirror limit of %d reached, skipping the rest",
				state.host.Target, d.cfg.MaxMirrors)
			break
		}
		if err := d.openMirror(state, rp, label, index); err != nil {
			d.backOff(state, rp.TerminalID, err)
			continue
		}
		delete(state.failures, rp.TerminalID)
		delete(state.retryAt, rp.TerminalID)
	}

	// Close mirrors whose remote pane is gone, and forget dismissals for
	// terminals that no longer exist so a reused id starts clean.
	for terminalID, paneID := range state.mirrors {
		if !seen[terminalID] {
			if err := herdrcli.ClosePane(paneID); err != nil {
				log.Printf("close mirror %s: %v", paneID, err)
			}
			delete(state.mirrors, terminalID)
		}
	}
	for terminalID := range state.dismissed {
		if !seen[terminalID] {
			delete(state.dismissed, terminalID)
		}
	}
	for terminalID := range state.retryAt {
		if !seen[terminalID] {
			delete(state.retryAt, terminalID)
			delete(state.failures, terminalID)
		}
	}
	return nil
}

// maxMirrorAttempts is how often one remote terminal may fail to mirror before
// it is given up on until it disappears and comes back.
const maxMirrorAttempts = 5

// backOff records a failed mirror attempt and schedules the next one.
func (d *Daemon) backOff(state *hostSync, terminalID string, cause error) {
	state.failures[terminalID]++
	attempts := state.failures[terminalID]
	log.Printf("mirror %s %s (attempt %d): %v",
		state.host.Target, terminalID, attempts, cause)

	if attempts >= maxMirrorAttempts {
		state.dismissed[terminalID] = true
		delete(state.retryAt, terminalID)
		return
	}
	delay := time.Duration(1<<attempts) * time.Second
	state.retryAt[terminalID] = time.Now().Add(delay)
}

// labels tracks the last label applied to each mirror so reconcile does not
// rename a pane on every poll.
var labels sync.Map // paneID -> label

func (d *Daemon) retitle(paneID, label string) {
	if prev, ok := labels.Load(paneID); ok && prev.(string) == label {
		return
	}
	if err := herdrcli.RenamePane(paneID, label); err != nil {
		log.Printf("rename %s: %v", paneID, err)
		return
	}
	labels.Store(paneID, label)
}

// label renders the configured LabelFormat for a remote pane.
func (d *Daemon) label(host config.Host, rp herdrcli.Pane) string {
	replacer := strings.NewReplacer(
		"{name}", rp.DisplayName(),
		"{host}", host.DisplayLabel(),
		"{agent}", rp.Agent,
		"{pane}", rp.PaneID,
	)
	return replacer.Replace(d.cfg.LabelFormat)
}

// openMirror creates the local pane that bridges one remote terminal.
func (d *Daemon) openMirror(state *hostSync, rp herdrcli.Pane, label string, index *paneIndex) error {
	workspaceID, err := d.ensureWorkspace(state)
	if err != nil {
		return err
	}

	env := map[string]string{
		mirror.EnvTarget:   state.host.Target,
		mirror.EnvSession:  d.cfg.SessionFor(state.host),
		mirror.EnvTerminal: rp.TerminalID,
		mirror.EnvMode:     string(d.cfg.ModeFor(state.host)),
		mirror.EnvName:     label,
	}
	if bin := d.cfg.BinFor(state.host); bin != "" {
		env[mirror.EnvBin] = bin
	}
	if !d.cfg.ShouldTakeover() {
		env[mirror.EnvTakeover] = "false"
	}

	opts := herdrcli.OpenOptions{
		PluginID:   PluginID,
		Entrypoint: paneEntrypoint,
		Placement:  d.cfg.PlacementFor(state.host),
		Env:        env,
	}

	// Herdr treats these targets as mutually exclusive. A split or zoomed pane
	// takes only a target pane, which implies its workspace; a tab takes only a
	// workspace; an overlay takes neither and lands on the active pane. Sending
	// both is rejected outright.
	switch opts.Placement {
	case "split", "zoomed":
		target := index.anyInWorkspace[workspaceID]
		if target == "" {
			// Nothing to split from, so fall back to a tab in that workspace.
			opts.Placement = "tab"
			opts.Workspace = workspaceID
			break
		}
		opts.TargetPane = target
	case "overlay", "popup":
		// Targeting is not allowed; the pane opens over the active one.
	default:
		opts.Workspace = workspaceID
	}

	pane, err := herdrcli.OpenPane(opts)
	if err != nil {
		return err
	}

	index.add(pane)
	state.mirrors[rp.TerminalID] = pane.PaneID
	d.retitle(pane.PaneID, label)
	return nil
}

// ensureWorkspace finds or creates the local workspace a host's mirrors belong
// in, resolving by label so several hosts can be pointed at one workspace.
//
// The id is looked up each time rather than cached: a workspace the user closed
// must be recreated, not remembered.
func (d *Daemon) ensureWorkspace(state *hostSync) (string, error) {
	label := d.cfg.WorkspaceFor(state.host)

	result, err := herdrcli.Run("workspace", "list")
	if err != nil {
		return "", err
	}
	var body struct {
		Workspaces []struct {
			WorkspaceID string `json:"workspace_id"`
			Label       string `json:"label"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return "", fmt.Errorf("parse workspace list: %w", err)
	}
	for _, ws := range body.Workspaces {
		if ws.Label == label {
			return ws.WorkspaceID, nil
		}
	}

	created, err := herdrcli.Run("workspace", "create", "--label", label)
	if err != nil {
		return "", err
	}
	var createdBody struct {
		Workspace struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(created, &createdBody); err != nil {
		return "", fmt.Errorf("parse workspace create: %w", err)
	}
	return createdBody.Workspace.WorkspaceID, nil
}
