package syncd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withRemoteHerdr adds a machine that has Herdr on it to the stand-in world.
//
// The ssh on PATH runs the remote command against a second copy of the
// stand-in, with its own state file, which is what a second machine is from
// here: the same program, a different set of panes. That makes the mirroring
// path drivable -- the only mode where this plugin talks to the far end at all.
func withRemoteHerdr(t *testing.T) (func() fakeHerdr, string) {
	t.Helper()

	dir := t.TempDir()
	remoteState := filepath.Join(dir, "remote-herdr.json")

	// ssh runs whatever it was given, with herdr on the far side being the
	// stand-in pointed at the machine's own panes. The probe that looks for
	// herdr is answered with that binary's path.
	script := "#!/bin/sh\n" +
		"last=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in\n" +
		"  *command\\ -v\\ herdr*) echo " + fakeHerdrBin + "; exit 0;;\n" +
		"  *--version*) echo 'herdr 0.8.0'; exit 0;;\n" +
		"  true) exit 0;;\n" +
		"esac\n" +
		"HRP_TEST_FAKE_HERDR_STATE=" + remoteState + " eval \"$last\"\n"

	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	return func() fakeHerdr {
		t.Helper()
		var held fakeHerdr
		raw, err := os.ReadFile(remoteState)
		if err != nil {
			return held
		}
		if err := json.Unmarshal(raw, &held); err != nil {
			t.Fatalf("reading what the machine is holding: %v", err)
		}
		return held
	}, remoteState
}

// addPaneOn puts a pane into a machine's own state, as work started there does:
// a space of its own, nothing to do with the one shared with this machine.
func addPaneOn(t *testing.T, statePath, workspace, title string) string {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var held fakeHerdr
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	if held.Workspaces == nil {
		held.Workspaces = map[string]map[string]any{}
	}
	if _, ok := held.Workspaces[workspace]; !ok {
		held.Workspaces[workspace] = map[string]any{"workspace_id": workspace, "label": workspace}
	}
	held.Next++
	id := fmt.Sprintf("%s:p%d", workspace, held.Next)
	held.Panes[id] = map[string]any{
		"pane_id": id, "tab_id": workspace + "-tab", "workspace_id": workspace,
		"terminal_id": fmt.Sprintf("term_%d", held.Next), "label": "",
		"terminal_title_stripped": title,
	}
	out, _ := json.Marshal(held)
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMirroringGivesOneTerminalForOneOnTheMachine(t *testing.T) {
	// The only mode where this plugin talks to the far end at all, and the one
	// with no end-to-end test until the stand-in learned to be a second
	// machine: the same program, a different set of panes, reached by running
	// the command ssh was handed.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 3; i++ {
		d.reconcileAll()
	}

	// One terminal on the machine, and one here showing it. The extra one is
	// what this is really about: a space Herdr creates comes with a shell in
	// it, retired as soon as a mirror can keep the space alive -- and the
	// retired shell was still in the listing this walks, unclaimed, so it read
	// as somebody's stray pane and was "moved onto the machine". Connecting
	// opened a terminal there that nobody asked for, every time.
	remote := there()
	if len(remote.Panes) != 1 {
		t.Errorf("connecting made %d terminals on the machine, want 1: %+v",
			len(remote.Panes), remote.Panes)
	}
	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("%d mirrors here, want 1", got)
	}

	// Mirrored, not a plain SSH terminal, which is the whole difference.
	hosts := d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 || hosts[0].SSHOnly {
		t.Fatalf("status = %+v, want the machine mirrored", hosts)
	}
	if hosts[0].Mirrors != 1 {
		t.Errorf("status reports %d mirrors, want 1", hosts[0].Mirrors)
	}
}

func TestATerminalOpenedOnTheMachineShowsUpHere(t *testing.T) {
	// The point of mirroring: work started on the machine appears here without
	// anybody asking for it.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()

	before := panesFor(here(), "bot")
	if before != 1 {
		t.Fatalf("started with %d mirrors, want 1", before)
	}

	// A tab opened on the machine, in the space the two ends share.
	var shared string
	for id := range there().Workspaces {
		shared = id
	}
	if reply := d.dispatch(Command{Cmd: "open", Host: "bot"}); !reply.OK {
		t.Fatalf("open on the machine: %s", reply.Message)
	}
	for i := 0; i < 3; i++ {
		d.reconcileAll()
	}

	if got := len(there().Panes); got != 2 {
		t.Errorf("the machine has %d terminals, want 2", got)
	}
	if got := panesFor(here(), "bot"); got != 2 {
		t.Errorf("%d mirrors here, want one for each terminal there", got)
	}
	// Both in the shared space on the machine, which is what keeps the two ends
	// showing the same tabs.
	for _, pane := range there().Panes {
		if ws, _ := pane["workspace_id"].(string); ws != shared {
			t.Errorf("a terminal on the machine is in %q, not the shared space %q", ws, shared)
		}
	}
}

func TestAPaneOpenedByHandInAMirroredMachinesSpaceMovesOntoIt(t *testing.T) {
	// Herdr's own new-tab key and the plus icon open a local shell, and no
	// plugin can intercept them. In a space that exists to hold one machine's
	// terminals that is nearly always a mistake -- the pane is on the wrong
	// host -- so it is replaced with a terminal on the machine.
	//
	// This is also the one path that closes a pane this plugin did not open,
	// which Herdr allows only through the ordinary close rather than the
	// plugin one. The stand-in refuses the wrong command now, so using it
	// would leave the pane sitting there.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 2; i++ {
		d.reconcileAll()
	}

	remoteBefore := len(there().Panes)
	var workspace string
	for _, pane := range here().Panes {
		workspace, _ = pane["workspace_id"].(string)
	}
	if workspace == "" {
		t.Fatal("the machine has no space here")
	}
	stray := addLocalPane(t, workspace)

	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}

	if _, still := here().Panes[stray]; still {
		t.Error("the pane opened by hand is still here, on the wrong machine")
	}
	if got := len(there().Panes); got != remoteBefore+1 {
		t.Errorf("the machine has %d terminals, want one more than the %d it had",
			got, remoteBefore)
	}
}

func TestScopeDecidesWhetherTheMachinesOwnWorkIsMirrored(t *testing.T) {
	// "shared mirrors the shared space; all mirrors everything." The default is
	// shared, and the reason is in the README: both ends then show the same
	// tabs in the same order, and whatever else the machine has running stays
	// in its own spaces, private and untouched.
	for _, tt := range []struct {
		scope       string
		wantMirrors int
		what        string
	}{
		{"shared", 1, "only the space the two ends share"},
		{"all", 2, "everything the machine has"},
	} {
		t.Run(tt.scope+": "+tt.what, func(t *testing.T) {
			here := withFakeHerdr(t)
			there, machineState := withRemoteHerdr(t)

			cfg := machineConfig("bot")
			cfg.Hosts[0].Mode = "attach"
			cfg.Scope = tt.scope
			d := New(cfg)

			if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
				t.Fatalf("connect: %s", reply.Message)
			}
			d.reconcileAll()

			// Work of the machine's own, in a space that has nothing to do with
			// this one: somebody's editor, left running there.
			addPaneOn(t, machineState, "w-theirs", "vim")
			for i := 0; i < 3; i++ {
				d.reconcileAll()
			}

			if got := len(there().Panes); got != 2 {
				t.Fatalf("the machine has %d terminals, want the shared one and its own", got)
			}
			if got := panesFor(here(), "bot"); got != tt.wantMirrors {
				t.Errorf("with scope %q there are %d mirrors here, want %d",
					tt.scope, got, tt.wantMirrors)
			}
		})
	}
}

func TestMaxMirrorsCapsWhatOneMachineCanFillTheScreenWith(t *testing.T) {
	// "Most terminals to mirror per machine." A machine with a runaway pane
	// count would otherwise open that many panes here, which is a session
	// nobody can use to fix it with.
	here := withFakeHerdr(t)
	there, machineState := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	cfg.Scope = "all"
	cfg.MaxMirrors = 3
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()

	for i := 0; i < 10; i++ {
		addPaneOn(t, machineState, "w-theirs", fmt.Sprintf("runaway-%d", i))
	}
	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}

	if got := len(there().Panes); got < 10 {
		t.Fatalf("the machine has %d terminals, want the ten that were started", got)
	}
	if got := panesFor(here(), "bot"); got > cfg.MaxMirrors {
		t.Errorf("%d mirrors here for a cap of %d", got, cfg.MaxMirrors)
	}
	if got := panesFor(here(), "bot"); got != cfg.MaxMirrors {
		t.Errorf("%d mirrors here, want the cap of %d filled", got, cfg.MaxMirrors)
	}
}
