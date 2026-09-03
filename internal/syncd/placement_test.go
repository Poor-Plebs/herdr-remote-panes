package syncd

import (
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
)

func TestATerminalOpenedAsATabComesBackAsATab(t *testing.T) {
	// Reported: "after some time it converted my 3 tabs to one tab with the 3
	// split terminals inside."
	//
	// The placement a new terminal was asked for is a request, held until its
	// mirror opens and spent there. Nothing remembers it after that. So when a
	// mirror is opened again -- the link dropped, Herdr restarted, the pane
	// went -- it is placed by the machine's ordinary setting instead, which
	// defaults to split. And split into a space with a pane already in it is
	// exactly "one tab with the terminals inside".
	held := withFakeHerdr(t)
	withRemoteHerdrRunning(t, true)
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Target: "bot", Mode: "attach"}}

	before := New(cfg)
	if reply := before.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	// Two terminals, each asked for as a tab, which is what the new-tab action
	// sends.
	for i := 0; i < 2; i++ {
		if reply := before.dispatch(Command{Cmd: "open", Host: "bot", Placement: "tab"}); !reply.OK {
			t.Fatalf("open-tab: %s", reply.Message)
		}
	}
	for i := 0; i < 4; i++ {
		before.reconcileAll()
	}

	wanted := tabsFor(held(), "bot")
	if len(wanted) < 2 {
		t.Fatalf("two terminals asked for as tabs are sharing %d tab(s): %v — "+
			"they were not tabs to begin with, so this test proves nothing", len(wanted), wanted)
	}
	before.persist()

	// The pane went and the mirror is opened again. A restart is the tidiest
	// way to make that happen to every one of them at once.
	herdrRestarted(t)
	after := New(cfg)
	if reply := after.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	for i := 0; i < 6; i++ {
		after.reconcileAll()
	}

	got := tabsFor(held(), "bot")
	if len(got) < len(wanted) {
		t.Errorf("terminals opened as %d tabs came back in %d: %v — a tab somebody "+
			"asked for turned into a split behind their back", len(wanted), len(got), got)
	}
}

func TestPlainTerminalsOpenedAsTabsComeBackAsTabs(t *testing.T) {
	// The same bug on the path most people are on: ssh is the default mode, so
	// a machine with no settings gives plain terminals rather than mirrors.
	//
	// They are harder to remember. A machine reached by plain ssh has no Herdr
	// on it and so no terminal ids, and a restart restores a count of shells
	// rather than a list of them -- so there is nothing to hang a placement on,
	// and every restored terminal takes the machine's ordinary setting.
	held := withFakeHerdr(t)
	cfg := machineConfig("bot") // ssh, and placement defaults to split

	before := New(cfg)
	if reply := before.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 2; i++ {
		if reply := before.dispatch(Command{Cmd: "open", Host: "bot", Placement: "tab"}); !reply.OK {
			t.Fatalf("open-tab: %s", reply.Message)
		}
	}
	before.reconcileAll()

	wanted := tabsFor(held(), "bot")
	if len(wanted) < 3 {
		t.Fatalf("a terminal plus two asked for as tabs are in %d tab(s): %v — "+
			"they were not tabs to begin with, so this proves nothing", len(wanted), wanted)
	}
	before.persist()

	herdrRestarted(t)
	after := New(cfg)
	if reply := after.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	for i := 0; i < 8; i++ {
		after.reconcileAll()
	}

	got := tabsFor(held(), "bot")
	if len(got) < len(wanted) {
		t.Errorf("terminals opened as %d tabs came back in %d: %v — a tab somebody "+
			"asked for turned into a split behind their back", len(wanted), len(got), got)
	}
}

func TestRestoredTerminalsKeepTheOrderTheyWereOpenedIn(t *testing.T) {
	// The order is the whole of it, and getting it wrong looks like a partial
	// fix rather than a broken one. The first terminal restored goes into an
	// empty space and becomes a tab whatever it asked for -- so a split
	// restored first becomes a tab, and the tab that follows it absorbs the
	// next one. Three tabs come back as two, which is what a sorted-by-pane-id
	// list produced: pane ids do not sort into the order they were opened.
	held := withFakeHerdr(t)
	cfg := machineConfig("bot")

	before := New(cfg)
	if reply := before.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	// A split in the middle, so an order that is merely stable is not enough:
	// it has to be the order they were opened in.
	for _, where := range []string{"tab", "split", "tab"} {
		if reply := before.dispatch(Command{Cmd: "open", Host: "bot", Placement: where}); !reply.OK {
			t.Fatalf("open %s: %s", where, reply.Message)
		}
	}
	before.reconcileAll()
	wanted := tabsFor(held(), "bot")
	before.persist()

	herdrRestarted(t)
	after := New(cfg)
	if reply := after.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	for i := 0; i < 10; i++ {
		after.reconcileAll()
	}

	if got := tabsFor(held(), "bot"); len(got) != len(wanted) {
		t.Errorf("a machine with %d tabs came back with %d: %v, want the same shape as %v",
			len(wanted), len(got), got, wanted)
	}
}

func TestASnapshotFromAnOlderDaemonStillRestoresItsTerminals(t *testing.T) {
	// Upgrading means reading a snapshot the last version wrote, and that one
	// recorded how many plain terminals there were and not how each was placed.
	// The count still has to be honoured: fewer terminals after an upgrade is
	// worse than terminals in the wrong places.
	state := newTestHost()
	restoreFromSnapshot(state, hostSnapshot{Shells: 3})

	if state.restoreShells != 3 {
		t.Errorf("an older snapshot restores %d terminals, want 3", state.restoreShells)
	}
	if len(state.restoreShellsAs) != 0 {
		t.Errorf("placements were invented for a snapshot that recorded none: %q",
			state.restoreShellsAs)
	}
}

func TestFinishingARestoreForgetsThePlacementsWithTheCount(t *testing.T) {
	// The count and the list are two records of one thing and can disagree.
	// Placements are spent one per terminal opened, and a terminal that was
	// still there is counted without opening one -- so a restore can finish
	// with placements nobody used. planShellsToRestore takes the count from
	// zero back to one when a machine that had panes stops mirroring, and the
	// next terminal would then be placed the way something else was.
	held := withFakeHerdr(t)
	d := New(machineConfig("bot"))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}

	d.mu.Lock()
	state := d.hosts["bot"]
	// A restore that will finish with one of its terminals already there.
	state.restoreShells = 1
	state.restoreShellsAs = []string{"tab", "tab"}
	d.mu.Unlock()

	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}
	_ = held()

	d.mu.Lock()
	defer d.mu.Unlock()
	if state.restoreShells != 0 {
		t.Fatalf("the restore did not finish: %d left", state.restoreShells)
	}
	if len(state.restoreShellsAs) != 0 {
		t.Errorf("a finished restore still holds %q, which the next one would spend "+
			"on a terminal that never asked for it", state.restoreShellsAs)
	}
}

func TestATabKeepsItsPlaceWhenItsLinkDrops(t *testing.T) {
	// The reported case said "after some time", which is more likely a link
	// that dropped than a restart: the bridge dies, its pane goes, and the
	// mirror is opened again on the next pass. No restart, so nothing is read
	// back from the snapshot -- this is the same memory being used while the
	// daemon is still running, and it is the commoner of the two paths.
	held := withFakeHerdr(t)
	withRemoteHerdrRunning(t, true)
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Target: "bot", Mode: "attach"}}

	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	before := held()
	if reply := d.dispatch(Command{Cmd: "open", Host: "bot", Placement: "tab"}); !reply.OK {
		t.Fatalf("open-tab: %s", reply.Message)
	}
	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}

	// The pane that appeared, which is the one in the tab that was asked for.
	var opened string
	for id := range held().Panes {
		if _, existed := before.Panes[id]; !existed {
			opened = id
			break
		}
	}
	if opened == "" {
		t.Fatal("the new terminal opened no pane")
	}
	wanted := tabsFor(held(), "bot")
	if len(wanted) < 2 {
		t.Fatalf("the terminal asked for as a tab is sharing a tab: %v", wanted)
	}

	// Which terminal that pane was showing, so its backoff can be cleared
	// below.
	var terminalID string
	d.mu.Lock()
	for id, paneID := range d.hosts["bot"].mirrors {
		if paneID == opened {
			terminalID = id
		}
	}
	d.mu.Unlock()
	if terminalID == "" {
		t.Fatal("the pane that opened is not mirroring anything")
	}

	// Its link drops: the bridge fails and the pane goes with it.
	terminalDied(t, opened, "ssh: connect to host bot port 22: Connection refused")

	// A mirror that failed is backed off before it is tried again, so that a
	// persistent error cannot open a pane on every tick. These passes take no
	// real time at all, so without this the mirror is never retried and the
	// test would be measuring the backoff rather than the placement.
	for i := 0; i < 6; i++ {
		d.mu.Lock()
		delete(d.hosts["bot"].retryAt, terminalID)
		d.mu.Unlock()
		d.reconcileAll()
	}

	if got := tabsFor(held(), "bot"); len(got) < len(wanted) {
		t.Errorf("a tab whose link dropped came back in %d tab(s), want %d: %v",
			len(got), len(wanted), got)
	}
}

func TestForgettingOnePaneForgetsOnlyItsPlacement(t *testing.T) {
	// A sweep found that inverting the test in this cleanup changes nothing
	// any test would notice. Inverted it keeps the pane that went and drops
	// every other one -- so closing a single terminal would lose the
	// arrangement of all the terminals beside it, and a restart afterwards
	// would put them wherever the machine's setting says.
	d := New(machineConfig("bot"))
	state := newTestHost()
	state.shellPlacement = []shellPlace{
		{"w1:p1", "tab"}, {"w1:p2", "split"}, {"w1:p3", "tab"},
	}
	state.shellPanes["w1:p1"] = true
	state.shellPanes["w1:p2"] = true
	state.shellPanes["w1:p3"] = true

	d.forgetPane(state, "w1:p2")

	var left []string
	for _, shell := range state.shellPlacement {
		left = append(left, shell.paneID)
	}
	if len(left) != 2 {
		t.Fatalf("forgetting one pane left %d placements: %v", len(left), left)
	}
	for _, want := range []string{"w1:p1", "w1:p3"} {
		found := false
		for _, got := range left {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s lost its placement because a different pane went: %v", want, left)
		}
	}
	for _, shell := range state.shellPlacement {
		if shell.paneID == "w1:p2" {
			t.Error("the pane that went kept its placement, which the next restore would spend")
		}
	}
	// The order the rest were opened in is what makes them restorable, so
	// removing one from the middle must not shuffle the others.
	if state.shellPlacement[0].paneID != "w1:p1" {
		t.Errorf("the remaining placements are out of order: %v", left)
	}
}

// TestAPlacementSomebodyAskedForIsWrittenDownForNextTime holds the kept half of
// that note.
//
// Asking for a terminal as a tab writes the placement down twice: once to be
// spent by the pass that opens the mirror, and once to be kept. The spent one
// carries every visible thing that happens in this session, which is why the
// tests either side of this -- terminals asked for as tabs coming back as tabs
// after a restart -- pass without the kept one. Removing it fails none of them.
//
// The kept one is what reaches the snapshot, and the snapshot is the only thing
// that survives the daemon. Without it a restart has nothing to place a mirror
// by, so it falls back to the machine's ordinary setting, which defaults to
// split -- and a tab somebody asked for comes back inside another tab.
func TestAPlacementSomebodyAskedForIsWrittenDownForNextTime(t *testing.T) {
	withFakeHerdr(t)
	withRemoteHerdrRunning(t, true)
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Target: "bot", Mode: "attach"}}

	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	if reply := d.dispatch(Command{Cmd: "open", Host: "bot", Placement: "tab"}); !reply.OK {
		t.Fatalf("open-tab: %s", reply.Message)
	}
	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}
	d.persist()

	saved := loadSnapshot()
	host, ok := saved.Hosts["bot"]
	if !ok {
		t.Fatalf("the snapshot has nothing about the machine at all: %+v", saved)
	}
	// A mirror was made, or there is no placement for anything to be recorded
	// against and what follows would be about an empty pass.
	if len(host.Mirrors) == 0 {
		t.Fatalf("no mirror was made, so nothing was placed: %+v", host)
	}
	if len(host.Placement) == 0 {
		t.Fatalf("the snapshot remembers %d mirror(s) and no placement for any of "+
			"them: the next daemon has nothing to place them by, so a tab comes "+
			"back a split", len(host.Mirrors))
	}
	for terminalID, where := range host.Placement {
		if where != "tab" {
			t.Errorf("terminal %s was asked for as a tab and written down as %q",
				terminalID, where)
		}
	}
}

// TestTheWarningAboutAPlacementSaysWhatActuallyHappens holds a message in one
// package to the behaviour in another.
//
// A placement this does not know is not replaced by the default: only an empty
// one takes that, so a misspelling is kept as written and carried all the way
// here. What happens then is planPaneTarget's `default`, and the config's
// complaint tells somebody in advance what that will be -- "terminals will open
// as tabs". Nothing held the two together, so the sentence could go on saying
// tabs while the fallback became a split, and the only way to find out would be
// to misspell a placement and watch.
//
// It is checked here rather than in config because config cannot see
// planPaneTarget: syncd imports config, not the other way about.
func TestTheWarningAboutAPlacementSaysWhatActuallyHappens(t *testing.T) {
	const misspelled = "tabb"

	cfg := config.Defaults()
	cfg.Placement = misspelled
	problems := cfg.Problems()
	if len(problems) != 1 {
		t.Fatalf("a good config with one misspelled placement reported %d problems: %v",
			len(problems), problems)
	}
	if !strings.Contains(problems[0], "open as tabs") {
		t.Fatalf("the complaint no longer says what will happen instead, so there is "+
			"nothing here to hold: %q", problems[0])
	}

	// What actually happens to it. A pane to sit beside is given, so a fallback
	// that split rather than opening a tab would have somewhere to go -- with
	// none, the split branches open a tab anyway and this would pass whatever
	// the default did.
	got := planPaneTarget(misspelled, "w1", "w1:p2")
	if got.Placement != placementTab {
		t.Errorf("the config says a misspelled placement opens terminals as tabs, and "+
			"this one is placed as %q", got.Placement)
	}
	if got.Workspace != "w1" {
		t.Errorf("a tab is opened in the machine's space, and this one names %q", got.Workspace)
	}
}
