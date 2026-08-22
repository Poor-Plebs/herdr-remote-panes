package herdrcli

import "testing"

// These pin the exact shapes Herdr returns. Reading a response from the wrong
// level is silent — it yields a zero value, not an error — which is how an
// empty pane id once made the daemon reopen a pane every two seconds.

func TestParseWorkspaceList(t *testing.T) {
	result, err := Decode([]byte(`{"id":"x","result":{"type":"workspace_list","workspaces":[
		{"workspace_id":"w1","label":"~","pane_count":1},
		{"workspace_id":"w2","label":"☁  bot","pane_count":3}]}}`), nil)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	workspaces, err := ParseWorkspaceList(result)
	if err != nil {
		t.Fatalf("ParseWorkspaceList: %v", err)
	}
	if len(workspaces) != 2 {
		t.Fatalf("got %d workspaces, want 2", len(workspaces))
	}
	if workspaces[1].Label != "☁  bot" || workspaces[1].PaneCount != 3 {
		t.Errorf("second workspace = %+v", workspaces[1])
	}

	// A marker in the label must not stop a machine's space being found.
	if id, ok := FindWorkspace(workspaces, "☁  bot"); !ok || id != "w2" {
		t.Errorf("FindWorkspace = %q, %v; want w2, true", id, ok)
	}
	if _, ok := FindWorkspace(workspaces, "nope"); ok {
		t.Error("FindWorkspace found a workspace that is not there")
	}
}

func TestParseWorkspaceListEmpty(t *testing.T) {
	// A session with no workspaces is normal, not an error.
	workspaces, err := ParseWorkspaceList([]byte(`{"type":"workspace_list","workspaces":[]}`))
	if err != nil {
		t.Fatalf("ParseWorkspaceList: %v", err)
	}
	if len(workspaces) != 0 {
		t.Errorf("got %d workspaces, want none", len(workspaces))
	}
}

func TestParseCreated(t *testing.T) {
	t.Run("a workspace comes with a pane", func(t *testing.T) {
		// The pane a new workspace comes with is the terminal that was asked
		// for; adding a tab as well opens two for one request.
		made, err := ParseCreated([]byte(`{"type":"workspace_created",
			"workspace":{"workspace_id":"w1","label":"bot"},
			"root_pane":{"pane_id":"w1:p1","terminal_id":"term_a"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if made.WorkspaceID != "w1" || made.RootPane.PaneID != "w1:p1" || made.RootPane.TerminalID != "term_a" {
			t.Errorf("got %+v", made)
		}
	})

	t.Run("a tab reports its terminal", func(t *testing.T) {
		// The terminal id is how a mirror opened later is matched back to the
		// request that created it.
		made, err := ParseCreated([]byte(`{"type":"tab_created",
			"tab":{"tab_id":"w1:t2"},
			"root_pane":{"pane_id":"w1:p2","terminal_id":"term_b"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if made.TabID != "w1:t2" || made.RootPane.TerminalID != "term_b" {
			t.Errorf("got %+v", made)
		}
	})

	t.Run("malformed input is an error", func(t *testing.T) {
		if _, err := ParseCreated([]byte(`not json`)); err == nil {
			t.Error("want an error for malformed input")
		}
	})
}

func TestParseTabOrder(t *testing.T) {
	// Herdr promises no order in a pane listing, so mirrors are sorted by the
	// tab order on the machine to keep both ends lined up.
	order, err := ParseTabOrder([]byte(`{"type":"tab_list","tabs":[
		{"tab_id":"w1:t1","number":1},
		{"tab_id":"w1:t5","number":2},
		{"tab_id":"w2:t1","number":1}]}`))
	if err != nil {
		t.Fatalf("ParseTabOrder: %v", err)
	}
	if order["w1:t5"] != 2 {
		t.Errorf("w1:t5 = %d, want 2", order["w1:t5"])
	}
	// Numbers repeat across workspaces, which is why the pane id breaks ties.
	if order["w1:t1"] != order["w2:t1"] {
		t.Errorf("tab numbers should repeat across workspaces: %v", order)
	}
	if _, ok := order["missing"]; ok {
		t.Error("an unknown tab should not appear")
	}
}
