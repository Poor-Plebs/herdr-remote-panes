package syncd

import (
	"fmt"
	"sort"

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

// planTrackedMirror decides what to do with a mirror recorded from before.
//
// adopted reports whether this host has already been reconciled by this daemon.
// Until it has, a pane recorded in the snapshot needs checking: Herdr restores
// a plugin pane after a restart as an ordinary shell without re-running its
// command, so the pane id survives while the mirror does not. Adopting such a
// husk leaves a dead local shell wearing a remote pane's name.
func planTrackedMirror(adopted, paneAlive, mirrorRunning bool) mirrorAction {
	if !paneAlive {
		if adopted {
			// It was live a moment ago and is not now: the user closed it.
			return mirrorDismiss
		}
		return mirrorForget
	}
	if !adopted && !mirrorRunning {
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

// planRestoreShell decides whether a plain SSH machine needs a terminal
// reopened after the daemon restarts.
//
// Such a machine has nothing to discover: its terminals do not survive a Herdr
// restart and there is nothing running remotely to re-derive them from, so
// without this the machine is simply missing from the sidebar afterwards. Only
// machines that had a terminal open get one back; connecting is not implied.
func planRestoreShell(hadShells, liveShells int) bool {
	return hadShells > 0 && liveShells == 0
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

// planShellName names a plain SSH terminal from how many are already open.
//
// Numbering from a running total instead drifts: close the only terminal and
// the next one is called "shell 2", with no "shell 1" anywhere.
func planShellName(liveTerminals int) string {
	if liveTerminals == 0 {
		return "shell"
	}
	return fmt.Sprintf("shell %d", liveTerminals+1)
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
