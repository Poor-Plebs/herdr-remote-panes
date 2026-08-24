package syncd

import (
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
)

func newTestHost() *hostSync {
	return &hostSync{
		mirrors:          map[string]string{},
		dismissed:        map[string]bool{},
		failures:         map[string]int{},
		labels:           map[string]string{},
		reportedAgents:   map[string]agentReport{},
		shellPanes:       map[string]bool{},
		pendingPlacement: map[string]string{},
		pendingFocus:     map[string]bool{},
	}
}

func TestForgetPane(t *testing.T) {
	// Herdr reuses pane ids. Anything remembered about a pane that has gone
	// would then be applied to whatever lands on that id next — most visibly a
	// remembered name, which made the rename be skipped and left the new mirror
	// showing Herdr's default plugin pane title.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	state := newTestHost()
	state.labels["w1:p2"] = "build@bot"
	state.reportedAgents["w1:p2"] = agentReport{agent: "claude", state: "idle"}
	state.shellPanes["w1:p2"] = true

	// Another pane's bookkeeping must survive.
	state.labels["w1:p3"] = "tests@bot"

	// Which panes have been considered for moving is the daemon's record, not
	// a machine's: a pane belongs to one machine at most.
	d := &Daemon{seenStray: map[string]bool{"w1:p2": true}}
	d.forgetPane(state, "w1:p2")

	if got, ok := state.labels["w1:p2"]; ok {
		t.Errorf("label %q remembered for a pane that is gone", got)
	}
	if _, ok := state.reportedAgents["w1:p2"]; ok {
		t.Error("agent report remembered for a pane that is gone")
	}
	if state.shellPanes["w1:p2"] {
		t.Error("terminal still tracked for a pane that is gone")
	}
	if d.seenStray["w1:p2"] {
		t.Error("stray mark remembered for a pane that is gone")
	}
	if state.labels["w1:p3"] != "tests@bot" {
		t.Error("forgetting one pane disturbed another")
	}
}

func TestPaneIndex(t *testing.T) {
	index := newPaneIndex([]herdrcli.Pane{
		{PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1"},
		{PaneID: "w1:p2", TabID: "w1:t1", WorkspaceID: "w1"},
		{PaneID: "w2:p1", TabID: "w2:t1", WorkspaceID: "w2"},
	})

	for _, id := range []string{"w1:p1", "w1:p2", "w2:p1"} {
		if !index.alive[id] {
			t.Errorf("%s should be alive", id)
		}
	}
	if index.alive["gone"] {
		t.Error("a pane that was not listed should not be alive")
	}

	// A split needs some pane in the destination workspace to split from.
	if index.anyInWorkspace["w1"] == "" {
		t.Error("w1 should offer a pane to split from")
	}
	if index.workspaceOf["w2:p1"] != "w2" {
		t.Errorf("workspaceOf = %q, want w2", index.workspaceOf["w2:p1"])
	}
	if len(index.panesIn["w1"]) != 2 {
		t.Errorf("w1 has %d panes, want 2", len(index.panesIn["w1"]))
	}

	// Tab membership decides whether a captured pane comes back as a tab or a
	// split, so a pane sharing a tab must be counted as such.
	if index.panesPerTab["w1:t1"] != 2 {
		t.Errorf("w1:t1 has %d panes, want 2", index.panesPerTab["w1:t1"])
	}
	if index.panesPerTab["w2:t1"] != 1 {
		t.Errorf("w2:t1 has %d panes, want 1", index.panesPerTab["w2:t1"])
	}

	// Panes created mid-pass are registered, since the snapshot predates them.
	index.add(herdrcli.Pane{PaneID: "w1:p9", TabID: "w1:t9", WorkspaceID: "w1"})
	if !index.alive["w1:p9"] || index.anyInWorkspace["w1"] != "w1:p9" {
		t.Error("a pane added during the pass should be usable as a split target")
	}
}

func TestPaneIndexIgnoresPanesWithoutAWorkspace(t *testing.T) {
	// A popup has no pane id or workspace; it must not be recorded as a
	// candidate to split from.
	index := newPaneIndex([]herdrcli.Pane{{PaneID: "w1:p1"}})
	if index.anyInWorkspace[""] != "" {
		t.Error("a pane with no workspace should not be a split target")
	}
	if !index.alive["w1:p1"] {
		t.Error("the pane should still count as alive")
	}
}

func TestForgetPaneClearsMirrorBookkeeping(t *testing.T) {
	// Turning mirroring off for a machine leaves mirrors recorded that nothing
	// in the SSH path would ever revisit, so they would sit in its space as
	// dead panes wearing live names.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	state := newTestHost()
	state.mirrors["t1"] = "w1:p2"
	state.labels["w1:p2"] = "build@bot"
	state.reportedAgents["w1:p2"] = agentReport{agent: "claude", state: "idle"}

	// Which panes have been considered for moving is the daemon's record, not
	// a machine's: a pane belongs to one machine at most.
	d := &Daemon{seenStray: map[string]bool{"w1:p2": true}}
	d.forgetPane(state, "w1:p2")
	delete(state.mirrors, "t1")

	if len(state.mirrors) != 0 || len(state.labels) != 0 || len(state.reportedAgents) != 0 {
		t.Errorf("bookkeeping survived: mirrors=%v labels=%v agents=%v",
			state.mirrors, state.labels, state.reportedAgents)
	}
}

func TestRemovingAPaneDoesNotDisturbAWalkOverTheSpace(t *testing.T) {
	// Both callers do the same thing: walk a space's panes and take out the
	// ones they close as they go. So removing must leave the panes the walk has
	// not reached where the walk expects to find them.
	//
	// It did not. The slice was filtered in place, into the same array the
	// caller's range was reading, so closing one pane shifted the next one down
	// into a position already passed. The walk then skipped a pane entirely and
	// looked at a later one twice -- and a pane skipped by the loop that clears
	// husks is never revisited, because that clearing happens once, when the
	// machine is adopted.
	panes := []herdrcli.Pane{
		{PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1-t1"},
		{PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1-t1"},
		{PaneID: "w1:p3", WorkspaceID: "w1", TabID: "w1-t1"},
		{PaneID: "w1:p4", WorkspaceID: "w1", TabID: "w1-t1"},
	}
	index := newPaneIndex(panes)

	var visited []string
	for _, paneID := range index.panesIn["w1"] {
		visited = append(visited, paneID)
		index.remove(paneID)
	}

	want := []string{"w1:p1", "w1:p2", "w1:p3", "w1:p4"}
	if len(visited) != len(want) {
		t.Fatalf("the walk visited %v, want each pane once: %v", visited, want)
	}
	for i := range want {
		if visited[i] != want[i] {
			t.Fatalf("the walk visited %v, want each pane once: %v", visited, want)
		}
	}
	if left := index.panesIn["w1"]; len(left) != 0 {
		t.Errorf("after removing every pane the space still lists %v", left)
	}
}

func TestRemovingOnePaneLeavesTheRestOfTheSpaceIntact(t *testing.T) {
	// The bookkeeping a removal has to keep in step, checked field by field.
	// Marking a pane no longer alive and leaving it named as the pane to split
	// from is how a replacement mirror came to be opened beside the pane it was
	// replacing, a moment after that pane was closed.
	index := newPaneIndex([]herdrcli.Pane{
		{PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1-t1", Label: "one@bot"},
		{PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1-t1", Label: "two@bot"},
		{PaneID: "w1:p3", WorkspaceID: "w1", TabID: "w1-t2", Label: "three@bot"},
	})
	index.remove("w1:p1")

	if index.alive["w1:p1"] || index.labelOf["w1:p1"] != "" || index.workspaceOf["w1:p1"] != "" {
		t.Errorf("the removed pane is still known: alive=%v label=%q workspace=%q",
			index.alive["w1:p1"], index.labelOf["w1:p1"], index.workspaceOf["w1:p1"])
	}
	if got := index.panesIn["w1"]; len(got) != 2 || got[0] != "w1:p2" || got[1] != "w1:p3" {
		t.Errorf("the space lists %v, want the other two in order", got)
	}
	if got := index.anyInWorkspace["w1"]; got != "w1:p2" && got != "w1:p3" {
		t.Errorf("the space offers %q to split from, which is not one of its live panes", got)
	}
	if got := index.panesPerTab["w1-t1"]; got != 1 {
		t.Errorf("the tab holds %d panes, want 1", got)
	}
	// The tab the removed pane was not in must not have been counted down: that
	// count decides whether a stray pane becomes a tab or a split.
	if got := index.panesPerTab["w1-t2"]; got != 1 {
		t.Errorf("the other tab holds %d panes, want 1", got)
	}
	// Everything about the panes that stayed.
	if !index.alive["w1:p2"] || index.labelOf["w1:p2"] != "two@bot" || index.tabOf["w1:p2"] != "w1-t1" {
		t.Errorf("the pane that stayed lost something: %+v", index)
	}
}

func TestClosingThePaneASpaceSplitsFromPicksAnotherLiveOne(t *testing.T) {
	// The space names one of its panes as the one to place things beside. When
	// that pane is the one being closed, the replacement has to be a pane that
	// is still there -- naming a closed one is how a replacement mirror came to
	// be opened beside the pane it was replacing: Herdr answered pane_not_found,
	// the mirror never opened, and the machine was then judged to have no
	// terminals and given a spare one. Every restart, for as long as it was
	// mirrored.
	index := newPaneIndex([]herdrcli.Pane{
		{PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1-t1"},
		{PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1-t1"},
		{PaneID: "w1:p3", WorkspaceID: "w1", TabID: "w1-t1"},
	})
	splitFrom := index.anyInWorkspace["w1"]
	if splitFrom == "" {
		t.Fatal("the space names no pane to split from")
	}

	index.remove(splitFrom)

	next := index.anyInWorkspace["w1"]
	if next == "" {
		t.Fatalf("after closing %s the space offers nothing to split from, though two panes are left", splitFrom)
	}
	if next == splitFrom {
		t.Errorf("the space still splits from %s, which was just closed", next)
	}
	if !index.alive[next] {
		t.Errorf("the space splits from %s, which is not alive", next)
	}
}

func TestASpaceWhoseLastPaneWentHasNothingToSplitFrom(t *testing.T) {
	index := newPaneIndex([]herdrcli.Pane{
		{PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1-t1"},
	})
	index.remove("w1:p1")
	if got, ok := index.anyInWorkspace["w1"]; ok {
		t.Errorf("the empty space still offers %q to split from", got)
	}
	if got := index.panesPerTab["w1-t1"]; got != 0 {
		t.Errorf("the tab holds %d panes, want none", got)
	}
}
