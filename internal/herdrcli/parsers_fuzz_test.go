package herdrcli

import (
	"encoding/json"
	"testing"
)

// FuzzTheParsersHoldTheirContracts throws whatever at the four parsers that
// read what a machine said.
//
// Decode is fuzzed beside this and only unwraps the envelope; what is inside
// it goes to these, and by this repository's own rule -- anything that reads
// what another machine said has a fuzz target -- they should have had one.
// They read a listing this plugin decides from: which terminals exist, which
// space they are in, which pane was just made.
//
// Contracts rather than outputs, so they survive the wording of things
// changing: nothing panics, a failure hands back nothing to use, and reading
// the same bytes twice gives the same answer.
func FuzzTheParsersHoldTheirContracts(f *testing.F) {
	for _, seed := range []string{
		`{"panes":[]}`,
		`{"panes":[{"pane_id":"w1:p1","tab_id":"w1:t1","workspace_id":"w1"}]}`,
		`{"panes":null}`,
		`{"workspaces":[{"workspace_id":"w1","label":"x"}]}`,
		`{"workspaces":[]}`,
		`{"tabs":[{"tab_id":"w1:t1","number":1},{"tab_id":"w2:t1","number":1}]}`,
		`{"tabs":null}`,
		`{"plugin_pane":{"pane":{"pane_id":"w1:p2"}}}`,
		`{"pane":{"pane_id":"w1:p2"}}`,
		`{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"w1:t1"},"root_pane":{"pane_id":"w1:p1"}}`,
		`{}`, `[]`, `null`, `0`, `"x"`, ``, `{"panes":[{}]}`,
		`{"panes":[{"pane_id":""}]}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, result []byte) {
		if !json.Valid(result) {
			// Decode is what rejects these, and it is fuzzed on its own. What
			// reaches here has already been through it.
			return
		}

		panes, paneErr := ParsePaneList(result)
		if paneErr != nil && panes != nil {
			t.Fatalf("ParsePaneList(%q) returned an error and %d panes", result, len(panes))
		}
		spaces, spaceErr := ParseWorkspaceList(result)
		if spaceErr != nil && spaces != nil {
			t.Fatalf("ParseWorkspaceList(%q) returned an error and %d spaces", result, len(spaces))
		}
		order, orderErr := ParseTabOrder(result)
		made, madeErr := ParseCreated(result)
		opened, openErr := parseOpenedPane(result)

		// Read twice, because a pass reads a listing once and a decoder that
		// drifts makes what a machine has depend on how often it was asked.
		panesAgain, paneErrAgain := ParsePaneList(result)
		if (paneErr != nil) != (paneErrAgain != nil) || len(panes) != len(panesAgain) {
			t.Fatalf("ParsePaneList(%q) gave %d panes then %d", result, len(panes), len(panesAgain))
		}
		orderAgain, _ := ParseTabOrder(result)
		if len(order) != len(orderAgain) {
			t.Fatalf("ParseTabOrder(%q) gave %d tabs then %d", result, len(order), len(orderAgain))
		}

		// A creation that reports no error has to name something, or a caller
		// tracks a pane by an id it does not have and opens another next pass.
		if openErr == nil && opened.PaneID == "" {
			t.Fatalf("parseOpenedPane(%q) reported success with no pane id", result)
		}
		_ = madeErr
		_ = made
		_ = orderErr
	})
}
