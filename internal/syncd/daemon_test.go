package syncd

import (
	"errors"
	"io"
	"log"
	"reflect"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"

	"encoding/json"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/mirror"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
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
		placement:        map[string]string{},
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

func TestEveryPerPaneThingIsClearedWhenThePaneGoes(t *testing.T) {
	// The other half of what perTerminalFields does for forgetTerminals. Three
	// tests cover these four today, each written for its own reason, and none
	// of them would notice a fifth field being added: the guard that reads
	// perPaneFields only asks whether a new field is named somewhere, not
	// whether anything ever puts a pane in it. Naming it is the easy half.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	state := newTestHost()
	state.labels["w1:p2"] = "build@bot"
	state.reportedAgents["w1:p2"] = agentReport{agent: "claude", state: "idle"}
	state.shellPanes["w1:p2"] = true
	state.shellPlacement = []shellPlace{{"w1:p2", "tab"}}

	value := reflect.ValueOf(state).Elem()
	for _, name := range perPaneFields {
		field := value.FieldByName(name)
		switch {
		case !field.IsValid():
			t.Fatalf("perPaneFields names %s, which hostSync does not have", name)
		case field.Kind() != reflect.Map && field.Kind() != reflect.Slice:
			t.Fatalf("perPaneFields names %s, which is neither a map nor a slice", name)
		case field.Len() == 0:
			t.Fatalf("the fixture never puts a pane in %s, so forgetPane is not "+
				"asked about it -- put w1:p2 in it", name)
		}
	}

	d := &Daemon{seenStray: map[string]bool{}}
	d.forgetPane(state, "w1:p2")

	for _, name := range perPaneFields {
		if field := value.FieldByName(name); field.Len() != 0 {
			t.Errorf("%s still remembers a pane that is gone", name)
		}
	}
}

func TestForgetPaneClearsMirrorBookkeeping(t *testing.T) {
	// Turning mirroring off for a machine leaves mirrors recorded that nothing
	// in the SSH path would ever revisit, so they would sit in its space as
	// dead panes wearing live names.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	state := newTestHost()
	state.mirrors["t1"] = "w1:p2"
	state.labels["w1:p2"] = "build@bot"
	state.reportedAgents["w1:p2"] = agentReport{agent: "claude", state: "idle"}

	// The marks the mirror keeps on disk are bookkeeping about a pane too, and
	// this asked only about the maps in memory: deleting either clearing call
	// left it green. They matter more than the maps, because they outlive the
	// daemon. A stale live mark stops the next daemon reopening a pane it
	// should, and a stale failure puts a reason on a pane that never failed.
	//
	// One for the pane being forgotten, one for the pane beside it.
	for _, paneID := range []string{"w1:p2", "w1:p3"} {
		mirrorIsRunning(t, paneID, "t-"+paneID)
		if err := mirror.MarkFailed(paneID, "could not reach bot"); err != nil {
			t.Fatal(err)
		}
	}
	if !mirror.IsLive("w1:p2") || !mirror.Failed("w1:p2") {
		t.Fatal("the marks were never written, so clearing them would prove nothing")
	}

	// Which panes have been considered for moving is the daemon's record, not
	// a machine's: a pane belongs to one machine at most.
	d := &Daemon{seenStray: map[string]bool{"w1:p2": true}}
	d.forgetPane(state, "w1:p2")
	delete(state.mirrors, "t1")

	if len(state.mirrors) != 0 || len(state.labels) != 0 || len(state.reportedAgents) != 0 {
		t.Errorf("bookkeeping survived: mirrors=%v labels=%v agents=%v",
			state.mirrors, state.labels, state.reportedAgents)
	}
	if mirror.IsLive("w1:p2") {
		t.Error("a pane that is gone is still marked as having a mirror running")
	}
	if mirror.Failed("w1:p2") {
		t.Errorf("a pane that is gone still carries a failure: %q",
			mirror.FailureReason("w1:p2"))
	}
	// And the pane beside it keeps both, since forgetting is about one pane.
	if !mirror.IsLive("w1:p3") || !mirror.Failed("w1:p3") {
		t.Error("forgetting one pane cleared another pane's marks")
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

func TestAFailureIsKeptToOneLine(t *testing.T) {
	// A bridge records everything the far side said, and ssh is not brief about
	// a refusal -- a rejected host key runs to a dozen lines. What comes out of
	// this is what the daemon logs and what the machine's line in the menu and
	// in status then says, so it has to be one line, and it has to say
	// something.
	for _, tt := range []struct {
		what, in, want string
	}{
		{"a single line is itself", "connection refused", "connection refused"},
		{"a banner keeps its first line", "Permission denied\nkeyboard-interactive\nfailed", "Permission denied"},
		{"nothing at all still says something", "", "the bridge failed"},
		{"and so does a leading newline", "\nPermission denied", "the bridge failed"},
		{"a trailing newline is not a second line", "connection refused\n", "connection refused"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			if got := firstLineOf(tt.in); got != tt.want {
				t.Errorf("firstLineOf(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAPaneIdIsShortenedToThePartThatDistinguishesIt(t *testing.T) {
	// This is appended to a name when two remote shells would otherwise render
	// identically, so it goes in front of somebody. A pane id that does not
	// look the way this expects must come back whole rather than empty: a name
	// ending in "@bot" with nothing before it is worse than a long one.
	for _, tt := range []struct{ in, want string }{
		{"w2:p6", "p6"},
		{"p6", "p6"},
		{"w10:p123", "p123"},
		{"w2:", "w2:"}, // nothing after the colon: keep what there is
		{":", ":"},
		// A colon first, with a pane after it. Not something Herdr hands out,
		// but the difference between finding the colon and finding it after
		// something, which is one character in the condition.
		{":p6", "p6"},
		{"", ""},
		{"a:b:c", "c"}, // the last colon, so a scoped id keeps its pane part
	} {
		if got := shortPaneID(tt.in); got != tt.want {
			t.Errorf("shortPaneID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWhichMachinesComeBackAfterARestart(t *testing.T) {
	// Herdr restarting starts a new daemon over the old one's snapshot, and
	// this decides which machines it goes back to. Nothing tested the gathering
	// — only the rule it hands its three lists to — so a machine could be left
	// out of any of them and the rule would still look right.
	//
	// Disabled is the one that matters. "Skip it without removing it" is what
	// the settings table promises, and a restart is exactly when a machine
	// switched off last week would quietly come back.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	cfg := config.Defaults()
	cfg.Hosts = []config.Host{
		{Target: "bot"},
		{Target: "prod"},
		{Target: "ci", Disabled: true},
		{Target: "staging"},
	}
	d := New(cfg)
	d.snapshot = snapshot{Hosts: map[string]hostSnapshot{
		"bot":     {Shells: 1},
		"prod":    {Shells: 2},
		"ci":      {Shells: 1},
		"staging": {Shells: 1},
	}}
	// One of them has already been dealt with by starting up.
	d.hosts["prod"] = newTestHost()

	got := map[string]bool{}
	for _, target := range d.rememberedHosts() {
		got[target] = true
	}

	if !got["bot"] || !got["staging"] {
		t.Errorf("machines that were connected did not come back: %v", got)
	}
	if got["ci"] {
		t.Error("a machine that is switched off came back after the restart")
	}
	if got["prod"] {
		t.Error("a machine already connected was brought back a second time")
	}
}

func TestARestartWithNothingRememberedBringsNothingBack(t *testing.T) {
	// A first run has no snapshot, and connecting to every configured machine
	// on startup is not what this does — that is what `connect` with no machine
	// named is for, and doing it unasked opens a terminal on every machine
	// somebody has ever written down.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Target: "bot"}, {Target: "prod"}}
	d := New(cfg)

	if got := d.rememberedHosts(); len(got) != 0 {
		t.Errorf("a first run wants to connect to %v", got)
	}
}

func TestATabCountNeverGoesNegative(t *testing.T) {
	// The count of panes in a tab decides how a stray pane is placed: one or
	// fewer opens a tab of its own, more than one splits the tab it belongs
	// to. Nothing in this type can drive the count below zero -- remove only
	// decrements a pane it still holds a tab for, and takes that entry with it
	// -- so the guard is defence against a way of getting here that does not
	// exist yet, and no test could reach it by asking the type nicely.
	//
	// It matters once the tab fills up again. A count sitting at -1 reads as 0
	// and then 1 while two panes are really there, and the next stray pane
	// opens as a tab beside them instead of splitting in.
	index := newPaneIndex([]herdrcli.Pane{{PaneID: "w1:p1", TabID: "w1:t1"}})

	// The state the guard is for, which is why it is written rather than
	// arrived at: a pane that still names its tab, and a tab that has lost
	// count of it.
	index.panesPerTab["w1:t1"] = 0
	index.remove("w1:p1")

	if got := index.panesPerTab["w1:t1"]; got < 0 {
		t.Errorf("the tab holds %d panes, and a tab cannot hold fewer than none", got)
	}
	if got := planStrayPlacement(index.panesPerTab["w1:t1"]); got != placementTab {
		t.Errorf("a stray pane beside an empty tab is placed as %q, want %q", got, placementTab)
	}
}

func TestWhatIsRememberedAboutANewRemoteTerminal(t *testing.T) {
	// A terminal made on the machine is mirrored here by the next pass, and
	// how it should be placed -- and whether to jump to it -- is remembered
	// against the terminal id in the reply. Every test around this builds that
	// state by hand; nothing put it there through the code that records it.
	//
	// The reply is what `herdr tab create` answers with.
	made := json.RawMessage(`{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"w1:t1"},` +
		`"root_pane":{"pane_id":"w1:p1","terminal_id":"term-1"}}`)
	d := &Daemon{}

	t.Run("a placement with no focus", func(t *testing.T) {
		// The "new tab on this machine" action: somewhere to put it, and no
		// jumping to it. Nothing else in the pair is set, so a check that
		// looks at only one of them drops this case entirely.
		state := newTestHost()
		d.rememberPlacement(state, made, "tab", false)

		if got := state.pendingPlacement["term-1"]; got != "tab" {
			t.Errorf("the placement was recorded as %q, want %q", got, "tab")
		}
		if state.pendingFocus["term-1"] {
			t.Error("a terminal nobody asked to be taken to is marked for focus")
		}
	})

	t.Run("focus with no placement", func(t *testing.T) {
		state := newTestHost()
		d.rememberPlacement(state, made, "", true)

		if !state.pendingFocus["term-1"] {
			t.Error("the terminal that was asked for is not marked to be shown")
		}
		if len(state.pendingPlacement) != 0 {
			t.Errorf("a placement was invented: %v", state.pendingPlacement)
		}
	})

	t.Run("neither", func(t *testing.T) {
		state := newTestHost()
		d.rememberPlacement(state, made, "", false)

		if len(state.pendingPlacement)+len(state.pendingFocus) != 0 {
			t.Errorf("something was remembered about a terminal nobody asked anything of: %v %v",
				state.pendingPlacement, state.pendingFocus)
		}
	})

	t.Run("a reply naming no terminal", func(t *testing.T) {
		// Parseable, and with nothing to key the note against. Remembering it
		// anyway files it under the empty string, where the next reply that
		// names no terminal picks up the last one's placement.
		state := newTestHost()
		d.rememberPlacement(state, json.RawMessage(`{"root_pane":{"pane_id":"w1:p1"}}`), "tab", true)

		if len(state.pendingPlacement)+len(state.pendingFocus) != 0 {
			t.Errorf("a note was filed under no terminal at all: %v %v",
				state.pendingPlacement, state.pendingFocus)
		}
	})
}

// TestACallHerdrRefusedIsNotRememberedAsApplied holds the three guards that
// stand between a failed Herdr call and the bookkeeping that says it worked.
//
// Each of these caches exists to stop reconcile redoing work every poll, so
// each is a claim that Herdr has already been told something. Recording that
// claim when the call in fact failed does not lose one update, it loses every
// future one: the cache matches from then on and the call is never made again.
//
// TestForgetPane holds the other half of the same hazard -- what is remembered
// about a pane that has gone. This is what is remembered about a call that
// never landed, which is the half with no pane death to clear it.
//
// The guard in each case is the bare return after the log line. No operator is
// flipped by removing one, and it is a return rather than a side effect, so
// neither make mutants nor make deletions would offer it: all three survived
// being taken out by hand with the whole suite green.
func TestACallHerdrRefusedIsNotRememberedAsApplied(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	withBrokenHerdr(t)

	// The failures are logged on purpose; this test is about what is kept, not
	// about what is said.
	saved := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(saved) })

	d := &Daemon{}

	t.Run("a name that did not take", func(t *testing.T) {
		// Left in the cache, the pane keeps Herdr's default plugin pane title
		// rather than the machine's name, and no later poll ever tries again.
		state := newTestHost()
		d.retitle(state, "w1:p2", "build@bot")
		if got, ok := state.labels["w1:p2"]; ok {
			t.Errorf("remembered %q as the pane's name though the rename failed", got)
		}
	})

	t.Run("an agent that was not reported", func(t *testing.T) {
		// Left in the cache, the sidebar keeps showing a bare ssh pane and the
		// agent it is running is never announced again.
		state := newTestHost()
		d.syncAgent(state, "w1:p2", herdrcli.Pane{Agent: "claude", AgentStatus: "working"})
		if got, ok := state.reportedAgents["w1:p2"]; ok {
			t.Errorf("remembered %+v as reported though the report failed", got)
		}
	})

	t.Run("an agent that was not released", func(t *testing.T) {
		// Dropped from the cache, the plugin forgets it ever claimed the agent,
		// so it never releases it: the sidebar shows one that stopped running.
		state := newTestHost()
		state.reportedAgents["w1:p2"] = agentReport{agent: "claude", state: "idle"}
		d.syncAgent(state, "w1:p2", herdrcli.Pane{})
		if _, ok := state.reportedAgents["w1:p2"]; !ok {
			t.Error("forgot the agent had been reported though the release failed")
		}
	})
}

// TestAFocusThatFailedIsNotAlsoAnnouncedAsDone holds the guard between a
// refused focus and the line that says it happened.
//
// The log is the only account of what the daemon did, and focusHost exists
// because a menu that appears to do nothing is the complaint being answered.
// Somebody reading the log to find out why picking a machine did not bring its
// space to the front is exactly who "bot: focused w1" misleads: it names the
// space it did not go to, immediately under the error saying why.
//
// Three of the other guard returns in this file were measured and left alone.
// Falling through the marshal failure in saveSnapshot cannot happen at all --
// a snapshot is strings, ints and maps of them, which json.MarshalIndent has
// no way to refuse. Falling through the workspace lookup above only reaches
// the "no space of its own to go to" line, which says the same thing. Both are
// equivalent rather than unheld.
func TestAFocusThatFailedIsNotAlsoAnnouncedAsDone(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	withBrokenHerdr(t)

	var logged strings.Builder
	saved := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(saved) })

	state := newTestHost()
	state.workspaceID = "w1"
	d := &Daemon{hosts: map[string]*hostSync{"bot": state}}

	d.focusHost("bot")

	out := logged.String()
	// The fixture proves itself: a focus that succeeded would leave no refusal
	// to find, and this test would be asserting about a call that never failed.
	if !strings.Contains(out, "server_unavailable") {
		t.Fatalf("the focus did not actually fail, so nothing here is being tested: %q", out)
	}
	if strings.Contains(out, "focused w1") {
		t.Errorf("the focus was refused and the log announces it anyway: %q", out)
	}
}

// TestAMachineLetGoOfMidCallIsNoLongerThisPassToDecide holds the re-check that
// awayFromTheLock makes when it takes the daemon's lock back.
//
// The lock is dropped for the round trip on purpose, so the menu and the
// status listing stay answerable while three machines are being polled. The
// price is that a command can run in the gap, and disconnect is one: it takes
// the machine out of d.hosts and closes its panes. A pass that carried on
// would then be acting for a machine somebody had just let go of, reopening
// the panes the disconnect had closed, and -- because the failure is recorded
// against the machine -- counting the disconnect as that machine's fault.
//
// The whole of the guard is one comparison against d.hosts, and nothing
// reached it: the pass paths that use this all run with a machine that stays
// put. Both directions are checked below, because a guard that fires always
// passes the half of this test that matters and breaks everything else.
func TestAMachineLetGoOfMidCallIsNoLongerThisPassToDecide(t *testing.T) {
	newDaemon := func() (*Daemon, *hostSync) {
		state := newTestHost()
		state.host = config.Host{Target: "bot"}
		return &Daemon{hosts: map[string]*hostSync{"bot": state}}, state
	}

	t.Run("let go of while the lock was down", func(t *testing.T) {
		d, state := newDaemon()

		ran := false
		d.mu.Lock()
		got, err := awayFromTheLock(d, state, func(*remote.Client) (int, error) {
			// The lock really is down here: taking it is what proves it, and
			// disconnect is doing exactly this much while the pass waits.
			ran = true
			d.mu.Lock()
			delete(d.hosts, "bot")
			d.mu.Unlock()
			return 42, nil
		})
		d.mu.Unlock()

		if !ran {
			t.Fatal("the call never ran, so nothing here was tested")
		}
		if !errors.Is(err, errWentAwayMidPass) {
			t.Errorf("err = %v, want errWentAwayMidPass", err)
		}
		if got != 0 {
			t.Errorf("answered %d for a machine that had gone; the pass would carry on with it", got)
		}
	})

	t.Run("still the same machine", func(t *testing.T) {
		// The control: without it, a guard that refused everything would look
		// exactly as good as the one that is here.
		d, state := newDaemon()

		d.mu.Lock()
		got, err := awayFromTheLock(d, state, func(*remote.Client) (int, error) {
			return 42, nil
		})
		d.mu.Unlock()

		if err != nil {
			t.Errorf("err = %v, want the answer to come back", err)
		}
		if got != 42 {
			t.Errorf("answered %d, want 42", got)
		}
	})
}

// TestADuplicateIsWarnedAboutInTheTermsItActuallyBehavesIn ties the warning to
// the behaviour it describes.
//
// A target written twice is not merged and not rejected, so every reader
// chooses one for itself. This one -- hostConfig, which decides how the
// machine is reached and what mode it is operated in -- takes the first match.
// The menu takes the last, because it overwrites the row it already holds for
// a target.
//
// The warning used to say "only the last entry counts", which reads as advice
// and sends somebody to edit the entry that does not decide how their machine
// is reached. A message about what happens is only worth having while it is
// still true, so it is checked here against the thing it is about rather than
// against itself in the package it is written in.
func TestADuplicateIsWarnedAboutInTheTermsItActuallyBehavesIn(t *testing.T) {
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{
		{Target: "bot"},
		{Target: "bot", Mode: config.ModeObserve},
	}
	d := withConfig(&Daemon{}, cfg)

	// The two entries have to disagree, or which one is picked says nothing.
	if cfg.EffectiveMode(cfg.Hosts[0]) == cfg.EffectiveMode(cfg.Hosts[1]) {
		t.Fatalf("both entries mean the same thing: %+v", cfg.Hosts)
	}

	host, err := d.hostConfig("bot")
	if err != nil {
		t.Fatalf("hostConfig: %v", err)
	}
	if got := cfg.EffectiveMode(host); got != cfg.EffectiveMode(cfg.Hosts[0]) {
		t.Errorf("the machine is reached in %q mode, and the warning says the first "+
			"entry decides, which is %q", got, cfg.EffectiveMode(cfg.Hosts[0]))
	}

	warning := strings.Join(d.config().Problems(), "\n")
	if !strings.Contains(warning, "listed more than once") {
		t.Fatalf("nothing warned that bot is listed twice: %q", warning)
	}
	if !strings.Contains(warning, "first entry") {
		t.Errorf("the warning does not say which entry the machine is reached "+
			"under, which is the one thing somebody needs from it: %q", warning)
	}
}
