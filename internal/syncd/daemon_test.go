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
