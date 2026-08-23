package syncd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
)

// This file holds the decisions the reconciler makes, separated from the calls
// that carry them out so they can be tested directly. Nearly every regression
// this plugin has had lived here: which placement fields may be sent together,
// whether a pane found at startup is a live mirror or a husk, and whether a
// missing pane means the user closed it.

// Placement values Herdr accepts for a plugin pane.
const (
	placementSplit   = "split"
	placementZoomed  = "zoomed"
	placementTab     = "tab"
	placementOverlay = "overlay"
	placementPopup   = "popup"
)

// paneTarget is where a new plugin pane should be attached.
type paneTarget struct {
	Placement  string
	Workspace  string
	TargetPane string
}

// planPaneTarget decides which targeting fields to send with plugin.pane.open.
//
// Herdr treats them as mutually exclusive and rejects any other combination
// with invalid_params: a split or zoomed pane takes only a target pane, which
// implies its workspace; a tab takes only a workspace; an overlay or popup
// takes neither and lands on the active pane. A split with nothing to split
// from falls back to a tab, because a workspace always accepts one.
func planPaneTarget(placement, workspaceID, paneInWorkspace string) paneTarget {
	switch placement {
	case placementSplit, placementZoomed:
		if paneInWorkspace == "" {
			return paneTarget{Placement: placementTab, Workspace: workspaceID}
		}
		return paneTarget{Placement: placement, TargetPane: paneInWorkspace}
	case placementOverlay, placementPopup:
		return paneTarget{Placement: placement}
	default:
		return paneTarget{Placement: placementTab, Workspace: workspaceID}
	}
}

// mirrorAction is what to do about one tracked mirror at the start of a pass.
type mirrorAction int

const (
	// mirrorKeep leaves a healthy mirror alone.
	mirrorKeep mirrorAction = iota
	// mirrorForget drops bookkeeping for a pane that is simply gone.
	mirrorForget
	// mirrorDismiss drops it and remembers the user closed it by hand.
	mirrorDismiss
	// mirrorReplace closes a pane that is no longer a running mirror so the
	// terminal can be mirrored again.
	mirrorReplace
)

// planTrackedMirrorFor decides what to do with a mirror recorded from before.
//
// adopted reports whether this host has already been reconciled by this daemon.
// Until it has, a pane recorded in the snapshot needs checking: Herdr restores
// a plugin pane after a restart as an ordinary shell without re-running its
// command, so the pane id survives while the mirror does not. Adopting such a
// husk leaves a dead local shell wearing a remote pane's name.
//
// wantTerminal and hasTerminal are the identity check: a pane id alone does not
// say which terminal is in it, and Herdr reuses pane ids.
//
// wantTerminal is the terminal the bookkeeping says this pane mirrors, and
// hasTerminal is what the running mirror says it is actually bridging. They
// differ when a pane id has been reused: the record survives, the pane behind
// it is now showing something else, and adopting it would leave one terminal
// unmirrored and another mirrored twice. An empty hasTerminal means the mark
// predates this being recorded and is taken at its word.
func planTrackedMirrorFor(adopted, paneAlive, mirrorRunning bool, wantTerminal, hasTerminal string) mirrorAction {
	if !paneAlive {
		if adopted {
			// It was live a moment ago and is not now: the user closed it.
			return mirrorDismiss
		}
		return mirrorForget
	}
	if adopted {
		return mirrorKeep
	}
	if !mirrorRunning {
		return mirrorReplace
	}
	if wantTerminal != "" && hasTerminal != "" && wantTerminal != hasTerminal {
		return mirrorReplace
	}
	return mirrorKeep
}

// planLabels names every mirror of one host so they stay distinguishable.
//
// Unnamed remote panes fall back to their working directory, so several shells
// in one directory would all read the same. Where a name repeats, the remote
// pane id is appended; short ids repeat across workspaces ("w2:p1" and "w3:p1"
// both shorten to "p1"), so the full id is used when the short one is not
// unique either.
func planLabels(panes []herdrcli.Pane) map[string]string {
	names := map[string]int{}
	shorts := map[string]int{}
	for _, pane := range panes {
		names[pane.DisplayName()]++
		shorts[shortPaneID(pane.PaneID)]++
	}

	labels := make(map[string]string, len(panes))
	for _, pane := range panes {
		name := pane.DisplayName()
		if names[name] > 1 {
			suffix := shortPaneID(pane.PaneID)
			if shorts[suffix] > 1 {
				suffix = pane.PaneID
			}
			name += " " + suffix
		}
		labels[pane.TerminalID] = name
	}
	return labels
}

// planStrayPane decides what to do about a pane sitting in a machine's space
// that this plugin did not put there.
//
// Herdr's own new-tab paths — the keybinding, and the plus icon in the tab bar,
// which no plugin can intercept — always create a local shell. In a space that
// exists to hold one machine's terminals, that is nearly always a mistake: the
// pane is on the wrong host. It is replaced with a terminal on that machine.
//
// Only panes noticed for the first time are replaced. A pane already seen and
// left alone stays left alone, so nothing is fought over repeatedly, and a
// pane deliberately kept there survives a daemon restart.
func planStrayPane(capture, isMirror, seenBefore bool) bool {
	if !capture || isMirror || seenBefore {
		return false
	}
	return true
}

// planStrayPlacement infers how a captured pane should be replaced, from the
// shape of what was created.
//
// A pane alone in its tab came from a new-tab path — the plus icon or the
// new-tab key — so its replacement is a tab. A pane sharing a tab came from a
// split, so it is replaced with a split. Replacing a tab with a split, or the
// reverse, rearranges the layout under someone who did not ask for it.
func planStrayPlacement(panesInSameTab int) string {
	if panesInSameTab <= 1 {
		return placementTab
	}
	return placementSplit
}

// planSharedPanes selects and orders the remote panes to mirror.
//
// With scope "shared" only this machine's own space on the remote is mirrored,
// so both ends show exactly the same terminals: the machine's other work stays
// where it is. Panes are ordered by the tab order on the remote, so the first
// tab here is the first tab there. Herdr does not promise an order in a pane
// listing, and without sorting the two sides drift apart as panes come and go.
func planSharedPanes(panes []herdrcli.Pane, sharedWorkspace string, tabOrder map[string]int, sharedOnly bool) []herdrcli.Pane {
	selected := make([]herdrcli.Pane, 0, len(panes))
	for _, pane := range panes {
		if sharedOnly && pane.WorkspaceID != sharedWorkspace {
			continue
		}
		selected = append(selected, pane)
	}

	sort.SliceStable(selected, func(i, j int) bool {
		a, b := selected[i], selected[j]
		if na, nb := tabOrder[a.TabID], tabOrder[b.TabID]; na != nb {
			return na < nb
		}
		return a.PaneID < b.PaneID
	})
	return selected
}

// planLostPane decides whether a pane that has gone should be reopened.
//
// A pane closes both when its bridge drops and when someone shuts the terminal,
// and the two need opposite responses: reopening a terminal someone just closed
// is infuriating, and never recovering from a dropped connection leaves the
// machine looking disconnected until it is reconnected by hand. The bridge
// records a failure on its way out, which tells them apart.
func planLostPane(failed bool) bool {
	return failed
}

// planRestoreShell decides whether another plain SSH terminal should be opened
// to bring a machine back to what it had before a restart.
//
// It reads the count, which is what the snapshot records and what this is named
// for. The reconcile loop had its own copy of this decision that treated the
// count as "there were some", so three terminals on a machine came back as one
// -- quietly, with the other two closed as husks a moment earlier. This
// function was written, tested, and never called.
//
// One per pass: restoring a machine with several is then a handful of SSH
// connections spread over a few seconds rather than all at once.
func planRestoreShell(hadShells, liveShells int) bool {
	return liveShells < hadShells
}

// planNeedsTerminal decides whether connecting to a machine should open one.
//
// It counts terminals that are actually still open, not how many have ever been
// opened: after closing the last one, a machine that answers "already has
// terminals" leaves the menu reporting a connection with nothing to show for
// it, and no way back short of editing the config.
func planNeedsTerminal(liveTerminals int) bool {
	return liveTerminals == 0
}

// planShellName picks a name for a new terminal on a machine, avoiding one
// already on screen.
//
// It used to be the count of live terminals plus one, which repeats the moment
// one in the middle is closed: with three open, closing the second gave the
// next terminal the third's name, and two panes were called "shell 3".
//
// label renders what a name would be shown as, so what is compared is what is
// actually drawn rather than a guess at how the format will treat it.
func planShellName(taken map[string]bool, label func(string) string) string {
	for n := 1; ; n++ {
		name := "shell"
		if n > 1 {
			name = fmt.Sprintf("shell %d", n)
		}
		if !taken[label(name)] {
			return name
		}
	}
}

// maxHostAttempts is how many times a machine is tried before it is left alone.
const maxHostAttempts = 2

// planGiveUp says whether to stop trying a machine.
//
// Some failures never resolve on their own — a changed host key needs someone
// to look at it — and retrying those every couple of seconds burns SSH
// connections, fills the log, and slows every other machine down. After a
// couple of attempts the machine is left alone until it is connected to again,
// which is an explicit "try now".
func planGiveUp(consecutiveFailures int) bool {
	return consecutiveFailures >= maxHostAttempts
}

// summarizeError reduces a failure to one line fit for a list.
//
// SSH is verbose when it refuses: a changed host key alone prints fifteen lines
// of banner, which turns a status listing into a wall of text and buries the
// machine it belongs to. The recognisable causes get a short phrase; anything
// else keeps its first line.
func summarizeError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()

	for _, known := range []struct{ needle, summary string }{
		{"REMOTE HOST IDENTIFICATION HAS CHANGED", "host key changed — verify it, then update ~/.ssh/known_hosts"},
		{"Host key verification failed", "host key not accepted"},
		{"Permission denied", "ssh permission denied — check your key"},
		{"Connection refused", "connection refused"},
		{"Connection timed out", "connection timed out"},
		// macOS words the same failure differently, so a timeout there used to
		// fall through and print the raw ssh line instead.
		{"Operation timed out", "connection timed out"},
		{"Network is unreachable", "no network — check you are online"},
		// Checked before the generic closed/reset rules below: the real message
		// is usually "kex_exchange_identification: Connection closed by remote
		// host", and the specific cause is the more useful of the two.
		{"kex_exchange_identification", "the machine dropped the connection before login — it may be busy or rate-limiting"},
		{"Connection closed by", "the machine closed the connection"},
		{"Connection reset by peer", "the machine reset the connection"},
		{"Too many authentication failures", "too many keys offered — set IdentitiesOnly=yes for this host"},
		{"Name or service not known", "host name does not resolve"},
		{"Could not resolve hostname", "host name does not resolve"},
		{"No route to host", "no route to host"},
		{"no herdr on the remote host", "herdr not found on the machine"},
	} {
		if strings.Contains(message, known.needle) {
			return known.summary
		}
	}

	// Otherwise the first line, which is where the cause usually is.
	if i := strings.IndexByte(message, '\n'); i >= 0 {
		message = message[:i]
	}
	// Trimmed by runes, not bytes: a host name or path can be non-ASCII, and
	// cutting mid-character would emit a broken rune into the sidebar.
	return truncateRunes(strings.TrimSpace(message), 90)
}

// truncateRunes shortens a string to at most max characters, ellipsis included.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max < 1 {
		return ""
	}
	return string(runes[:max-1]) + "…"
}

// mirrorPlan is what one reconcile pass should do about a machine's terminals.
type mirrorPlan struct {
	// Existing are terminals already mirrored, to be renamed and have their
	// agent state refreshed.
	Existing []herdrcli.Pane
	// Open are terminals that need a mirror.
	Open []herdrcli.Pane
	// Gone are terminal ids whose remote terminal has disappeared, so their
	// mirror should be closed here.
	Gone []string
	// AtCapacity reports that the per-machine limit stopped further mirrors.
	AtCapacity bool
}

// mirrorState is what the planner needs to know about a machine, kept as plain
// values so the decision can be exercised without a daemon or a network.
type mirrorState struct {
	Mirrored map[string]string
	// Dismissed are terminals whose pane someone closed by hand.
	Dismissed map[string]bool
	// Abandoned are terminals whose mirror failed too many times to keep
	// trying. Separate from Dismissed because only one of the two is worth
	// remembering across a restart.
	Abandoned map[string]bool
	// BackedOff are terminals whose mirror failed recently and should be left
	// until their retry is due.
	BackedOff map[string]bool
	Max       int
}

// planMirrors decides what to do about each of a machine's terminals.
//
// The order of the checks is the behaviour: a terminal already mirrored is only
// refreshed, one the user closed is left alone, one that keeps failing waits its
// turn, and the per-machine limit stops the rest rather than flooding the
// session. Terminals arrive in the order they appear on the machine, and that
// order is preserved so mirrors line up with the tabs there.
func planMirrors(remote []herdrcli.Pane, state mirrorState) mirrorPlan {
	var plan mirrorPlan
	seen := make(map[string]bool, len(remote))

	for _, pane := range remote {
		if pane.TerminalID == "" {
			continue
		}
		seen[pane.TerminalID] = true

		if _, ok := state.Mirrored[pane.TerminalID]; ok {
			plan.Existing = append(plan.Existing, pane)
			continue
		}
		if state.Dismissed[pane.TerminalID] || state.Abandoned[pane.TerminalID] ||
			state.BackedOff[pane.TerminalID] {
			continue
		}
		if state.Max > 0 && len(state.Mirrored)+len(plan.Open) >= state.Max {
			plan.AtCapacity = true
			break
		}
		plan.Open = append(plan.Open, pane)
	}

	for terminalID := range state.Mirrored {
		if !seen[terminalID] {
			plan.Gone = append(plan.Gone, terminalID)
		}
	}
	sort.Strings(plan.Gone)
	return plan
}

// planOrphanedPane decides whether a pane in a machine's space is a mirror that
// lost its process and should be closed.
//
// Herdr restores a plugin pane after a restart as a plain shell without
// re-running its command, so what is left wears the name of a remote terminal
// while being a local shell. Untracked ones — left behind by turning mirroring
// off, or by a restart the bookkeeping did not survive — would otherwise sit in
// the space forever.
//
// The name is what tells them from a terminal someone opened there themselves,
// which is moved onto the machine instead of being closed.
func planOrphanedPane(label, hostSuffix string, tracked, mirrorRunning bool) bool {
	if tracked || mirrorRunning || label == "" || hostSuffix == "" {
		return false
	}
	return strings.HasSuffix(label, hostSuffix)
}

// planRemoteWorkspaceIsStale reports whether a remembered remote space should
// be looked up again before it is trusted.
//
// The id is remembered from when the space was found or made, and a space goes
// when its last terminal does. A remembered id that matches nothing filters
// every pane out, so the machine looks as though it has no terminals -- with
// nothing said, which is worse than the noisy version of this the local side
// had. An empty space matches nothing either, so this asks once more and finds
// it again; that costs one call while a machine has nothing open in it.
func planRemoteWorkspaceIsStale(workspaceID string, panes []herdrcli.Pane) bool {
	if workspaceID == "" {
		return true
	}
	for _, pane := range panes {
		if pane.WorkspaceID == workspaceID {
			return false
		}
	}
	return true
}
