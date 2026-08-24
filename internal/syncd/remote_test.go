package syncd

import (
	"encoding/json"
	"fmt"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withRemoteHerdr adds a machine that has Herdr on it to the stand-in world.
//
// The ssh on PATH runs the remote command against another copy of the stand-in,
// with its own state file, which is what another machine is from here: the same
// program, a different set of panes. That makes the mirroring path drivable --
// the only mode where this plugin talks to the far end at all.
//
// Which state file is chosen by the destination ssh was given, so one script
// stands in for as many machines as a test wants.
func withRemoteHerdr(t *testing.T) (func() fakeHerdr, string) {
	held, statePath := withRemoteHerdrRunning(t, true)
	return func() fakeHerdr { return held("bot") }, statePath("bot")
}

// withRemoteHerdrRunning is withRemoteHerdr with a say in whether the machines'
// Herdr is already up, and a look at any of them by name.
//
// A machine that is reachable but has no session answering is the ordinary case
// -- nobody has logged in since it booted -- and auto_start exists for it: the
// plugin starts one rather than reporting the machine as broken. Until the
// stand-in could refuse, that could not be told from a machine that was fine.
func withRemoteHerdrRunning(t *testing.T, up bool) (func(string) fakeHerdr, func(string) string) {
	t.Helper()

	dir := t.TempDir()
	// Where the transcript lands, so a test can ask what was actually said to
	// a machine rather than reasoning about what should have been.
	t.Setenv("HRP_TEST_REMOTE_DIR", dir)
	statePath := func(target string) string {
		return filepath.Join(dir, "machine-"+target+".json")
	}

	// A file that exists once a machine's Herdr is up. Starting one creates it;
	// until then every command is answered the way Herdr answers when nothing
	// is listening.
	running := filepath.Join(dir, "herdr-is-up")
	if up {
		if err := os.WriteFile(running, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The destination is the argument before the command, since the command is
	// last and ssh is given "-- <destination> <command>".
	script := "#!/bin/sh\n" +
		"last=\"\"; prev=\"\"; for a in \"$@\"; do prev=\"$last\"; last=\"$a\"; done\n" +
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
		"echo \"$prev | $last\" >> " + dir + "/asked.log\n" +
		"HRP_TEST_FAKE_HERDR_STATE=" + dir + "/machine-$prev.json eval \"$last\"\n"

	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	held := func(target string) fakeHerdr {
		t.Helper()
		var machine fakeHerdr
		raw, err := os.ReadFile(statePath(target))
		if err != nil {
			return machine
		}
		if err := json.Unmarshal(raw, &machine); err != nil {
			t.Fatalf("reading what %s is holding: %v", target, err)
		}
		return machine
	}
	return held, statePath
}

// asked is every command the daemon has sent to a machine, in order.
//
// A transcript beats reasoning about the conversation, which is how the last
// several of these were actually found: the line that named this one was the
// daemon asking a machine for a new tab, which it only does when it believes
// the machine has none.
func asked(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HRP_TEST_REMOTE_DIR"), "asked.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// addPaneOn puts a pane into a machine's own state, as work started there does:
// a space of its own, nothing to do with the one shared with this machine.
func addPaneOn(t *testing.T, statePath, workspace, title string) string {
	return addAgentPaneOn(t, statePath, workspace, title, "", "")
}

// addAgentPaneOn is addPaneOn for a terminal with an agent running in it.
func addAgentPaneOn(t *testing.T, statePath, workspace, title, agent, status string) string {
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
		"agent":                   agent,
		"agent_status":            status,
	}
	out, _ := json.Marshal(held)
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

// settle runs reconcile passes and lets the mirrors they open report themselves
// alive, which is what happens a moment later in a real session.
//
// Without that a mirror never looks alive, so every pass replaces every one of
// them: the tests then exercise a machine in permanent churn rather than a
// settled one. That difference hid a bug for a week and invented another.
func settle(t *testing.T, d *Daemon, here func() fakeHerdr, passes int, machines ...func() fakeHerdr) {
	t.Helper()
	for i := 0; i < passes; i++ {
		d.reconcileAll()
		mirrorsAreRunning(t, here, machines...)
	}
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
	settle(t, d, here, 3, there)

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
	settle(t, d, here, 3, there)

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
	settle(t, d, here, 2, there)

	remoteBefore := len(there().Panes)
	var workspace string
	for _, pane := range here().Panes {
		workspace, _ = pane["workspace_id"].(string)
	}
	if workspace == "" {
		t.Fatal("the machine has no space here")
	}
	stray := addLocalPane(t, workspace)

	settle(t, d, here, 4, there)

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
	settle(t, d, here, 4, there)

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
	settle(t, d, here, 2, there)

	var shared string
	for id := range there().Workspaces {
		shared = id
	}
	if shared == "" {
		t.Fatal("no shared space was made on the machine")
	}
	deleteSpaceOn(t, machineState, shared)

	settle(t, d, here, 4, there)

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
	settle(t, d, here, 3, there)
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
	heldOn, _ := withRemoteHerdrRunning(t, false)
	there := func() fakeHerdr { return heldOn("bot") }

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)

	reply := d.dispatch(Command{Cmd: "connect", Host: "bot"})
	if !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

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
	heldOn, _ := withRemoteHerdrRunning(t, false)
	there := func() fakeHerdr { return heldOn("bot") }

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	off := false
	cfg.AutoStart = &off
	d := New(cfg)

	d.dispatch(Command{Cmd: "connect", Host: "bot"})
	settle(t, d, here, 3, there)

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
	settle(t, d, here, 3, there)
	if got := len(there().Panes); got != 2 {
		t.Fatalf("the machine has %d terminals, want 2 to start from", got)
	}

	var closed string
	for id := range there().Panes {
		closed = id
	}
	closePaneOn(t, machineState, closed)

	settle(t, d, here, 4, there)

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
	settle(t, d, here, 2, there)

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
		settle(t, d, here, 3, there)

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
		settle(t, d, here, 3, there)

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

func TestAnAgentOnTheMachineAppearsHereAsItself(t *testing.T) {
	// The point of mirroring an agent rather than merely its output: something
	// running on another machine turns up in the sidebar under its own name and
	// state, rather than as a bare SSH pane you have to remember the contents
	// of. When it finishes, the pane stops claiming it.
	here := withFakeHerdr(t)
	there, machineState := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	cfg.Scope = "all"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()

	// A terminal on the machine with an agent working in it.
	addAgentPaneOn(t, machineState, "w-theirs", "claude", "claude", "working")
	settle(t, d, here, 3, there)

	mirror := agentPaneHere(t, here(), "claude")
	if mirror == nil {
		t.Fatalf("no pane here is showing the agent: %+v", here().Panes)
	}
	if got, _ := mirror["agent_status"].(string); got != "working" {
		t.Errorf("the agent shows as %q here, want it working", got)
	}

	// It finishes: the pane stops claiming an agent rather than showing a
	// stale one for the rest of the session.
	for id, pane := range there().Panes {
		if agent, _ := pane["agent"].(string); agent == "claude" {
			clearAgentOn(t, machineState, id)
		}
	}
	settle(t, d, here, 3, there)
	if mirror := agentPaneHere(t, here(), "claude"); mirror != nil {
		t.Errorf("a pane here is still showing an agent that has finished: %+v", mirror)
	}
}

// agentPaneHere finds the local pane reporting a given agent.
func agentPaneHere(t *testing.T, held fakeHerdr, agent string) map[string]any {
	t.Helper()
	for _, pane := range held.Panes {
		if got, _ := pane["agent"].(string); got == agent {
			return pane
		}
	}
	return nil
}

// clearAgentOn ends the agent running in a terminal on the machine.
func clearAgentOn(t *testing.T, statePath, paneID string) {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var held fakeHerdr
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	delete(held.Panes[paneID], "agent")
	delete(held.Panes[paneID], "agent_status")
	out, _ := json.Marshal(held)
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAnAgentNameFromAnotherMachineIsMadeSafe(t *testing.T) {
	// The agent's name comes from the far machine and reaches the sidebar
	// through report-agent rather than through the pane's label. That is a
	// second route for the same text, and it was left unguarded when the first
	// one was fixed.
	here := withFakeHerdr(t)
	there, machineState := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	cfg.Scope = "all"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()

	addAgentPaneOn(t, machineState, "w-theirs", "shell",
		"\x1b[31mclaude\x1b[0m\nrest", "working")
	settle(t, d, here, 3, there)

	for _, pane := range here().Panes {
		agent, _ := pane["agent"].(string)
		if agent == "" {
			continue
		}
		if strings.ContainsAny(agent, "\n\r") || strings.ContainsRune(agent, 0x1b) {
			t.Errorf("the agent reads as %q here, which steers the terminal", agent)
		}
	}
}

func TestTwoMachinesInOneSpaceLeaveEachOtherAlone(t *testing.T) {
	// A space named outright holds every machine's terminals. Deciding what to
	// move onto a machine by asking only "is this pane mine" then made every
	// other machine's terminal look like a stray sitting in this one's space --
	// so each machine carried the others' terminals off to itself and closed
	// them here, in turn, for as long as the session lasted.
	//
	// Two machines, one space, which is the configuration that does it.
	here := withFakeHerdr(t)
	heldOn, _ := withRemoteHerdrRunning(t, true)
	botThere := func() fakeHerdr { return heldOn("bot") }
	prodThere := func() fakeHerdr { return heldOn("prod") }

	cfg := config.Defaults()
	cfg.Workspace = "remote"
	cfg.Hosts = []config.Host{
		{Target: "bot", Mode: "attach"},
		{Target: "prod", Mode: "attach"},
	}
	d := New(cfg)

	// One at a time, with passes in between, which is how it happens and is
	// also the case that breaks: a machine that settles before the next one
	// arrives meets the newcomer's pane as something it has never seen in its
	// space before.
	for _, target := range []string{"bot", "prod"} {
		if reply := d.dispatch(Command{Cmd: "connect", Host: target}); !reply.OK {
			t.Fatalf("connect %s: %s", target, reply.Message)
		}
		settle(t, d, here, 3, botThere, prodThere)
	}

	// One terminal on each machine, and a mirror of each here, in the one space.
	for _, target := range []string{"bot", "prod"} {
		if got := len(heldOn(target).Panes); got != 1 {
			t.Errorf("%s has %d terminals, want 1: %+v", target, got, heldOn(target).Panes)
		}
		if got := panesFor(here(), target); got != 1 {
			t.Errorf("%d mirrors of %s here, want 1", got, target)
		}
	}
	if got := len(here().Workspaces); got != 1 {
		t.Errorf("%d spaces here, want the one they share", got)
	}

	// And across a restart, which is when it bites. A daemon that has been
	// running has seen every pane in the space go by and leaves them alone on
	// that basis alone; a fresh one has seen nothing, so the only thing
	// standing between the two machines is each knowing the other's panes are
	// spoken for.
	after := New(cfg)
	for _, target := range []string{"bot", "prod"} {
		if reply := after.dispatch(Command{Cmd: "connect", Host: target}); !reply.OK {
			t.Fatalf("reconnect %s: %s", target, reply.Message)
		}
	}
	for i := 0; i < 6; i++ {
		after.reconcileAll()
	}

	// Each machine has its own terminal mirrored, and neither is showing the
	// other's. That is what was going wrong: bot carried prod's pane off to
	// itself, and prod came up a moment later with its terminal on bot.
	for _, target := range []string{"bot", "prod"} {
		if got := panesFor(here(), target); got != 1 {
			t.Errorf("%d mirrors of %s here, want 1", got, target)
		}
	}
	if got := len(here().Panes); got != 2 {
		t.Errorf("%d panes here for two machines with one terminal each: %+v",
			got, here().Panes)
	}
	// And each machine still has the terminal it had, rather than one of them
	// holding both.
	if got := len(heldOn("bot").Panes); got != 1 {
		t.Errorf("bot has %d terminals: it has taken one that is not its own", got)
	}

	// Not asserted: prod ends this with a second terminal of its own, which
	// nothing here mirrors. It is not one of bot's -- the stealing is fixed --
	// and the sidebar is right either way, so it is a spare terminal on a
	// machine rather than anything visible. Left as a known loose end rather
	// than written down as correct.
}

// mirrorIsRunning writes the mark a live bridge leaves, so a mirror in these
// tests can report itself alive the way a real one does.
//
// The mark is a pid and the terminal it is bridging; Herdr checks the pid is
// alive and belongs to the same program, so the test's own pid is exactly what
// a running mirror's looks like from here. Without this a mirror never reports
// itself alive and is replaced on every pass, which is a difference from the
// real thing big enough to hide or invent a bug.
func mirrorIsRunning(t *testing.T, paneID, terminalID string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HERDR_PLUGIN_STATE_DIR"), "panes",
		sanitizeForPath(os.Getenv("HERDR_SESSION")))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("%d\n%s", os.Getpid(), terminalID)
	if err := os.WriteFile(filepath.Join(dir, sanitizeForPath(paneID)+".pid"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sanitizeForPath matches how the mirror package names its files.
func sanitizeForPath(s string) string {
	out := []rune(s)
	for i, r := range out {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			out[i] = '-'
		}
	}
	return string(out)
}

// forgetGoneBridges removes the marks of panes that are no longer there.
//
// A bridge removes its own mark on the way out, so a pane that has gone leaves
// nothing behind claiming to be alive. Leaving those lying about is its own kind
// of wrong: a closed pane that still reads as running is not a thing that
// happens, and a test built on it is testing something that cannot occur.
func forgetGoneBridges(t *testing.T, held fakeHerdr) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HERDR_PLUGIN_STATE_DIR"), "panes",
		sanitizeForPath(os.Getenv("HERDR_SESSION")))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".pid")
		if entry.Name() == name {
			continue
		}
		stillThere := false
		for id := range held.Panes {
			if sanitizeForPath(id) == name {
				stillThere = true
				break
			}
		}
		if !stillThere {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// mirrorsAreRunning marks every mirror here as having a live bridge, which is
// what happens a moment after one is opened.
func mirrorsAreRunning(t *testing.T, here func() fakeHerdr, machines ...func() fakeHerdr) {
	t.Helper()

	forgetGoneBridges(t, here())

	for id, pane := range here().Panes {
		label, _ := pane["label"].(string)
		if !strings.Contains(label, "@") {
			continue
		}
		for _, machine := range machines {
			for _, rp := range machine().Panes {
				rid, _ := rp["pane_id"].(string)
				if strings.Contains(label, shortPaneID(rid)) {
					tid, _ := rp["terminal_id"].(string)
					mirrorIsRunning(t, id, tid)
				}
			}
		}
	}
}

// herdrRestarted is what a restart does to the bridges: every mirror process
// goes with it, so every mark does too.
func herdrRestarted(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(os.Getenv("HERDR_PLUGIN_STATE_DIR"), "panes")); err != nil {
		t.Fatal(err)
	}
}

func TestARestartDoesNotAddATerminalToAMirroredMachine(t *testing.T) {
	// Connecting a machine that already has a terminal should mirror it, not
	// make a second one. It made a second one, every restart, so terminals
	// piled up on the machine.
	//
	// The replacement mirror was opened as a split beside the pane it was
	// replacing, a moment after that pane had been closed: Herdr answered
	// pane_not_found, the mirror never opened, and the machine was then judged
	// to have nothing open and given a spare terminal.
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
		mirrorsAreRunning(t, here, there)
	}
	d.persist()
	if got := len(there().Panes); got != 1 {
		t.Fatalf("started with %d terminals on the machine, want 1", got)
	}

	herdrRestarted(t)
	after := New(cfg)
	if reply := after.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	for i := 0; i < 4; i++ {
		after.reconcileAll()
		mirrorsAreRunning(t, here, there)
	}

	if got := len(there().Panes); got != 1 {
		t.Errorf("the machine has %d terminals after a restart, want the one it had", got)
		for _, line := range asked(t) {
			if strings.Contains(line, "tab create") {
				t.Logf("  the daemon asked: %s", line)
			}
		}
	}
	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("%d mirrors here, want 1", got)
	}
}

func TestWhichSessionOnTheMachineIsShared(t *testing.T) {
	// A machine's terminals live in a Herdr session, and which one is a
	// setting: the machine's own default, so that plain `herdr` there shows the
	// shared terminals, or a named one to keep them apart from its own work.
	//
	// It reaches the machine as an environment variable on every command, and
	// as one handed to the pane that bridges a terminal. Getting it wrong does
	// not fail: it quietly talks to a different session, which looks like a
	// machine with nothing open.
	for _, tt := range []struct {
		name    string
		session string
		want    string
	}{
		{"the machine's own default", "default", ""},
		{"a session of its own", "remote-work", "HERDR_SESSION=remote-work"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			here := withFakeHerdr(t)
			there, _ := withRemoteHerdr(t)

			cfg := machineConfig("bot")
			cfg.Hosts[0].Mode = "attach"
			cfg.Hosts[0].Session = tt.session
			d := New(cfg)
			if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
				t.Fatalf("connect: %s", reply.Message)
			}
			settle(t, d, here, 3, there)

			// Every command carries it, or none does.
			var carried, plain int
			for _, line := range asked(t) {
				if !strings.Contains(line, "herdr") {
					continue
				}
				if strings.Contains(line, "HERDR_SESSION=") {
					carried++
				} else {
					plain++
				}
			}
			if tt.want == "" {
				if carried > 0 {
					t.Errorf("%d commands named a session; the machine's default is unnamed", carried)
				}
			} else {
				if plain > 0 {
					t.Errorf("%d commands went to the machine without naming the session", plain)
				}
				found := false
				for _, line := range asked(t) {
					if strings.Contains(line, tt.want) {
						found = true
					}
				}
				if !found {
					t.Errorf("no command named %q", tt.want)
				}
			}

			// And the pane that bridges a terminal is told the same, or it
			// would attach to a different session than the one being listed.
			if got := len(here().Panes); got != 1 {
				t.Fatalf("%d panes here, want 1", got)
			}
		})
	}
}

func TestAMirrorThatFailsDoesNotCloseTheWorkOnTheMachine(t *testing.T) {
	// Closing a mirrored tab closes the terminal on the machine too, which is
	// the point of close_propagates. So whether a pane that has gone was closed
	// or merely dropped decides whether somebody's work is destroyed.
	//
	// A bridge that fails records why on its way out, which is what that record
	// is for. The mirrored path never read it: a mirror whose attach failed --
	// a moment of trouble reaching the machine, a terminal briefly held by
	// something else -- looked exactly like a tab somebody had shut, and the
	// terminal on the machine was closed to match.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	remote := there()
	if len(remote.Panes) != 1 {
		t.Fatalf("the machine has %d terminals, want 1 to start from", len(remote.Panes))
	}
	var terminal string
	for id := range remote.Panes {
		terminal = id
	}

	// The bridge failed and said so, and its pane went with it.
	terminalDied(t, onlyPane(t, here()),
		"bot: exit status 1 running: herdr terminal attach term_1")
	settle(t, d, here, 4, there)

	if _, alive := there().Panes[terminal]; !alive {
		t.Error("the terminal on the machine was closed because a mirror of it failed")
	}
	if got := len(there().Panes); got != 1 {
		t.Errorf("the machine has %d terminals, want the one it had: %+v", got, there().Panes)
	}
	// Not asserted here: whether it is mirrored again straight away. It is
	// mirrored again, but a bridge that failed backs off first, so how soon
	// depends on how many times it has failed. What matters on this side is
	// that the terminal was not treated as closed; that it comes back is the
	// churn test's business, which counts the attempts.
}

func TestAMirroredTabYouCloseStillClosesTheTerminal(t *testing.T) {
	// The other half, which the fix must not cost: a tab somebody shuts leaves
	// no record of a failure, and that is still a deliberate close.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	closePaneByHand(t, onlyPane(t, here()))
	settle(t, d, here, 4, there)

	if got := len(there().Panes); got != 0 {
		t.Errorf("the machine has %d terminals after the tab was closed, want none", got)
	}
}

func TestAMirrorThatKeepsFailingStopsTryingSoOften(t *testing.T) {
	// A terminal something else is holding cannot be mirrored, and the attach
	// fails every time. Forgetting the pane and mirroring it again -- which is
	// right, since the terminal is fine and the bridge is not -- means opening
	// a new pane on every pass unless something counts the failures.
	//
	// The count already existed for a mirror that could not be opened at all.
	// A bridge that opened and then failed never reached it, so the pane
	// flickered in the sidebar for as long as the session lasted, with nothing
	// anywhere saying why.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	const rounds = 12
	opened := map[string]bool{}
	for i := 0; i < rounds; i++ {
		for id := range here().Panes {
			opened[id] = true
			terminalDied(t, id, "bot: exit status 1 running: herdr terminal attach term_1")
		}
		d.reconcileAll()
	}

	// A couple of attempts, not one per pass.
	if len(opened) > maxMirrorAttempts {
		t.Errorf("%d panes were opened over %d passes; the failures are not being counted",
			len(opened), rounds)
	}
	// And the terminal on the machine is untouched throughout: a bridge that
	// fails is not somebody closing a tab.
	if got := len(there().Panes); got != 1 {
		t.Errorf("the machine has %d terminals, want the one it had", got)
	}
}
