package syncd

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
)

// openedOnTheMachine adds a terminal to a machine's Herdr without this plugin
// having asked for it, which is what opening a tab over there is. Everything
// else in these tests makes remote terminals by telling the plugin to, and
// that is a different thing: it leaves a note saying how the mirror should be
// placed, where this leaves none.
func openedOnTheMachine(t *testing.T, statePath, tab string) string {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var held fakeHerdr
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	// Whichever space the machine's own terminals are in.
	workspace := ""
	for _, pane := range held.Panes {
		if id, _ := pane["workspace_id"].(string); id != "" {
			workspace = id
		}
	}
	if workspace == "" {
		t.Fatal("the machine has no space to open a tab in")
	}
	held.Next++
	paneID := "wR:p" + tab
	terminal := "term_on_machine_" + tab
	held.Panes[paneID] = map[string]any{
		"pane_id": paneID, "tab_id": "wR:t" + tab, "workspace_id": workspace,
		"terminal_id": terminal, "label": "",
	}
	out, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return terminal
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
	cfg := config.Defaults()
	cfg.Placement = "tab"
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
	openedOnTheMachine(t, statePath, "9")
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
	openedOnTheMachine(t, statePath, "9")
	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}

	if after := tabsFor(here(), "bot"); len(after) != len(before)+1 {
		t.Errorf("after setting placement to tab, a terminal opened on the machine "+
			"arrived in %d tabs and there were %d: %v — the edit was never read",
			len(after), len(before), after)
	}
}
