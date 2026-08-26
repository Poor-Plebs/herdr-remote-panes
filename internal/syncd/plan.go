package syncd

import (
	"fmt"
	"sort"
	"strings"

	"errors"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
	"time"
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
func planTrackedMirrorFor(adopted, paneAlive, mirrorRunning, failed bool, wantTerminal, hasTerminal string) mirrorAction {
	if !paneAlive {
		// A bridge that fails records why on its way out, which is the whole
		// point of that record: it tells a pane that dropped from one somebody
		// shut. Without reading it here, a mirror whose attach failed looked
		// exactly like a closed tab -- and a closed tab closes the terminal on
		// the machine, so a moment of trouble reaching a machine destroyed the
		// work on it. Forgetting it instead leaves the terminal alone and lets
		// the next pass mirror it again.
		if adopted && !failed {
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

	// Again, against the names just made rather than the ones came in with.
	//
	// Adding the pane id to a repeated name can land on a name somebody else
	// already has: two terminals called "build" become "build p1" and "build
	// p2", and a third whose own title is "build p1" was left alone, because
	// nothing it came in with was repeated. Three panes, two of them wearing
	// one name, which is the thing this function exists to prevent.
	//
	// The full pane id this time, which is unique, so one pass settles it.
	made := map[string]int{}
	for _, pane := range panes {
		made[labels[pane.TerminalID]]++
	}
	for _, pane := range panes {
		if made[labels[pane.TerminalID]] > 1 {
			labels[pane.TerminalID] += " " + pane.PaneID
		}
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
// where it is. Panes are ordered by the tab order on the remote, so what came
// first there comes first here. Herdr does not promise an order in a pane
// listing, and without sorting the two sides drift apart as panes come and go.
//
// The order, not the shape. Where each mirror is put is `placement`, which
// defaults to splitting -- so a machine with three tabs is three panes in one
// tab here, in the order its tabs are in there.
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

// planWorkspaceMark decides whether a space needs renaming and marking again.
//
// It used to happen on every pass, so a space whose name and marker had not
// changed since the last one was renamed to the name it already had and marked
// with the marker it already carried, a couple of seconds later, for as long as
// Herdr was open. Two processes spawned per machine per pass: with the default
// interval and five machines, better than five a second, for ever, to change
// nothing.
//
// Re-asserting is still worth doing, and still happens -- just on its own
// slower clock. The marker comes back if anything else clears it, and Herdr
// reuses space ids, so an id this remembers can turn out to belong to a
// different space. Both are repairs rather than the normal case. What the
// slower clock costs is that such a space reads wrongly for up to that long
// rather than for up to one pass.
//
// A failed attempt is always retried, since nothing was put there to keep.
func planWorkspaceMark(mark workspaceMark, label, token string, now time.Time) bool {
	if mark.failed || mark.label != label || mark.token != token {
		return true
	}
	return now.Sub(mark.at) >= workspaceRepairInterval
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

// lostPaneAction is what to do about a terminal that has gone.
type lostPaneAction int

const (
	// reopenPane opens another, which is right for a link that dropped.
	reopenPane lostPaneAction = iota
	// stopForNow leaves the machine alone after too many have dropped.
	stopForNow
	// stopUntilFixed leaves it alone because the failure needs a person.
	stopUntilFixed
)

// reopenSettled is how long a terminal has to stay up before the machine
// counts as steady again. A variable so a test can shorten it.
var reopenSettled = 30 * time.Second

// planLostPaneAction decides what a terminal going away means.
//
// Reopening used to be the only way to find out why one went, which is the
// right guess for a dropped connection and the wrong one for a changed host
// key: the replacement fails exactly as the first did. Two terminals flash open
// and shut, two more copies of a fifteen-line banner land in the log, and only
// then does anything say what is wrong. The bridge now records what killed it,
// so that case can be recognised without spending a pane on it.
//
// An unknown reason -- a pane marked by an older build, or one that left no
// trace -- still goes by the count, which is what it did before.
func planLostPaneAction(consecutiveFailures int, reason string) lostPaneAction {
	if reason != "" && settledFailure(errors.New(reason)) {
		return stopUntilFixed
	}
	if planGiveUp(consecutiveFailures, nil) {
		return stopForNow
	}
	return reopenPane
}

// planShellsToRestore is how many plain SSH terminals a machine should end up
// with when the way it is reached has just changed.
//
// A machine that had panes should not vanish because it stopped being
// mirrored: one terminal in the new style, so its space still has something in
// it. A count already saved from before a restart is what the machine actually
// had, and is kept -- coming back with one terminal where three were open is
// the same disappearance in smaller print, which is the bug planRestoreShell
// below was written for.
//
// Here rather than inline in the reconcile loop, because inline it could be
// turned inside out -- giving nothing to a machine whose panes had just been
// closed, or cutting a saved count of three down to one -- with nothing in the
// suite minding either.
func planShellsToRestore(hadPanes bool, saved int) int {
	if hadPanes && saved == 0 {
		return 1
	}
	return saved
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
// Retrying a machine every couple of seconds burns SSH connections, fills the
// log, and slows every other machine down, so after a couple of attempts it is
// left alone until it is connected to again -- which is an explicit "try now".
//
// Some failures do not even earn the second attempt. A changed host key, a name
// that does not resolve, a key the machine will not take: none of those can
// come good between one attempt and the next, so the retry is guaranteed to
// fail in exactly the same way. It is not free either -- an unresolvable name
// costs another DNS wait, and a changed host key another fifteen-line banner in
// the log -- and it delays saying the one thing worth saying, which is what to
// go and fix.
func planGiveUp(consecutiveFailures int, err error) bool {
	if settledFailure(err) {
		return true
	}
	return consecutiveFailures >= maxHostAttempts
}

// settledFailure says whether a failure is one that will not change on its own.
func settledFailure(err error) bool {
	known, ok := classify(err)
	return ok && known.settled
}

// classify matches a failure against the causes worth naming.
func classify(err error) (knownFailure, bool) {
	if err == nil {
		return knownFailure{}, false
	}
	message := err.Error()
	for _, known := range knownFailures {
		if strings.Contains(message, known.needle) {
			return known, true
		}
	}
	return knownFailure{}, false
}

// knownFailure is a cause of failure worth naming, and whether trying again
// could ever produce a different answer.
type knownFailure struct {
	needle, summary string
	// settled marks a failure that needs a person, not another attempt.
	settled bool
}

// knownFailures is the single list behind both what a failure is called and
// whether it is worth retrying, so the two cannot drift apart.
var knownFailures = []knownFailure{
	{"REMOTE HOST IDENTIFICATION HAS CHANGED", "host key changed — verify it, then update ~/.ssh/known_hosts", true},
	{"Host key verification failed", "host key not accepted", true},
	// Matched on ssh's own wording rather than on the words alone. What is
	// classified here is the whole failure text, and that carries whatever the
	// command on the machine printed: "bash: /opt/herdr: Permission denied" is
	// a file mode over there, and reading it as a refused key gave up on the
	// machine for good and sent somebody to look at their ssh key.
	//
	// ssh says it with the methods it tried in brackets, or asks again. Missing
	// a real one costs a retry and a rawer message; taking somebody else's
	// costs the machine.
	{"Permission denied (", "ssh permission denied — check your key", true},
	{"Permission denied, please try again", "ssh permission denied — check your key", true},
	{"Connection refused", "connection refused", false},
	{"Connection timed out", "connection timed out", false},
	// macOS words the same failure differently, so a timeout there used to
	// fall through and print the raw ssh line instead.
	{"Operation timed out", "connection timed out", false},
	{"Network is unreachable", "no network — check you are online", false},
	// Checked before the generic closed/reset rules below: the real message
	// is usually "kex_exchange_identification: Connection closed by remote
	// host", and the specific cause is the more useful of the two.
	{"kex_exchange_identification", "the machine dropped the connection before login — it may be busy or rate-limiting", false},
	{"Connection closed by", "the machine closed the connection", false},
	{"Connection reset by peer", "the machine reset the connection", false},
	{"Too many authentication failures", "too many keys offered — set IdentitiesOnly=yes for this host", true},
	{"Name or service not known", "host name does not resolve", true},
	{"Could not resolve hostname", "host name does not resolve", true},
	{"No route to host", "no route to host", false},
	// Taken from the error itself rather than copied out of it. This is the one
	// cause in the list that this plugin writes, so it is the one that can be
	// reworded on a quiet afternoon -- and a needle that no longer matches does
	// not fail, it just stops recognising a machine without Herdr on it.
	{remote.ErrNoHerdr.Error(), "herdr not found on the machine", true},
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

	if known, ok := classify(err); ok {
		return known.summary
	}

	// Otherwise the first line with anything on it, which is where the cause
	// usually is. The first line rather than the first non-empty one left a
	// message that opens with a blank line summarised as nothing at all -- and
	// nothing is what the listing and the menu would then show for a machine
	// that is not working, which is the one moment they have a job to do.
	for _, line := range strings.Split(message, "\n") {
		if strings.TrimSpace(line) != "" {
			message = line
			break
		}
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

// planSnapshotRestore lists the machines to reconnect at startup beyond those
// written in the config.
//
// A machine picked from ~/.ssh/config is never written to the config file, so
// the only record that it was connected is the snapshot. Starting up walked the
// config alone, which made "restarting brings your machines back" true for the
// ones written down and quietly false for the rest.
//
// A machine turned off in the config stays off: it is not connected at startup
// for the same reason it is not offered in the menu, and a snapshot from before
// it was turned off should not undo that.
func planSnapshotRestore(remembered []string, connected, disabled map[string]bool) []string {
	var out []string
	for _, target := range remembered {
		if connected[target] || disabled[target] {
			continue
		}
		out = append(out, target)
	}
	sort.Strings(out)
	return out
}
