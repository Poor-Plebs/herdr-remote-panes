package syncd

import (
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
