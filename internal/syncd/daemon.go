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
	"sort"
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

	// configErr records a configuration that could not be read. The daemon
	// still runs, so the menu and actions work and can say what is wrong,
	// rather than everything failing with no visible reason.
	configErr error

	// snapshot is the bookkeeping left by a previous daemon, consulted when a
	// host connects so its panes are adopted rather than reopened.
	snapshot snapshot

	// markedWorkspaces avoids re-reporting the same workspace metadata.
	markedWorkspaces map[string]string

	// rootPanes maps a workspace this daemon created to the shell Herdr opened
	// with it, so that placeholder can be closed once a mirror replaces it.
	rootPanes map[string]string
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

	// workspaceID is the local workspace this host's panes live in, remembered
	// so the remote marker can be updated without another lookup.
	workspaceID string
	// remoteWorkspaceID is this machine's space on the remote one.
	remoteWorkspaceID string
	// pendingPlacement overrides, per remote terminal, how its mirror is placed
	// here, so a "new tab" key gives a tab even when the host normally splits.
	pendingPlacement map[string]string
	// pendingFocus marks mirrors that should take focus when they open, so
	// replacing a pane someone just created leaves them in the new terminal
	// rather than back where they started.
	pendingFocus map[string]bool
	// labels is the name last applied to each of this host's panes.
	labels map[string]string
	// seenStray records local panes in this host's space that were noticed and
	// left alone, so a pane is only considered for capture once.
	seenStray map[string]bool
	// strays is what the last pass decided to move onto the machine, each with
	// the placement to recreate it as. Acted on after the lock is released.
	strays []strayPane
	// reopenShell asks for a replacement SSH terminal, acted on once the lock
	// is released because opening one takes it again.
	reopenShell bool

	// sshOnly marks a host used through plain SSH panes: it has no Herdr, so
	// there is nothing to discover or mirror and reconcile leaves it alone.
	sshOnly bool
	// shellPanes are the plain SSH panes opened for this host, watched so one
	// whose connection drops can be brought back.
	shellPanes map[string]bool
	// restoreShells is how many plain SSH terminals this host had before the
	// daemon restarted, used to bring the connection back.
	restoreShells int

	// adopted marks that the first reconcile after a restart has run. Until it
	// does, mirrors restored from the snapshot whose panes are gone are stale
	// bookkeeping rather than panes the user closed.
	adopted bool

	// reportedAgents remembers what was last reported for each mirror pane, so
	// an unchanged agent is not re-reported on every poll.
	reportedAgents map[string]agentReport

	lastErr error
	// failCount and gaveUp stop a machine being retried forever when it cannot
	// be reached at all.
	failCount int
	gaveUp    bool
	// shellFailures counts terminals that died on this machine in a row, so an
	// unreachable one is not reopened endlessly.
	shellFailures int
}

// agentReport is the agent identity and state last pushed to a mirror pane.
type agentReport struct {
	agent string
	state string
}

// agentSource identifies this plugin as the authority for reported agents.
const agentSource = PluginID

// paneIndex is the local pane snapshot used for one reconcile pass. It also
// records panes created during the pass, so a split mirror can target a
// sibling that did not exist when the snapshot was taken.
type paneIndex struct {
	alive          map[string]bool
	anyInWorkspace map[string]string
	workspaceOf    map[string]string
	panesIn        map[string][]string
	tabOf          map[string]string
	panesPerTab    map[string]int
}

func newPaneIndex(panes []herdrcli.Pane) *paneIndex {
	index := &paneIndex{
		alive:          make(map[string]bool, len(panes)),
		anyInWorkspace: map[string]string{},
		workspaceOf:    map[string]string{},
		panesIn:        map[string][]string{},
		tabOf:          map[string]string{},
		panesPerTab:    map[string]int{},
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
		p.workspaceOf[pane.PaneID] = pane.WorkspaceID
		p.panesIn[pane.WorkspaceID] = append(p.panesIn[pane.WorkspaceID], pane.PaneID)
	}
	if pane.TabID != "" {
		p.tabOf[pane.PaneID] = pane.TabID
		p.panesPerTab[pane.TabID]++
	}
}

// New builds a daemon from the on-disk configuration.
func New(cfg config.Config) *Daemon {
	return NewWithConfigError(cfg, nil)
}

// NewWithConfigError builds a daemon that knows its configuration was not
// readable, so it can report that instead of behaving as if none was set.
func NewWithConfigError(cfg config.Config, configErr error) *Daemon {
	return &Daemon{
		cfg:              cfg,
		hosts:            map[string]*hostSync{},
		rootPanes:        map[string]string{},
		markedWorkspaces: map[string]string{},
		snapshot:         loadSnapshot(),
		configErr:        configErr,
	}
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
		// With no host, reconnect everything configured. That gives a single
		// bindable "bring my remote spaces back" with nothing to select.
		if cmd.Host == "" {
			connected, err := d.connectAll()
			if err != nil {
				return Reply{Message: err.Error()}
			}
			d.reconcileAll()
			return Reply{OK: true, Message: "connected to " + strings.Join(connected, ", ")}
		}
		host, ok := d.hostConfig(cmd.Host)
		if !ok {
			return Reply{Message: fmt.Sprintf("%s is not in the plugin config", cmd.Host)}
		}
		if err := d.connect(host); err != nil {
			return Reply{Message: err.Error()}
		}
		d.reconcileAll()

		// Make the connection visible from both ends. Without this, connecting
		// to a machine that already had terminals mirrored them here but left
		// no trace over there, so from the machine it looked like nothing had
		// happened.
		opened, err := d.ensureRemotePresence(host)
		if err != nil {
			return Reply{Message: fmt.Sprintf("connected to %s, but could not open a terminal: %v",
				host.Target, err)}
		}
		d.reconcileAll()
		if opened {
			return Reply{OK: true, Message: "connected to " + host.Target + " and opened a terminal"}
		}
		return Reply{OK: true, Message: "connected to " + host.Target}

	case "disconnect":
		if err := d.disconnect(cmd.Host); err != nil {
			return Reply{Message: err.Error()}
		}
		return Reply{OK: true, Message: "disconnected from " + cmd.Host}

	case "open":
		host, ok := d.resolveOpenTarget(cmd)
		if !ok {
			// Not a mirrored workspace, so "new terminal" means what it
			// normally means. This keeps one keybinding usable everywhere.
			if cmd.Placement == "tab" {
				if _, err := herdrcli.Run("tab", "create", "--focus"); err != nil {
					return Reply{Message: err.Error()}
				}
				return Reply{OK: true, Message: "opened a local tab"}
			}
			if err := herdrcli.SplitPane("right"); err != nil {
				return Reply{Message: err.Error()}
			}
			return Reply{OK: true, Message: "opened a local pane"}
		}
		if err := d.connect(host); err != nil {
			return Reply{Message: err.Error()}
		}
		if err := d.openRemotePane(host, cmd.Placement, true); err != nil {
			return Reply{Message: err.Error()}
		}
		d.reconcileAll()
		return Reply{OK: true, Message: "opened a terminal on " + host.Target}

	case "set-mode":
		if cmd.Host == "" {
			return Reply{Message: "no machine given"}
		}
		cfg, err := config.SetHostMode(cmd.Host, config.Mode(cmd.Mode))
		if err != nil {
			return Reply{Message: err.Error()}
		}
		d.mu.Lock()
		d.cfg = cfg
		d.mu.Unlock()

		// The mechanism changed, so the machine's panes here no longer match
		// how it is reached. Drop them and connect again under the new mode.
		_ = d.disconnect(cmd.Host)
		host, _ := d.hostConfig(cmd.Host)
		if err := d.connect(host); err != nil {
			return Reply{Message: err.Error()}
		}
		if _, err := d.ensureRemotePresence(host); err != nil {
			return Reply{Message: err.Error()}
		}
		d.reconcileAll()
		if cmd.Mode == string(config.ModeSSH) {
			return Reply{OK: true, Message: "mirroring off for " + cmd.Host}
		}
		return Reply{OK: true, Message: "mirroring on for " + cmd.Host}

	case "refresh":
		d.reconcileAll()
		return Reply{OK: true, Message: "refreshed"}

	case "status":
		return Reply{OK: true, Hosts: d.status(), Warning: d.configWarning()}

	default:
		return Reply{Message: "unknown command " + cmd.Cmd}
	}
}

// openRemotePane creates a new pane on the remote host. The reconcile that
// follows mirrors it back, so opening a pane "on a machine" is one action.
func (d *Daemon) openRemotePane(host config.Host, placement string, focus bool) error {
	d.mu.Lock()
	state, ok := d.hosts[host.Target]
	d.mu.Unlock()
	if !ok {
		return fmt.Errorf("%s is not connected", host.Target)
	}

	if state.sshOnly {
		return d.openShellPane(state)
	}

	workspaceID, created, err := d.ensureRemoteWorkspace(state)
	if err != nil {
		return err
	}
	if created {
		// A new workspace already comes with a pane; that is the terminal.
		// Adding a tab as well would open two for one request.
		return nil
	}
	result, err := state.client.Run("tab", "create", "--workspace", workspaceID)
	if err != nil {
		return err
	}
	d.rememberPlacement(state, result, placement, focus)
	return nil
}

// rememberPlacement records how the mirror of a just-created remote terminal
// should be placed here. The remote reply carries the terminal id, so the
// mirror opened by the next pass can be matched back to this request.
func (d *Daemon) rememberPlacement(state *hostSync, result json.RawMessage, placement string, focus bool) {
	if placement == "" && !focus {
		return
	}
	made, err := herdrcli.ParseCreated(result)
	if err != nil || made.RootPane.TerminalID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if placement != "" {
		state.pendingPlacement[made.RootPane.TerminalID] = placement
	}
	if focus {
		state.pendingFocus[made.RootPane.TerminalID] = true
	}
}

// closeRemoteTerminals closes the machine's terminals behind mirrors that were
// closed here, and forgets the dismissal: the terminal is gone, so there is
// nothing left to avoid reopening.
func (d *Daemon) closeRemoteTerminals(state *hostSync, terminalIDs []string, remotePanes []herdrcli.Pane) {
	if len(terminalIDs) == 0 {
		return
	}
	byTerminal := make(map[string]string, len(remotePanes))
	for _, pane := range remotePanes {
		byTerminal[pane.TerminalID] = pane.PaneID
	}

	for _, terminalID := range terminalIDs {
		paneID, ok := byTerminal[terminalID]
		if !ok {
			// Already gone on the machine; nothing to close.
			delete(state.dismissed, terminalID)
			continue
		}
		if _, err := state.client.Run("pane", "close", paneID); err != nil {
			log.Printf("close %s on %s: %v", paneID, state.host.Target, err)
			continue
		}
		log.Printf("%s: closed terminal %s to match", state.host.Target, paneID)
		delete(state.dismissed, terminalID)
	}
}

// findRemoteWorkspace looks up this machine's space on the remote without
// creating it, for the reconcile path, which should not open terminals.
func (d *Daemon) findRemoteWorkspace(state *hostSync) (bool, error) {
	label := d.cfg.RemoteWorkspaceLabel()
	result, err := state.client.Run("workspace", "list")
	if err != nil {
		return false, err
	}
	workspaces, err := herdrcli.ParseWorkspaceList(result)
	if err != nil {
		return false, err
	}
	if id, ok := herdrcli.FindWorkspace(workspaces, label); ok {
		state.remoteWorkspaceID = id
		return true, nil
	}
	return false, nil
}

// ensureRemoteWorkspace finds or creates the workspace on the remote machine
// that this plugin's panes live in.
//
// It is named after this machine rather than the remote one: sitting on the
// remote, "☁ L14" says who these panes are shared with, whereas a space named
// after the machine you are already on says nothing.
// The bool reports whether the workspace was created just now, in which case
// its root pane is already the terminal that was asked for.
func (d *Daemon) ensureRemoteWorkspace(state *hostSync) (string, bool, error) {
	label := d.cfg.RemoteWorkspaceLabel()

	result, err := state.client.Run("workspace", "list")
	if err != nil {
		return "", false, err
	}
	workspaces, err := herdrcli.ParseWorkspaceList(result)
	if err != nil {
		return "", false, err
	}
	if id, ok := herdrcli.FindWorkspace(workspaces, label); ok {
		state.remoteWorkspaceID = id
		return id, false, nil
	}

	created, err := state.client.Run("workspace", "create", "--label", label)
	if err != nil {
		return "", false, err
	}
	made, err := herdrcli.ParseCreated(created)
	if err != nil {
		return "", false, err
	}
	state.remoteWorkspaceID = made.WorkspaceID
	return state.remoteWorkspaceID, true, nil
}

// resolveOpenTarget decides which machine a new pane belongs to: an explicitly
// named host, otherwise the machine whose mirrors live in the workspace the
// action was invoked from. Creating a terminal while looking at a machine's
// workspace should create it on that machine.
func (d *Daemon) resolveOpenTarget(cmd Command) (config.Host, bool) {
	if cmd.Host != "" {
		return d.hostConfig(cmd.Host)
	}
	if cmd.Workspace == "" {
		return config.Host{}, false
	}

	label := herdrcli.WorkspaceLabel(cmd.Workspace)
	if label == "" {
		return config.Host{}, false
	}
	for _, h := range d.cfg.Hosts {
		if d.cfg.WorkspaceFor(h) == label {
			return h, true
		}
	}
	// Also consider hosts connected ad hoc, which are not in the config file.
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, state := range d.hosts {
		if d.cfg.WorkspaceFor(state.host) == label {
			return state.host, true
		}
	}
	return config.Host{}, false
}

// openShellPane opens a plain SSH pane for a host that is not running Herdr.
// There is no remote pane to mirror, so the pane is the session itself.
func (d *Daemon) openShellPane(state *hostSync) error {
	index := newPaneIndex(nil)
	if local, err := herdrcli.PaneList(); err == nil {
		index = newPaneIndex(local)
	}

	workspaceID, err := d.ensureWorkspace(state, index)
	if err != nil {
		return err
	}

	name := planShellName(d.liveTerminalCount(state.host.Target))

	shellTarget := planPaneTarget(d.cfg.PlacementFor(state.host), workspaceID, index.anyInWorkspace[workspaceID])
	opts := herdrcli.OpenOptions{
		PluginID:   PluginID,
		Entrypoint: paneEntrypoint,
		Placement:  shellTarget.Placement,
		Workspace:  shellTarget.Workspace,
		TargetPane: shellTarget.TargetPane,
		Env: map[string]string{
			mirror.EnvTarget: state.host.Target,
			mirror.EnvMode:   string(config.ModeSSH),
			mirror.EnvName:   name,
		},
	}
	pane, err := herdrcli.OpenPane(opts)
	if err != nil {
		return err
	}
	index.add(pane)
	d.mu.Lock()
	state.shellPanes[pane.PaneID] = true
	d.mu.Unlock()
	d.retitle(state, pane.PaneID, d.label(state.host, herdrcli.Pane{}, name))
	d.retireRootPane(workspaceID, index)
	return nil
}

// configWarning describes an unreadable configuration, if there was one.
func (d *Daemon) configWarning() string {
	if d.configErr == nil {
		return ""
	}
	return fmt.Sprintf("the plugin config could not be read, so no machines are configured: %v", d.configErr)
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

	sshOnly := d.cfg.ModeFor(host) == config.ModeSSH
	var connectErr error
	if sshOnly {
		// Without this an unreachable machine reports "ok", because nothing
		// else in the plain SSH path ever talks to it: the failure only shows
		// up later as terminals that will not stay open.
		connectErr = client.Reachable()
	} else {
		if err := d.prepareRemote(client, host); err != nil {
			// A machine without Herdr is still perfectly usable over plain SSH,
			// so fall back instead of refusing to connect. Anything else — an
			// unreachable host, a broken login — is a real failure, but the
			// host is still registered so it keeps being retried and shows up
			// as unreachable rather than vanishing from status entirely.
			if errors.Is(err, remote.ErrNoHerdr) {
				log.Printf("%s has no herdr; using plain ssh panes", host.Target)
				sshOnly = true
			} else {
				connectErr = err
			}
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	state, ok := d.hosts[host.Target]
	if ok {
		state.host = host
		state.sshOnly = sshOnly
		clear(state.dismissed)
	} else {
		state = &hostSync{
			host:      host,
			client:    client,
			sshOnly:   sshOnly,
			mirrors:   map[string]string{},
			dismissed: map[string]bool{},
			failures:  map[string]int{},
			retryAt:   map[string]time.Time{},

			reportedAgents:   map[string]agentReport{},
			pendingPlacement: map[string]string{},
			pendingFocus:     map[string]bool{},
			labels:           map[string]string{},
			seenStray:        map[string]bool{},
			shellPanes:       map[string]bool{},
		}
		if saved, ok := d.snapshot.Hosts[host.Target]; ok {
			for terminalID, paneID := range saved.Mirrors {
				state.mirrors[terminalID] = paneID
			}
			state.restoreShells = saved.Shells
		}
		d.hosts[host.Target] = state
	}
	state.lastErr = connectErr
	// An explicit connect is a request to try now, so a machine that was given
	// up on is tried again.
	state.failCount = 0
	state.shellFailures = 0
	state.gaveUp = false
	return connectErr
}

// prepareRemote makes sure the host has a Herdr session answering, starting one
// when it is allowed to.
func (d *Daemon) prepareRemote(client *remote.Client, host config.Host) error {
	// Establish that Herdr is usable here before trying to start or talk to a
	// session, so a host without it is recognised as such rather than looking
	// like an unreachable one.
	if err := client.CheckHerdr(); err != nil {
		return err
	}
	if err := client.Ping(); err != nil {
		// The host is reachable but its session is not up yet. Start one and
		// retry before treating this as a failure.
		if !d.cfg.ShouldAutoStart() {
			return err
		}
		if startErr := client.Start(); startErr != nil {
			return startErr
		}
		// The server is launched in the background and needs a moment to bind
		// its socket, so an immediate ping would fail and leave the host
		// disconnected until the next manual connect.
		if retryErr := waitForRemote(client); retryErr != nil {
			return retryErr
		}
	}
	return nil
}

// remoteStartTimeout bounds how long a freshly launched remote session is
// given to come up.
const remoteStartTimeout = 10 * time.Second

// waitForRemote polls a just-started remote session until it answers. The
// server is launched detached over SSH, so it is not listening yet when Start
// returns.
func waitForRemote(client *remote.Client) error {
	deadline := time.Now().Add(remoteStartTimeout)
	var err error
	for attempt := 0; ; attempt++ {
		if err = client.Ping(); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond)
	}
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
	// Persist immediately so a reconnect before the next reconcile does not
	// pick this host's stale bookkeeping back up.
	d.persist()
	return nil
}

// ensureRemotePresence makes this machine's connection visible on the remote
// one, by making sure its space exists there. It reports whether a terminal was
// opened as a result.
//
// A machine that already has terminals gets the space too: seeing "☁ L14" over
// there is how you know the connection is live, and it is where terminals
// opened from here will land.
func (d *Daemon) ensureRemotePresence(host config.Host) (bool, error) {
	d.mu.Lock()
	state, ok := d.hosts[host.Target]
	d.mu.Unlock()
	if !ok {
		return false, fmt.Errorf("%s is not connected", host.Target)
	}

	if state.sshOnly {
		// Nothing to create over there; a plain SSH machine has no Herdr.
		if !planNeedsTerminal(d.liveTerminalCount(host.Target)) {
			return false, nil
		}
		return true, d.openShellPane(state)
	}

	_, created, err := d.ensureRemoteWorkspace(state)
	if err != nil {
		return false, err
	}
	if created {
		// The new space comes with a pane, which mirrors back here.
		return true, nil
	}
	if !planNeedsTerminal(d.liveTerminalCount(host.Target)) {
		return false, nil
	}
	// The space is there but empty of anything to mirror.
	_, err = state.client.Run("tab", "create", "--workspace", state.remoteWorkspaceID)
	return true, err
}

// liveTerminalCount reports how many of a machine's terminals are still open.
func (d *Daemon) liveTerminalCount(target string) int {
	// Counted from the panes Herdr actually has, rather than from bookkeeping:
	// a terminal closed a moment ago may not have been reconciled away yet,
	// and a stale count is what makes a machine impossible to reopen.
	local, err := herdrcli.PaneList()
	if err != nil {
		log.Printf("local pane list: %v", err)
		return 0
	}
	alive := make(map[string]bool, len(local))
	for _, pane := range local {
		alive[pane.PaneID] = true
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	state, ok := d.hosts[target]
	if !ok {
		return 0
	}

	live := 0
	if state.sshOnly {
		for paneID := range state.shellPanes {
			if alive[paneID] {
				live++
			}
		}
		return live
	}
	for _, paneID := range state.mirrors {
		if alive[paneID] {
			live++
		}
	}
	return live
}

// connectAll connects every configured host that is not disabled, reporting
// which ones answered. It fails only when none of them do.
func (d *Daemon) connectAll() ([]string, error) {
	var connected []string
	var lastErr error
	for _, host := range d.cfg.Hosts {
		if host.Disabled {
			continue
		}
		if err := d.connect(host); err != nil {
			log.Printf("connect %s: %v", host.Target, err)
			lastErr = err
			continue
		}
		connected = append(connected, host.Target)
	}
	if len(connected) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no hosts configured")
	}
	return connected, nil
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
			SSHOnly:   state.sshOnly,
			Terminals: len(state.shellPanes),
			Mirroring: d.cfg.Mirrors(state.host),
			GaveUp:    state.gaveUp,
		}
		if state.lastErr != nil {
			info.LastError = summarizeError(state.lastErr)
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

	defer d.persist()

	var wg sync.WaitGroup
	for _, state := range states {
		wg.Add(1)
		go func(state *hostSync) {
			defer wg.Done()
			d.mu.Lock()
			defer d.mu.Unlock()
			if state.gaveUp {
				return
			}
			if err := d.reconcileHost(state, index); err != nil {
				if state.lastErr == nil || state.lastErr.Error() != err.Error() {
					log.Printf("reconcile %s: %v", state.host.Target, err)
				}
				state.lastErr = err
				state.failCount++
				if planGiveUp(state.failCount) {
					state.gaveUp = true
					log.Printf("%s: giving up after %d attempts; connect again to retry",
						state.host.Target, state.failCount)
				}
			} else {
				state.lastErr = nil
				state.failCount = 0
			}
			d.markWorkspaceState(state, state.lastErr == nil)
		}(state)
	}
	wg.Wait()

	// Outside the lock: capturing a stray opens a terminal on the machine,
	// which takes the lock again.
	for _, state := range states {
		d.mu.Lock()
		strays := state.strays
		state.strays = nil
		reopen := state.reopenShell
		state.reopenShell = false
		d.mu.Unlock()

		d.captureStrayPanes(state, strays)
		if reopen {
			if err := d.openShellPane(state); err != nil {
				log.Printf("reopen terminal on %s: %v", state.host.Target, err)
			}
		}
	}
}

// persist records the current mirror bookkeeping for the next daemon.
func (d *Daemon) persist() {
	d.mu.Lock()
	current := snapshot{Hosts: make(map[string]hostSnapshot, len(d.hosts))}
	for target, state := range d.hosts {
		mirrors := make(map[string]string, len(state.mirrors))
		for terminalID, paneID := range state.mirrors {
			mirrors[terminalID] = paneID
		}
		dismissed := make([]string, 0, len(state.dismissed))
		for terminalID := range state.dismissed {
			dismissed = append(dismissed, terminalID)
		}
		sort.Strings(dismissed)
		current.Hosts[target] = hostSnapshot{
			Mirrors:   mirrors,
			Dismissed: dismissed,
			Shells:    len(state.shellPanes),
		}
	}
	d.snapshot = current
	d.mu.Unlock()

	if err := saveSnapshot(current); err != nil {
		log.Printf("save mirror state: %v", err)
	}
}

// reconcileHost brings one host's mirrors in line with its remote panes.
func (d *Daemon) reconcileHost(state *hostSync, index *paneIndex) error {
	// A plain SSH host exposes no panes to discover, so there is nothing to add
	// or retire. Its terminals are still watched, because one whose connection
	// drops would otherwise take the machine's whole space with it.
	if state.sshOnly {
		state.strays = nil

		// A Herdr restart leaves the machine with no terminals and nothing to
		// discover, so the space would simply be missing. Bring back what was
		// open rather than making the connection look lost.
		if !state.adopted {
			state.adopted = true
			if state.restoreShells > 0 {
				workspaceID, wsErr := d.ensureWorkspace(state, index)
				if wsErr != nil {
					return wsErr
				}
				// Herdr restores a plugin pane as a plain shell without
				// re-running its command, so what is left in the space are
				// husks wearing the old name. Clear them before reopening.
				live := 0
				for _, paneID := range index.panesIn[workspaceID] {
					if mirror.IsLive(paneID) {
						live++
						state.shellPanes[paneID] = true
						continue
					}
					if err := herdrcli.ClosePaneByID(paneID); err != nil {
						log.Printf("close stale terminal %s: %v", paneID, err)
					}
					d.forgetPane(state, paneID)
					delete(index.alive, paneID)
				}
				if live == 0 {
					log.Printf("%s: restoring terminal after restart", state.host.Target)
					state.reopenShell = true
					return nil
				}
			}
		}

		for paneID := range state.shellPanes {
			if index.alive[paneID] {
				// A terminal that is up means the machine is fine again.
				state.shellFailures = 0
				continue
			}
			if planLostPane(mirror.Failed(paneID)) {
				state.shellFailures++
				if planGiveUp(state.shellFailures) {
					state.gaveUp = true
					state.lastErr = fmt.Errorf("terminals keep dropping on %s", state.host.Target)
					log.Printf("%s: giving up after %d dropped terminals; connect again to retry",
						state.host.Target, state.shellFailures)
				} else {
					log.Printf("%s: terminal %s dropped, reopening", state.host.Target, paneID)
					state.reopenShell = true
				}
			}
			// Read the failure marker before forgetting the pane, since that
			// is what tells a dropped link from a deliberate close.
			d.forgetPane(state, paneID)
		}

		// With no terminal up, nothing else here ever talks to the machine, so
		// a failure would look like "ok" forever. Checking costs one SSH call
		// and only happens while the machine has nothing running.
		if len(state.shellPanes) == 0 {
			return state.client.Reachable()
		}
		return nil
	}

	// Drop mirrors the user closed by hand and do not reopen them. On the first
	// pass after a restart, a missing pane is stale bookkeeping from a previous
	// daemon rather than a deliberate close, so it is dropped silently.
	var closedHere []string
	for terminalID, paneID := range state.mirrors {
		switch planTrackedMirror(state.adopted, index.alive[paneID], mirror.IsLive(paneID)) {
		case mirrorKeep:
		case mirrorForget:
			delete(state.mirrors, terminalID)
			d.forgetPane(state, paneID)
		case mirrorDismiss:
			delete(state.mirrors, terminalID)
			d.forgetPane(state, paneID)
			state.dismissed[terminalID] = true
			closedHere = append(closedHere, terminalID)
		case mirrorReplace:
			log.Printf("%s: replacing pane %s, its mirror is not running", state.host.Target, paneID)
			if err := herdrcli.ClosePaneByID(paneID); err != nil {
				log.Printf("close stale mirror %s: %v", paneID, err)
			}
			d.forgetPane(state, paneID)
			delete(index.alive, paneID)
			delete(state.mirrors, terminalID)
		}
	}
	state.adopted = true

	// A host whose mirrors were adopted never went through ensureWorkspace, so
	// learn its workspace from a mirror pane rather than looking it up again.
	if state.workspaceID == "" {
		for _, paneID := range state.mirrors {
			if workspaceID, ok := index.workspaceOf[paneID]; ok {
				state.workspaceID = workspaceID
				break
			}
		}
	}

	remotePanes, err := state.client.PaneList()
	if err != nil {
		return err
	}

	// Restrict to this machine's own space on the remote, and put the panes in
	// the order they appear there so the tabs line up on both ends.
	sharedWorkspace := state.remoteWorkspaceID
	if d.cfg.SharedOnly() && sharedWorkspace == "" {
		if found, lookupErr := d.findRemoteWorkspace(state); lookupErr != nil {
			return lookupErr
		} else if found {
			sharedWorkspace = state.remoteWorkspaceID
		}
	}
	tabOrder, err := state.client.TabOrder()
	if err != nil {
		return err
	}
	remotePanes = planSharedPanes(remotePanes, sharedWorkspace, tabOrder, d.cfg.SharedOnly())

	// Closing a mirrored tab closes the terminal on the machine too. Without
	// this, mirroring is two-way for everything except closing: the tab goes
	// and the work quietly carries on over there.
	if d.cfg.ShouldClosePropagate() {
		d.closeRemoteTerminals(state, closedHere, remotePanes)
	}

	labels := d.labelsFor(state.host, remotePanes)

	seen := map[string]bool{}
	for _, rp := range remotePanes {
		if rp.TerminalID == "" {
			continue
		}
		seen[rp.TerminalID] = true

		label := labels[rp.TerminalID]
		if paneID, ok := state.mirrors[rp.TerminalID]; ok {
			d.retitle(state, paneID, label)
			d.syncAgent(state, paneID, rp)
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
		if paneID, ok := state.mirrors[rp.TerminalID]; ok {
			d.syncAgent(state, paneID, rp)
		}
	}

	// Close mirrors whose remote pane is gone, and forget dismissals for
	// terminals that no longer exist so a reused id starts clean.
	for terminalID, paneID := range state.mirrors {
		if !seen[terminalID] {
			if err := herdrcli.ClosePane(paneID); err != nil {
				log.Printf("close mirror %s: %v", paneID, err)
			}
			d.forgetPane(state, paneID)
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

	state.strays = d.planStrayCapture(state, index)
	return nil
}

// strayPane is a local pane to move onto its machine, with the placement its
// replacement should use.
type strayPane struct {
	PaneID    string
	Placement string
}

// planStrayCapture lists local panes sitting in a machine's space that should
// be moved onto that machine. Herdr's plus icon and new-tab key always open a
// local shell and neither can be intercepted by a plugin, so they are corrected
// a moment later instead.
//
// This only decides. Acting on the list needs the host lock released, because
// opening a terminal on the machine takes that lock again.
func (d *Daemon) planStrayCapture(state *hostSync, index *paneIndex) []strayPane {
	if state.sshOnly || state.workspaceID == "" {
		return nil
	}

	mirrors := make(map[string]bool, len(state.mirrors))
	for _, paneID := range state.mirrors {
		mirrors[paneID] = true
	}

	var strays []strayPane
	for _, paneID := range index.panesIn[state.workspaceID] {
		if planStrayPane(d.cfg.ShouldCaptureNewPanes(), mirrors[paneID], state.seenStray[paneID]) {
			strays = append(strays, strayPane{
				PaneID:    paneID,
				Placement: planStrayPlacement(index.panesPerTab[index.tabOf[paneID]]),
			})
		}
		state.seenStray[paneID] = true
	}

	// Forget panes that are gone, so ids reused later are judged afresh.
	for paneID := range state.seenStray {
		if !index.alive[paneID] {
			delete(state.seenStray, paneID)
		}
	}
	return strays
}

// captureStrayPanes carries out what planStrayCapture decided. It must run with
// the host lock released.
func (d *Daemon) captureStrayPanes(state *hostSync, strays []strayPane) {
	for _, stray := range strays {
		log.Printf("%s: moving pane %s onto the machine as a %s",
			state.host.Target, stray.PaneID, stray.Placement)
		if err := d.openRemotePane(state.host, stray.Placement, true); err != nil {
			log.Printf("open terminal on %s: %v", state.host.Target, err)
			continue
		}
		if err := herdrcli.ClosePaneByID(stray.PaneID); err != nil {
			log.Printf("close local pane %s: %v", stray.PaneID, err)
		}
	}
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

// syncAgent mirrors the remote pane's agent identity and status onto the local
// pane, so a remote agent appears in the sidebar with the right name and state
// instead of showing up as a bare ssh pane.
func (d *Daemon) syncAgent(state *hostSync, paneID string, rp herdrcli.Pane) {
	agent := strings.TrimSpace(rp.Agent)
	previous, reported := state.reportedAgents[paneID]

	if agent == "" {
		if reported {
			if err := herdrcli.ReleaseAgent(paneID, agentSource, previous.agent); err != nil {
				log.Printf("release agent %s: %v", paneID, err)
				return
			}
			delete(state.reportedAgents, paneID)
		}
		return
	}

	want := agentReport{agent: agent, state: herdrcli.AgentState(rp.AgentStatus)}
	if reported && previous == want {
		return
	}
	if err := herdrcli.ReportAgent(paneID, agentSource, want.agent, want.state); err != nil {
		log.Printf("report agent %s: %v", paneID, err)
		return
	}
	state.reportedAgents[paneID] = want
}

// retitle names a pane, skipping the call when it already carries that name so
// reconcile does not rename everything on every poll.
//
// What was applied is remembered per host and forgotten when the pane goes.
// Herdr reuses pane ids, so a cache that outlived the pane would decide a
// recycled id already had its name and skip the rename — leaving the new mirror
// showing Herdr's default plugin pane title instead of the machine's.
func (d *Daemon) retitle(state *hostSync, paneID, label string) {
	if state.labels[paneID] == label {
		return
	}
	if err := herdrcli.RenamePane(paneID, label); err != nil {
		log.Printf("rename %s: %v", paneID, err)
		return
	}
	state.labels[paneID] = label
}

// forgetPane drops everything remembered about a pane that has gone, so a
// recycled id starts clean.
func (d *Daemon) forgetPane(state *hostSync, paneID string) {
	delete(state.labels, paneID)
	delete(state.reportedAgents, paneID)
	delete(state.shellPanes, paneID)
	delete(state.seenStray, paneID)
	mirror.ClearLive(paneID)
	mirror.ClearFailed(paneID)
}

// labelsFor names every mirror from one host, keeping them distinguishable.
//
// Unnamed remote panes fall back to their working directory, so several shells
// in the same directory would all render as "deploy@bot". Where that happens
// the remote pane id is appended, which is stable and short.
func (d *Daemon) labelsFor(host config.Host, panes []herdrcli.Pane) map[string]string {
	names := planLabels(panes)
	labels := make(map[string]string, len(panes))
	for _, rp := range panes {
		labels[rp.TerminalID] = d.label(host, rp, names[rp.TerminalID])
	}
	return labels
}

// shortPaneID trims a remote pane id such as "w2:p6" down to "p6".
func shortPaneID(paneID string) string {
	if i := strings.LastIndex(paneID, ":"); i >= 0 && i+1 < len(paneID) {
		return paneID[i+1:]
	}
	return paneID
}

// label renders the configured LabelFormat for a remote pane.
func (d *Daemon) label(host config.Host, rp herdrcli.Pane, name string) string {
	replacer := strings.NewReplacer(
		"{name}", name,
		"{host}", host.DisplayLabel(),
		"{agent}", rp.Agent,
		"{pane}", rp.PaneID,
	)
	return replacer.Replace(d.cfg.LabelFormat)
}

// openMirror creates the local pane that bridges one remote terminal.
func (d *Daemon) openMirror(state *hostSync, rp herdrcli.Pane, label string, index *paneIndex) error {
	workspaceID, err := d.ensureWorkspace(state, index)
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

	placement := d.cfg.PlacementFor(state.host)
	if requested, ok := state.pendingPlacement[rp.TerminalID]; ok {
		placement = requested
		delete(state.pendingPlacement, rp.TerminalID)
	}
	focus := state.pendingFocus[rp.TerminalID]
	delete(state.pendingFocus, rp.TerminalID)

	target := planPaneTarget(placement, workspaceID, index.anyInWorkspace[workspaceID])
	opts := herdrcli.OpenOptions{
		PluginID:   PluginID,
		Entrypoint: paneEntrypoint,
		Placement:  target.Placement,
		Workspace:  target.Workspace,
		TargetPane: target.TargetPane,
		Focus:      focus,
		Env:        env,
	}

	pane, err := herdrcli.OpenPane(opts)
	if err != nil {
		return err
	}

	index.add(pane)
	state.mirrors[rp.TerminalID] = pane.PaneID
	d.retitle(state, pane.PaneID, label)
	d.retireRootPane(workspaceID, index)
	return nil
}

// ensureWorkspace finds or creates the local workspace a host's mirrors belong
// in, resolving by label so several hosts can be pointed at one workspace.
//
// The id is looked up each time rather than cached: a workspace the user closed
// must be recreated, not remembered.
func (d *Daemon) ensureWorkspace(state *hostSync, index *paneIndex) (string, error) {
	label := d.cfg.WorkspaceFor(state.host)

	result, err := herdrcli.Run("workspace", "list")
	if err != nil {
		return "", err
	}
	workspaces, err := herdrcli.ParseWorkspaceList(result)
	if err != nil {
		return "", err
	}
	for _, ws := range workspaces {
		if ws.Label == label || sameWorkspace(ws.Label, state.host.DisplayLabel()) {
			state.workspaceID = ws.WorkspaceID
			return ws.WorkspaceID, nil
		}
	}

	created, err := herdrcli.Run("workspace", "create", "--label", label)
	if err != nil {
		return "", err
	}
	madeLocal, err := herdrcli.ParseCreated(created)
	if err != nil {
		return "", err
	}

	workspaceID := madeLocal.WorkspaceID
	// Herdr opens a local shell with every new workspace. In a workspace that
	// exists only to hold mirrors that is a stray local terminal, so remember
	// it and close it once a mirror has taken its place. It cannot be closed
	// now: a workspace with no panes at all closes itself.
	state.workspaceID = workspaceID

	if root := madeLocal.RootPane.PaneID; root != "" {
		d.rootPanes[workspaceID] = root
		// The index was captured before this workspace existed. Register the
		// new pane so a split has something to target and so the placeholder
		// is recognised as live when it is retired.
		index.add(herdrcli.Pane{PaneID: root, WorkspaceID: workspaceID})
	}
	return workspaceID, nil
}

// markerRunes are the decorations a workspace label may carry in front of the
// host name.
const markerRunes = "☁⛅⚠🔴🟢 \t"

// sameWorkspace reports whether a workspace label names this host, ignoring any
// leading marker. Without this, changing workspace_format would orphan the
// workspace a host's panes already live in.
func sameWorkspace(label, hostLabel string) bool {
	return strings.TrimLeft(label, markerRunes) == hostLabel
}

// Sidebar token names carrying the remote marker. Two names rather than one
// value because the sidebar styles a token by name, which is what lets the
// marker be green while connected and red while not.
const (
	tokenRemoteUp   = "remote_up"
	tokenRemoteDown = "remote_down"
	remoteGlyph     = "☁"
)

// markWorkspaceState publishes the remote marker for a host's workspace,
// reporting whichever token matches the connection state and clearing the
// other. Herdr shows these only where the sidebar template asks for them, so
// the workspace name carries a plain marker too.
func (d *Daemon) markWorkspaceState(state *hostSync, connected bool) {
	workspaceID := state.workspaceID
	if workspaceID == "" {
		return
	}

	want, clear := tokenRemoteUp, tokenRemoteDown
	if !connected {
		want, clear = tokenRemoteDown, tokenRemoteUp
	}
	// Reported on every pass rather than once. It is a local socket call, and
	// re-asserting means the marker comes back if anything else clears it.
	// The marker also lives in the space's name, because Herdr puts " · "
	// between sidebar tokens and a name is the only place a marker can sit
	// directly beside it.
	if label := d.cfg.WorkspaceLabelFor(state.host, connected); label != "" {
		if _, err := herdrcli.Run("workspace", "rename", workspaceID, label); err != nil {
			log.Printf("rename workspace %s: %v", workspaceID, err)
		}
	}

	if _, err := herdrcli.Run("workspace", "report-metadata", workspaceID,
		"--source", agentSource,
		"--token", want+"="+remoteGlyph,
		"--clear-token", clear,
		"--clear-token", "remote"); err != nil {
		if d.markedWorkspaces[workspaceID] != "failed" {
			log.Printf("mark workspace %s: %v", workspaceID, err)
			d.markedWorkspaces[workspaceID] = "failed"
		}
		return
	}
	d.markedWorkspaces[workspaceID] = want
}

// retireRootPane closes the placeholder shell Herdr created with a workspace,
// once a mirror is there to keep the workspace alive.
func (d *Daemon) retireRootPane(workspaceID string, index *paneIndex) {
	root, ok := d.rootPanes[workspaceID]
	if !ok {
		return
	}
	delete(d.rootPanes, workspaceID)

	if !index.alive[root] {
		return
	}
	if err := herdrcli.ClosePaneByID(root); err != nil {
		log.Printf("close placeholder pane %s: %v", root, err)
		return
	}
	delete(index.alive, root)
}
