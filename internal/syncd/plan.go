package syncd

import "github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"

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
