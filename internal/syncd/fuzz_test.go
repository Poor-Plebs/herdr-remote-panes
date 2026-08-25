package syncd

import (
	"encoding/json"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
)

// What Herdr says it has is taken on trust: a pane listing is decoded and then
// walked, sorted and matched against what is here. The decoding is fuzzed where
// it happens; this is the layer above, where the shapes are already valid JSON
// and still not what anybody expected -- a pane with no id, two panes sharing
// one, a tab nothing orders, a listing of nothing at all.
//
// None of that should be able to take the daemon down or hand back a pane that
// was not in the listing, because everything after this acts on the result:
// panes are opened, closed and renamed from it.

func FuzzPlanSharedPanes(f *testing.F) {
	for _, seed := range []string{
		`[]`,
		`[{"pane_id":"w1:p1","tab_id":"t1","workspace_id":"w1"}]`,
		`[{"pane_id":"w1:p1","tab_id":"t1","workspace_id":"w1"},{"pane_id":"w1:p2","tab_id":"t2","workspace_id":"w2"}]`,
		// The shapes nobody writes down: no ids at all, and two panes wearing
		// the same one.
		`[{"pane_id":"","tab_id":"","workspace_id":""}]`,
		`[{"pane_id":"p","tab_id":"t","workspace_id":"w"},{"pane_id":"p","tab_id":"t","workspace_id":"w"}]`,
		`[{"pane_id":"w1:p1","tab_id":"missing","workspace_id":"w1"}]`,
	} {
		f.Add([]byte(seed), "w1", true)
	}

	f.Fuzz(func(t *testing.T, raw []byte, shared string, sharedOnly bool) {
		var panes []herdrcli.Pane
		if err := json.Unmarshal(raw, &panes); err != nil {
			return // Not a listing; the decoder's job, and fuzzed where it is done.
		}
		if len(panes) > 64 {
			return // Nothing is learned from a longer one, and sorting is not free.
		}

		// A tab order that knows about some of the tabs and not others, which
		// is what a listing racing a tab being made looks like.
		order := map[string]int{}
		for i, pane := range panes {
			if i%2 == 0 {
				order[pane.TabID] = i
			}
		}

		got := planSharedPanes(panes, shared, order, sharedOnly)

		// Everything handed back was in the listing. What follows this opens,
		// closes and renames panes by what it returns, so a pane invented here
		// is a pane acted on that Herdr never mentioned.
		have := map[string]int{}
		for _, pane := range panes {
			have[pane.PaneID]++
		}
		for _, pane := range got {
			if have[pane.PaneID] == 0 {
				t.Fatalf("planSharedPanes returned %q, which was not in the listing", pane.PaneID)
			}
			have[pane.PaneID]--
		}

		// Sharing means sharing: with it on, nothing outside the machine's own
		// space comes back, which is what keeps the two ends showing the same
		// tabs.
		if sharedOnly {
			for _, pane := range got {
				if pane.WorkspaceID != shared {
					t.Fatalf("with scope shared, %q came back from space %q rather than %q",
						pane.PaneID, pane.WorkspaceID, shared)
				}
			}
		}

		// The same listing twice is the same order. This runs every couple of
		// seconds and the order decides which tab is which on both ends; one
		// that drifts reorders somebody's tabs under them.
		again := planSharedPanes(panes, shared, order, sharedOnly)
		if len(again) != len(got) {
			t.Fatalf("the same listing gave %d panes then %d", len(got), len(again))
		}
		for i := range got {
			if got[i].PaneID != again[i].PaneID {
				t.Fatalf("the same listing came back in a different order at %d: %q then %q",
					i, got[i].PaneID, again[i].PaneID)
			}
		}
	})
}
