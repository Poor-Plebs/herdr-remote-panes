package syncd

import (
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
)

func TestAMirrorKilledOutrightLooksLikeAClosedTab(t *testing.T) {
	// close_propagates is on by default: closing a mirrored tab here closes the
	// terminal on the machine, with whatever was running in it. A tab that was
	// closed and a bridge that was killed leave the same trace -- none -- so
	// the daemon reads the second as the first.
	//
	// This is not a claim that it is wrong, but it is worth knowing which way
	// round the risk falls, because one outcome is a terminal left running and
	// the other is somebody's work destroyed.
	held := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Target: "bot", Mode: "attach"}}
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}
	before := len(there().Panes)
	if before == 0 {
		t.Fatal("nothing mirrored to begin with")
	}

	// A pane that went with no failure recorded, which is what a bridge killed
	// outright leaves behind -- the liveness code says so itself: "one killed
	// rather than failed leaves no file".
	var paneID string
	d.mu.Lock()
	for _, id := range d.hosts["bot"].mirrors {
		paneID = id
		break
	}
	d.mu.Unlock()
	if paneID == "" {
		t.Fatal("no mirror to kill")
	}
	closePaneByHand(t, paneID)
	_ = held

	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}

	// Pinned rather than judged. This is what close_propagates is for, and a
	// tab somebody shuts must close the terminal it was showing. What the test
	// records is that the same thing happens when nobody shut anything -- so
	// changing which way this falls is a decision somebody makes on purpose,
	// with this failing to tell them they made it.
	if after := len(there().Panes); after != before-1 {
		t.Errorf("the machine had %d terminals and has %d; a mirror that went "+
			"with no failure recorded closes the terminal it was showing",
			before, after)
	}
}
