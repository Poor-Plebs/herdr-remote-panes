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
