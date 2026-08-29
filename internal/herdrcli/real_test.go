package herdrcli

import (
	"encoding/json"
	"os"
	"testing"
)

// TestARealPaneListingParses holds the plugin's idea of Herdr's wire format to
// a recording of what Herdr actually sent.
//
// Every other test of this parser writes the JSON by hand, which means writing
// exactly the fields the parser reads. That cannot fail the way this is meant
// to fail: a field renamed on Herdr's side, or a listing wrapped differently,
// looks the same to a fixture built from the parser's own expectations.
//
// The recording is `herdr pane list` from Herdr 0.8.2, with two panes, one
// tab each, and a label on each -- the shape this plugin actually works on.
// The paths and titles in it are replaced; it went into a public repository.
//
// Refreshing it against a newer Herdr is the point. If a field has moved, this
// is where it shows.
func TestARealPaneListingParses(t *testing.T) {
	raw, err := os.ReadFile("testdata/pane-list-0.8.2.json")
	if err != nil {
		t.Fatal(err)
	}
	// The envelope Herdr puts around every answer, unwrapped the way Run does.
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("the recording is not the shape Run unwraps: %v", err)
	}

	panes, err := ParsePaneList(envelope.Result)
	if err != nil {
		t.Fatalf("parsing what Herdr really sent: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("got %d panes from a recording of two", len(panes))
	}

	// Each field this plugin reads, against what was actually in the listing.
	// Named one at a time rather than compared as a struct: what matters is
	// which field went missing, and a struct comparison says only that one did.
	first := panes[0]
	for _, tt := range []struct{ what, got, want string }{
		{"pane id", first.PaneID, "w1:p1"},
		{"tab id", first.TabID, "w1:t1"},
		{"workspace id", first.WorkspaceID, "w1"},
		{"label", first.Label, "build@bot"},
		{"agent status", first.AgentStatus, "unknown"},
		{"working directory", first.Cwd, "/home/you"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s came back %q, and the recording has %q -- the field it "+
				"is read from has moved", tt.what, tt.got, tt.want)
		}
	}
	if first.TerminalID == "" {
		t.Error("the terminal id is empty, and everything this plugin remembers " +
			"about a remote pane is keyed by it")
	}
	if first.Title == "" {
		t.Error("the stripped title is empty; it is the fallback name for a pane " +
			"with no label")
	}

	// The two panes are told apart, which is what a listing is for.
	if panes[1].PaneID == first.PaneID || panes[1].TabID == first.TabID {
		t.Errorf("two panes came back the same: %+v and %+v", first, panes[1])
	}
}

// resultOf unwraps a recorded reply the way Run does, so a test reads what the
// parsers are actually handed.
func resultOf(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("%s is not the shape Run unwraps: %v", name, err)
	}
	if len(envelope.Result) == 0 {
		t.Fatalf("%s has no result to parse", name)
	}
	return envelope.Result
}

// TestARealWorkspaceListingParses holds the shape the plugin decides from when
// it is looking for a machine's space. Reading it wrongly means not finding a
// space that is there, and not finding one is what makes this plugin create
// one -- with a terminal in it, on somebody's machine.
func TestARealWorkspaceListingParses(t *testing.T) {
	spaces, err := ParseWorkspaceList(resultOf(t, "workspace-list-0.8.2.json"))
	if err != nil {
		t.Fatalf("parsing what Herdr really sent: %v", err)
	}
	if len(spaces) != 2 {
		t.Fatalf("got %d spaces from a recording of two", len(spaces))
	}
	if spaces[0].WorkspaceID != "w1" || spaces[0].Label != "hrp probe" {
		t.Errorf("first space came back %+v; the recording has w1 labelled "+
			"\"hrp probe\"", spaces[0])
	}
	// Only the id and the label are taken from a listing. Whether a space has
	// anything in it is counted from the panes Herdr actually has, because a
	// terminal closed a moment ago may not have been reconciled away yet --
	// so the pane_count the listing carries is not read, and the recording is
	// here to show what else is on offer if it is ever wanted.
	if spaces[1].WorkspaceID != "w2" || spaces[1].Label != "hrp second" {
		t.Errorf("second space came back %+v; the recording has w2 labelled "+
			"\"hrp second\"", spaces[1])
	}
}

// TestARealTabListingParses holds the order mirrors are opened in.
func TestARealTabListingParses(t *testing.T) {
	order, err := ParseTabOrder(resultOf(t, "tab-list-0.8.2.json"))
	if err != nil {
		t.Fatalf("parsing what Herdr really sent: %v", err)
	}
	for tab, want := range map[string]int{"w1:t1": 1, "w1:t2": 2, "w2:t1": 1} {
		if got := order[tab]; got != want {
			t.Errorf("tab %s came back at %d and the recording has it at %d",
				tab, got, want)
		}
	}
	// Numbers repeat across spaces -- w1:t1 and w2:t1 are both 1 -- so the tab
	// id has to be what they are kept under. Keyed by number, one would have
	// replaced the other.
	if len(order) != 3 {
		t.Errorf("three tabs came back as %d entries: %v", len(order), order)
	}
}

// TestARealCreationParses holds both shapes ParseCreated is given: a workspace
// created, which carries the space, and a tab created, which does not.
func TestARealCreationParses(t *testing.T) {
	t.Run("a workspace", func(t *testing.T) {
		made, err := ParseCreated(resultOf(t, "workspace-create-0.8.2.json"))
		if err != nil {
			t.Fatalf("parsing what Herdr really sent: %v", err)
		}
		if made.WorkspaceID == "" {
			t.Error("no space id came back from creating a space")
		}
		if made.RootPane.PaneID == "" || made.RootPane.TerminalID == "" {
			t.Errorf("the pane that came with the space is %+v; everything "+
				"remembered about it is keyed by those", made.RootPane)
		}
	})

	t.Run("a tab", func(t *testing.T) {
		made, err := ParseCreated(resultOf(t, "tab-create-0.8.2.json"))
		if err != nil {
			t.Fatalf("parsing what Herdr really sent: %v", err)
		}
		if made.TabID == "" {
			t.Error("no tab id came back from creating a tab")
		}
		// Creating a tab says nothing about the space at the top level, and
		// the caller needs one: it comes off the pane. Worth pinning, because
		// the empty WorkspaceID here reads like a parsing fault otherwise.
		if made.WorkspaceID != "" {
			t.Errorf("creating a tab reported space %q; the recording has no "+
				"workspace of its own at the top level", made.WorkspaceID)
		}
		if made.RootPane.WorkspaceID == "" {
			t.Error("the pane that came with the tab names no space, so there " +
				"is nowhere to learn it from")
		}
	})
}
