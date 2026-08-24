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
	return withRemoteHerdrRunning(t, true)
}

// withRemoteHerdrRunning is withRemoteHerdr with a say in whether the machine's
// Herdr is already up.
//
// A machine that is reachable but has no session answering is the ordinary case
// -- nobody has logged in since it booted -- and auto_start exists for it: the
// plugin starts one rather than reporting the machine as broken. Until the
// stand-in could refuse, that could not be told from a machine that was fine.
func withRemoteHerdrRunning(t *testing.T, up bool) (func() fakeHerdr, string) {
	t.Helper()

	dir := t.TempDir()
	remoteState := filepath.Join(dir, "remote-herdr.json")

	// ssh runs whatever it was given, with herdr on the far side being the
	// stand-in pointed at the machine's own panes. The probe that looks for
	// herdr is answered with that binary's path.
	// A file that exists once the machine's Herdr is up. Starting one creates
	// it; until then every command is answered the way Herdr answers when
	// nothing is listening.
	running := filepath.Join(dir, "herdr-is-up")
	if up {
		if err := os.WriteFile(running, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	script := "#!/bin/sh\n" +
		"last=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in\n" +
		"  *command\\ -v\\ herdr*) echo " + fakeHerdrBin + "; exit 0;;\n" +
		"  *--version*) echo 'herdr 0.8.0'; exit 0;;\n" +
		"  true) exit 0;;\n" +
		"  *\\ server*) : > " + running + "; exit 0;;\n" +
		"esac\n" +
		"if [ ! -f " + running + " ]; then\n" +
		"  echo '{\"error\":{\"code\":\"server_not_running\",\"message\":\"no herdr server is running\"},\"id\":\"cli:fake\"}'\n" +
		"  exit 1\n" +
		"fi\n" +
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

func TestClosingAMirroredTabClosesItOnTheMachine(t *testing.T) {
	// Without this, mirroring is two-way for everything except closing: the tab
	// goes here and the work quietly carries on over there, which is the one
	// asymmetry that surprises people. The setting exists for anyone who wants
	// the old behaviour back.
	for _, tt := range []struct {
		propagate bool
		wantThere int
		what      string
	}{
		{true, 1, "the terminal goes with it"},
		{false, 2, "the terminal stays on the machine"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			here := withFakeHerdr(t)
			there, _ := withRemoteHerdr(t)

			cfg := machineConfig("bot")
			cfg.Hosts[0].Mode = "attach"
			cfg.ClosePropagates = &tt.propagate
			d := New(cfg)

			if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
				t.Fatalf("connect: %s", reply.Message)
			}
			d.reconcileAll()
			// A second terminal, so there is one to close and one to keep.
			if reply := d.dispatch(Command{Cmd: "open", Host: "bot"}); !reply.OK {
				t.Fatalf("open: %s", reply.Message)
			}
			for i := 0; i < 3; i++ {
				d.reconcileAll()
			}
			if got := len(there().Panes); got != 2 {
				t.Fatalf("the machine has %d terminals, want 2 to start from", got)
			}

			// Closed here, in the sidebar, the way somebody would.
			var mirror string
			for id := range here().Panes {
				mirror = id
			}
			closePaneByHand(t, mirror)
			for i := 0; i < 3; i++ {
				d.reconcileAll()
			}

			if got := len(there().Panes); got != tt.wantThere {
				t.Errorf("the machine has %d terminals, want %d", got, tt.wantThere)
			}
			// Either way the mirror stays shut: closing something is not a
			// request to have it back.
			if got := panesFor(here(), "bot"); got != 1 {
				t.Errorf("%d mirrors here, want the one that was not closed", got)
			}
		})
	}
}

// deleteSpaceOn removes a space and everything in it from a machine, as closing
// its last tab there does.
func deleteSpaceOn(t *testing.T, statePath, workspace string) {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var held fakeHerdr
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	delete(held.Workspaces, workspace)
	for id, pane := range held.Panes {
		if ws, _ := pane["workspace_id"].(string); ws == workspace {
			delete(held.Panes, id)
		}
	}
	out, _ := json.Marshal(held)
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheSharedSpaceGoingOnTheMachineIsNotABreakage(t *testing.T) {
	// A space goes when its last terminal does, and the shared one is a space
	// like any other: close its last tab on the machine and it is gone. The id
	// this remembers then matches nothing, which would filter every pane out --
	// so the machine looks as though it has nothing open, with nothing said
	// about why.
	//
	// It is forgotten instead, and made again the next time the machine is
	// connected to. Not by the reconcile: that deliberately does not open
	// terminals on a machine, and a space comes with one.
	here := withFakeHerdr(t)
	there, machineState := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 2; i++ {
		d.reconcileAll()
	}

	var shared string
	for id := range there().Workspaces {
		shared = id
	}
	if shared == "" {
		t.Fatal("no shared space was made on the machine")
	}
	deleteSpaceOn(t, machineState, shared)

	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}

	// Nothing is made behind the user's back, and nothing is left over here.
	if got := len(there().Panes); got != 0 {
		t.Errorf("the machine has %d terminals; reconciling should not open any: %+v",
			got, there().Panes)
	}
	if got := panesFor(here(), "bot"); got != 0 {
		t.Errorf("%d mirrors here of terminals that are gone", got)
	}
	// The machine is still connected, because it is: its space went, it did
	// not break.
	hosts := d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 || !hosts[0].Connected {
		t.Fatalf("status = %+v, want the machine still connected", hosts)
	}

	// And connecting again is what brings it back, as the message says.
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	for i := 0; i < 3; i++ {
		d.reconcileAll()
	}
	if got := len(there().Panes); got != 1 {
		t.Errorf("after connecting again the machine has %d terminals, want 1", got)
	}
	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("after connecting again there are %d mirrors here, want 1", got)
	}
}

func TestAMachineWhoseHerdrIsNotUpIsStartedRatherThanRefused(t *testing.T) {
	// The ordinary case for a machine nobody has logged into since it booted:
	// reachable, Herdr installed, no session answering. Without auto_start that
	// is a machine you cannot mirror until you go and start one by hand; with
	// it, the plugin starts one and carries on.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdrRunning(t, false)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)

	reply := d.dispatch(Command{Cmd: "connect", Host: "bot"})
	if !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 3; i++ {
		d.reconcileAll()
	}

	if got := len(there().Panes); got != 1 {
		t.Errorf("the machine has %d terminals, want the one that comes with its space", got)
	}
	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("%d mirrors here, want 1", got)
	}
	hosts := d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 || hosts[0].SSHOnly {
		t.Fatalf("status = %+v, want the machine mirrored rather than fallen back", hosts)
	}
}

func TestWithoutAutoStartAMachineWithNoSessionIsNotStarted(t *testing.T) {
	// Turning it off means the plugin leaves the machine's sessions alone. It
	// still connects -- the machine is reachable and its terminals are usable
	// over plain SSH -- so what it must not do is quietly look the same as
	// having started one.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdrRunning(t, false)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	off := false
	cfg.AutoStart = &off
	d := New(cfg)

	d.dispatch(Command{Cmd: "connect", Host: "bot"})
	for i := 0; i < 3; i++ {
		d.reconcileAll()
	}

	if got := len(there().Panes); got != 0 {
		t.Errorf("the machine has %d terminals; nothing should have started a session", got)
	}
	if got := panesFor(here(), "bot"); got != 0 {
		t.Errorf("%d mirrors here of a machine with no session", got)
	}
}

// closePaneOn closes a terminal on the machine, as somebody sitting at it does.
func closePaneOn(t *testing.T, statePath, id string) {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var held fakeHerdr
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	delete(held.Panes, id)
	out, _ := json.Marshal(held)
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClosingATerminalOnTheMachineSticks(t *testing.T) {
	// Mirroring is two-way, so a terminal closed on the machine should take its
	// mirror here with it and stop. It did the first half and then undid the
	// second: the mirror was closed but left in the listing the rest of the
	// pass works from, alive as far as anything could tell and no longer
	// belonging to the machine -- which is the description of a pane somebody
	// opened by hand in a machine's space, and those get moved onto the
	// machine. So the work went and something took its seat.
	here := withFakeHerdr(t)
	there, machineState := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()
	if reply := d.dispatch(Command{Cmd: "open", Host: "bot"}); !reply.OK {
		t.Fatalf("open: %s", reply.Message)
	}
	for i := 0; i < 3; i++ {
		d.reconcileAll()
	}
	if got := len(there().Panes); got != 2 {
		t.Fatalf("the machine has %d terminals, want 2 to start from", got)
	}

	var closed string
	for id := range there().Panes {
		closed = id
	}
	closePaneOn(t, machineState, closed)

	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}

	remote := there()
	if _, back := remote.Panes[closed]; back {
		t.Error("the terminal closed on the machine is back")
	}
	if len(remote.Panes) != 1 {
		t.Errorf("the machine has %d terminals, want the one that was left: %+v",
			len(remote.Panes), remote.Panes)
	}
	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("%d mirrors here, want one for the terminal that is left", got)
	}
}

// withConfigFile puts a config on disk, which the mode toggle needs: it changes
// the setting by writing the file, so that the choice outlives the session.
func withConfigFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	return path
}

func TestTogglingMirroringOnAndOffFromTheMenu(t *testing.T) {
	// The m key. The mechanism changes, so the machine's panes here no longer
	// match how it is reached: they are dropped and the machine connected again
	// under the new one. Both directions, because the interesting half is going
	// back -- a mirror and a plain SSH terminal look alike in a sidebar and are
	// nothing alike underneath.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)
	configPath := withConfigFile(t, `{"hosts":[{"target":"bot"}]}`)

	d := New(machineConfig("bot"))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 2; i++ {
		d.reconcileAll()
	}

	// Plain SSH: a terminal here, and nothing asked of the machine's Herdr.
	if got := panesFor(here(), "bot"); got != 1 {
		t.Fatalf("started with %d terminals, want 1", got)
	}
	if got := len(there().Panes); got != 0 {
		t.Fatalf("the machine has %d terminals; plain SSH should not have made any", got)
	}

	t.Run("on", func(t *testing.T) {
		reply := d.dispatch(Command{Cmd: "set-mode", Host: "bot", Mode: "attach"})
		if !reply.OK {
			t.Fatalf("toggle on: %s", reply.Message)
		}
		if !strings.Contains(reply.Message, "mirroring on") {
			t.Errorf("toggling on said %q", reply.Message)
		}
		for i := 0; i < 3; i++ {
			d.reconcileAll()
		}

		if got := len(there().Panes); got != 1 {
			t.Errorf("the machine has %d terminals, want the shared one", got)
		}
		if got := panesFor(here(), "bot"); got != 1 {
			t.Errorf("%d panes here, want one mirror", got)
		}
		hosts := d.dispatch(Command{Cmd: "status"}).Hosts
		if len(hosts) != 1 || hosts[0].SSHOnly {
			t.Errorf("status = %+v, want the machine mirrored", hosts)
		}
		// Written down, or the choice would not survive a restart.
		if raw, err := os.ReadFile(configPath); err != nil {
			t.Fatal(err)
		} else if !strings.Contains(string(raw), "attach") {
			t.Errorf("the config file does not record the change: %s", raw)
		}
	})

	t.Run("and off again", func(t *testing.T) {
		remoteBefore := len(there().Panes)

		reply := d.dispatch(Command{Cmd: "set-mode", Host: "bot", Mode: "ssh"})
		if !reply.OK {
			t.Fatalf("toggle off: %s", reply.Message)
		}
		if !strings.Contains(reply.Message, "mirroring off") {
			t.Errorf("toggling off said %q", reply.Message)
		}
		for i := 0; i < 3; i++ {
			d.reconcileAll()
		}

		hosts := d.dispatch(Command{Cmd: "status"}).Hosts
		if len(hosts) != 1 || !hosts[0].SSHOnly {
			t.Errorf("status = %+v, want the machine on plain SSH", hosts)
		}
		// A plain SSH terminal, not a mirror wearing its name.
		if got := panesFor(here(), "bot"); got != 1 {
			t.Errorf("%d panes here, want one terminal", got)
		}
		for _, pane := range here().Panes {
			if label, _ := pane["label"].(string); !strings.HasPrefix(label, "shell") {
				t.Errorf("the terminal here is named %q, which is a mirror's name", label)
			}
		}
		// And the work on the machine is left alone, as it is when
		// disconnecting: turning a setting off is not a reason to close it.
		if got := len(there().Panes); got != remoteBefore {
			t.Errorf("the machine has %d terminals, want the %d it had", got, remoteBefore)
		}
	})
}
