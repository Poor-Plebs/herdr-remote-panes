// Package syncd runs the long-lived reconciler behind this plugin.
//
// It keeps one SSH connection per machine and makes sure the panes here match
// what each machine should be showing. By default that is a plain SSH terminal
// per machine, opened locally and talking to nothing on the far side; with
// mirroring turned on, which is experimental, it is a pane for each terminal
// the machine has, kept in step in both directions.
package syncd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/mirror"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/sshconfig"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/version"
	"io"
)

// PluginID must match the id in herdr-plugin.toml.
const PluginID = "poorplebs.remote-panes"

// paneEntrypoint must match the [[panes]] id in herdr-plugin.toml.
const paneEntrypoint = "mirror"

// Daemon keeps the panes here in step with the machines they belong to.
type Daemon struct {
	// cfg is replaced wholesale when a mode is toggled from the menu. Control
	// connections are each handled in their own goroutine, so a command that
	// changes it runs alongside commands that read it. Holding it atomically
	// rather than under the lock means a read is safe from anywhere, including
	// the reconcile paths that already hold the lock -- guarding it with the
	// lock instead would have meant auditing three dozen call sites for which
	// ones already held it, and getting one wrong is a deadlock.
	cfg atomic.Pointer[config.Config]

	mu    sync.Mutex
	hosts map[string]*hostSync

	// configErr records a configuration that could not be read. The daemon
	// still runs, so the menu and actions work and can say what is wrong,
	// rather than everything failing with no visible reason.
	configErr error

	// snapshot is the bookkeeping left by a previous daemon, consulted when a
	// host connects so its panes are adopted rather than reopened.
	snapshot snapshot

	// lastSaved is the snapshot as it was last written to disk, so one that has
	// not changed is not written again.
	lastSaved []byte

	// seenStray records local panes already considered for moving onto a
	// machine, so one is acted on once rather than on every pass. Held here
	// rather than per machine: a pane belongs to one machine at most, and when
	// several share a space each of them used to claim the same stray pane and
	// open a terminal of its own for it.
	seenStray map[string]bool

	// reconciles folds overlapping reconcile requests into one.
	reconciles coalescer

	// touched remembers every machine this daemon has connected, so persisting
	// can tell one it has dealt with from one it has not reached yet.
	touched map[string]bool

	// markedWorkspaces is what was last put on each space, and when.
	markedWorkspaces map[string]workspaceMark

	// lastPrune is when marks left behind by vanished panes were last cleared,
	// as Unix nanoseconds. This used to happen once, on the first pane listing
	// after startup, which left every mark dropped later in the session in
	// place until the daemon was restarted -- and Herdr reuses pane ids, so a
	// stale mark is eventually read as belonging to a different pane.
	lastPrune atomic.Int64

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
	// abandoned are terminals whose mirror failed too many times. Kept apart
	// from dismissed, which means someone closed the pane: the two block
	// mirroring alike but deserve different treatment across a restart, since
	// restarting is exactly when a mirror that kept failing is worth another go.
	abandoned map[string]bool
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
	// strays is what the last pass decided to move onto the machine, each with
	// the placement to recreate it as. Acted on after the lock is released.
	strays []strayPane
	// reopenShell asks for a replacement SSH terminal, acted on once the lock
	// is released because opening one takes it again.
	reopenShell bool

	// sshOnly marks a host used through plain SSH panes: it has no Herdr, so
	// there is nothing to discover or mirror and reconcile leaves it alone.
	sshOnly bool
	// noHerdr is set when mirroring was asked for and the machine turned out
	// not to have Herdr where this could find it. The machine still works —
	// falling back is better than refusing — but the setting says one thing
	// and the machine is doing another, and only the log knew.
	noHerdr bool
	// atCapacity is a machine with more terminals than max_mirrors allows. The
	// ones over the limit are simply not mirrored, which looks from here like a
	// machine with fewer terminals than it has -- and the number is a setting,
	// so somebody can do something about it if they are told.
	atCapacity bool
	// outsideShared is how many terminals the machine has in spaces of its own,
	// which the default scope does not mirror. Not a failure -- it is the
	// setting doing what it says -- but indistinguishable from one without
	// being told.
	outsideShared int
	// duplicateSpaces is more than one space on the machine answering to the
	// name this machine's terminals live under.
	duplicateSpaces bool
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
	// linkRetryAt is when a machine given up on may try again by itself, and is
	// only ever set when every machine went down together. Zero means it waits
	// to be connected to, which is what a machine that failed on its own does.
	//
	// Not the retryAt above: that one backs off a single terminal whose mirror
	// will not start, and is per terminal. This is the whole machine, and is
	// about the link to it.
	linkRetryAt time.Time
	// linkRetryStep is how many automatic retries this machine has already had,
	// so a link that stays down backs off instead of retrying every few seconds
	// for as long as it lasts.
	linkRetryStep int
	// lastReopen is when a terminal was last opened again, which is how long
	// the current one has had to stay up.
	lastReopen time.Time

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
	labelOf        map[string]string
	tabOf          map[string]string
	panesPerTab    map[string]int
}

func newPaneIndex(panes []herdrcli.Pane) *paneIndex {
	index := &paneIndex{
		alive:          make(map[string]bool, len(panes)),
		anyInWorkspace: map[string]string{},
		workspaceOf:    map[string]string{},
		panesIn:        map[string][]string{},
		labelOf:        map[string]string{},
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
	p.labelOf[pane.PaneID] = pane.Label
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

// remove takes a pane out of the listing, once this pass has closed it.
//
// Every field, not just the one the caller happened to think of. Marking a pane
// no longer alive and leaving it named as the pane to split from is how a
// replacement mirror came to be opened beside the pane it was replacing, a
// moment after that pane was closed: Herdr answered pane_not_found, the mirror
// never opened, and the machine was then judged to have no terminals and given
// a spare one -- every restart, for as long as it was mirrored.
func (p *paneIndex) remove(paneID string) {
	delete(p.alive, paneID)
	delete(p.labelOf, paneID)

	if workspaceID, ok := p.workspaceOf[paneID]; ok {
		delete(p.workspaceOf, paneID)

		// A new slice, not a filter in place. Both callers walk a space's
		// panes and remove the ones they close as they go, and filtering into
		// the array their range is reading shifted the pane after the removed
		// one down into a position already passed: the walk skipped it and
		// looked at a later one twice. A pane skipped by the loop that clears
		// husks is never revisited -- that clearing happens once, when the
		// machine is adopted.
		kept := make([]string, 0, len(p.panesIn[workspaceID]))
		for _, id := range p.panesIn[workspaceID] {
			if id != paneID {
				kept = append(kept, id)
			}
		}
		p.panesIn[workspaceID] = kept

		// Somewhere else to split from, or nowhere: a space whose last pane has
		// gone has no pane to place anything beside.
		if p.anyInWorkspace[workspaceID] == paneID {
			delete(p.anyInWorkspace, workspaceID)
			for _, id := range kept {
				if p.alive[id] {
					p.anyInWorkspace[workspaceID] = id
					break
				}
			}
		}
	}

	if tabID, ok := p.tabOf[paneID]; ok {
		delete(p.tabOf, paneID)
		if p.panesPerTab[tabID] > 0 {
			p.panesPerTab[tabID]--
		}
	}
}

// config reports the current configuration.
func (d *Daemon) config() config.Config {
	if cfg := d.cfg.Load(); cfg != nil {
		return *cfg
	}
	return config.Config{}
}

// setConfig replaces the configuration, for when a mode is toggled.
func (d *Daemon) setConfig(cfg config.Config) {
	d.cfg.Store(&cfg)
}

// New builds a daemon from the on-disk configuration.
func New(cfg config.Config) *Daemon {
	return NewWithConfigError(cfg, nil)
}

// NewWithConfigError builds a daemon that knows its configuration was not
// readable, so it can report that instead of behaving as if none was set.
func NewWithConfigError(cfg config.Config, configErr error) *Daemon {
	d := &Daemon{
		hosts:            map[string]*hostSync{},
		rootPanes:        map[string]string{},
		touched:          map[string]bool{},
		markedWorkspaces: map[string]workspaceMark{},
		seenStray:        map[string]bool{},
		snapshot:         loadSnapshot(),
		configErr:        configErr,
	}
	d.setConfig(cfg)
	return d
}

// rememberedHosts lists machines the snapshot says were connected and that
// starting up has not already dealt with.
func (d *Daemon) rememberedHosts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	remembered := make([]string, 0, len(d.snapshot.Hosts))
	for target := range d.snapshot.Hosts {
		remembered = append(remembered, target)
	}
	connected := make(map[string]bool, len(d.hosts))
	for target := range d.hosts {
		connected[target] = true
	}
	disabled := map[string]bool{}
	for _, h := range d.config().Hosts {
		if h.Disabled {
			disabled[h.Target] = true
		}
	}
	return planSnapshotRestore(remembered, connected, disabled)
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
	// Where, and that it got that far. A healthy daemon says nothing else for
	// the rest of its life, so a log holding "starting" and nothing after it
	// reads the same whether the socket was bound or the binding is what went
	// wrong -- and "no running daemon" is the report either way.
	log.Printf("listening on %s", socket)
	// Closing is the whole of it: a Unix listener unlinks the path it bound.
	//
	// Removing the file separately as well is a hazard now that a replacing
	// daemon waits for this socket rather than giving up on it. The two ran as
	// separate deferred calls, so between them the path is unowned — and a
	// daemon that binds in that gap has its brand new socket deleted by the
	// old one finishing its cleanup, leaving it listening on a path nothing
	// can reach. Which looks, from every action, exactly like no daemon at
	// all.
	//
	// A socket left behind by a daemon that was killed before any of this ran
	// is cleared by the next one, which finds nothing answering it.
	defer listener.Close()

	go d.serveControl(listener)

	// Every configured machine at once. One at a time, each unreachable one
	// costs its whole connect timeout before the next is even tried, so a
	// couple of them and the machines that are fine are still not back.
	var configured []config.Host
	for _, h := range d.config().Hosts {
		if !h.Disabled {
			configured = append(configured, h)
		}
	}
	d.connectEach(configured)

	// Then the machines that were connected but are not written down: one
	// picked from ~/.ssh/config never reaches the config file, so the snapshot
	// is the only record that it was there at all.
	//
	// After the configured ones rather than alongside them, because which
	// machines are remembered is worked out from which are already connected.
	var remembered []config.Host
	for _, target := range d.rememberedHosts() {
		if host, err := d.hostConfig(target); err == nil {
			remembered = append(remembered, host)
		}
	}
	d.connectEach(remembered)

	// Herdr stops a plugin's startup process with a signal, and the default
	// action is to die on the spot: the deferred cleanup above never ran, so
	// the control socket was left behind on every shutdown. When the state
	// directory is deep enough that the socket lives in the temp directory
	// instead, nothing tidies it afterwards either, because the path it was
	// derived from has gone.
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stopping)

	ticker := time.NewTicker(d.config().Interval())
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.reconcileAll()
		case sig := <-stopping:
			log.Printf("stopping on %s", sig)
			return nil
		}
	}
}

// handoverWait is how long a starting daemon will wait for the one it is
// replacing to let go of the socket.
//
// Restarting Herdr starts this one before the old one has finished: they
// overlap, and the socket belongs to the old one until it does. Refusing there
// leaves nothing running at all -- Herdr does not retry a startup command that
// exited, so the new daemon is gone, and when the old one goes a moment later
// every action says there is no daemon, which is true and unfixable without
// restarting again.
//
// Generously long, because what it costs is a daemon that is idle for a minute
// in the one case where something really is already serving, and what it saves
// is a plugin that does nothing until somebody works out why.
var handoverWait = 90 * time.Second

// dialControl asks whether anything is serving the socket. A variable so that
// a test can count the asking, which is the whole of what went wrong: a daemon
// that is busy is not accepting, so every connection made to find out whether
// it is there stays in its backlog, and once that is full the next one is
// refused -- which reads exactly like a daemon that has gone.
var dialControl = func(socket string) (net.Conn, error) {
	return net.DialTimeout("unix", socket, time.Second)
}

// listenControl binds the control socket, clearing a socket left behind by a
// daemon that did not shut down cleanly and waiting out one that is still
// shutting down.
func listenControl(socket string) (net.Listener, error) {
	listener, err := net.Listen("unix", socket)
	if err == nil {
		return listener, restrict(socket, listener)
	}

	// Asked once, and only once. A daemon that is busy is not accepting, so
	// every connection made to find out whether it is there stays queued in
	// its backlog -- and once that is full the next one is refused, which
	// reads exactly like a daemon that has gone. Asking on a timer therefore
	// ends in taking the socket from a daemon that was answering perfectly
	// well a moment before, which is the one thing this must not do.
	if conn, dialErr := dialControl(socket); dialErr != nil {
		// Nothing answering, so the file is what a daemon that was killed
		// leaves behind.
		if rmErr := os.Remove(socket); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return nil, err
		}
		listener, err = net.Listen("unix", socket)
		if err != nil {
			return nil, err
		}
		return listener, restrict(socket, listener)
	} else {
		conn.Close()
	}

	// Someone is serving it. On a restart that is the daemon this one is
	// replacing, still shutting down, and the socket becomes bindable the
	// moment it goes -- so wait for that rather than exiting, which would
	// leave nothing running once it does.
	log.Print("another daemon still has the control socket; waiting for it to stop")
	for giveUp := time.Now().Add(handoverWait); time.Now().Before(giveUp); {
		time.Sleep(250 * time.Millisecond)
		if listener, err := net.Listen("unix", socket); err == nil {
			return listener, restrict(socket, listener)
		}
	}
	return nil, fmt.Errorf("another herdr-remote-panes daemon is already running")
}

// restrict keeps the control socket private to its owner.
//
// Its permissions were left to the umask, and connecting to a Unix socket needs
// write permission on it, so with the usual umask nobody else could reach it.
// That is a property of the umask rather than of this, though, and what the
// socket accepts is instructions to open SSH connections to other machines.
func restrict(socket string, listener net.Listener) error {
	if err := os.Chmod(socket, 0o600); err != nil {
		listener.Close()
		return fmt.Errorf("could not restrict %s: %w", socket, err)
	}
	return nil
}

// acceptGraceTime is how long serveControl keeps trying after Accept starts
// failing for a reason that is not the listener closing. Long enough for the
// descriptors a fan-out of connects took to be given back, and bounded so a
// failure that is never going to clear does not retry until the machine is
// turned off.
//
// Variables so a test does not spend that long proving it.
var (
	acceptGraceTime  = 2 * time.Minute
	acceptRetryFloor = 5 * time.Millisecond
	acceptRetryCeil  = time.Second
)

func (d *Daemon) serveControl(listener net.Listener) {
	// Accepting is the whole of the daemon's ability to be told anything. Every
	// action in the menu is a connection to this socket, so when this loop
	// stops the daemon goes on mirroring, goes on reconnecting, and answers
	// nothing -- and every action reports no running daemon, about a daemon
	// that is right there in the process list.
	//
	// It stopped on any error at all, including the ones that pass: too many
	// open files is the one to expect here, since connecting fans out over
	// every configured machine at once and each is an ssh with its pipes. That
	// is a burst, and it clears. The daemon it left behind did not.
	//
	// So: a closed listener is the shutdown and returns, and anything else is
	// given time to clear, backing off the way a server does rather than
	// spinning on it.
	var failingSince time.Time
	delay := time.Duration(0)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if failingSince.IsZero() {
				failingSince = time.Now()
				// Said once, when it starts. The retries are quiet: a log line
				// per attempt at the ceiling is a line a second for as long as
				// this lasts, and it buries the one that says what happened.
				log.Printf("could not accept on the control socket: %v (retrying)", err)
			}
			if time.Since(failingSince) > acceptGraceTime {
				// The line somebody reading this log needs, because the
				// alternative is a daemon that looks healthy and is not.
				log.Printf("giving up on the control socket after %s of %v -- "+
					"mirroring continues, but no action can reach this daemon; "+
					"stop it and start it again", acceptGraceTime, err)
				return
			}
			if delay < acceptRetryFloor {
				delay = acceptRetryFloor
			} else if delay *= 2; delay > acceptRetryCeil {
				delay = acceptRetryCeil
			}
			time.Sleep(delay)
			continue
		}
		failingSince, delay = time.Time{}, 0
		go d.handleControl(conn)
	}
}

// exchangeTimeout bounds each half of a control exchange: reading the request,
// and writing the answer. It does not bound the work in between.
//
// A variable so a test can shorten it, the way the ssh timeout next door is.
var exchangeTimeout = 30 * time.Second

// maxRequestBytes is far more than a command can be -- five short strings --
// and stops a client that never finishes its JSON from growing the daemon a
// buffer at a time.
const maxRequestBytes = 1 << 16

func (d *Daemon) handleControl(conn net.Conn) {
	serveExchange(conn, d.dispatch)
}

// serveExchange reads one command off a connection, answers it, and closes.
//
// Separate from the daemon because the mechanics of the exchange and what the
// daemon does about a command are separate concerns -- and because holding the
// deadlines to their contract means running a command that takes its time,
// which is not something to make a real ssh connection for.
func serveExchange(conn net.Conn, dispatch func(Command) Reply) {
	defer conn.Close()

	// The two halves are bounded separately, and the work between them is not
	// bounded here at all.
	//
	// One deadline for the whole exchange had to cover that work, and the work
	// is a connect: several ssh calls, each with its own timeout, and for a
	// connect that names no machine, that again for every machine in the config
	// one after another. Three unreachable ones is enough to pass thirty
	// seconds -- at which point the daemon cut off the answer it had just
	// finished working out, and the client, still waiting, reported a broken
	// connection rather than the result. The work is bounded where it belongs:
	// by the ssh timeouts.
	_ = conn.SetReadDeadline(time.Now().Add(exchangeTimeout))

	var cmd Command
	if err := json.NewDecoder(io.LimitReader(conn, maxRequestBytes)).Decode(&cmd); err != nil {
		// The reason, rather than "bad request". It is the plugin talking to
		// itself, so a malformed command means a bug, and the shape of it is
		// the only clue there will be.
		_ = conn.SetWriteDeadline(time.Now().Add(exchangeTimeout))
		_ = json.NewEncoder(conn).Encode(Reply{Message: "could not read the request: " + err.Error()})
		return
	}

	reply := dispatch(cmd)

	_ = conn.SetWriteDeadline(time.Now().Add(exchangeTimeout))
	_ = json.NewEncoder(conn).Encode(reply)
}

func (d *Daemon) dispatch(cmd Command) Reply {
	switch cmd.Cmd {
	case "connect":
		// With no host, reconnect everything configured. That gives a single
		// bindable "bring my remote spaces back" with nothing to select.
		if cmd.Host == "" {
			connected, err := d.connectAll()
			if err != nil {
				return Reply{Message: summarizeError(err)}
			}
			d.reconcileAll()
			// Worded so it cannot be mistaken for the single-machine reply
			// below. With one machine reachable both said "connected to bot",
			// which makes a connect that named no machine indistinguishable
			// from one that did -- including to whoever is reading a log to
			// work out why something did not happen.
			return Reply{OK: true, Message: fmt.Sprintf("reconnected %d of your machines: %s",
				len(connected), strings.Join(connected, ", "))}
		}
		host, err := d.hostConfig(cmd.Host)
		if err != nil {
			return Reply{Message: err.Error()}
		}
		if err := d.connect(host); err != nil {
			// Summarised like the status line and the log: ssh prints fifteen
			// lines of banner for a changed host key, and the screen after
			// pressing enter is the one place somebody is certainly looking.
			return Reply{Message: summarizeError(err)}
		}
		d.reconcileAll()

		// Make the connection visible from both ends. Without this, connecting
		// to a machine that already had terminals mirrored them here but left
		// no trace over there, so from the machine it looked like nothing had
		// happened.
		opened, err := d.ensureRemotePresence(host)
		if err != nil {
			return Reply{Message: fmt.Sprintf("connected to %s, but could not open a terminal: %s",
				host.Target, summarizeError(err))}
		}
		d.reconcileAll()
		d.focusHost(host.Target)
		if opened {
			return Reply{OK: true, Message: "connected to " + host.Target + " and opened a terminal"}
		}
		return Reply{OK: true, Message: "connected to " + host.Target}

	case "disconnect":
		// Resolved the way connecting is, so a machine can be disconnected by
		// whichever of its names you have in front of you. The listing shows
		// labels, so "build box  1 ssh  ok" followed by "build box is not
		// connected" was a straight contradiction -- and connect had taken
		// either name all along.
		host, err := d.hostConfig(cmd.Host)
		if err != nil {
			return Reply{Message: err.Error()}
		}
		if err := d.disconnect(host.Target); err != nil {
			return Reply{Message: err.Error()}
		}
		return Reply{OK: true, Message: "disconnected from " + host.Target}

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
			return Reply{Message: summarizeError(err)}
		}
		if err := d.openRemotePane(host, cmd.Placement, true); err != nil {
			return Reply{Message: summarizeError(err)}
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
		d.setConfig(cfg)

		// The mechanism changed, so the machine's panes here no longer match
		// how it is reached. Drop them and connect again under the new mode.
		_ = d.disconnect(cmd.Host)
		host, _ := d.hostConfig(cmd.Host)

		changed := "mirroring on for " + cmd.Host
		if cmd.Mode == string(config.ModeSSH) {
			changed = "mirroring off for " + cmd.Host
		}

		// The setting is changed and written by this point. A machine that
		// will not answer is worth saying, but it is a fact about the machine
		// rather than a failure of the change -- reporting it as one told
		// somebody their toggle had not worked while the file on disk said
		// otherwise, and put fifteen lines of ssh banner on the screen to
		// explain it.
		if err := d.connect(host); err != nil {
			return Reply{OK: true, Message: changed + ", but it is not reachable: " + summarizeError(err)}
		}
		if _, err := d.ensureRemotePresence(host); err != nil {
			return Reply{OK: true, Message: changed + ", but no terminal opened: " + summarizeError(err)}
		}
		d.reconcileAll()
		// No focus here: m keeps you in the menu, so moving the screen
		// underneath it only surprises you when you leave. Going somewhere is
		// what enter is for.
		return Reply{OK: true, Message: changed}

	case "refresh":
		d.reconcileAll()
		return Reply{OK: true, Message: "refreshed"}

	case "status":
		return Reply{OK: true, Hosts: d.status(), Warning: d.configWarning(), Revision: version.Short()}

	default:
		return Reply{Message: "unknown command " + cmd.Cmd}
	}
}

// openRemotePane creates a new pane on the remote host. The reconcile that
// follows mirrors it back, so opening a pane "on a machine" is one action.
func (d *Daemon) openRemotePane(host config.Host, placement string, focus bool) error {
	// Held for the whole of the work, not just the lookup. Everything below
	// reads and writes this machine's own bookkeeping, and the reconcile loop
	// does the same to the same fields under this same lock -- so taking it
	// only long enough to find the machine and then working on it unlocked was
	// two goroutines in one hostSync. Which is what it was.
	d.mu.Lock()
	defer d.mu.Unlock()

	state, ok := d.hosts[host.Target]
	if !ok {
		return fmt.Errorf("%s is not connected", host.Target)
	}

	if state.sshOnly {
		// A plain SSH machine has no remote pane to open, so this is the
		// terminal. The focus asked for used to stop at this line, which meant
		// "open a terminal on this machine" left you looking at somewhere else
		// -- for the default mode, which is most people.
		return d.openShellPane(state, placement, focus)
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
// Callers hold d.mu.
func (d *Daemon) rememberPlacement(state *hostSync, result json.RawMessage, placement string, focus bool) {
	if placement == "" && !focus {
		return
	}
	made, err := herdrcli.ParseCreated(result)
	if err != nil || made.RootPane.TerminalID == "" {
		return
	}
	if placement != "" {
		state.pendingPlacement[made.RootPane.TerminalID] = placement
	}
	if focus {
		state.pendingFocus[made.RootPane.TerminalID] = true
	}
}

// closeRemoteTerminals closes the machine's terminals behind mirrors that were
// closed here.
//
// The dismissal that stops a terminal being mirrored again is deliberately left
// in place. Forgetting it here on the grounds that the terminal is now gone was
// true of the machine and not of this pass: the listing everything after this
// works from was taken before the close, so the terminal is still in it, and
// with nothing left to say otherwise it was mirrored straight back. Closing a
// mirrored tab therefore closed the terminal on the machine, opened a new
// mirror of it, and -- once that mirror turned out to have nothing behind it --
// opened a fresh terminal on the machine to replace it. You closed a tab and
// got a different one.
//
// The dismissal goes on its own, from forgetTerminals, once a listing comes
// back without that terminal in it.
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
			continue
		}
		if _, err := state.client.Run("pane", "close", paneID); err != nil {
			// Already gone at the far end is the outcome, not a failure: the
			// listing this works from is a moment old, and somebody may have
			// closed it there in between.
			if !herdrcli.IsNotFound(err) {
				log.Printf("close %s on %s: %s", paneID, state.host.Target, summarizeError(err))
				continue
			}
		}
		log.Printf("%s: closed terminal %s to match", state.host.Target, paneID)
	}
}

// findRemoteWorkspace looks up this machine's space on the remote without
// creating it, for the reconcile path, which should not open terminals.
func (d *Daemon) findRemoteWorkspace(state *hostSync) (bool, error) {
	label := d.config().RemoteWorkspaceLabel()
	result, err := state.client.Run("workspace", "list")
	if err != nil {
		return false, err
	}
	workspaces, err := herdrcli.ParseWorkspaceList(result)
	if err != nil {
		return false, err
	}
	// Matched the way the local space is matched, marker and all. Without it,
	// changing remote_workspace_format orphans the space this plugin's
	// terminals are already in on that machine and quietly makes a second one
	// beside it -- which is the reason the local lookup is tolerant, and the
	// far side is no different.
	// Two spaces with the same name is a different thing from the tolerance
	// above, and worth saying. It happens when two machines answer to one hub
	// name -- two laptops called the same thing, or two people sharing a space
	// on purpose -- and both create it before either sees the other's. Each
	// then settles on whichever came back first, which need not be the same
	// one, and the two sit in separate spaces both called the same thing,
	// seeing none of each other's terminals.
	//
	// Counted on the exact name only. A space that merely matches the looser
	// rule is the format having changed, which is deliberate and not a problem.
	duplicates := 0
	for _, ws := range workspaces {
		if ws.Label == label {
			duplicates++
		}
	}

	for _, ws := range workspaces {
		if ws.Label == label || sameWorkspace(ws.Label, config.HubName()) {
			if duplicates > 1 && !state.duplicateSpaces {
				// Not "give this machine its own": remote_workspace_format is
				// one setting for every machine, so advice to set it per
				// machine sends somebody looking for a key that is not there.
				log.Printf("%s: %d spaces there are called %q; using %s. "+
					"Rename the others there, or change remote_workspace_format, "+
					"which names this space on every machine you connect to",
					state.host.Target, duplicates, label, ws.WorkspaceID)
			}
			state.duplicateSpaces = duplicates > 1
			state.remoteWorkspaceID = ws.WorkspaceID
			return true, nil
		}
	}
	state.duplicateSpaces = false
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
	// Looked for the same way the other lookup looks for it. This had its own
	// search that matched the name exactly, so the two could disagree about
	// whether the space already existed -- and this is the one that makes a new
	// one when it decides there is none.
	found, err := d.findRemoteWorkspace(state)
	if err != nil {
		return "", false, err
	}
	if found {
		return state.remoteWorkspaceID, false, nil
	}

	label := d.config().RemoteWorkspaceLabel()
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
		// Only whether, not why: a name that is not a machine falls back to
		// opening an ordinary pane, which is what "new terminal" means
		// everywhere else.
		host, err := d.hostConfig(cmd.Host)
		return host, err == nil
	}
	if cmd.Workspace == "" {
		return config.Host{}, false
	}

	label := herdrcli.WorkspaceLabel(cmd.Workspace)
	if label == "" {
		return config.Host{}, false
	}
	return d.hostForWorkspaceLabel(label)
}

// hostForWorkspaceLabel finds the machine whose space carries a label.
//
// The marker is ignored, as it is everywhere else a space is looked up by name.
// Comparing against the reachable form alone meant that a machine which could
// not be reached -- whose space is renamed to say so -- stopped being
// recognised as the owner of its own space. Opening a tab there then made an
// ordinary local shell, inside that machine's space, with nothing to say why.
func (d *Daemon) hostForWorkspaceLabel(label string) (config.Host, bool) {
	cfg := d.config()
	matches := func(h config.Host) bool {
		return cfg.WorkspaceFor(h) == label || sameWorkspace(label, h.DisplayLabel())
	}

	for _, h := range cfg.Hosts {
		if matches(h) {
			return h, true
		}
	}
	// Also machines connected ad hoc, which are not in the config file.
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, state := range d.hosts {
		if matches(state.host) {
			return state.host, true
		}
	}
	return config.Host{}, false
}

// openShellPane opens a plain SSH pane for a host that is not running Herdr.
// There is no remote pane to mirror, so the pane is the session itself.
// Callers hold d.mu: everything here is this machine's own bookkeeping.
func (d *Daemon) openShellPane(state *hostSync, placement string, focus bool) error {
	index := newPaneIndex(nil)
	if local, err := herdrcli.PaneList(); err == nil {
		index = newPaneIndex(local)
	}

	workspaceID, err := d.ensureWorkspace(state, index)
	if err != nil {
		return err
	}

	// The names already on this machine's terminals, so the new one does not
	// repeat any of them.
	taken := make(map[string]bool, len(state.shellPanes))
	for paneID := range state.shellPanes {
		if existing := state.labels[paneID]; existing != "" {
			taken[existing] = true
		}
	}

	name := planShellName(taken, func(candidate string) string {
		return d.label(state.host, herdrcli.Pane{}, candidate)
	})
	// The name the pane will carry, worked out once. A mirrored pane is told
	// its full label; a plain SSH pane used to be told only the bare part, so
	// when one failed it announced itself as "shell" with no mention of which
	// machine had gone -- ninety-five such lines in the log on this machine.
	label := d.label(state.host, herdrcli.Pane{}, name)

	// The placement asked for, when there was one, and otherwise the machine's
	// own. "New tab" is a separate action precisely because the answer is not
	// always the machine's setting, and for a plain SSH machine -- the default
	// mode, and most people -- it was: this line read the config and the
	// argument went nowhere, so "new tab" opened whatever "new terminal" would
	// have. Focus reached here by the same route and was fixed; placement was
	// left behind.
	if placement == "" {
		placement = d.config().PlacementFor(state.host)
	}
	shellTarget := planPaneTarget(placement, workspaceID, index.anyInWorkspace[workspaceID])
	opts := herdrcli.OpenOptions{
		PluginID:   PluginID,
		Entrypoint: paneEntrypoint,
		Placement:  shellTarget.Placement,
		Workspace:  shellTarget.Workspace,
		TargetPane: shellTarget.TargetPane,
		Focus:      focus,
		Env: map[string]string{
			mirror.EnvTarget: state.host.Target,
			mirror.EnvMode:   string(config.ModeSSH),
			mirror.EnvName:   label,
		},
	}
	pane, err := herdrcli.OpenPane(opts)
	if err != nil {
		return err
	}
	index.add(pane)
	state.shellPanes[pane.PaneID] = true
	d.retitle(state, pane.PaneID, label)
	d.retireRootPane(workspaceID, index)
	return nil
}

// configWarning describes an unreadable configuration, if there was one.
func (d *Daemon) configWarning() string {
	if d.configErr != nil {
		return fmt.Sprintf("the plugin config could not be read, so no machines are configured: %v", d.configErr)
	}
	if problems := d.config().Problems(); len(problems) > 0 {
		// The count first, because this is drawn in a menu that has machines
		// to show as well: the warning is wrapped to two lines and the rest
		// becomes an ellipsis, so somebody with three problems reads one of
		// them and cannot tell there are others. `status` prints the lot.
		if len(problems) > 1 {
			return fmt.Sprintf("check the plugin config, %d problems (`status` lists them): %s",
				len(problems), strings.Join(problems, "; "))
		}
		return "check the plugin config: " + strings.Join(problems, "; ")
	}
	return ""
}

// hostConfig finds a configured host by target or label, or says why the name
// cannot be used as one.
//
// The reason is returned rather than a bare no. The caller used to answer "%s
// is not in the plugin config", which was the wrong thing to be told twice
// over: a name that is fine needs no entry in that file -- anything in
// ~/.ssh/config works, and so does anything that looks like a machine -- and a
// name that is not fine would still be refused after adding one. It sent people
// to edit a file that had nothing to do with it.
func (d *Daemon) hostConfig(name string) (config.Host, error) {
	for _, h := range d.config().Hosts {
		if h.Target == name || h.Label == name {
			return h, nil
		}
	}
	// An unconfigured target is still usable: treat it as an ad-hoc host so
	// `connect` works against anything in the user's ssh config. It has not
	// been through the config file's checks, though, and it need not have come
	// from a person typing it: connect falls back to whatever text is selected
	// in the terminal, so a line of someone else's output can arrive here.
	//
	// Which is why a name in ~/.ssh/config is held to less. Somebody wrote it
	// down, so there is nothing left to guess about whether it is a machine --
	// and `Host "my server"` is a machine, one ssh reaches as `ssh "my
	// server"`. A name from nowhere still has to look like a name.
	check := config.PlausibleTarget
	if declaredInSSHConfig(name) {
		check = config.ValidTarget
	}
	if err := check(name); err != nil {
		return config.Host{}, err
	}
	return config.Host{Target: name}, nil
}

// declaredInSSHConfig reports whether the user wrote this machine down.
//
// Read rather than cached: ~/.ssh/config is edited by hand between connects,
// and this runs on connect rather than in the reconcile loop.
func declaredInSSHConfig(name string) bool {
	for _, host := range sshconfig.Hosts() {
		if host == name {
			return true
		}
	}
	return false
}

func (d *Daemon) connect(host config.Host) error {
	client := remote.NewWithBin(host.Target, d.config().SessionFor(host), d.config().BinFor(host))

	sshOnly := !d.config().Mirrors(host)
	noHerdr := false
	var connectErr error
	if sshOnly {
		// Without this an unreachable machine reports "ok", because nothing
		// else in the plain SSH path ever talks to it: the failure only shows
		// up later as terminals that will not stay open.
		connectErr = client.Reachable()
	} else {
		if err := d.prepareRemote(client); err != nil {
			// A machine without Herdr is still perfectly usable over plain SSH,
			// so fall back instead of refusing to connect. Anything else — an
			// unreachable host, a broken login — is a real failure, but the
			// host is still registered so it keeps being retried and shows up
			// as unreachable rather than vanishing from status entirely.
			if errors.Is(err, remote.ErrNoHerdr) {
				// Named the remedy, because there usually is one: herdr is
				// commonly installed somewhere the PATH an SSH session gets
				// does not reach, and herdr_bin is how to say where.
				log.Printf("%s: no herdr on the machine's PATH, so plain ssh terminals; "+
					"if it is installed elsewhere there, set herdr_bin for this machine",
					host.Target)
				sshOnly = true
				noHerdr = true
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
		state.noHerdr = noHerdr
		clear(state.dismissed)
		clear(state.abandoned)
		// The settings can have moved on since this host was first connected:
		// toggling a mode from the menu rereads the whole config file, so an
		// edit to any other machine's session or Herdr path lands then too.
		// Keeping the old client would leave state.host describing one thing
		// and the connection doing another.
		if state.client == nil || !state.client.SameSettings(
			host.Target, d.config().SessionFor(host), d.config().BinFor(host)) {
			if state.client != nil {
				state.client.Close()
			}
			state.client = client
		}
	} else {
		state = &hostSync{
			host:      host,
			client:    client,
			sshOnly:   sshOnly,
			noHerdr:   noHerdr,
			mirrors:   map[string]string{},
			dismissed: map[string]bool{},
			abandoned: map[string]bool{},
			failures:  map[string]int{},
			retryAt:   map[string]time.Time{},

			reportedAgents:   map[string]agentReport{},
			pendingPlacement: map[string]string{},
			pendingFocus:     map[string]bool{},
			labels:           map[string]string{},
			shellPanes:       map[string]bool{},
		}
		if saved, ok := d.snapshot.Hosts[host.Target]; ok {
			restoreFromSnapshot(state, saved)
		}
		d.hosts[host.Target] = state
	}
	if d.touched == nil {
		d.touched = map[string]bool{}
	}
	d.touched[host.Target] = true
	state.lastErr = connectErr
	// An explicit connect is a request to try now, so a machine that was given
	// up on is tried again.
	state.failCount = 0
	state.shellFailures = 0
	state.lastReopen = time.Time{}
	state.gaveUp = false
	state.linkRetryAt = time.Time{}
	state.linkRetryStep = 0
	return connectErr
}

// restoreFromSnapshot carries a machine's bookkeeping across a restart.
//
// Terminals closed by hand were written to the snapshot and then never read
// back, so a restart forgot them and mirrored them again -- reopening, on the
// machine's next reconcile, exactly the terminals someone had shut.
func restoreFromSnapshot(state *hostSync, saved hostSnapshot) {
	for terminalID, paneID := range saved.Mirrors {
		state.mirrors[terminalID] = paneID
	}
	for _, terminalID := range saved.Dismissed {
		state.dismissed[terminalID] = true
	}
	state.restoreShells = saved.Shells
}

// prepareRemote makes sure the host has a Herdr session answering, starting one
// when it is allowed to.
func (d *Daemon) prepareRemote(client *remote.Client) error {
	// Establish that Herdr is usable here before trying to start or talk to a
	// session, so a host without it is recognised as such rather than looking
	// like an unreachable one.
	if err := client.CheckHerdr(); err != nil {
		return err
	}
	if err := client.Ping(); err != nil {
		// The host is reachable but its session is not up yet. Start one and
		// retry before treating this as a failure.
		if !d.config().ShouldAutoStart() {
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

// focusHost brings a machine's space to the front.
//
// Picking a machine from the menu is a request to go and work on it, so leaving
// the terminal open behind whatever was already on screen makes the menu look
// as though it did nothing. Only an explicit connect does this: a reconcile
// that happens to open a pane must not steal the screen from underneath
// somebody.
func (d *Daemon) focusHost(target string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	state, ok := d.hosts[target]
	if !ok {
		return
	}
	// The id is learned along the way by whatever needed it. Every path that
	// reaches this reconciles first, and reconciling learns it -- so this
	// lookup is insurance rather than the usual way round, whatever the commit
	// that added it thought.
	//
	// Measured rather than assumed: it fires nowhere in the suite, and the
	// three shapes it was written for all arrive with the id already known --
	// a machine reconnected after a restart, one whose panes are husks, and one
	// whose panes are still alive and need nothing opened. Kept because the
	// alternative is focusing nothing, and because a path that does not learn
	// it would fail silently: the menu would look like it had done nothing.
	if state.workspaceID == "" {
		if _, err := d.findLocalWorkspace(state); err != nil {
			log.Printf("focus %s: %v", target, err)
			return
		}
	}
	if state.workspaceID == "" {
		// Worth saying rather than returning quietly: this is the whole of what
		// picking a machine from the menu is supposed to do, and a silent
		// no-op looks exactly like the feature not existing.
		log.Printf("focus %s: no space of its own to go to", target)
		return
	}
	if err := herdrcli.FocusWorkspace(state.workspaceID); err != nil {
		log.Printf("focus %s: %v", target, err)
		return
	}
	log.Printf("%s: focused %s", target, state.workspaceID)
}

// panesToClose lists every local pane a machine has.
//
// Disconnecting used to close its mirrors only. A plain SSH machine has no
// mirrors -- its panes are the sessions themselves -- so disconnecting one
// stopped tracking it and left its terminals open, each still holding an SSH
// connection, with nothing watching them any more. That is the default mode,
// so it was the usual case rather than an unusual one.
func panesToClose(state *hostSync) []string {
	out := make([]string, 0, len(state.mirrors)+len(state.shellPanes))
	for _, paneID := range state.mirrors {
		out = append(out, paneID)
	}
	for paneID := range state.shellPanes {
		out = append(out, paneID)
	}
	sort.Strings(out)
	return out
}

// disconnect stops tracking a machine and closes its panes here.
//
// Its terminals go, of either kind; the work on the machine does not, which is
// what lets connecting again pick up where it left off.
func (d *Daemon) disconnect(target string) error {
	d.mu.Lock()
	state, ok := d.hosts[target]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("%s is not connected", target)
	}
	delete(d.hosts, target)

	// Held across the closing rather than released after the lookup. Taking the
	// machine out of the map does not stop a pass that began before it: that
	// pass took its own list of machines first and is still holding this very
	// hostSync, writing the maps that panesToClose reads.
	for _, paneID := range panesToClose(state) {
		if err := herdrcli.ClosePane(paneID); err != nil {
			log.Printf("close %s: %v", paneID, err)
		}
	}
	state.client.Close()
	d.mu.Unlock()

	// Outside the lock, which persist takes for itself. Immediately, though, so
	// a reconnect before the next reconcile does not pick this machine's stale
	// bookkeeping back up.
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
	// As openRemotePane: held across the work rather than the lookup.
	d.mu.Lock()
	defer d.mu.Unlock()

	state, ok := d.hosts[host.Target]
	if !ok {
		return false, fmt.Errorf("%s is not connected", host.Target)
	}

	if state.sshOnly {
		// Nothing to create over there; a plain SSH machine has no Herdr.
		if !planNeedsTerminal(d.liveTerminalCountLocked(host.Target)) {
			return false, nil
		}
		// Nothing asked for a placement here: this is a machine being
		// connected to, so the machine's own setting is the answer.
		return true, d.openShellPane(state, "", false)
	}

	_, created, err := d.ensureRemoteWorkspace(state)
	if err != nil {
		return false, err
	}
	if created {
		// The new space comes with a pane, which mirrors back here.
		return true, nil
	}
	if !planNeedsTerminal(d.liveTerminalCountLocked(host.Target)) {
		return false, nil
	}
	// The space is there but empty of anything to mirror.
	_, err = state.client.Run("tab", "create", "--workspace", state.remoteWorkspaceID)
	return true, err
}

// liveTerminalCountLocked reports how many of a machine's terminals are still
// open. Callers hold d.mu: it took the lock itself until both of its callers
// began holding it, at which point taking it again would have been a deadlock
// rather than a guard.
func (d *Daemon) liveTerminalCountLocked(target string) int {
	// Counted from the panes Herdr actually has, rather than from bookkeeping:
	// a terminal closed a moment ago may not have been reconciled away yet,
	// and a stale count is what makes a machine impossible to reopen.
	local, err := herdrcli.PaneList()
	if err != nil {
		// The error already names the command it was: "herdr pane list: ...".
		// What is worth adding is what it cost, not saying "pane list" twice.
		log.Printf("cannot count %s's terminals: %v", target, err)
		return 0
	}
	alive := make(map[string]bool, len(local))
	for _, pane := range local {
		alive[pane.PaneID] = true
	}

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
//
// The machines are reached at the same time rather than one after another.
// They are independent, and connect does its ssh work before it takes the lock,
// so nothing was gained by the queue -- while an unreachable machine costs up
// to its own connect timeout, and one at a time those add up. Three of them was
// enough to outlast the deadline on the connection carrying the answer, so the
// caller was told the connection broke rather than what happened. Reaching them
// together bounds the whole thing by the slowest one instead of by the sum.
func (d *Daemon) connectAll() ([]string, error) {
	hosts := make([]config.Host, 0, len(d.config().Hosts))
	for _, host := range d.config().Hosts {
		if !host.Disabled {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("no hosts configured")
	}

	errs := d.connectEach(hosts)

	var connected []string
	var lastErr error
	for i, host := range hosts {
		if errs[i] != nil {
			lastErr = errs[i]
			continue
		}
		connected = append(connected, host.Target)
	}
	if len(connected) == 0 {
		return nil, lastErr
	}
	return connected, nil
}

// connectEach connects a set of machines at the same time, returning one error
// per machine in the order given.
//
// Together rather than one after another. They are independent, and connect
// does its ssh work before it takes the lock, so a queue bought nothing --
// while an unreachable machine costs its whole connect timeout, and one at a
// time those add up: two of them and the machines that are fine are still not
// back. Every failure is logged here, so the callers need only decide what to
// do about them.
func (d *Daemon) connectEach(hosts []config.Host) []error {
	errs := make([]error, len(hosts))
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Add(1)
		go func(i int, host config.Host) {
			defer wg.Done()
			if err := d.connect(host); err != nil {
				errs[i] = err
				log.Printf("connect %s: %s", host.Target, summarizeError(err))
			}
		}(i, host)
	}
	wg.Wait()

	// The same second step connecting to one machine takes. Connecting alone
	// only opens the SSH connection; what makes a machine's space exist over
	// there -- and records which space it is -- is ensuring its presence.
	//
	// Without it, connecting everything left every mirrored machine with no
	// space on it and no id for one, so nothing was mirrored and the reply
	// still said how many machines had been reconnected. Worse, each pass then
	// looked the space up again over SSH, found nothing, and did it for the
	// rest of the session.
	//
	// After the wait rather than inside it: this takes the daemon's lock, and
	// the point of the goroutines above is the connecting, which does not.
	for i, host := range hosts {
		if errs[i] != nil {
			continue
		}
		if _, err := d.ensureRemotePresence(host); err != nil {
			// Not a connect failure: the machine answered, and its terminals
			// here are what the caller asked for. Said once, where the rest of
			// a pass's trouble is said.
			log.Printf("%s: connected, but no terminal opened there: %s",
				host.Target, summarizeError(err))
		}
	}
	return errs
}

// orderedHosts lists the tracked machines in a stable order: those named in
// the config first, in the order they appear there, then anything else picked
// up along the way, sorted by name. Callers must already hold d.mu.
func (d *Daemon) orderedHosts() []*hostSync {
	out := make([]*hostSync, 0, len(d.hosts))
	seen := make(map[string]bool, len(d.hosts))

	for _, host := range d.config().Hosts {
		if state, ok := d.hosts[host.Target]; ok && !seen[host.Target] {
			seen[host.Target] = true
			out = append(out, state)
		}
	}

	rest := make([]string, 0, len(d.hosts)-len(out))
	for target := range d.hosts {
		if !seen[target] {
			rest = append(rest, target)
		}
	}
	sort.Strings(rest)
	for _, target := range rest {
		out = append(out, d.hosts[target])
	}
	return out
}

func (d *Daemon) status() []HostInfo {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Ranging over the map directly reshuffled the list between runs, so the
	// same three machines came back in a different order each time and could
	// not be compared at a glance. Config order is what someone wrote down, so
	// it is the order they expect to read back.
	out := make([]HostInfo, 0, len(d.hosts))
	for _, state := range d.orderedHosts() {
		info := HostInfo{
			Target:        state.host.Target,
			Label:         state.host.DisplayLabel(),
			Connected:     state.lastErr == nil,
			Mirrors:       len(state.mirrors),
			SSHOnly:       state.sshOnly,
			NoHerdr:       state.noHerdr,
			AtCapacity:    state.atCapacity,
			OutsideShared: state.outsideShared,
			SharedName:    state.duplicateSpaces,
			Terminals:     len(state.shellPanes),
			Mirroring:     d.config().Mirrors(state.host),
			GaveUp:        state.gaveUp,
			Unmirrored:    len(state.abandoned),
		}
		if state.lastErr != nil {
			info.LastError = summarizeError(state.lastErr)
		}
		out = append(out, info)
	}
	return out
}

// pruneInterval is how often marks left by vanished panes are cleared. A pane
// that goes without the daemon noticing leaves one behind, which stays until
// something clears it; reading a whole directory of a few small files is not
// worth doing every couple of seconds, but leaving it until the next restart
// is how a stale mark meets a reused pane id.
const pruneInterval = time.Minute

func (d *Daemon) maybePrune(alive map[string]bool) {
	now := time.Now()
	if last := d.lastPrune.Load(); last != 0 && now.Sub(time.Unix(0, last)) < pruneInterval {
		return
	}
	d.lastPrune.Store(now.UnixNano())
	mirror.Prune(alive)
}

// coalescer runs a job, folding requests that arrive while it is running into
// a single further run.
//
// The alternative -- a flag saying "one is running", cleared on the way out --
// drops a request that arrives between the last check and the clearing. Doing
// both under one lock is what makes that impossible.
type coalescer struct {
	mu      sync.Mutex
	running bool
	wanted  bool
}

func (c *coalescer) run(job func()) {
	c.mu.Lock()
	if c.running {
		// One is under way. Ask it to go round again rather than starting a
		// second, so whatever prompted this is still seen.
		c.wanted = true
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()

	for {
		job()

		c.mu.Lock()
		if !c.wanted {
			c.running = false
			c.mu.Unlock()
			return
		}
		c.wanted = false
		c.mu.Unlock()
	}
}

// scheduleWholePassRetry gives every machine another go, later, when they all
// went down in the same pass.
func (d *Daemon) scheduleWholePassRetry(states []*hostSync) {
	d.mu.Lock()
	defer d.mu.Unlock()

	pass := make([]passResult, 0, len(states))
	for _, state := range states {
		pass = append(pass, passResult{
			gaveUp:  state.gaveUp,
			settled: settledFailure(state.lastErr),
		})
	}
	if !planWholePassRetry(pass) {
		return
	}

	// Only the ones not already waiting. This runs every pass, so rescheduling
	// a machine that is already counting down would push the retry further
	// away every couple of seconds and it would never arrive.
	var wait time.Duration
	scheduled := 0
	now := time.Now()
	for _, state := range states {
		if !state.linkRetryAt.IsZero() {
			continue
		}
		wait = planAutoRetryWait(state.linkRetryStep)
		state.linkRetryAt = now.Add(wait)
		state.linkRetryStep++
		scheduled++
	}
	if scheduled == 0 {
		return
	}
	// Once for the pass rather than once per machine: every machine is the
	// case this is about, so a line each is the same sentence n times.
	log.Printf("all %d machines went down together, which is usually this end "+
		"rather than them; trying again in %s", len(states), wait)
}

// recordReconcile updates a machine's health from one reconcile pass, and
// reports whether that pass was the one that gave up on it.
//
// A pass that went fine is evidence the machine can be reached only when
// reconciling actually contacts it. A plain SSH machine is deliberately not
// contacted -- the ssh runs inside the pane -- so clearing its connect failure
// on that basis made an unreachable machine report "ok" until one of its panes
// had dropped twice, which is a different mechanism arriving later. What
// settles it for those is connect, which does test the machine.
func recordReconcile(state *hostSync, err error) (gaveUp bool) {
	if err != nil {
		state.lastErr = err
		state.failCount++
		if planGiveUp(state.failCount, err) && !state.gaveUp {
			state.gaveUp = true
			return true
		}
		return false
	}
	// Whatever else this pass meant, the machine answered: the next time the
	// link goes it starts at five seconds again rather than wherever the last
	// outage left the backoff.
	state.linkRetryStep = 0
	state.linkRetryAt = time.Time{}
	if !state.sshOnly {
		state.lastErr = nil
		state.failCount = 0
	}
	return false
}

// reconcileAll brings every connected machine in line, folding calls that
// arrive while one is already running into a single further pass.
//
// Herdr fires a pane.created event for every pane, and this plugin creates
// panes, so opening a few at once started that many full reconciles on top of
// one another -- six inside six hundred milliseconds on one startup, each with
// its own pane listing and its own round trips to every machine. They cannot
// usefully overlap in any case: reconciling a host holds the lock for as long
// as it takes.
func (d *Daemon) reconcileAll() {
	d.reconciles.run(d.reconcileOnce)
}

func (d *Daemon) reconcileOnce() {
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
		log.Printf("skipping this pass: %v", err)
		return
	}
	index := newPaneIndex(local)

	// Marks are left behind whenever a pane goes without the daemon noticing,
	// and Herdr reuses pane ids, so a stale one would be read as belonging to
	// whatever lands on that id next.
	d.maybePrune(index.alive)

	defer d.persist()

	var wg sync.WaitGroup
	for _, state := range states {
		wg.Add(1)
		go func(state *hostSync) {
			defer wg.Done()
			d.mu.Lock()
			defer d.mu.Unlock()
			// Disconnected while this pass was away fetching its pane
			// listing. The lock was released for that -- it is a subprocess,
			// and holding it across one would stop the daemon answering --
			// so the machine can be gone and its panes closed by the time
			// this comes back.
			//
			// Reconciling it now reads those panes as tabs closed one at a
			// time, which is what disconnecting looks like from here and is
			// not what it means: with close_propagates on, that closes the
			// terminals on the machine, and disconnecting is the one thing
			// that promises not to.
			if d.hosts[state.host.Target] != state {
				return
			}
			if state.gaveUp {
				if state.linkRetryAt.IsZero() || time.Now().Before(state.linkRetryAt) {
					return
				}
				// Due. Given up on when everything was down, which says the
				// link here was the trouble rather than the machine -- so it
				// gets another go without being asked, with a full budget of
				// attempts as though it had just been connected to.
				state.linkRetryAt = time.Time{}
				state.gaveUp = false
				state.failCount = 0
			}
			err := d.reconcileHost(state, index)
			if err != nil && (state.lastErr == nil || state.lastErr.Error() != err.Error()) {
				log.Printf("reconcile %s: %s", state.host.Target, summarizeError(err))
			}
			if recordReconcile(state, err) {
				// Saying "connect again to retry" after a changed host key was
				// advice that could not work: the next attempt fails the same
				// way until somebody looks at known_hosts.
				if settledFailure(err) {
					log.Printf("%s: not retrying — %s", state.host.Target, summarizeError(err))
				} else {
					log.Printf("%s: giving up after %d attempts; connect again to retry",
						state.host.Target, state.failCount)
				}
			}
			d.markWorkspaceState(state, state.lastErr == nil)
		}(state)
	}
	wg.Wait()

	d.scheduleWholePassRetry(states)

	// Outside the lock: capturing a stray opens a terminal on the machine,
	// which takes the lock again.
	for _, state := range states {
		d.mu.Lock()
		strays := state.strays
		state.strays = nil
		reopen := state.reopenShell
		state.reopenShell = false
		// Copied out with them, since what follows runs unlocked and connect
		// writes this.
		host := state.host
		d.mu.Unlock()

		// Capturing a stray opens a terminal on the machine, which takes the
		// lock itself; reopening one wants it held, as every other caller of
		// openShellPane now does.
		d.captureStrayPanes(host, strays)
		if reopen {
			// Never focused: a link coming back is not a request to go
			// there, and somebody may be working elsewhere.
			d.mu.Lock()
			err := d.openShellPane(state, "", false)
			d.mu.Unlock()
			if err != nil {
				log.Printf("reopen terminal on %s: %v", host.Target, err)
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
	// What the last daemon recorded about machines this one has not reached.
	//
	// The snapshot was rebuilt from the machines currently connected, so
	// connecting one erased every other -- and the snapshot is the only record
	// of what a machine had open and of machines picked from ~/.ssh/config that
	// were never written to the config file. The control socket answers while
	// startup is still connecting, on purpose, so one connect from the menu in
	// that window was enough: everything not yet reached lost its terminals and
	// its place in the list.
	//
	// A machine this daemon has connected and then dropped is gone on purpose
	// and stays gone. One it has never touched keeps what it had.
	for target, saved := range d.snapshot.Hosts {
		if _, live := current.Hosts[target]; !live && !d.touched[target] {
			current.Hosts[target] = saved
		}
	}
	d.snapshot = current

	// Reconciling happens every couple of seconds whether anything changed or
	// not, and this used to write the file every time: the same bytes, tens of
	// thousands of times a day, keeping a laptop's disk awake for nothing.
	raw, err := marshalSnapshot(current)
	if err != nil {
		d.mu.Unlock()
		log.Printf("save mirror state: %v", err)
		return
	}
	if bytes.Equal(raw, d.lastSaved) {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()

	// Recorded only once it is actually on disk. Recording it first meant a
	// failed write -- a full disk, a state directory that went away -- left
	// this believing the file held content it never received, so every later
	// pass saw nothing to do and the snapshot silently stopped being saved for
	// as long as the daemon ran.
	if err := writeSnapshot(raw); err != nil {
		log.Printf("save mirror state: %v", err)
		return
	}
	d.mu.Lock()
	d.lastSaved = raw
	d.mu.Unlock()
}

// forgetTerminals drops what is remembered about terminals the machine no
// longer has.
//
// Each of these used to be cleared where it was most obviously needed, which
// left the ones nobody had thought about growing for as long as the daemon ran.
// The count of failed attempts was only cleared alongside a pending retry --
// and giving up on a terminal deletes the retry first, so the count that had
// just reached the limit stayed for good, one entry per terminal that ever
// failed that often.
//
// Going through every one of them in a single place is the only way this stays
// true when the next one is added.
func forgetTerminals(state *hostSync, seen map[string]bool) {
	for terminalID := range state.dismissed {
		if !seen[terminalID] {
			delete(state.dismissed, terminalID)
		}
	}
	for terminalID := range state.abandoned {
		if !seen[terminalID] {
			delete(state.abandoned, terminalID)
		}
	}
	for terminalID := range state.retryAt {
		if !seen[terminalID] {
			delete(state.retryAt, terminalID)
		}
	}
	for terminalID := range state.failures {
		if !seen[terminalID] {
			delete(state.failures, terminalID)
		}
	}
	// Set when a terminal is made on the machine and consumed when its mirror
	// opens here. A terminal that goes before that happens leaves one behind.
	for terminalID := range state.pendingPlacement {
		if !seen[terminalID] {
			delete(state.pendingPlacement, terminalID)
		}
	}
	for terminalID := range state.pendingFocus {
		if !seen[terminalID] {
			delete(state.pendingFocus, terminalID)
		}
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

			// Mirrors recorded while this machine was mirrored cannot be kept
			// up in SSH mode, and nothing here would ever revisit them, so
			// they would sit in the space as dead panes wearing live names.
			// Panes wearing this machine's name whose mirror is not running are
			// left over from when it was mirrored, and nothing else revisits
			// them.
			d.closeOrphans(state, index)

			hadPanes := len(state.mirrors) > 0
			for terminalID, paneID := range state.mirrors {
				if index.alive[paneID] {
					log.Printf("%s: closing mirror %s, no longer mirroring this machine",
						state.host.Target, paneID)
					if err := herdrcli.ClosePaneByID(paneID); err != nil {
						log.Printf("close mirror %s: %v", paneID, err)
					}
					index.remove(paneID)
				}
				d.forgetPane(state, paneID)
				delete(state.mirrors, terminalID)
			}
			// A machine that had panes should not vanish because the way it is
			// reached changed; give it a terminal in the new style instead.
			state.restoreShells = planShellsToRestore(hadPanes, state.restoreShells)

			if state.restoreShells > 0 {
				workspaceID, wsErr := d.ensureWorkspace(state, index)
				if wsErr != nil {
					return wsErr
				}
				// Herdr restores a plugin pane as a plain shell without
				// re-running its command, so what is left in the space are
				// husks wearing the old name. Clear them before reopening.
				for _, paneID := range index.panesIn[workspaceID] {
					if mirror.IsLive(paneID) {
						state.shellPanes[paneID] = true
						continue
					}
					if err := herdrcli.ClosePaneByID(paneID); err != nil {
						log.Printf("close stale terminal %s: %v", paneID, err)
					}
					d.forgetPane(state, paneID)
					index.remove(paneID)
				}
			}
		}

		// Reopening carries on across passes, one terminal each, until the
		// machine has as many as it had. Only the clearing above is a
		// once-after-a-restart job -- doing that repeatedly would close a pane
		// that had just been opened and not yet said it was alive.
		if state.restoreShells > 0 {
			live := 0
			for paneID := range state.shellPanes {
				if index.alive[paneID] {
					live++
				}
			}
			if planRestoreShell(state.restoreShells, live) {
				log.Printf("%s: restoring terminal %d of %d after restart",
					state.host.Target, live+1, state.restoreShells)
				state.reopenShell = true
				return nil
			}
			// Restored. Forgetting the count matters: without it, closing one
			// of them afterwards would look like another to restore, and it
			// would come back.
			state.restoreShells = 0
		}

		for paneID := range state.shellPanes {
			if index.alive[paneID] {
				// A terminal that has stayed up means the machine is fine
				// again. Staying up is the part that was missing: a machine
				// that accepts a connection and then drops it has a terminal
				// that is up for a moment on every pass, and counting that as
				// recovery reset the tally every time -- so the giving up this
				// promises could not happen, and such a machine had a pane
				// opened and shut every couple of seconds for as long as the
				// session lasted.
				if state.lastReopen.IsZero() || time.Since(state.lastReopen) > reopenSettled {
					state.shellFailures = 0
				}
				continue
			}
			if planLostPane(mirror.Failed(paneID)) {
				state.shellFailures++
				// What killed the pane, when the bridge managed to record it.
				reason := mirror.FailureReason(paneID)
				switch planLostPaneAction(state.shellFailures, reason) {
				case stopUntilFixed:
					state.gaveUp = true
					state.lastErr = errors.New(reason)
					log.Printf("%s: terminal %s went — not reopening: %s",
						state.host.Target, paneID, summarizeError(state.lastErr))
				case stopForNow:
					state.gaveUp = true
					state.lastErr = fmt.Errorf("terminals keep dropping on %s", state.host.Target)
					log.Printf("%s: giving up after %d dropped terminals; connect again to retry",
						state.host.Target, state.shellFailures)
				default:
					log.Printf("%s: terminal %s dropped, reopening", state.host.Target, paneID)
					state.lastReopen = time.Now()
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
		running, live := mirror.LiveTerminal(paneID)
		switch planTrackedMirrorFor(state.adopted, index.alive[paneID], live,
			mirror.Failed(paneID), terminalID, running) {
		case mirrorKeep:
		case mirrorForget:
			// A bridge that failed counts against the same limit as one that
			// could not be opened at all. Otherwise a terminal something else
			// is holding gets a new pane every pass for ever: the pane opens,
			// the attach fails, the pane goes, and nothing counts it -- a
			// flicker in the sidebar with no end and no explanation. The reason
			// is read before the mark is cleared, since forgetting the pane
			// clears it.
			if reason := mirror.FailureReason(paneID); mirror.Failed(paneID) {
				d.backOff(state, terminalID, errors.New(firstLineOf(reason)))
			}
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
			index.remove(paneID)
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
	if d.config().SharedOnly() && planRemoteWorkspaceIsStale(sharedWorkspace, remotePanes) {
		found, lookupErr := d.findRemoteWorkspace(state)
		if lookupErr != nil {
			return lookupErr
		}
		if found {
			sharedWorkspace = state.remoteWorkspaceID
		} else {
			// Gone on the machine. Forgetting it stops every pane being
			// filtered against something that is not there any more.
			//
			// Making one again is not this loop's to do: reconciling does not
			// open terminals on a machine, deliberately, and a space comes with
			// one. It is made the next time the machine is connected to, which
			// is what the message says rather than promising it presently.
			if state.remoteWorkspaceID != "" {
				log.Printf("%s: its space on the machine is gone; connect again to make one",
					state.host.Target)
			}
			state.remoteWorkspaceID = ""
			sharedWorkspace = ""
		}
	}
	tabOrder, err := state.client.TabOrder()
	if err != nil {
		return err
	}
	// What the scope leaves out, counted before it is dropped. With the
	// default scope this mirrors the space it made on the machine and nothing
	// else, so somebody who already had terminals open there turns mirroring
	// on and sees one -- working exactly as designed, and looking exactly like
	// a machine whose other terminals failed to arrive.
	shared := planSharedPanes(remotePanes, sharedWorkspace, tabOrder, d.config().SharedOnly())
	state.outsideShared = len(remotePanes) - len(shared)
	remotePanes = shared

	// Closing a mirrored tab closes the terminal on the machine too. Without
	// this, mirroring is two-way for everything except closing: the tab goes
	// and the work quietly carries on over there.
	if d.config().ShouldClosePropagate() {
		d.closeRemoteTerminals(state, closedHere, remotePanes)
	}

	labels := d.labelsFor(state.host, remotePanes)

	backedOff := make(map[string]bool, len(state.retryAt))
	now := time.Now()
	for terminalID, until := range state.retryAt {
		if now.Before(until) {
			backedOff[terminalID] = true
		}
	}

	plan := planMirrors(remotePanes, mirrorState{
		Mirrored:  state.mirrors,
		Dismissed: state.dismissed,
		Abandoned: state.abandoned,
		BackedOff: backedOff,
		Max:       d.config().MaxMirrors,
	})
	state.atCapacity = plan.AtCapacity
	if plan.AtCapacity {
		log.Printf("%s: mirror limit of %d reached, skipping the rest; raise max_mirrors to see them",
			state.host.Target, d.config().MaxMirrors)
	}

	for _, rp := range plan.Existing {
		paneID := state.mirrors[rp.TerminalID]
		d.retitle(state, paneID, labels[rp.TerminalID])
		d.syncAgent(state, paneID, rp)
	}

	for _, rp := range plan.Open {
		if err := d.openMirror(state, rp, labels[rp.TerminalID], index); err != nil {
			d.backOff(state, rp.TerminalID, err)
			continue
		}
		delete(state.failures, rp.TerminalID)
		delete(state.retryAt, rp.TerminalID)
		if paneID, ok := state.mirrors[rp.TerminalID]; ok {
			d.syncAgent(state, paneID, rp)
		}
	}

	// Close mirrors whose terminal is gone on the machine.
	for _, terminalID := range plan.Gone {
		paneID := state.mirrors[terminalID]
		if err := herdrcli.ClosePane(paneID); err != nil {
			log.Printf("close mirror %s: %v", paneID, err)
		}
		// Struck from the listing this pass works from, as every other place
		// that closes a pane does. Without it the pane stayed in the listing,
		// alive as far as anything after this could tell, and no longer
		// belonging to the machine -- which is the description of a pane
		// somebody opened by hand in a machine's space, and those are moved
		// onto the machine. So closing a terminal on the machine closed its
		// mirror here and opened a fresh terminal there in its place: the work
		// went and something took its seat.
		index.remove(paneID)
		d.forgetPane(state, paneID)
		delete(state.mirrors, terminalID)
	}

	// Forget bookkeeping for terminals that no longer exist, so a reused id
	// starts clean.
	seen := make(map[string]bool, len(remotePanes))
	for _, rp := range remotePanes {
		seen[rp.TerminalID] = true
	}
	forgetTerminals(state, seen)

	state.strays = d.planStrayCapture(state, index)
	return nil
}

// strayPane is a local pane to move onto its machine, with the placement its
// replacement should use.
type strayPane struct {
	PaneID    string
	Placement string
}

// closeOrphans clears panes in a machine's space that carry a remote terminal's
// name but have no mirror behind them.
func (d *Daemon) closeOrphans(state *hostSync, index *paneIndex) {
	workspaceID := state.workspaceID
	if workspaceID == "" {
		found, err := d.findLocalWorkspace(state)
		if err != nil || !found {
			return
		}
		workspaceID = state.workspaceID
	}

	suffix := "@" + state.host.DisplayLabel()
	tracked := make(map[string]bool, len(state.mirrors)+len(state.shellPanes))
	for _, paneID := range state.mirrors {
		tracked[paneID] = true
	}
	for paneID := range state.shellPanes {
		tracked[paneID] = true
	}

	for _, paneID := range index.panesIn[workspaceID] {
		label := index.labelOf[paneID]
		if !planOrphanedPane(label, suffix, tracked[paneID], mirror.IsLive(paneID)) {
			continue
		}
		log.Printf("%s: closing %s, a leftover %q with nothing behind it",
			state.host.Target, paneID, label)
		if err := herdrcli.ClosePaneByID(paneID); err != nil {
			log.Printf("close leftover %s: %v", paneID, err)
		}
		d.forgetPane(state, paneID)
		index.remove(paneID)
	}
}

// findLocalWorkspace looks up this machine's space here without creating one.
func (d *Daemon) findLocalWorkspace(state *hostSync) (bool, error) {
	result, err := herdrcli.Run("workspace", "list")
	if err != nil {
		return false, err
	}
	workspaces, err := herdrcli.ParseWorkspaceList(result)
	if err != nil {
		return false, err
	}
	label := d.config().WorkspaceFor(state.host)
	if id, ok := pickWorkspace(workspaces, label, state.host.DisplayLabel()); ok {
		state.workspaceID = id
		return true, nil
	}
	return false, nil
}

// hostList is the tracked machines as a slice. Callers hold d.mu.
func (d *Daemon) hostList() []*hostSync {
	out := make([]*hostSync, 0, len(d.hosts))
	for _, state := range d.hosts {
		out = append(out, state)
	}
	return out
}

// claimedPanes lists every local pane that any machine has, of either kind.
//
// The machine being asked about is included whether or not it is registered
// yet, so this does not depend on the caller having got that far. Callers hold
// d.mu.
func (d *Daemon) claimedPanes(also *hostSync) map[string]bool {
	claimed := make(map[string]bool)
	for _, other := range append([]*hostSync{also}, d.hostList()...) {
		if other == nil {
			continue
		}
		for _, paneID := range other.mirrors {
			claimed[paneID] = true
		}
		for paneID := range other.shellPanes {
			claimed[paneID] = true
		}
	}

	// And what the last daemon left, for machines this one has not reached yet.
	//
	// The control socket is answering before startup has finished connecting,
	// on purpose, so the menu works throughout. Connect a machine in that
	// window and it reconciles while another machine's panes are still sitting
	// there belonging to nobody -- and in a space they all share, that reads as
	// somebody's stray pane and gets carried off. The machine that had it comes
	// up a moment later with its terminal on the wrong host.
	//
	// A pane in here is either about to be reclaimed by its own machine or
	// about to be closed as a leftover. Either way it is not a stray.
	for _, saved := range d.snapshot.Hosts {
		for _, paneID := range saved.Mirrors {
			claimed[paneID] = true
		}
	}
	return claimed
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

	// Every machine's panes, not just this one's. A space named outright holds
	// all of them, and asking only "is this one mine" made every other
	// machine's terminal look like a stray to be moved onto this machine and
	// closed here -- each of them doing that to the others, in turn.
	claimed := d.claimedPanes(state)

	var strays []strayPane
	for _, paneID := range index.panesIn[state.workspaceID] {
		// A pane that has gone is not a stray: there is nothing left to move.
		//
		// This pass closes one itself. A space Herdr creates comes with a shell
		// in it, which is retired as soon as there is a mirror to keep the
		// space alive -- and the listing this walks was taken before that
		// happened, so the retired shell was still in it, unclaimed, and read
		// as somebody's stray pane. Connecting to a mirrored machine therefore
		// opened a terminal on it that nobody asked for, every time.
		if !index.alive[paneID] {
			continue
		}
		if planStrayPane(d.config().ShouldCaptureNewPanes(), claimed[paneID], d.seenStray[paneID]) {
			strays = append(strays, strayPane{
				PaneID:    paneID,
				Placement: planStrayPlacement(index.panesPerTab[index.tabOf[paneID]]),
			})
		}
		d.seenStray[paneID] = true
	}

	// Forget panes that are gone, so ids reused later are judged afresh.
	for paneID := range d.seenStray {
		if !index.alive[paneID] {
			delete(d.seenStray, paneID)
		}
	}
	return strays
}

// captureStrayPanes carries out what planStrayCapture decided. It must run with
// the host lock released.
// The machine is taken by value rather than reached for through the hostSync:
// this runs outside the lock, on purpose, and connect writes state.host under
// it. Reading it here was a race nothing had exercised, since it needs a stray
// pane and a machine being reconnected at the same moment.
func (d *Daemon) captureStrayPanes(host config.Host, strays []strayPane) {
	for _, stray := range strays {
		log.Printf("%s: moving pane %s onto the machine as a %s",
			host.Target, stray.PaneID, stray.Placement)
		if err := d.openRemotePane(host, stray.Placement, true); err != nil {
			log.Printf("open terminal on %s: %v", host.Target, err)
			continue
		}
		if err := herdrcli.ClosePaneByID(stray.PaneID); err != nil {
			log.Printf("close local pane %s: %v", stray.PaneID, err)
		}
	}
}

// firstLineOf keeps a failure to one line for the log, since a bridge records
// everything the far side said and ssh is not brief about a refusal.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "the bridge failed"
	}
	return s
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
		// Worth saying. The machine-level giving up says so and says what to do
		// about it; this one stopped silently, leaving a terminal on the
		// machine that nothing here shows and nothing anywhere accounts for.
		// The listing now counts it, and this says which one and why.
		log.Printf("%s: giving up on terminal %s after %d attempts; it is still "+
			"open on the machine — connect again to try mirroring it",
			state.host.Target, terminalID, attempts)
		state.abandoned[terminalID] = true
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
	// Cleaned like every other name from the far machine: this reaches the
	// sidebar through report-agent rather than through the pane's label, and
	// that second route was left unguarded when the first one was fixed.
	agent := rp.SafeAgent()
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
	delete(d.seenStray, paneID)
	mirror.ClearLive(paneID)
	mirror.ClearFailed(paneID)
}

// maxLabelWidth bounds a terminal's name in the sidebar, which is narrow and
// shared with the machine it belongs to.
const maxLabelWidth = 28

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
	// A terminal's name comes from whatever is running on the far machine — a
	// shell sets its own title as a matter of course — and ends up in the
	// sidebar here. A newline or an escape sequence in one would be drawn
	// rather than read, and a long one crowds out everything beside it.
	name = text.Truncate(text.Sanitize(name), maxLabelWidth)
	if name == "" {
		// A name made only of things that cannot be drawn is left with nothing
		// once it is made safe, and the terminal arrives in the sidebar called
		// "@bot" -- which says only which machine it is on, and cannot be told
		// from any other terminal on that machine. The pane's own id is not a
		// name anybody chose, but it is the one thing that distinguishes it.
		name = text.Sanitize(rp.PaneID)
	}

	// The agent name comes from the far machine as well, and reaches the
	// sidebar the same way a terminal's name does whenever {agent} is used in
	// the format. Sanitising one and not the other left the same hole open by
	// a different route.
	replacer := strings.NewReplacer(
		"{name}", name,
		// Already safe: DisplayLabel cleans a label once, where it is read,
		// so that the pane's name, the space's name and the suffix they are
		// matched against cannot disagree about it. Left here because reading
		// this line should not require knowing that, and because it costs a
		// pass over a string that is already short.
		"{host}", text.Sanitize(host.DisplayLabel()),
		"{agent}", rp.SafeAgent(),
		"{pane}", text.Sanitize(rp.PaneID),
	)
	return replacer.Replace(d.config().LabelFormat)
}

// takeRequest reports how somebody asked for a terminal to appear, and returns
// a function that forgets the request.
//
// Forgetting is separate because opening a pane can fail and the mirror is
// retried afterwards: spending the request on the attempt rather than on the
// pane meant a "new tab on this machine" that failed once came back as whatever
// that machine's usual placement is, and unfocused.
func takeRequest(state *hostSync, terminalID, fallback string) (placement string, focus bool, done func()) {
	placement = fallback
	if requested, ok := state.pendingPlacement[terminalID]; ok {
		placement = requested
	}
	focus = state.pendingFocus[terminalID]

	return placement, focus, func() {
		delete(state.pendingPlacement, terminalID)
		delete(state.pendingFocus, terminalID)
	}
}

// openMirror creates the local pane that bridges one remote terminal.
func (d *Daemon) openMirror(state *hostSync, rp herdrcli.Pane, label string, index *paneIndex) error {
	workspaceID, err := d.ensureWorkspace(state, index)
	if err != nil {
		return err
	}

	env := map[string]string{
		mirror.EnvTarget:   state.host.Target,
		mirror.EnvSession:  d.config().SessionFor(state.host),
		mirror.EnvTerminal: rp.TerminalID,
		mirror.EnvMode:     string(d.config().EffectiveMode(state.host)),
		mirror.EnvName:     label,
	}
	if bin := d.config().BinFor(state.host); bin != "" {
		env[mirror.EnvBin] = bin
	}
	if !d.config().ShouldTakeover() {
		env[mirror.EnvTakeover] = "false"
	}

	placement, focus, asked := takeRequest(state, rp.TerminalID, d.config().PlacementFor(state.host))

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

	asked()

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
	label := d.config().WorkspaceFor(state.host)

	result, err := herdrcli.Run("workspace", "list")
	if err != nil {
		return "", err
	}
	workspaces, err := herdrcli.ParseWorkspaceList(result)
	if err != nil {
		return "", err
	}
	if id, ok := pickWorkspace(workspaces, label, state.host.DisplayLabel()); ok {
		state.workspaceID = id
		return id, nil
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

// pickWorkspace chooses the space that belongs to a name, preferring one that
// carries it exactly.
//
// The tolerant match exists so that changing workspace_format does not orphan
// the space a machine's terminals are already in, which means it also accepts a
// space called just "bot". Taking the first match found made that a matter of
// what order Herdr happened to list them in: somebody with a space of their own
// called "bot", and a machine called bot, could have either one adopted --
// renamed, and given terminals from the machine.
//
// An exact match is the space this made; anything else is a guess, and a guess
// should not win over a certainty.
func pickWorkspace(workspaces []herdrcli.Workspace, label, hostLabel string) (string, bool) {
	for _, ws := range workspaces {
		if ws.Label == label {
			return ws.WorkspaceID, true
		}
	}
	for _, ws := range workspaces {
		if sameWorkspace(ws.Label, hostLabel) {
			return ws.WorkspaceID, true
		}
	}
	return "", false
}

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

// forgetWorkspace drops a space that Herdr says is gone.
//
// A machine's space disappears when its last pane closes, and the id was kept
// regardless: every pass then renamed and marked a space that no longer
// existed, two failing calls each time, for as long as the daemon ran. The next
// pass finds or creates one as it would have at the start. Callers hold d.mu.
func (d *Daemon) forgetWorkspace(state *hostSync, workspaceID string) {
	log.Printf("%s: space %s is gone; will find it again", state.host.Target, workspaceID)
	if state.workspaceID == workspaceID {
		state.workspaceID = ""
	}
	delete(d.markedWorkspaces, workspaceID)
}

// workspaceMark is what was last put on a space: the name it was given, the
// marker it was told to carry, whether that failed, and when.
type workspaceMark struct {
	label  string
	token  string
	failed bool
	at     time.Time
}

// workspaceRepairInterval is how often a space is renamed and marked again
// even though nothing has changed, to undo anything that changed it from
// elsewhere.
//
// A variable so a test can shorten it.
var workspaceRepairInterval = 30 * time.Second

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
	label := d.config().WorkspaceLabelFor(state.host, connected)

	if !planWorkspaceMark(d.markedWorkspaces[workspaceID], label, want, time.Now()) {
		return
	}
	mark := workspaceMark{label: label, token: want, at: time.Now()}

	// The marker also lives in the space's name, because Herdr puts " · "
	// between sidebar tokens and a name is the only place a marker can sit
	// directly beside it.
	if label != "" {
		if _, err := herdrcli.Run("workspace", "rename", workspaceID, label); err != nil {
			if herdrcli.IsNotFound(err) {
				d.forgetWorkspace(state, workspaceID)
				return
			}
			log.Printf("rename space %s: %v", workspaceID, err)
			mark.failed = true
		}
	}

	// A space named outright can hold several machines, and its name is used as
	// given for that reason. The state marker is the same: two machines in
	// different states would each claim it, every couple of seconds, for as
	// long as both were connected. The rename above still runs, because that is
	// what keeps the name matching the config and so findable.
	if d.config().SharesWorkspace(state.host) {
		d.markedWorkspaces[workspaceID] = mark
		return
	}

	if _, err := herdrcli.Run("workspace", "report-metadata", workspaceID,
		"--source", agentSource,
		"--token", want+"="+remoteGlyph,
		"--clear-token", clear,
		"--clear-token", "remote"); err != nil {
		if herdrcli.IsNotFound(err) {
			d.forgetWorkspace(state, workspaceID)
			return
		}
		if !d.markedWorkspaces[workspaceID].failed {
			log.Printf("mark space %s: %v", workspaceID, err)
		}
		mark.failed = true
		d.markedWorkspaces[workspaceID] = mark
		return
	}
	d.markedWorkspaces[workspaceID] = mark
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
	index.remove(root)
}
