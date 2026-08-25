package syncd

import (
	"encoding/json"
	"fmt"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/mirror"
	"os"
	"path/filepath"
	"sort"
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

// mirrorsHere returns the mirror panes for a machine, newest first, so a test
// can look at the one that was just opened.
func mirrorsHere(held fakeHerdr, target string) []map[string]any {
	var out []map[string]any
	for _, pane := range held.Panes {
		if label, _ := pane["label"].(string); strings.HasSuffix(label, "@"+target) {
			out = append(out, pane)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		id := func(p map[string]any) string { s, _ := p["pane_id"].(string); return s }
		return paneNumber(id(out[i])) > paneNumber(id(out[j]))
	})
	return out
}

func TestOpeningATerminalOnAMirroredMachineGoesToItToo(t *testing.T) {
	// The same promise the manifest makes for every machine -- "new terminal on
	// the machine whose space you are in, and go to it" -- but by a completely
	// different route. A mirrored machine's terminal is made on the machine and
	// the mirror of it arrives a pass later, so the focus has to be remembered
	// across the gap rather than asked for on the spot.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()
	if got := len(mirrorsHere(here(), "bot")); got != 1 {
		t.Fatalf("started with %d mirrors, want 1", got)
	}

	if reply := d.dispatch(Command{Cmd: "open", Host: "bot"}); !reply.OK {
		t.Fatalf("open: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	mirrors := mirrorsHere(here(), "bot")
	if len(mirrors) != 2 {
		t.Fatalf("%d mirrors here, want one for each terminal on the machine", len(mirrors))
	}
	if focused, _ := mirrors[0]["focused"].(bool); !focused {
		t.Errorf("the terminal that was opened was not focused: %+v", mirrors[0])
	}
}

func TestNewTabOnAMirroredMachineIsATabHereAsWell(t *testing.T) {
	// Mirroring makes this two decisions rather than one: a tab is asked for on
	// the machine, and the mirror that comes back a pass later has to be placed
	// as a tab here too. Nothing carries that across by itself -- the mirror is
	// opened by a later pass that only sees a terminal it has not mirrored yet
	// -- so how it was asked for has to be remembered.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot") // placement defaults to split
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()
	first := mirrorsHere(here(), "bot")
	if len(first) != 1 {
		t.Fatalf("started with %d mirrors, want 1", len(first))
	}
	firstTab, _ := first[0]["tab_id"].(string)

	if reply := d.dispatch(Command{Cmd: "open", Host: "bot", Placement: "tab"}); !reply.OK {
		t.Fatalf("open-tab: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	mirrors := mirrorsHere(here(), "bot")
	if len(mirrors) != 2 {
		t.Fatalf("%d mirrors here, want 2", len(mirrors))
	}
	if tab, _ := mirrors[0]["tab_id"].(string); tab == firstTab {
		t.Errorf("the new mirror shares tab %q with the first: it split rather than making a tab", tab)
	}
}

func TestASettledMachineStopsAskingForThings(t *testing.T) {
	// A poll runs every two seconds for as long as Herdr is open, so what a
	// settled machine costs per pass is what this costs to leave running. It
	// used to rename every pane, re-report every agent and re-check every
	// space on every pass -- eleven calls where one would do -- and none of
	// that is visible in the outcome, which is identical either way. Only the
	// count differs.
	//
	// So: once a machine has settled, a pass asks what is there and does
	// nothing else.
	here := withFakeHerdr(t)
	there, remoteState := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()

	// With an agent running on the machine, because the agent's name and state
	// reach the sidebar by their own route and carry their own promise not to
	// be sent again unchanged. Without one here, that route is never taken and
	// the promise is not being tested at all.
	var shared string
	for id := range there().Workspaces {
		shared = id
	}
	if shared == "" {
		t.Fatal("the machine has no shared space")
	}
	addAgentPaneOn(t, remoteState, shared, "claude", "claude", "idle")
	settle(t, d, here, 5, there)

	taken := func(held fakeHerdr) map[string]int {
		out := map[string]int{}
		for verb, n := range held.Calls {
			out[verb] = n
		}
		return out
	}
	delta := func(before, after map[string]int) map[string]int {
		out := map[string]int{}
		for verb, n := range after {
			if d := n - before[verb]; d != 0 {
				out[verb] = d
			}
		}
		return out
	}

	beforeHere, beforeThere := taken(here()), taken(there())
	const passes = 5
	settle(t, d, here, passes, there)

	// Here: one listing a pass, and nothing else at all. Every other verb is a
	// change being made, and a settled machine has no changes to make.
	gotHere := delta(beforeHere, taken(here()))
	if n := gotHere["pane list"]; n != passes {
		t.Errorf("%d local pane listings over %d passes, want one each", n, passes)
	}
	delete(gotHere, "pane list")
	if len(gotHere) != 0 {
		t.Errorf("a settled machine still does things here: %v", gotHere)
	}

	// On the machine: the same, allowing for the two listings mirroring needs
	// -- what panes are there, and what order the tabs are in.
	gotThere := delta(beforeThere, taken(there()))
	for _, listing := range []string{"pane list", "tab list"} {
		if n := gotThere[listing]; n > passes {
			t.Errorf("%d %q on the machine over %d passes, want no more than one each",
				n, listing, passes)
		}
		delete(gotThere, listing)
	}
	if len(gotThere) != 0 {
		t.Errorf("a settled machine is still being changed: %v", gotThere)
	}
}

// withUnreachableMachine replaces ssh with one that fails the way ssh does when
// it cannot reach the host, and returns how many times it has been called.
//
// 255 is ssh's own failure; anything else is the remote command's status coming
// back through it, which is a different thing entirely.
func withUnreachableMachine(t *testing.T) func() int {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "dialled.log")
	script := "#!/bin/sh\n" +
		"echo dialled >> " + log + "\n" +
		"echo 'ssh: connect to host bot port 22: Connection refused' >&2\n" +
		"exit 255\n"
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() int {
		raw, err := os.ReadFile(log)
		if err != nil {
			return 0
		}
		return strings.Count(string(raw), "dialled")
	}
}

func TestAMachineThatCannotBeReachedIsEventuallyLeftAlone(t *testing.T) {
	// A poll every two seconds against a machine that is not answering is an
	// ssh a machine cannot refuse quickly: a blackholed address takes the
	// operating system's own timeout to fail. Left to retry for ever it would
	// be a dial every couple of seconds for as long as Herdr is open, each one
	// holding the reconcile loop's lock while it waited.
	//
	// So the retries fall off and then stop, and the machine is marked as given
	// up on rather than being tried again.
	withFakeHerdr(t)
	dialled := withUnreachableMachine(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); reply.OK {
		t.Fatalf("connecting to an unreachable machine reported success: %s", reply.Message)
	}

	// Long enough for any retrying to have run its course.
	for i := 0; i < 10; i++ {
		d.reconcileAll()
	}
	settled := dialled()

	for i := 0; i < 10; i++ {
		d.reconcileAll()
	}
	if after := dialled(); after != settled {
		t.Errorf("a machine that cannot be reached was dialled %d more times over ten passes; "+
			"it should have been left alone", after-settled)
	}

	status := d.status()
	if len(status) != 1 {
		t.Fatalf("want one machine in the status, got %d", len(status))
	}
	if !status[0].GaveUp {
		t.Error("the machine is not marked as given up on, so nothing in the menu says why it is quiet")
	}
	if status[0].LastError == "" {
		t.Error("nothing was recorded about why it could not be reached")
	}
}

func TestGivingUpIsNotForeverIfYouAskAgain(t *testing.T) {
	// "On a machine that has been given up on, enter is also how you say try
	// again now" -- the README, and the reason the menu offers "enter to retry"
	// on that line. Giving up has to stop the polling without also making the
	// machine unreachable until Herdr restarts.
	withFakeHerdr(t)
	dialled := withUnreachableMachine(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	d.dispatch(Command{Cmd: "connect", Host: "bot"})
	for i := 0; i < 10; i++ {
		d.reconcileAll()
	}

	quiet := dialled()
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); reply.OK {
		t.Fatalf("the machine is still unreachable but connect reported success: %s", reply.Message)
	}
	if dialled() == quiet {
		t.Error("asking again did not try the machine, so there is no way back short of a restart")
	}
}

func TestTerminalsComingAndGoingDoNotAccumulate(t *testing.T) {
	// The daemon runs for as long as Herdr does, and nearly everything it
	// remembers is keyed by a pane or a terminal that will not be there
	// tomorrow: which mirrors exist, which were dismissed, which failed and
	// when to try them again, what each was named, which agent each was last
	// reported as running. On a machine somebody actually works on, those keys
	// turn over all day.
	//
	// Each of those has code to forget an entry whose terminal has gone. This
	// is what says the forgetting keeps up with the arriving.
	here := withFakeHerdr(t)
	there, remoteState := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()

	var shared string
	for id := range there().Workspaces {
		shared = id
	}
	if shared == "" {
		t.Fatal("the machine has no shared space")
	}

	// Every map that is keyed by something that comes and goes, added up.
	remembered := func() int {
		d.mu.Lock()
		defer d.mu.Unlock()
		s := d.hosts["bot"]
		if s == nil {
			t.Fatal("the machine is gone")
		}
		return len(s.mirrors) + len(s.dismissed) + len(s.abandoned) + len(s.failures) +
			len(s.retryAt) + len(s.pendingPlacement) + len(s.pendingFocus) +
			len(s.labels) + len(s.reportedAgents) + len(s.shellPanes) + len(d.seenStray)
	}

	settled := remembered()
	peak := 0
	const cycles = 30
	for i := 0; i < cycles; i++ {
		// Two ways a terminal arrives, because they leave different things
		// behind: asked for from here, which writes down how its mirror should
		// be placed and that it should be gone to, or simply appearing on the
		// machine, which writes down nothing until the mirror opens.
		if i%3 == 0 {
			if reply := d.dispatch(Command{Cmd: "open", Host: "bot", Placement: "tab"}); !reply.OK {
				t.Fatalf("open: %s", reply.Message)
			}
			settle(t, d, here, 2, there)
			// And away again, so the cycle ends where it began. A terminal
			// left running is a thing legitimately remembered, and counting it
			// as a leak would make this fail for working correctly.
			newest, highest := "", -1
			for id := range there().Panes {
				if n := paneNumber(id); n > highest {
					newest, highest = id, n
				}
			}
			if newest != "" {
				closePaneOn(t, remoteState, newest)
				settle(t, d, here, 2, there)
			}
		}

		paneID := addAgentPaneOn(t, remoteState, shared, "work", "claude", "idle")
		settle(t, d, here, 2, there)
		if n := remembered(); n > peak {
			peak = n
		}

		// Half the time the terminal simply goes on the machine; the other half
		// somebody closes the mirror here first, which is a different route
		// through this and the one that writes a dismissal down. Both keys are
		// the remote terminal's, which is new every time -- unlike a pane id,
		// which Herdr hands back out and which therefore cannot accumulate
		// however badly it is handled.
		if mirrors := mirrorsHere(here(), "bot"); i%2 == 0 && len(mirrors) > 1 {
			// The newest, which is the one this cycle added. mirrorsHere
			// returns them newest first.
			if id, _ := mirrors[0]["pane_id"].(string); id != "" {
				closePaneByHand(t, id)
				settle(t, d, here, 2, there)
			}
		}

		closePaneOn(t, remoteState, paneID)
		settle(t, d, here, 2, there)
	}

	// The churn has to have been real, or this measures nothing: a test where
	// no terminal was ever mirrored would show a flat count and prove it.
	if peak <= settled {
		t.Fatalf("nothing was ever remembered beyond the resting %d, so no terminal was mirrored", settled)
	}

	// And back to where it started, not thirty entries higher.
	if got := remembered(); got > settled {
		t.Errorf("after %d terminals came and went, %d things are remembered, up from %d at rest",
			cycles, got, settled)
	}
}

// envOf is what a pane was told when it was opened.
func envOf(pane map[string]any) map[string]string {
	out := map[string]string{}
	raw, _ := pane["env"].(map[string]any)
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func TestAMirrorIsToldWhichTerminalOnWhichMachineInWhichMode(t *testing.T) {
	// Everything the pane's process knows arrives this way: which machine to
	// reach, which session on it, which terminal to bridge, and how. Nothing
	// checked any of it, so a setting that stopped reaching the pane looked
	// exactly like one that arrived — and two of these decide whether what you
	// type reaches the far end at all.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "observe"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	mirrors := mirrorsHere(here(), "bot")
	if len(mirrors) == 0 {
		t.Fatal("no mirror was opened")
	}
	env := envOf(mirrors[0])

	if got := env["HRP_TARGET"]; got != "bot" {
		t.Errorf("the pane was told to reach %q, not bot", got)
	}
	// observe is read-only, and this is the only thing that makes it so. A
	// machine set to watch a terminal that instead attaches to it takes the
	// terminal away from whoever is using it.
	if got := env["HRP_MODE"]; got != "observe" {
		t.Errorf("the pane was told mode %q, not observe", got)
	}
	if env["HRP_TERMINAL"] == "" {
		t.Error("the pane was not told which terminal to bridge")
	}
	if got := env["HRP_NAME"]; !strings.HasSuffix(got, "@bot") {
		t.Errorf("the pane was named %q, which does not say which machine it is on", got)
	}
}

func TestTurningTakeoverOffReachesThePane(t *testing.T) {
	// takeover decides whether a mirror may evict a stale attach left by a
	// terminal that went without saying so. Turning it off is a setting in the
	// table, and the only thing it does is put one variable in front of the
	// pane: if that stops happening the setting reads as working and does
	// nothing at all.
	for _, tt := range []struct {
		what     string
		takeover bool
		want     string
	}{
		{"on, which is the default, says nothing", true, ""},
		{"off has to be said", false, "false"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			here := withFakeHerdr(t)
			there, _ := withRemoteHerdr(t)

			cfg := machineConfig("bot")
			cfg.Hosts[0].Mode = "attach"
			cfg.Takeover = &tt.takeover
			d := New(cfg)
			if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
				t.Fatalf("connect: %s", reply.Message)
			}
			settle(t, d, here, 3, there)

			mirrors := mirrorsHere(here(), "bot")
			if len(mirrors) == 0 {
				t.Fatal("no mirror was opened")
			}
			if got := envOf(mirrors[0])["HRP_TAKEOVER"]; got != tt.want {
				t.Errorf("the pane was told HRP_TAKEOVER=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestWhereHerdrLivesOnAMachineReachesThePane(t *testing.T) {
	// herdr_bin is two rows in the settings table — one for all machines and
	// one for a particular machine — and what it does is put a path in front
	// of the pane, which then runs herdr there. Nothing held either.
	//
	// It exists for machines where herdr is not on the PATH the SSH session
	// gets, which is most machines where it was installed by hand. Set it,
	// have it not arrive, and the pane looks for herdr where it is not.
	// Real paths, both of them: a machine told to run herdr somewhere it is not
	// simply cannot be reached, which is a different test and a slow one. Two
	// names for the same stand-in, so which one arrived is the answer.
	dir := t.TempDir()
	everywhere := filepath.Join(dir, "herdr-for-all")
	justThisOne := filepath.Join(dir, "herdr-for-bot")
	for _, name := range []string{everywhere, justThisOne} {
		if err := os.Symlink(fakeHerdrBin, name); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		what   string
		global string
		host   string
		want   string
	}{
		{"nothing set says nothing", "", "", ""},
		{"the setting for all machines", everywhere, "", everywhere},
		{"a machine's own", "", justThisOne, justThisOne},
		// The per-machine row exists to override the general one, or it is the
		// same row written twice.
		{"a machine's own wins", everywhere, justThisOne, justThisOne},
	} {
		t.Run(tt.what, func(t *testing.T) {
			here := withFakeHerdr(t)
			there, _ := withRemoteHerdr(t)

			cfg := machineConfig("bot")
			cfg.Hosts[0].Mode = "attach"
			cfg.HerdrBin = tt.global
			cfg.Hosts[0].HerdrBin = tt.host
			d := New(cfg)
			if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
				t.Fatalf("connect: %s", reply.Message)
			}
			settle(t, d, here, 3, there)

			mirrors := mirrorsHere(here(), "bot")
			if len(mirrors) == 0 {
				t.Fatal("no mirror was opened, so nothing was told anything")
			}
			if got := envOf(mirrors[0])[mirror.EnvBin]; got != tt.want {
				t.Errorf("the pane was told herdr lives at %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChangingHowTheSpaceOnAMachineIsNamedDoesNotMakeASecondOne(t *testing.T) {
	// The space this plugin makes on a machine is named after this machine, and
	// remote_workspace_format decides how. Change it and the lookup has to
	// still recognise the space the terminals are already in — otherwise it
	// decides there is none, makes a second one beside the first, and the work
	// is left in a space nothing is watching.
	//
	// The rule that recognises it is tested on its own. This is the part that
	// uses it: the lookup, and the creating that happens when the lookup says
	// no.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	first := New(cfg)
	if reply := first.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, first, here, 3, there)

	spacesOnTheMachine := func() map[string]string {
		out := map[string]string{}
		for id, ws := range there().Workspaces {
			label, _ := ws["label"].(string)
			out[id] = label
		}
		return out
	}
	before := spacesOnTheMachine()
	if len(before) != 1 {
		t.Fatalf("the machine has %d spaces to begin with: %v", len(before), before)
	}
	var originalID, originalLabel string
	for id, label := range before {
		originalID, originalLabel = id, label
	}

	// A different format, and a daemon that has never seen this machine — which
	// is what a restart after editing the config is.
	changed := machineConfig("bot")
	changed.Hosts[0].Mode = "attach"
	changed.RemoteWorkspaceFormat = "from {hub}"
	second := New(changed)
	if reply := second.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	settle(t, second, here, 3, there)

	after := spacesOnTheMachine()
	if len(after) != 1 {
		t.Errorf("after the format changed the machine has %d spaces, want the one it had:\n  was %v\n  now %v",
			len(after), before, after)
	}
	if label, still := after[originalID]; !still {
		t.Errorf("the space the terminals were in (%s, %q) is gone", originalID, originalLabel)
	} else if label != originalLabel {
		t.Logf("the space was renamed from %q to %q, which is fine: it is the same space", originalLabel, label)
	}
}

func TestAMachinesOwnSpacesAreNotClaimed(t *testing.T) {
	// "Whatever else the machine has running stays in its own spaces, private
	// and untouched" is what the README promises, and the lookup that finds
	// this plugin's space on a machine is what keeps it: a lookup that settled
	// for any space would adopt whatever the machine had open, mirror it here,
	// and start closing terminals in it to match.
	here := withFakeHerdr(t)
	there, remoteState := withRemoteHerdr(t)

	// The machine is already being used for something of its own, before this
	// plugin has ever connected. Written straight into its state, because
	// addPaneOn needs a machine that has been talked to and the whole point
	// here is that this was there first.
	seed := fakeHerdr{
		Panes: map[string]map[string]any{
			"their-work:p1": {
				"pane_id": "their-work:p1", "workspace_id": "their-work",
				"tab_id": "their-work-tab", "terminal_id": "term_theirs", "label": "",
				"terminal_title_stripped": "a build nobody asked us about",
			},
		},
		Workspaces: map[string]map[string]any{
			"their-work": {"workspace_id": "their-work", "label": "their-work"},
		},
		Next: 1,
	}
	raw, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteState, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := len(there().Workspaces); got != 1 {
		t.Fatalf("the machine started with %d spaces, want the one it was given", got)
	}

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	spaces := there().Workspaces
	if len(spaces) != 2 {
		t.Fatalf("the machine has %d spaces, want its own and one for us: %v", len(spaces), spaces)
	}
	if _, still := spaces["their-work"]; !still {
		t.Error("the machine's own space is gone")
	}

	// And what is mirrored here is ours, not theirs.
	for _, pane := range mirrorsHere(here(), "bot") {
		if title, _ := pane["terminal_title_stripped"].(string); strings.Contains(title, "nobody asked") {
			t.Error("the machine's own work was mirrored here")
		}
	}
	// The pane in the machine's own space is still there and still theirs.
	theirs := 0
	for _, pane := range there().Panes {
		if ws, _ := pane["workspace_id"].(string); ws == "their-work" {
			theirs++
		}
	}
	if theirs != 1 {
		t.Errorf("the machine's own space holds %d panes, want the one it had", theirs)
	}
}

func TestEditingAMachinesSessionTakesEffectWithoutDisconnectingIt(t *testing.T) {
	// A machine's connection is kept and reused. When the settings behind it
	// change, the connection has to be replaced, or it goes on addressing the
	// session it was built for while the config says another.
	//
	// This is reachable without touching the machine at all: toggling mirroring
	// from the menu rereads the whole config file, so an edit to any other
	// machine's session lands then. Pick that machine afterwards and it is
	// connected already — nothing disconnects it, so nothing would rebuild its
	// connection unless this does.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	cfg.Hosts[0].Session = "before"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	said := func() string { return strings.Join(asked(t), "\n") }
	if !strings.Contains(said(), "HERDR_SESSION=before") {
		t.Fatalf("the machine was never addressed with the session it was configured for:\n%s", said())
	}

	// The config moves on, the way it does when the file is reread.
	changed := machineConfig("bot")
	changed.Hosts[0].Mode = "attach"
	changed.Hosts[0].Session = "after"
	d.setConfig(changed)

	// Picked from the menu: already connected, so nothing is torn down.
	before := len(asked(t))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	since := strings.Join(asked(t)[before:], "\n")
	if !strings.Contains(since, "HERDR_SESSION=after") {
		t.Errorf("after the session changed the machine is still being addressed with the old one:\n%s", since)
	}
	if strings.Contains(since, "HERDR_SESSION=before") {
		t.Errorf("the connection built for the old session is still being used:\n%s", since)
	}
}

func TestEditingWhereHerdrLivesTakesEffectTheSameWay(t *testing.T) {
	// The other half of what a kept connection is compared against. A machine's
	// herdr_bin can be edited exactly as its session can, and a connection
	// built for the old path would go on running herdr where it used to be.
	//
	// Real paths, both: a connection told to run herdr somewhere it is not
	// cannot be used at all, which is a different failure.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	dir := t.TempDir()
	before := filepath.Join(dir, "herdr-before")
	after := filepath.Join(dir, "herdr-after")
	for _, name := range []string{before, after} {
		if err := os.Symlink(fakeHerdrBin, name); err != nil {
			t.Fatal(err)
		}
	}

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	cfg.Hosts[0].HerdrBin = before
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)
	if !strings.Contains(strings.Join(asked(t), "\n"), before) {
		t.Fatalf("the machine was never asked to run herdr at %q", before)
	}

	changed := machineConfig("bot")
	changed.Hosts[0].Mode = "attach"
	changed.Hosts[0].HerdrBin = after
	d.setConfig(changed)

	was := len(asked(t))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	since := strings.Join(asked(t)[was:], "\n")
	if !strings.Contains(since, after) {
		t.Errorf("after herdr_bin changed the machine is not being asked to run it at %q:\n%s", after, since)
	}
	if strings.Contains(since, before) {
		t.Errorf("the connection built for the old path is still being used:\n%s", since)
	}
}

func TestTogglingTheModeLeavesOneTerminalWhateverThereWas(t *testing.T) {
	// The two modes are nothing alike underneath, so switching drops the
	// machine's panes here and connects it again in the new way. What comes
	// back is one terminal, not the several you may have had — which the README
	// now says, because it is visible and somebody would otherwise think their
	// terminals had been lost by accident.
	//
	// Held so the README stays true, and because the alternative reading of
	// this line — a machine that had nothing getting a terminal it never asked
	// for — is the other way it can go wrong.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)
	withConfigFile(t, `{"hosts":[{"target":"bot"}]}`)

	d := New(machineConfig("bot"))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 2; i++ {
		if reply := d.dispatch(Command{Cmd: "open", Host: "bot"}); !reply.OK {
			t.Fatalf("open: %s", reply.Message)
		}
	}
	settle(t, d, here, 3, there)
	if got := panesFor(here(), "bot"); got != 3 {
		t.Fatalf("started with %d terminals, want 3", got)
	}

	if reply := d.dispatch(Command{Cmd: "set-mode", Host: "bot", Mode: "attach"}); !reply.OK {
		t.Fatalf("toggle on: %s", reply.Message)
	}
	settle(t, d, here, 5, there)

	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("after turning mirroring on there are %d terminals, want the one the README promises", got)
	}
	// And it is a mirror now, not the plain terminal it was.
	if hosts := d.dispatch(Command{Cmd: "status"}).Hosts; len(hosts) != 1 || hosts[0].SSHOnly {
		t.Errorf("status = %+v, want the machine mirrored", hosts)
	}
}

func TestTogglingAMachineWithNothingOpenStillLeavesOne(t *testing.T) {
	// I expected nothing here, on the grounds that turning a setting on is not
	// a request for a terminal. It gives one, and that is right: toggling is a
	// disconnect and a connect, and connecting to a machine opens a terminal
	// and takes you to it — which is what the menu's enter does and what this
	// key is beside.
	//
	// So the answer is one terminal either way, which is what the README says.
	// Written down because "one back, not all of them" invites the question of
	// what happens when there were none.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)
	withConfigFile(t, `{"hosts":[{"target":"bot"}]}`)

	d := New(machineConfig("bot"))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	// Closed by hand: the machine is connected and has nothing open.
	for _, pane := range mirrorsHere(here(), "bot") {
		if id, _ := pane["pane_id"].(string); id != "" {
			closePaneByHand(t, id)
		}
	}
	settle(t, d, here, 2, there)
	if got := panesFor(here(), "bot"); got != 0 {
		t.Fatalf("the machine still has %d terminals after they were closed", got)
	}

	if reply := d.dispatch(Command{Cmd: "set-mode", Host: "bot", Mode: "attach"}); !reply.OK {
		t.Fatalf("toggle: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("toggling a machine with nothing open left it with %d terminals, want 1", got)
	}
}

func TestAMachineTurnedOffMirroringInTheFileComesBackWithATerminal(t *testing.T) {
	// The other route to changing a mode: edit the file rather than press m,
	// then restart Herdr. The daemon comes back to a machine whose snapshot is
	// full of mirrors and a config that says plain SSH — and mirrors cannot be
	// kept up that way, so they are closed.
	//
	// Closing them is right; leaving it at that is not. The machine would come
	// back with nothing, looking like a connection that had failed, when all
	// that happened is somebody changed a setting. It gets a terminal in the
	// new style instead.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	mirrored := machineConfig("bot")
	mirrored.Hosts[0].Mode = "attach"
	before := New(mirrored)
	if reply := before.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, before, here, 3, there)
	if got := panesFor(here(), "bot"); got != 1 {
		t.Fatalf("started with %d mirrors, want 1", got)
	}
	before.persist()

	// The file now says plain SSH, and Herdr restarts.
	herdrRestarted(t)
	plain := machineConfig("bot") // mode defaults to ssh
	after := New(plain)

	// The way starting up reaches a machine, which is not the way the menu
	// does: it connects and leaves it there. Nothing opens a terminal on the
	// spot, so what the machine ends up with is decided by the pass that
	// follows — which is the whole of what is being tested.
	after.connectEach(after.config().Hosts)
	for i := 0; i < 4; i++ {
		after.reconcileAll()
		terminalsAreRunning(t, here())
	}

	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("after mirroring was turned off in the file the machine has %d terminals, want 1", got)
	}
	if hosts := after.dispatch(Command{Cmd: "status"}).Hosts; len(hosts) != 1 || !hosts[0].SSHOnly {
		t.Errorf("status = %+v, want the machine on plain SSH", hosts)
	}
}
