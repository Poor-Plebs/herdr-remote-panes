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

func FuzzPaneIndexSurvivesRemoval(f *testing.F) {
	for _, seed := range []string{
		`[{"pane_id":"w1:p1","tab_id":"t1","workspace_id":"w1"}]`,
		`[{"pane_id":"w1:p1","tab_id":"t1","workspace_id":"w1"},{"pane_id":"w1:p2","tab_id":"t1","workspace_id":"w1"}]`,
		// Two panes in one tab in one space is the ordinary case; the rest are
		// the ones a listing is not supposed to contain.
		`[{"pane_id":"p","tab_id":"t","workspace_id":"w"},{"pane_id":"p","tab_id":"t","workspace_id":"w"}]`,
		`[{"pane_id":"p1","tab_id":"","workspace_id":""}]`,
		`[]`,
	} {
		f.Add([]byte(seed), byte(1))
	}

	f.Fuzz(func(t *testing.T, raw []byte, order byte) {
		var panes []herdrcli.Pane
		if err := json.Unmarshal(raw, &panes); err != nil {
			return
		}
		if len(panes) > 32 {
			return
		}

		// One pane, one id. Herdr's are scoped to the space they are in --
		// "w1:p3" -- so two panes cannot share one, and everything here keys on
		// that: which pane mirrors which terminal, what each is called, which
		// space each belongs to. An index handed the same id twice does come
		// apart, and the fuzzer finds that in seconds; it is not a shape the
		// thing on the other end can produce, and holding this to it would mean
		// carrying duplicate handling through every map in the daemon for a
		// listing that cannot arrive.
		seen := map[string]bool{}
		for _, pane := range panes {
			if pane.PaneID == "" || seen[pane.PaneID] {
				return
			}
			seen[pane.PaneID] = true
		}

		index := newPaneIndex(panes)

		// Remove them in an order the input chooses, because the bug this
		// guards against was about which pane is left named after another goes.
		//
		// The offset matters as much as the step: a space names the pane added
		// last, so an order that always starts at the first one never removes
		// the named pane while others are still there -- which is the only case
		// where choosing the next one to name does anything at all.
		removed := map[string]bool{}
		for i := range panes {
			// Worked out inside the loop, which is the only place the listing
			// is known not to be empty. Outside it, a listing of nothing
			// divides by zero -- in the test rather than in the daemon, but a
			// test that panics is a test nobody can read the result of.
			step := int(order|1)%len(panes) + 1
			pane := panes[(int(order)+i*step)%len(panes)]
			index.remove(pane.PaneID)
			removed[pane.PaneID] = true

			// A space names a pane to split from. Naming one that has gone is
			// how a replacement came to be opened beside the pane it was
			// replacing: Herdr answered pane_not_found, nothing opened, and the
			// machine was then judged to have no terminals and given a spare
			// one -- every restart, for as long as it was mirrored.
			for space, id := range index.anyInWorkspace {
				if !index.alive[id] {
					t.Fatalf("space %q still names %q to split from, and it is gone", space, id)
				}
			}
			// And the other half, which naming nothing satisfies for free: a
			// space that still has panes has to name one of them. Without it
			// there is nowhere for the next mirror in that space to be put,
			// and opening one falls back to wherever the cursor happens to be.
			for space, ids := range index.panesIn {
				live := ""
				for _, id := range ids {
					if index.alive[id] {
						live = id
						break
					}
				}
				if live == "" {
					continue
				}
				if named, ok := index.anyInWorkspace[space]; !ok {
					t.Fatalf("space %q still holds %q and names nothing to split from", space, live)
				} else if !index.alive[named] {
					t.Fatalf("space %q names %q, which is gone, while %q is there", space, named, live)
				}
			}
			// A count of panes in a tab decides whether the next one splits or
			// opens a tab of its own. Below zero it is not a count of anything.
			for tab, n := range index.panesPerTab {
				if n < 0 {
					t.Fatalf("tab %q holds %d panes", tab, n)
				}
			}
		}

		// Everything gone means everything gone: a pane still listed in its
		// space is one a later pass walks and acts on.
		for id := range removed {
			if index.alive[id] {
				t.Fatalf("%q was removed and is still alive", id)
			}
			if _, ok := index.workspaceOf[id]; ok {
				t.Fatalf("%q was removed and still belongs to a space", id)
			}
			if _, ok := index.tabOf[id]; ok {
				t.Fatalf("%q was removed and still belongs to a tab", id)
			}
		}
		for space, ids := range index.panesIn {
			for _, id := range ids {
				if removed[id] {
					t.Fatalf("space %q still lists %q, which was removed", space, id)
				}
			}
		}
	})
}
