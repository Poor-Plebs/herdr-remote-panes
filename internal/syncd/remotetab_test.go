package syncd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
)

// sharedSpaceOn is the space a machine's terminals are in.
func sharedSpaceOn(t *testing.T, statePath string) string {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var held fakeHerdr
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	for _, pane := range held.Panes {
		if id, _ := pane["workspace_id"].(string); id != "" {
			return id
		}
	}
	t.Fatal("the machine has no space to open a tab in")
	return ""
}

func TestATabOpenedOnTheMachineArrivesAsATab(t *testing.T) {
	// Reported: mirroring is on, a new tab is opened on the machine, and it
	// comes back here as a second pane in the last tab rather than a tab of
	// its own -- with placement set to "tab", which is what it is set to.
	here := withFakeHerdr(t)
	_, statePath := withRemoteHerdr(t)

	// The reporter's settings, rather than the defaults: scope and capture are
	// the two that decide what is mirrored and what is taken over, and a test
	// on defaults is a test of a configuration nobody is running.
	cfg := config.Defaults() // no placement set: the machine's shape is followed
	cfg.Scope = "shared"
	capture := true
	cfg.CaptureNewPanes = &capture
	cfg.Hosts = []config.Host{{Target: "bot", Mode: "attach"}}
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}
	before := tabsFor(here(), "bot")
	if len(before) == 0 {
		t.Fatal("nothing was mirrored to begin with")
	}

	// A tab opened over there, by somebody at that machine.
	addPaneInTabOn(t, statePath, sharedSpaceOn(t, statePath), "wR:t9", "a new tab")
	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}

	after := tabsFor(here(), "bot")
	if len(after) != len(before)+1 {
		t.Errorf("a tab opened on the machine arrived in %d tabs, and there were "+
			"%d before: %v — it was put in an existing tab rather than its own",
			len(after), len(before), after)
	}
}

func TestEditingTheConfigTakesEffectWithoutARestart(t *testing.T) {
	// The reported symptom, and its cause: placement is set to "tab" in the
	// file and mirrors keep arriving as splits. The daemon reads the config
	// twice in its life -- once at startup, and once as a side effect of
	// pressing m -- so an edit does nothing at all until Herdr is restarted,
	// while the file plainly says otherwise and the README says "edit the
	// config to change it".
	here := withFakeHerdr(t)
	_, statePath := withRemoteHerdr(t)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())

	cfg := config.Defaults() // placement: split
	cfg.Hosts = []config.Host{{Target: "bot", Mode: "attach"}}
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}

	// Somebody edits the file, the way the settings table invites them to.
	edited := cfg
	edited.Placement = "tab"
	if err := config.Save(edited); err != nil {
		t.Fatal(err)
	}

	before := tabsFor(here(), "bot")
	addPaneInTabOn(t, statePath, sharedSpaceOn(t, statePath), "wR:t9", "a new tab")
	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}

	if after := tabsFor(here(), "bot"); len(after) != len(before)+1 {
		t.Errorf("after setting placement to tab, a terminal opened on the machine "+
			"arrived in %d tabs and there were %d: %v — the edit was never read",
			len(after), len(before), after)
	}
}

func TestTerminalsSharingATabOnTheMachineShareOneHere(t *testing.T) {
	// The other half of following the machine. A tab with two terminals in it
	// over there is one tab with two panes here -- not two tabs, which would
	// be just as wrong as the collapsing was, in the other direction.
	here := withFakeHerdr(t)
	_, statePath := withRemoteHerdr(t)

	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Target: "bot", Mode: "attach"}}
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}
	before := tabsFor(here(), "bot")

	// Two terminals, one tab, the way somebody splits a pane over there.
	space := sharedSpaceOn(t, statePath)
	addPaneInTabOn(t, statePath, space, "wR:together", "left")
	addPaneInTabOn(t, statePath, space, "wR:together", "right")
	for i := 0; i < 6; i++ {
		d.reconcileAll()
	}

	after := tabsFor(here(), "bot")
	if len(after) != len(before)+1 {
		t.Errorf("two terminals sharing one tab on the machine arrived in %d tabs, "+
			"and there were %d before: %v — want one new tab holding both",
			len(after), len(before), after)
	}

	// And both of them did arrive, rather than one being lost into the count.
	panes := 0
	for _, pane := range here().Panes {
		if label, _ := pane["label"].(string); strings.HasSuffix(label, "@bot") {
			panes++
		}
	}
	if want := len(before) + 2; panes < want {
		t.Errorf("%d panes are mirroring bot, want at least %d: one of the pair "+
			"never opened", panes, want)
	}
}
