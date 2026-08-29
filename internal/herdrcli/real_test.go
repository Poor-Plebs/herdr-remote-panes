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
