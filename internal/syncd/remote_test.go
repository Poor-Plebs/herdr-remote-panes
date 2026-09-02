package syncd

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/mirror"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
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

// teardownsOf counts how many times a machine's connection has been torn down.
//
// Tearing one down is the one call that names the machine and asks it to run
// nothing: "ssh -O exit -- bot". Every other call has a command after the
// target.
func teardownsOf(t *testing.T, target string) int {
	t.Helper()
	n := 0
	for _, line := range asked(t) {
		if strings.HasSuffix(strings.TrimSpace(line), "| "+target) {
			n++
		}
	}
	return n
}

// addPaneOn puts a pane into a machine's own state, as work started there does:
// a space of its own, nothing to do with the one shared with this machine.
func addPaneOn(t *testing.T, statePath, workspace, title string) string {
	return addAgentPaneOn(t, statePath, workspace, title, "", "")
}

// addWorkspaceOn puts a space on a machine, for the ones this plugin did not
// make: another hub's, or one left from before a format changed.
func addWorkspaceOn(t *testing.T, statePath, id, label string) {
	t.Helper()
	// The machine's file is written by the stand-in the first time it is
	// asked anything, so before a connect there is nothing there yet -- and a
	// space that was already on the machine is exactly what this puts there.
	var held fakeHerdr
	if raw, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(raw, &held); err != nil {
			t.Fatal(err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if held.Workspaces == nil {
		held.Workspaces = map[string]map[string]any{}
	}
	if held.Panes == nil {
		held.Panes = map[string]map[string]any{}
	}
	held.Workspaces[id] = map[string]any{"workspace_id": id, "label": label}
	out, _ := json.Marshal(held)
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// addPaneInTabOn is addPaneOn with a say in which of the machine's tabs the
// terminal belongs to, for the placement that follows them.
func addPaneInTabOn(t *testing.T, statePath, workspace, tab, title string) string {
	t.Helper()
	id := addAgentPaneOn(t, statePath, workspace, title, "", "")
	var held fakeHerdr
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	held.Panes[id]["tab_id"] = tab
	out, _ := json.Marshal(held)
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

// addAgentPaneOn is addPaneOn for a terminal with an agent running in it.
func addAgentPaneOn(t *testing.T, statePath, workspace, title, agent, status string) string {
	t.Helper()
	var held fakeHerdr
	// A machine nobody has spoken to has no state file yet, which is the
	// ordinary case for one reached over plain SSH: nothing here ever runs its
	// Herdr, so nothing there ever writes anything down.
	if raw, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(raw, &held); err != nil {
			t.Fatal(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if held.Workspaces == nil {
		held.Workspaces = map[string]map[string]any{}
	}
	if held.Panes == nil {
		held.Panes = map[string]map[string]any{}
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

func TestASpaceHerdrHasLostIsNoticedAndForgotten(t *testing.T) {
	// A machine's space here goes when its last pane closes, and the id was
	// kept regardless: every pass then renamed and marked a space that no
	// longer existed, two failing calls each time, for as long as the daemon
	// ran. One machine's log had them every couple of seconds.
	//
	// What notices is the rename coming back "not found", and forgetting is
	// what stops it. Both halves have been tested apart -- planWorkspaceMark
	// decides when to rename, forgetWorkspace clears what is remembered -- and
	// the line between them, which reads the refusal and decides it means the
	// space is gone, had nothing on it.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	d.mu.Lock()
	had := d.hosts["bot"].workspaceID
	d.mu.Unlock()
	if had == "" {
		t.Fatal("the machine has no space here to lose")
	}

	// A rename only happens when the name or the marker has moved on; a
	// settled space is deliberately left alone, which is what the record of
	// what was last put on it is for. Clearing that record puts this in the
	// position of a machine whose state has just changed, which is when the
	// rename that meets the refusal actually happens.
	d.mu.Lock()
	d.markedWorkspaces = map[string]workspaceMark{}
	d.mu.Unlock()

	// From here Herdr says that space is not there, which is what it says
	// once somebody has closed the last pane in it.
	refuseOnMachine(t, os.Getenv(fakeHerdrState), "workspace rename:workspace_not_found")
	renames := here().Calls["workspace rename"]

	settle(t, d, here, 3, there)

	// Checked first: if nothing tried to rename, the refusal was never reached
	// and what follows is about something else.
	if here().Calls["workspace rename"] == renames {
		t.Fatalf("nothing tried to rename the space, so the refusal this is "+
			"about never happened: %+v", here().Calls)
	}

	d.mu.Lock()
	still := d.hosts["bot"].workspaceID
	d.mu.Unlock()
	if still == had {
		t.Errorf("the machine still claims space %q after Herdr said it is not "+
			"there, so every later pass renames and marks it again", still)
	}
}

func TestAMarkThatKeepsFailingIsReportedOnce(t *testing.T) {
	// The marker on a machine's space is put there again whenever the machine's
	// state moves, which is often. A call that keeps failing therefore keeps
	// failing, and reporting each one fills the log with the same line for as
	// long as the daemon runs -- which is how a log stops being read at all.
	//
	// So the failure is recorded on the space and reported only when it was
	// not already failing. What that costs is a second report of the same
	// trouble; what it buys is a log somebody still reads.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	// Refused from here, and not with "not found": that answer means the space
	// has gone and is handled by forgetting it, which is a different branch.
	refuseOnMachine(t, os.Getenv(fakeHerdrState), "workspace report-metadata:internal_error")

	var said strings.Builder
	log.SetOutput(&said)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// Marked again each pass by clearing what was last put there, which is
	// what a machine whose state keeps moving does on its own.
	marks := here().Calls["workspace report-metadata"]
	for i := 0; i < 4; i++ {
		d.mu.Lock()
		for target, mark := range d.markedWorkspaces {
			mark.label, mark.token = "", ""
			d.markedWorkspaces[target] = mark
		}
		d.mu.Unlock()
		settle(t, d, here, 1, there)
	}

	tried := here().Calls["workspace report-metadata"] - marks
	if tried < 2 {
		t.Fatalf("the mark was attempted %d times, so there is no repetition "+
			"to report once: %+v", tried, here().Calls)
	}
	if got := strings.Count(said.String(), "mark space"); got != 1 {
		t.Errorf("%d attempts were reported %d times; the same trouble should be "+
			"said once:\n%s", tried, got, said.String())
	}
}

func TestAPlainShellIsNamedAfterWhereItIs(t *testing.T) {
	// What a machine sends for an ordinary shell is a banner title --
	// "you@laptop:~" -- and a working directory. The banner is skipped, since
	// appending the machine to it would give a name with two "@" in it, so the
	// name comes from the directory.
	//
	// That is the common case on any machine and it was not reached from here
	// until the stand-in started sending what Herdr sends: it sent a title of
	// "zsh" and no directory at all, so every mirror in every test was named
	// from a field that a real shell does not set that way.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	var names []string
	for _, pane := range here().Panes {
		if label, _ := pane["label"].(string); label != "" {
			names = append(names, label)
		}
	}
	if len(names) != 1 {
		t.Fatalf("want one mirror to read the name of, got %v", names)
	}
	// The directory the shell is in, and the machine it is on. Not the banner,
	// which would read "you@laptop:~@bot".
	if names[0] != "you@bot" {
		t.Errorf("the mirror is called %q; a shell in /home/you on bot is "+
			"\"you@bot\"", names[0])
	}
	if strings.Count(names[0], "@") != 1 {
		t.Errorf("the name %q has more than one @, so the banner was used "+
			"rather than skipped", names[0])
	}
}

func TestAPaneIsNotClosedWhenTheMachineWillNotTakeIt(t *testing.T) {
	// Capturing a stray is the one path that closes a pane this plugin did not
	// open, and it closes it because a terminal on the machine is replacing
	// it. If the machine will not open that terminal there is no replacement,
	// and closing anyway destroys somebody's shell and whatever was running in
	// it -- for a feature meant to move work, not lose it.
	//
	// The order in the code is what makes it safe: open there, and only then
	// close here. Reversing those two lines is the mistake this exists for,
	// and it is invisible on the happy path.
	here := withFakeHerdr(t)
	there, remoteState := withRemoteHerdr(t)

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

	// From here the machine refuses to open a terminal, which is what the
	// capture needs before it may close anything.
	refuseOnMachine(t, remoteState, "tab create")
	stray := addLocalPane(t, workspace)

	settle(t, d, here, 4, there)

	if _, still := here().Panes[stray]; !still {
		t.Error("the pane was closed although the machine never opened anything " +
			"to replace it, so whatever was running in it is gone")
	}
	if got := len(there().Panes); got != remoteBefore {
		t.Errorf("the machine has %d terminals against the %d it had, so the "+
			"refusal this is about did not happen and it is testing nothing",
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

func TestATerminalThatWouldNotCloseOnTheMachineSaysSo(t *testing.T) {
	// Closing a mirrored tab closes the terminal on the machine, and that is a
	// call to the machine like any other: it can be refused. The tab has gone
	// here by then and the work is still running there.
	//
	// Nothing else says so. The terminal is recorded as one somebody closed,
	// so it is not mirrored again and does not come back on its own -- the
	// divergence is silent, and the log line is the whole of what anyone gets.
	// So it says what it means rather than which call failed.
	here := withFakeHerdr(t)
	there, remoteState := withRemoteHerdr(t)

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

	// The machine will not close anything from here on.
	refuseOnMachine(t, remoteState, "pane close")

	var said strings.Builder
	log.SetOutput(&said)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	var mirror string
	for id := range here().Panes {
		mirror = id
	}
	closePaneByHand(t, mirror)
	settle(t, d, here, 3, there)

	// Checked before the message: if nothing tried to close it, the message
	// below would be right about a different thing.
	if got := len(there().Panes); got != 2 {
		t.Fatalf("the machine has %d terminals, so the close was not refused "+
			"and this is testing nothing", got)
	}
	if !strings.Contains(said.String(), "still running there") {
		t.Errorf("a terminal that would not close said nothing about still "+
			"running on the machine:\n%s", said.String())
	}
	if !strings.Contains(said.String(), "bot") {
		t.Errorf("it does not name the machine, which is what somebody needs "+
			"to go and look:\n%s", said.String())
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
	// Turning it off means the plugin leaves the machine's sessions alone.
	//
	// A machine set to mirror then has nothing to mirror from, and connecting
	// says so rather than reporting success -- measured, because the comment
	// here used to claim it connected anyway and it does not. Reporting
	// success would be the worse of the two: the menu would show a machine
	// connected and mirroring, with nothing on it and no reason given, and
	// saying what to do about it is the whole use of the reply.
	//
	// Herdr's own sentence is a socket path followed by the command, which is
	// too long for the line the menu gives it -- the path fills the room and
	// the command is what gets cut. So it is summarised, and what the summary
	// has to keep is the command: this is the reply somebody reads when
	// nothing happened, and "no session" without "start one" is a diagnosis
	// with no next step.
	here := withFakeHerdr(t)
	heldOn, _ := withRemoteHerdrRunning(t, false)
	there := func() fakeHerdr { return heldOn("bot") }

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	off := false
	cfg.AutoStart = &off
	d := New(cfg)

	reply := d.dispatch(Command{Cmd: "connect", Host: "bot"})
	settle(t, d, here, 3, there)

	if reply.OK {
		t.Errorf("connecting to a machine with no session to mirror reported success: %q", reply.Message)
	}
	if !strings.Contains(reply.Message, "no herdr session on the machine") {
		t.Errorf("the reply does not say what is wrong: %q", reply.Message)
	}
	if !strings.Contains(reply.Message, "herdr session attach") {
		t.Errorf("the reply says what is wrong and not what to do: %q", reply.Message)
	}
	// And it does not open by saying the machine connected. Letting this get
	// as far as opening a terminal produces "connected to bot, but could not
	// open a terminal", which reads as a healthy connection that failed at the
	// last step -- when what happened is that the machine's Herdr never
	// answered at all, and the thing to do about it is start the session.
	if strings.Contains(reply.Message, "connected to") {
		t.Errorf("a machine whose session never answered is reported as connected: %q", reply.Message)
	}
	if got := len(there().Panes); got != 0 {
		t.Errorf("the machine has %d terminals; nothing should have started a session", got)
	}
	if got := panesFor(here(), "bot"); got != 0 {
		t.Errorf("%d mirrors here of a machine with no session", got)
	}
	// And the menu is told the same thing the reply was, rather than showing a
	// machine that is connected and mirroring nothing.
	status := d.status()
	if len(status) != 1 {
		t.Fatalf("want one machine in the status, got %d", len(status))
	}
	if status[0].Connected {
		t.Error("a machine whose session never answered is reported as connected")
	}
	if status[0].LastError == "" {
		t.Error("the machine is quiet in the menu with no reason given")
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

	// And a format change on its own is not two spaces of one name: there is
	// one space, found by the looser rule, which is the whole point of that
	// rule.
	if hosts := second.dispatch(Command{Cmd: "status"}).Hosts; len(hosts) == 1 && hosts[0].SharedName {
		t.Errorf("changing the format was reported as two spaces sharing a name: %+v", hosts[0])
	}

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

func TestAMachineOverTheMirrorLimitReportsIt(t *testing.T) {
	// End to end, because the flag has to survive the pass that sets it: the
	// status listing is asked long after the reconcile that noticed.
	here := withFakeHerdr(t)
	there, remoteState := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	cfg.MaxMirrors = 2
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	var shared string
	for id := range there().Workspaces {
		shared = id
	}
	// More terminals on the machine than the limit allows.
	for i := 0; i < 3; i++ {
		addPaneOn(t, remoteState, shared, "work")
	}
	settle(t, d, here, 4, there)

	hosts := d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 {
		t.Fatalf("want one machine, got %+v", hosts)
	}
	if !hosts[0].AtCapacity {
		t.Errorf("the machine has more terminals than the limit and does not say so: %+v", hosts[0])
	}
	if hosts[0].Mirrors > cfg.MaxMirrors {
		t.Errorf("%d mirrors against a limit of %d", hosts[0].Mirrors, cfg.MaxMirrors)
	}

	// And it stops saying so once the limit is no longer being hit.
	under := machineConfig("bot")
	under.Hosts[0].Mode = "attach"
	under.MaxMirrors = 32
	d.setConfig(under)
	settle(t, d, here, 4, there)

	if hosts := d.dispatch(Command{Cmd: "status"}).Hosts; len(hosts) == 1 && hosts[0].AtCapacity {
		t.Errorf("the machine still says it is at its limit after the limit was raised: %+v", hosts[0])
	}
}

func TestTwoSpacesWithOneNameOnAMachineAreReported(t *testing.T) {
	// Two machines answering to one hub name — two laptops called the same
	// thing, or two people sharing a space on purpose — can both create it
	// before either sees the other's. Each then settles on whichever came back
	// first, which need not be the same one, and they sit in separate spaces
	// with the same name seeing none of each other's terminals.
	//
	// Nothing can stop that from one side. What it can do is notice, because
	// the state it leaves is the most confusing one there is: both of you are
	// "in pairing" and neither is wrong.
	here := withFakeHerdr(t)
	there, remoteState := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	cfg.RemoteWorkspaceFormat = "pairing"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	if hosts := d.dispatch(Command{Cmd: "status"}).Hosts; len(hosts) != 1 || hosts[0].SharedName {
		t.Fatalf("one space of that name is not a problem: %+v", hosts)
	}

	// Somebody else's daemon makes a second one before seeing ours.
	raw, err := json.Marshal(fakeHerdr{
		Panes: map[string]map[string]any{},
		Workspaces: map[string]map[string]any{
			"w1":    {"workspace_id": "w1", "label": "pairing"},
			"other": {"workspace_id": "other", "label": "pairing"},
		},
		Next: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteState, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// The machine's panes are gone with them, so the space it knew is stale and
	// it looks again — which is when it sees both.
	settle(t, d, here, 3, there)

	hosts := d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 {
		t.Fatalf("want one machine, got %+v", hosts)
	}
	if !hosts[0].SharedName {
		t.Errorf("two spaces called the same thing and nothing says so: %+v", hosts[0])
	}
}

// refuseOnMachine makes the machine whose state file is at path say no to the
// calls named, the way Herdr does when a pane has gone between the listing and
// the request. Verbs are comma separated, each optionally with the error code
// to refuse with: "tab create", "pane split:pane_not_found".
//
// Beside the state rather than in the environment, so it names one machine.
// Both ends of a mirroring test run the same stand-in, and a variable reaches
// both of them.
func refuseOnMachine(t *testing.T, path, verbs string) {
	t.Helper()
	if err := os.WriteFile(path+".refuse", []byte(verbs), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path + ".refuse") })
}

func TestAnUnlabelledPaneIsNotQueuedForAnotherClose(t *testing.T) {
	// The label is what tells a refused pane from whatever takes its id next,
	// so a pane that had none cannot be told apart at all. Queueing it anyway
	// means the next pane on that id gets closed for it -- the empty string
	// matching the empty string.
	//
	// Not closing it leaves a pane that a labelled one would have had closed,
	// which is what happened to every refused pane before any of this. Losing
	// that is the safe half of the trade.
	withFakeHerdr(t)
	d := New(machineConfig("bot"))

	d.closeRefused("w1:p6", "", "close", errors.New("herdr said no"))

	if label, ok := d.unclosed["w1:p6"]; ok {
		t.Errorf("queued a pane with nothing to recognise it by (label %q)", label)
	}
}

func TestAPaneClosedOnTheRetryIsNoLongerListedToClose(t *testing.T) {
	// The test below holds the dangerous case: the id was reused, so the entry
	// is dropped without closing anything. This holds the ordinary one, which
	// nothing did -- the pane is still the pane that was refused, the close
	// works this time, and the entry has to go.
	//
	// Deleting the line that records what was closed left every test here
	// green, because the other route out of the list covers the case they were
	// written for. What is left is an entry for a pane that is already gone,
	// asking to close it again on the next pass.
	here := withFakeHerdr(t)
	d := New(machineConfig("bot"))

	// Refused last pass, and still the same pane: the label matches, which is
	// what says the id has not been handed to something else.
	d.unclosed["w1:p6"] = "build@bot"
	index := newPaneIndex([]herdrcli.Pane{
		{PaneID: "w1:p6", WorkspaceID: "w1", Label: "build@bot"},
	})
	before := here().Calls["pane close"]

	d.retryUnclosed(index)

	if got := here().Calls["pane close"]; got != before+1 {
		t.Errorf("the refused pane was closed %d times, want once", got-before)
	}
	if label, ok := d.unclosed["w1:p6"]; ok {
		t.Errorf("a pane that was closed is still listed to close as %q", label)
	}
	// And struck from the listing this pass is working from, as every other
	// close here corrects.
	if index.alive["w1:p6"] {
		t.Error("the closed pane is still in the listing the pass reads")
	}
}

func TestARecycledPaneIdIsNotClosedByAnOldRefusal(t *testing.T) {
	// The list of panes to try closing again is a list of ids, and Herdr reuses
	// ids. A pane that goes by some other route between two passes -- somebody
	// closes it, the space is remade -- can have its id taken by a new pane
	// before the next pass, and closing by id alone would close whatever had
	// just been opened there.
	//
	// This is the hazard the rest of this file keeps naming: a stale mark meets
	// a reused id. Retrying a close is a worse version of it than most, because
	// what it does about the mistake is destroy somebody's terminal.
	here := withFakeHerdr(t)
	d := New(machineConfig("bot"))

	// Refused while it was a mirror.
	d.unclosed["w1:p6"] = "build@bot"

	// By the next pass the id belongs to something else entirely.
	index := newPaneIndex([]herdrcli.Pane{
		{PaneID: "w1:p6", WorkspaceID: "w1", Label: "notes"},
	})
	before := here().Calls["pane close"]

	d.retryUnclosed(index)

	if got := here().Calls["pane close"]; got != before {
		t.Errorf("closed a pane that had taken the id of a refused one: %d closes, want %d",
			got, before)
	}
	if _, ok := d.unclosed["w1:p6"]; ok {
		t.Error("the id is still listed to close, so a later pass tries again")
	}
	if !index.alive["w1:p6"] {
		t.Error("the pane that took the id was struck from the listing")
	}
}

func TestAPaneHerdrWouldNotCloseIsClosedOnceItWill(t *testing.T) {
	// Turning mirroring off closes the mirrors and opens a plain terminal.
	// Every close site logged a refusal and forgot the pane, which reads as
	// harmless because a close that fails for a pane that has already gone
	// comes back as success -- a refusal means it is still open.
	//
	// Nothing revisited it. The mode change disconnects the machine, so the
	// bookkeeping naming that pane is thrown away; the sweeps that would find
	// it again by its label run once, on adoption. What was left was a dead
	// mirror wearing a live name in the machine's space, for as long as the
	// daemon ran.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)
	withConfigFile(t, `{"hosts":[{"target":"bot","mode":"attach"}]}`)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	// Both spellings of "close this pane", so the fallback from one to the
	// other does not quietly do the work this is about.
	refuseOnMachine(t, os.Getenv(fakeHerdrState), "plugin pane close,pane close")
	asked := here().Calls["plugin pane close"]

	if reply := d.dispatch(Command{Cmd: "set-mode", Host: "bot", Mode: "ssh"}); !reply.OK {
		t.Fatalf("set-mode: %s", reply.Message)
	}

	// Before anything below: if the toggle never tried to close a pane then
	// the refusal was never reached and the rest of this is about nothing.
	if here().Calls["plugin pane close"] == asked {
		t.Fatalf("nothing tried to close a pane, so the refusal this is about "+
			"was never reached: %+v", here().Calls)
	}
	if got := panesFor(here(), "bot"); got != 2 {
		t.Fatalf("want the refused mirror still open beside the new terminal, "+
			"got %d panes", got)
	}

	// Herdr goes on refusing: the pane stays, and nothing says so every pass.
	for i := 0; i < 2; i++ {
		d.reconcileAll()
	}
	if got := panesFor(here(), "bot"); got != 2 {
		t.Fatalf("while the close is refused the pane should still be there, got %d", got)
	}

	// It stops refusing. The next pass closes what it would not close before.
	if err := os.Remove(os.Getenv(fakeHerdrState) + ".refuse"); err != nil {
		t.Fatal(err)
	}
	d.reconcileAll()

	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("after Herdr stopped refusing, %d panes for bot, want the one "+
			"terminal SSH mode gives -- the mirror was left behind", got)
	}
}

func TestTurningMirroringOnSaysWhenNoTerminalOpened(t *testing.T) {
	// The sibling of the unreachable case: the machine answers, the setting is
	// written, and then opening a terminal on it fails anyway. The change has
	// still happened -- it is on disk -- so the reply says so and then says
	// what went wrong, rather than reading as though the toggle had not taken.
	here := withFakeHerdr(t)
	there, remoteState := withRemoteHerdr(t)
	withConfigFile(t, `{"hosts":[{"target":"bot"},{"target":"bot","mode":"attach"}]}`)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)

	// Mirrored once already, so the machine's space is there and the toggle
	// below finds it rather than making it. Made fresh, a space comes with a
	// shell in it and there is nothing left to open -- which is the path the
	// ordinary toggle takes, and not this one.
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	// From here the machine refuses the one call that opens a terminal in it.
	refuseOnMachine(t, remoteState, "tab create")
	asked := there().Calls["tab create"]

	reply := d.dispatch(Command{Cmd: "set-mode", Host: "bot", Mode: "attach"})

	// Checked before the message, because if the toggle never got as far as
	// asking then the message below is right about a different thing and says
	// so confusingly. This is the setup failing, not the code.
	if there().Calls["tab create"] == asked {
		t.Fatalf("the toggle never asked the machine to open a terminal, so the "+
			"branch this is about was not reached: %+v", there().Calls)
	}

	if !reply.OK {
		t.Errorf("set-mode reported failure (%q), but the setting was written "+
			"before this and the menu will show it changed", reply.Message)
	}
	if !strings.Contains(reply.Message, "mirroring on for bot") {
		t.Errorf("reply = %q, want it to say the change happened", reply.Message)
	}
	if !strings.Contains(reply.Message, "no terminal opened") {
		t.Errorf("reply = %q, want it to say what did not happen", reply.Message)
	}
	// One line. This goes on a menu screen, and the refusal it is reporting
	// arrives as a JSON envelope.
	if strings.Contains(reply.Message, "\n") {
		t.Errorf("reply spans lines, which the menu cannot draw: %q", reply.Message)
	}

}

func TestConnectingToASpaceThatIsAlreadyThereIsNotTwoSpaces(t *testing.T) {
	// The ordinary case, and the one with no test: a machine whose space this
	// plugin made earlier and is now finding again. Connecting the first time
	// creates it, which is a different path -- nothing is counted, because
	// there was nothing there to count -- so every test so far went down that
	// one and came back before the counting ever ran.
	//
	// What it guards is the arithmetic that decides two spaces share a name.
	// Off by one there, and every reconnection to a perfectly ordinary machine
	// reports the most confusing state there is.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	cfg.RemoteWorkspaceFormat = "pairing"
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	// Away and back, so the space is found rather than made.
	if reply := d.dispatch(Command{Cmd: "disconnect", Host: "bot"}); !reply.OK {
		t.Fatalf("disconnect: %s", reply.Message)
	}
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connecting again: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	// One space, not a second one beside it.
	spaces := 0
	for _, ws := range there().Workspaces {
		if label, _ := ws["label"].(string); label == "pairing" {
			spaces++
		}
	}
	if spaces != 1 {
		t.Fatalf("connecting again left %d spaces called pairing, want 1: %+v", spaces, there().Workspaces)
	}

	hosts := d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 {
		t.Fatalf("want one machine, got %+v", hosts)
	}
	if hosts[0].SharedName {
		t.Errorf("finding the one space it made earlier was reported as two "+
			"spaces sharing a name: %+v", hosts[0])
	}
}

func TestTheWarningAboutTwoSpacesIsSaidOnceAndOnlyWhenItIsTrue(t *testing.T) {
	// The warning is the only thing that explains the state, so it has to be
	// there when it is true. It also has to be quiet the rest of the time: the
	// lookup it sits in runs on every connection and on every poll that finds
	// the space stale, and a warning repeated at that rate is one nobody reads
	// by the time it matters.
	here := withFakeHerdr(t)
	there, remoteState := withRemoteHerdr(t)

	var logged strings.Builder
	saved := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(saved) })
	said := func() int {
		return strings.Count(logged.String(), "spaces there are called")
	}

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	cfg.RemoteWorkspaceFormat = "pairing"
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)
	// Away and back, so the space is looked up and found rather than made.
	d.dispatch(Command{Cmd: "disconnect", Host: "bot"})
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connecting again: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	if n := said(); n != 0 {
		t.Fatalf("one space of that name was called two %d times: %s", n, logged.String())
	}

	// Somebody else's daemon makes a second one of the same name.
	raw, err := json.Marshal(fakeHerdr{
		Panes: map[string]map[string]any{},
		Workspaces: map[string]map[string]any{
			"w1":    {"workspace_id": "w1", "label": "pairing"},
			"other": {"workspace_id": "other", "label": "pairing"},
		},
		Next: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteState, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// Several passes, each of which looks the space up again.
	settle(t, d, here, 4, there)

	switch n := said(); {
	case n == 0:
		t.Errorf("two spaces called the same thing and the log never said so: %s", logged.String())
	case n > 1:
		t.Errorf("the warning was written %d times, once per pass, which is how "+
			"a log stops being read: %s", n, logged.String())
	}
}

// setAgentStatusOn changes what an agent on a machine reports about itself,
// the way an agent does when it stops waiting and starts working.
func setAgentStatusOn(t *testing.T, statePath, paneID, status string) {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var held fakeHerdr
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	pane, ok := held.Panes[paneID]
	if !ok {
		t.Fatalf("no pane %s on the machine: %+v", paneID, held.Panes)
	}
	pane["agent_status"] = status
	out, _ := json.Marshal(held)
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// agentHere is what the sidebar would show for the one pane carrying an agent.
func agentHere(t *testing.T, held fakeHerdr) (string, string) {
	t.Helper()
	name, status := "", ""
	found := 0
	for _, pane := range held.Panes {
		if a, _ := pane["agent"].(string); a != "" {
			name, _ = pane["agent"].(string)
			status, _ = pane["agent_status"].(string)
			found++
		}
	}
	if found > 1 {
		t.Fatalf("%d panes here carry an agent, want one: %+v", found, held.Panes)
	}
	return name, status
}

func TestAnAgentThatChangesStateOnTheMachineChangesHere(t *testing.T) {
	// The state is the point of the agent showing up at all: an agent that is
	// waiting for you looks the same as one that is working, unless the state
	// keeps up. It is read on every pass and reported only when it differs,
	// because reporting it every pass is a call per pane per poll.
	//
	// That "only when it differs" is the part with teeth. Skip too eagerly --
	// on the pane having been reported at all, rather than on it having been
	// reported the same -- and the first state a pane ever has is the only one
	// it will ever show.
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

	paneThere := addAgentPaneOn(t, machineState, "w-theirs", "shell", "claude", "working")
	settle(t, d, here, 3, there)

	if name, status := agentHere(t, here()); name != "claude" || status != "working" {
		t.Fatalf("the agent shows here as %q/%q, want claude/working", name, status)
	}

	// It finishes and goes back to waiting.
	setAgentStatusOn(t, machineState, paneThere, "idle")
	settle(t, d, here, 3, there)

	name, status := agentHere(t, here())
	if name != "claude" {
		t.Errorf("the agent is now shown as %q, want claude still", name)
	}
	if status != "idle" {
		t.Errorf("the agent went idle on the machine and still reads as %q here, "+
			"so the sidebar shows it working at something it finished", status)
	}

	// And now that it has settled, nothing is reported again. "Only when it
	// differs" is what keeps this from being a call to Herdr per pane per
	// poll, for every mirrored pane with an agent in it, for as long as the
	// session lasts.
	reported := held(here(), "pane report-agent")
	if reported == 0 {
		t.Fatal("the agent was never reported, so this checks nothing")
	}
	settle(t, d, here, 4, there)
	if got := held(here(), "pane report-agent"); got != reported {
		t.Errorf("the agent was reported %d more times over four passes with nothing "+
			"about it changing", got-reported)
	}
}

func TestAPlainSSHMachineReportsNoAgents(t *testing.T) {
	// The README says an agent on a machine reaches the sidebar only if the
	// machine is mirrored, and somebody hit exactly this: Claude started in an
	// SSH terminal, and nothing under agents.
	//
	// It is true because the agent's name and state are things the machine's
	// own Herdr knows, and plain SSH never asks it anything. Held here so that
	// the sentence and the behaviour cannot drift apart -- and so that a change
	// which started reporting agents from a machine nobody is mirroring, on a
	// pane that is only a terminal here, is a failure rather than a surprise.
	prose := readmeProse(t)
	if !strings.Contains(prose, "shows in the sidebar only if the machine is mirrored") {
		t.Error("the README no longer says that an agent needs the machine mirrored")
	}

	here := withFakeHerdr(t)
	there, machineState := withRemoteHerdr(t)

	// No mode set, so plain SSH: the default, and what the sentence is about.
	d := New(machineConfig("bot"))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()

	// An agent hard at work on the machine, which nothing here is watching.
	addAgentPaneOn(t, machineState, "w-theirs", "shell", "claude", "working")
	settle(t, d, here, 3, there)

	for id, pane := range here().Panes {
		if agent, _ := pane["agent"].(string); agent != "" {
			t.Errorf("pane %s here reports the agent %q, but this machine is "+
				"reached over plain SSH and nothing here has asked it anything", id, agent)
		}
	}

	// And the terminal is there, so this is not passing because nothing is.
	if got := panesFor(here(), "bot"); got != 1 {
		t.Fatalf("%d terminals here for bot, want 1: the test proved nothing", got)
	}
}

// markerOn is the state token a machine's space carries here: "remote_up" when
// the machine is answering, "remote_down" when it is not. It is what the glyph
// beside the name in the sidebar is drawn from.
func markerOn(t *testing.T, held fakeHerdr, label string) string {
	t.Helper()
	for _, ws := range held.Workspaces {
		name, _ := ws["label"].(string)
		if !strings.Contains(name, label) {
			continue
		}
		tokens, _ := ws["tokens"].(map[string]any)
		var carried []string
		for token := range tokens {
			carried = append(carried, token)
		}
		sort.Strings(carried)
		return strings.Join(carried, ",")
	}
	return ""
}

func TestTheMarkerOnASpaceFollowsWhetherTheMachineAnswers(t *testing.T) {
	// A machine's space wears a glyph saying whether the machine behind it is
	// reachable, and it is the only thing in the sidebar that says so: the
	// terminals look the same either way until you type in one.
	//
	// Which of the two it wears was decided by one negation with nothing
	// holding it, so dropping that negation swapped them -- every reachable
	// machine wearing the warning and every dead one looking fine, which is
	// worse than having no marker at all.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	// Mirrored, because that is what gets polled: a machine on plain SSH is
	// never asked anything, so it goes on wearing whatever it wore until you
	// type in one of its terminals.
	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	if got := markerOn(t, here(), "bot"); got != "remote_up" {
		t.Fatalf("a machine that answers wears %q, want remote_up", got)
	}

	// The machine stops answering.
	sshFails(t, "ssh: connect to host bot port 22: Connection refused")
	d.reconcileAll()
	d.reconcileAll()

	if got := markerOn(t, here(), "bot"); got != "remote_down" {
		t.Errorf("a machine that has stopped answering wears %q, want remote_down", got)
	}
}

func TestReconnectingEverythingMirrorsTheSameAsNamingOneMachine(t *testing.T) {
	// "connect" with no machine is a bindable "bring my remote spaces back",
	// and it is what the daemon does to every configured machine at startup.
	// It used to do half of what naming a machine does: it opened the SSH
	// connection and stopped there.
	//
	// For a machine on plain SSH that is the whole job. For a mirrored one it
	// is not: what makes its space exist over there, and records which space it
	// is, is the step after connecting. Without it nothing was mirrored, the
	// reply still said how many machines had been reconnected, and every pass
	// afterwards looked the space up again over SSH -- found nothing, and did
	// it again two seconds later for the rest of the session.
	//
	// So this compares the two paths rather than asserting a number: whatever
	// naming a machine gets you, reconnecting everything gets you too.
	named := func(t *testing.T, cmd Command) (mirrors, panesHere, spacesThere, lookups int) {
		here := withFakeHerdr(t)
		there, _ := withRemoteHerdr(t)
		cfg := machineConfig("bot")
		cfg.Hosts[0].Mode = "attach"
		d := New(cfg)
		if reply := d.dispatch(cmd); !reply.OK {
			t.Fatalf("%+v: %s", cmd, reply.Message)
		}
		settle(t, d, here, 5, there)

		before := there().Calls["workspace list"]
		settle(t, d, here, 5, there)
		lookups = there().Calls["workspace list"] - before

		hosts := d.dispatch(Command{Cmd: "status"}).Hosts
		if len(hosts) != 1 {
			t.Fatalf("want one machine, got %+v", hosts)
		}
		spaces := 0
		for _, ws := range there().Workspaces {
			_ = ws
			spaces++
		}
		return hosts[0].Mirrors, panesFor(here(), "bot"), spaces, lookups
	}

	wantMirrors, wantPanes, wantSpaces, wantLookups := named(t, Command{Cmd: "connect", Host: "bot"})
	if wantMirrors == 0 || wantSpaces == 0 {
		t.Fatalf("naming a machine mirrored %d into %d spaces; this test has nothing to compare against",
			wantMirrors, wantSpaces)
	}

	mirrors, panes, spaces, lookups := named(t, Command{Cmd: "connect"})
	if mirrors != wantMirrors || panes != wantPanes || spaces != wantSpaces {
		t.Errorf("reconnecting everything gave %d mirrors, %d panes here and %d spaces there; "+
			"naming the machine gave %d, %d and %d",
			mirrors, panes, spaces, wantMirrors, wantPanes, wantSpaces)
	}
	// And it settles rather than asking the same question forever.
	if lookups > wantLookups {
		t.Errorf("after reconnecting everything the machine is asked for its spaces "+
			"%d times over five passes, against %d when the machine was named",
			lookups, wantLookups)
	}
}

func TestTheOldConnectionIsTornDownWhenSettingsChange(t *testing.T) {
	// Editing a machine's session or Herdr path builds a new connection to it.
	// The tests beside this one check the new one is used and the old one is
	// not, which is the visible half. The other half is that the old one is
	// closed.
	//
	// A connection here is an SSH ControlMaster: a process holding a socket,
	// which lives until it is told to exit. One left behind is not wrong in any
	// way somebody would notice -- nothing uses it, nothing fails -- it is a
	// process and a socket per settings edit, for as long as the session lasts.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	before := teardownsOf(t, "bot")

	cfg.Session = "somewhere-else"
	d.setConfig(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect after the edit: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	if got := teardownsOf(t, "bot") - before; got != 1 {
		t.Errorf("changing the session tore down %d connections, want 1: the old one "+
			"is a process and a socket that nothing will close now", got)
	}

	// And an edit that changes nothing keeps the connection it has: rebuilding
	// one costs a round trip and drops whatever it was multiplexing.
	before = teardownsOf(t, "bot")
	d.setConfig(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect without an edit: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	if got := teardownsOf(t, "bot") - before; got != 0 {
		t.Errorf("connecting again with the same settings tore down %d connections, want none", got)
	}
}

func TestAnAdoptedMachineLearnsWhereItsSpaceIs(t *testing.T) {
	// After a Herdr restart the machine's mirrors are still on screen and the
	// daemon adopts them rather than opening them again. Adopting skips
	// ensureWorkspace, which is what would otherwise have told it which space
	// the machine lives in -- so it learns that from a mirror it has just
	// adopted instead.
	//
	// Nothing had called reconcileHost, so the check that decides whether to
	// learn it could be turned inside out: the machine keeps an empty space
	// id, and everything downstream that places a pane by it has nowhere to
	// put one.
	withFakeHerdr(t)
	withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := withConfig(&Daemon{}, cfg)

	host := cfg.Hosts[0]
	state := newTestHost()
	state.host = host
	state.client = remote.NewWithBin(host.Target, cfg.SessionFor(host), cfg.BinFor(host))
	// What adoption looks like: a mirror on screen, its bridge still running,
	// and no idea yet which space it is in.
	state.mirrors["term-1"] = "w1:p1"
	state.workspaceID = ""
	mirrorIsRunning(t, "w1:p1", "term-1")

	// Known to the daemon, as a machine a pass reconciles always is. The pass
	// gives up its lock for the round trip to the machine and checks on the
	// way back that this is still the machine it started on; a state the
	// daemon has never heard of does not survive that check.
	d.hosts = map[string]*hostSync{host.Target: state}

	index := newPaneIndex([]herdrcli.Pane{
		{PaneID: "w1:p1", WorkspaceID: "w1", TerminalID: "term-1", Label: "shell@bot"},
	})

	// Held, as the pass holds it: reconcileHost gives the lock up for the round
	// trip to the machine and takes it again.
	d.mu.Lock()
	err := d.reconcileHost(state, index)
	d.mu.Unlock()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if state.workspaceID != "w1" {
		t.Errorf("the machine's space is %q, and its own mirror is in %q",
			state.workspaceID, "w1")
	}
}

func TestADaemonSaysWhenAPassOutlastsTheGapBetweenPasses(t *testing.T) {
	// A pass costs about what its slowest machine costs. Past the poll interval
	// there is no gap left: a pass starts as the last ends and the daemon is
	// always in one. That is felt as a menu that takes a moment to open, and
	// had no explanation anywhere -- nothing in the log, nothing in the status,
	// and each machine individually fine.
	var said strings.Builder
	log.SetOutput(&said)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	d := New(machineConfig("bot"))

	// Exactly the gap is not "longer than" it, and the line says longer than.
	// A measured duration never lands there -- these are nanoseconds apart --
	// but the boundary is a decision either way, and this says which.
	d.reportIfSlow(100*time.Millisecond, 100*time.Millisecond, "bot", 90*time.Millisecond)
	if said.Len() != 0 {
		t.Errorf("a pass exactly as long as the gap was called longer than it:\n%s",
			said.String())
	}

	// A pass that took a second, against a gap of a hundred milliseconds.
	d.reportIfSlow(time.Second, 100*time.Millisecond, "bot", 900*time.Millisecond)
	if !strings.Contains(said.String(), "longer than the") {
		t.Errorf("a pass over the interval said nothing:\n%s", said.String())
	}
	if !strings.Contains(said.String(), "poll_interval") {
		t.Error("it does not say what to change, which is the point of saying anything")
	}
	// Named. Machines are polled together, so a pass costs about what its
	// slowest costs and the others are blameless -- "whichever machine is
	// slow" leaves somebody timing them by hand to find out which.
	if !strings.Contains(said.String(), "bot was the slowest") {
		t.Errorf("it does not name the machine the pass was waiting on:\n%s", said.String())
	}

	// Still slow: said once, not every couple of seconds for as long as it lasts.
	said.Reset()
	d.reportIfSlow(time.Second, 100*time.Millisecond, "bot", 900*time.Millisecond)
	if said.Len() != 0 {
		t.Errorf("it said so again while nothing had changed:\n%s", said.String())
	}

	// Back inside the interval: worth saying once, so the log shows it cleared.
	said.Reset()
	d.reportIfSlow(10*time.Millisecond, 100*time.Millisecond, "bot", 9*time.Millisecond)
	if !strings.Contains(said.String(), "back inside") {
		t.Errorf("recovering said nothing, so the log only ever shows the bad news:\n%s",
			said.String())
	}
}

func TestASteadyPassAsksAMachineOneThing(t *testing.T) {
	// Every machine is polled on a timer for as long as Herdr is open, so what
	// one pass costs is paid again every couple of seconds -- and the daemon
	// holds the lock it answers the menu on for the whole of it, so this is
	// what the menu waits for.
	//
	// It asked two things: which panes the machine has, and what order its
	// tabs are in. The order decides the sequence mirrors are opened in, and a
	// settled pass opens nothing, so the second was half the cost of a pass
	// bought for nothing. Existing mirrors are never repositioned by it --
	// they are retitled, and their placement comes from a pane's tab id rather
	// than from the order.
	//
	// Counted from the transcript rather than reasoned about, which is how the
	// conversation with a machine has been checked here before.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	// Settled: connecting itself costs more, and rightly so.
	settle(t, d, here, 3, there)

	before := len(asked(t))
	const passes = 5
	settle(t, d, here, passes, there)
	spoken := asked(t)[before:]

	if len(spoken) != passes {
		t.Errorf("%d settled passes cost %d calls, want %d:\n%s",
			passes, len(spoken), passes, strings.Join(spoken, "\n"))
	}
	for _, line := range spoken {
		if !strings.HasSuffix(strings.TrimSpace(line), "pane list") {
			t.Errorf("a settled pass asked for something besides its pane listing:\n  %s", line)
		}
	}

	// A terminal appears on the machine, so there is something to place and
	// the order is worth asking for again.
	before = len(asked(t))
	if reply := d.dispatch(Command{Cmd: "open", Host: "bot"}); !reply.OK {
		t.Fatalf("open: %s", reply.Message)
	}
	settle(t, d, here, 1, there)

	tabs := 0
	for _, line := range asked(t)[before:] {
		if strings.HasSuffix(strings.TrimSpace(line), "tab list") {
			tabs++
		}
	}
	if tabs == 0 {
		t.Error("a pass with a terminal to place never asked what order the " +
			"machine's tabs are in, so it is placed in whatever order the " +
			"listing happened to arrive in")
	}
}

func TestTerminalsAlreadyOpenOnAMachineAreCounted(t *testing.T) {
	// Turning mirroring on for a machine somebody already works on: their
	// terminals are in spaces of their own there, and the default scope
	// mirrors only the space this plugin made. So one arrives and three do
	// not, which is the setting doing what it says and looks exactly like
	// three that failed.
	//
	// Nothing is mirrored differently by this. What changes is that the
	// machine can say how many it is leaving alone, and which setting decides.
	here := withFakeHerdr(t)
	there, statePath := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	addPaneOn(t, statePath, "their-work", "vim")
	addPaneOn(t, statePath, "their-work", "top")
	addPaneOn(t, statePath, "another", "tail -f")
	settle(t, d, here, 4, there)

	status := d.status()
	if len(status) != 1 {
		t.Fatalf("want one machine, got %d", len(status))
	}
	if status[0].OutsideShared != 3 {
		t.Errorf("the machine has three terminals of its own and reports %d outside the shared space",
			status[0].OutsideShared)
	}
	// Still mirroring what it was mirroring: this counts, it does not collect.
	if status[0].Mirrors != 1 {
		t.Errorf("%d mirrors, want the one in the shared space", status[0].Mirrors)
	}
	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("%d panes here, want 1: counting what is left out must not mirror it", got)
	}
}

func TestWithScopeAllNothingIsLeftOut(t *testing.T) {
	// The other setting: everything on the machine is mirrored, so there is
	// nothing outside the scope to report and saying so on every machine
	// forever would be noise.
	here := withFakeHerdr(t)
	there, statePath := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	cfg.Scope = config.ScopeAll
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	addPaneOn(t, statePath, "their-work", "vim")
	settle(t, d, here, 4, there)

	status := d.status()
	if len(status) != 1 {
		t.Fatalf("want one machine, got %d", len(status))
	}
	if status[0].OutsideShared != 0 {
		t.Errorf("scope all left %d terminals out, and it leaves none out",
			status[0].OutsideShared)
	}
	if status[0].Mirrors < 2 {
		t.Errorf("scope all mirrors %d, want the machine's own terminal as well", status[0].Mirrors)
	}
}

// withSlowPaneList makes `herdr pane list` take a moment, which is the window
// a reconcile pass is in when it is holding no lock: it has taken its list of
// machines and is away fetching the panes.
func withSlowPaneList(t *testing.T, delay string) {
	t.Helper()
	real := os.Getenv("HERDR_BIN_PATH")
	if real == "" {
		t.Fatal("no stand-in herdr to wrap")
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "herdr")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1 $2\" = \"pane list\" ]; then sleep %s; fi\nexec %s \"$@\"\n",
		delay, real)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", wrapper)
}

func TestDisconnectingDuringAPassLeavesTheWorkOnTheMachine(t *testing.T) {
	// Disconnecting closes a machine's panes here and leaves the work running
	// there -- that is the whole difference between it and closing the tabs,
	// and it is what makes `enter` bring the machine straight back.
	//
	// A pass reconciles from a list of panes it fetched while holding no lock,
	// because fetching it is a subprocess and holding the lock across one
	// would stop the daemon answering. So the machine can be disconnected
	// while that is in flight, and the pass then comes back to panes that are
	// gone -- which is exactly what closing the tabs one at a time looks like,
	// and with close_propagates on it closes the terminals on the machine.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)
	if got := len(there().Panes); got != 1 {
		t.Fatalf("the machine has %d terminals, want 1 to start from", got)
	}

	withSlowPaneList(t, "0.4")

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.reconcileOnce()
	}()
	// Long enough to be past the snapshot and inside the fetch, short enough
	// to be well before it returns.
	time.Sleep(150 * time.Millisecond)

	if reply := d.dispatch(Command{Cmd: "disconnect", Host: "bot"}); !reply.OK {
		t.Fatalf("disconnect: %s", reply.Message)
	}
	<-done

	if got := len(there().Panes); got != 1 {
		t.Errorf("the machine has %d terminals after disconnecting, want 1: "+
			"disconnecting closes panes here and leaves the work there", got)
	}
}

func TestTogglingMirroringOffLeavesOneTerminalWhateverYouHad(t *testing.T) {
	// What the README promises about the m key, and what it costs. The test
	// above toggles a machine with one terminal, where "whatever you had" says
	// nothing at all.
	//
	// Both halves matter and they pull opposite ways. Here: the panes cannot
	// be kept -- a mirror and a plain SSH terminal are nothing alike
	// underneath -- so they go and one comes back, rather than the machine
	// vanishing from the sidebar. There: the work is on the machine and stays
	// there, which is the whole reason mirroring is worth the trouble, and is
	// what makes toggling recoverable rather than destructive.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)
	withConfigFile(t, `{"hosts":[{"target":"bot","mode":"attach"}]}`)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)
	for i := 0; i < 2; i++ {
		if reply := d.dispatch(Command{Cmd: "open", Host: "bot"}); !reply.OK {
			t.Fatalf("open: %s", reply.Message)
		}
		settle(t, d, here, 2, there)
	}
	if got := panesFor(here(), "bot"); got != 3 {
		t.Fatalf("started with %d mirrors, want 3 so that one is a change", got)
	}

	if reply := d.dispatch(Command{Cmd: "set-mode", Host: "bot", Mode: "ssh"}); !reply.OK {
		t.Fatalf("toggle off: %s", reply.Message)
	}
	settle(t, d, here, 6, there)

	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("%d terminals here after toggling, want the one the machine is given back", got)
	}
	if got := len(there().Panes); got != 3 {
		t.Errorf("the machine has %d terminals, want the 3 it had: toggling drops the "+
			"panes here, not the work there", got)
	}
}

// tabsFor is which local tabs a machine's terminals are spread across.
func tabsFor(held fakeHerdr, target string) []string {
	seen := map[string]bool{}
	for _, pane := range held.Panes {
		if label, _ := pane["label"].(string); !strings.HasSuffix(label, "@"+target) {
			continue
		}
		tab, _ := pane["tab_id"].(string)
		seen[tab] = true
	}
	var out []string
	for tab := range seen {
		out = append(out, tab)
	}
	sort.Strings(out)
	return out
}

func TestHowAMachinesTerminalsAreLaidOutHere(t *testing.T) {
	// Mirrors are placed by `placement`, which follows the machine: the three
	// terminals here are across two tabs over there -- the one connecting made
	// and the one the other two share -- so they arrive as two tabs.
	//
	// Every setting is held here because the difference between them is the
	// whole of what somebody is asking about when they say their tabs turned
	// into one tab, and none of it is written down anywhere the code would
	// notice if it changed. The default used to be `split`, which is what
	// turned three tabs into one and what the question was about.
	for _, tt := range []struct {
		placement string
		wantTabs  int
		what      string
	}{
		{"", 2, "the default follows the machine's own tabs"},
		{"follow", 2, "and so does asking for that"},
		{"split", 1, "split puts them all in one tab"},
		{"tab", 3, "a tab each regardless of the machine"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			here := withFakeHerdr(t)
			there, statePath := withRemoteHerdr(t)

			cfg := machineConfig("bot")
			cfg.Hosts[0].Mode = "attach"
			cfg.Hosts[0].Placement = tt.placement
			d := New(cfg)
			if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
				t.Fatalf("connect: %s", reply.Message)
			}
			settle(t, d, here, 3, there)

			// Two more started on the machine, in the space the two ends share.
			var shared string
			for id := range there().Workspaces {
				shared = id
			}
			addPaneOn(t, statePath, shared, "vim")
			addPaneOn(t, statePath, shared, "top")
			settle(t, d, here, 5, there)

			if got := panesFor(here(), "bot"); got != 3 {
				t.Fatalf("%d panes here, want the machine's 3 mirrored", got)
			}
			if got := tabsFor(here(), "bot"); len(got) != tt.wantTabs {
				t.Errorf("three terminals on the machine are in %d tabs here (%v), want %d",
					len(got), got, tt.wantTabs)
			}
		})
	}
}

func TestAMirrorThatOpensForgetsWhatItTook(t *testing.T) {
	// Opening a mirror can fail, and the attempts are counted: enough of them
	// and the terminal is given up on, left running on the machine with
	// nothing here showing it. So a terminal that does open has to start
	// again from nothing, or a flaky one that recovers carries its old count
	// for ever and is abandoned at the first hint of trouble after that.
	//
	// The count is written rather than arrived at: making an open fail needs
	// Herdr itself to refuse, and what is being checked is what success does,
	// not what failure does.
	here := withFakeHerdr(t)
	there, statePath := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 3, there)

	var shared string
	for id := range there().Workspaces {
		shared = id
	}
	paneID := addPaneOn(t, statePath, shared, "vim")
	// The stand-in numbers a pane and its terminal together.
	terminalID := "term_" + paneID[strings.LastIndex(paneID, ":p")+2:]

	d.mu.Lock()
	state := d.hosts["bot"]
	state.failures[terminalID] = 2
	state.retryAt[terminalID] = time.Now().Add(-time.Second)
	d.mu.Unlock()

	settle(t, d, here, 4, there)

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, mirrored := state.mirrors[terminalID]; !mirrored {
		t.Fatalf("%s was never mirrored, so this checks nothing: %v", terminalID, state.mirrors)
	}
	if got := state.failures[terminalID]; got != 0 {
		t.Errorf("a mirror that opened still carries %d failed attempts", got)
	}
	if _, waiting := state.retryAt[terminalID]; waiting {
		t.Error("a mirror that opened is still being waited on before the next try")
	}
}

func TestAnAgentIsReleasedOnceAndNotOnEveryPassAfter(t *testing.T) {
	// An agent that finishes on the machine has to be taken off the sidebar
	// here, and the pane is remembered as no longer having one so that it is
	// not taken off again. Forget to remember it and the release is repeated
	// for as long as the pane lives: a call to Herdr per pane per poll, for a
	// pane whose agent went hours ago.
	//
	// The failure is the other way round and matters more: if a release that
	// did not work were remembered as done, an agent that has finished would
	// sit in the sidebar for ever, and the record here is the only thing that
	// would try again.
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

	paneThere := addAgentPaneOn(t, machineState, "w-theirs", "shell", "claude", "working")
	settle(t, d, here, 3, there)
	if name, _ := agentHere(t, here()); name != "claude" {
		t.Fatalf("the agent shows here as %q, want claude", name)
	}

	clearAgentOn(t, machineState, paneThere)
	settle(t, d, here, 3, there)

	if name, _ := agentHere(t, here()); name != "" {
		t.Errorf("the agent finished on the machine and still shows here as %q", name)
	}
	released := held(here(), "pane release-agent")
	if released == 0 {
		t.Fatal("the agent was never released, so this checks nothing")
	}

	settle(t, d, here, 4, there)
	if got := held(here(), "pane release-agent"); got != released {
		t.Errorf("releasing was repeated %d more times over four passes; once it is "+
			"done it is done", got-released)
	}
}

// held is how many times Herdr was asked to do something.
func held(state fakeHerdr, call string) int {
	return state.Calls[call]
}

// setTitleOn renames what a terminal on the machine is showing, the way a
// command starting or finishing there does.
func setTitleOn(t *testing.T, statePath, paneID, title string) {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var held fakeHerdr
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	pane, ok := held.Panes[paneID]
	if !ok {
		t.Fatalf("no pane %s on the machine: %+v", paneID, held.Panes)
	}
	pane["terminal_title_stripped"] = title
	out, _ := json.Marshal(held)
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAMirrorIsRenamedWhenItsTerminalIsAndNotOtherwise(t *testing.T) {
	// A mirror is named after what the terminal on the machine is showing, so
	// the sidebar says what is running rather than which pane it is. That
	// changes whenever a command starts or ends over there.
	//
	// And it is asked for only when it differs. Renaming on every pass is a
	// call to Herdr per mirrored pane per poll, which for somebody with
	// several machines mirrored is most of what the daemon does all day.
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

	paneThere := addPaneOn(t, machineState, "w-theirs", "vim")
	settle(t, d, here, 3, there)
	if !strings.Contains(labelsHere(here(), "bot"), "vim") {
		t.Fatalf("the mirror is not named after what the terminal shows: %s", labelsHere(here(), "bot"))
	}

	// Settled: nothing about it is changing, so nothing should be asked.
	renamed := held(here(), "pane rename")
	if renamed == 0 {
		t.Fatal("nothing was ever renamed, so this checks nothing")
	}
	settle(t, d, here, 4, there)
	if got := held(here(), "pane rename"); got != renamed {
		t.Errorf("a mirror whose terminal has not changed was renamed %d more times "+
			"over four passes", got-renamed)
	}

	// And when it does change, it follows.
	setTitleOn(t, machineState, paneThere, "make test")
	settle(t, d, here, 3, there)
	if !strings.Contains(labelsHere(here(), "bot"), "make test") {
		t.Errorf("the terminal is showing something else and the mirror still reads %s",
			labelsHere(here(), "bot"))
	}
}

// labelsHere is every label this machine's panes are wearing.
func labelsHere(state fakeHerdr, target string) string {
	var out []string
	for _, pane := range state.Panes {
		if label, _ := pane["label"].(string); strings.HasSuffix(label, "@"+target) {
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestReconnectingEverythingRevivesMachinesThatGaveUp(t *testing.T) {
	// The shape of a laptop coming back from sleep, or a VPN reconnecting:
	// every machine goes at once, each is retried, each fails the same way,
	// and all of them are given up on within a few seconds. Nothing is wrong
	// with any of them by the time somebody looks.
	//
	// Connecting with no machine named is the way back for all of them at
	// once, rather than picking each from the menu in turn -- which is what
	// the README used to say, and is one press per machine.
	withFakeHerdr(t)
	dialled := withUnreachableMachine(t)

	d := New(machineConfig("bot", "prod", "ci"))
	d.dispatch(Command{Cmd: "connect"})
	for i := 0; i < 10; i++ {
		d.reconcileAll()
	}

	given := 0
	for _, h := range d.status() {
		if h.GaveUp {
			given++
		}
	}
	if given != 3 {
		t.Fatalf("%d machines gave up, want all 3 so there is something to revive", given)
	}
	settled := dialled()

	d.dispatch(Command{Cmd: "connect"})

	for _, h := range d.status() {
		if h.GaveUp {
			t.Errorf("%s is still given up on after reconnecting everything", h.Target)
		}
	}
	// And each was actually tried again, rather than only being marked as if.
	if tried := dialled() - settled; tried < 3 {
		t.Errorf("reconnecting everything dialled %d times for 3 machines", tried)
	}
}

// withMachineLackingHerdr answers ssh but has no herdr on it, which is most
// machines somebody has an account on.
func withMachineLackingHerdr(t *testing.T) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"last=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in\n" +
		"  true) exit 0;;\n" +
		"  *) echo 'sh: herdr: command not found' >&2; exit 127;;\n" +
		"esac\n"
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAMachineAskedToMirrorWithNoHerdrStillWorks(t *testing.T) {
	// The headline promise, and the reason this is usable on machines nobody
	// controls: mirroring is the only part that needs Herdr at both ends, and
	// a machine that turns out not to have it falls back rather than refusing.
	//
	// The remote package holds that the probe answers ErrNoHerdr. What was not
	// held anywhere is what the daemon then does with it, which is the half
	// somebody actually meets: asked to mirror, and given a plain terminal
	// instead of an error.
	here := withFakeHerdr(t)
	withMachineLackingHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)

	reply := d.dispatch(Command{Cmd: "connect", Host: "bot"})
	if !reply.OK {
		t.Fatalf("a machine without herdr refused to connect: %s", reply.Message)
	}
	d.reconcileAll()

	status := d.status()
	if len(status) != 1 {
		t.Fatalf("want one machine, got %d", len(status))
	}
	if !status[0].Connected {
		t.Errorf("the machine is not connected: %s", status[0].LastError)
	}
	// Reached the way it can be reached, and said so. Without the mark the
	// menu offers to toggle mirroring on a machine that cannot do it, and
	// nothing explains why it stayed off.
	if !status[0].SSHOnly {
		t.Error("the machine is not marked as reached over plain ssh")
	}
	if !status[0].NoHerdr {
		t.Error("the machine is not marked as having no herdr, so nothing says why " +
			"the mode it was asked for is not the mode it is in")
	}
	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("%d terminals here, want the one connecting opens", got)
	}
}

func TestATabYouClosedStaysClosedAcrossARestart(t *testing.T) {
	// "A dropped connection comes back; one you closed does not — and is still
	// closed after a restart." The pieces are held apart: the snapshot carries
	// the dismissals, and the planner skips what is in it. Nothing held the
	// two together, which is the only form the promise is made in.
	//
	// close_propagates off, or there is nothing to come back: with it on, the
	// terminal on the machine goes when the tab does, and a terminal that does
	// not exist stays closed for reasons that have nothing to do with this.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	no := false
	cfg.ClosePropagates = &no

	before := New(cfg)
	if reply := before.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	if reply := before.dispatch(Command{Cmd: "open", Host: "bot"}); !reply.OK {
		t.Fatalf("open: %s", reply.Message)
	}
	settle(t, before, here, 4, there)
	if got, want := panesFor(here(), "bot"), 2; got != want {
		t.Fatalf("started with %d mirrors, want %d", got, want)
	}

	var mirror string
	for id := range here().Panes {
		mirror = id
	}
	closePaneByHand(t, mirror)
	settle(t, before, here, 3, there)
	if got := panesFor(here(), "bot"); got != 1 {
		t.Fatalf("%d mirrors after closing one, want 1", got)
	}
	if got := len(there().Panes); got != 2 {
		t.Fatalf("the machine has %d terminals, want both still there", got)
	}
	before.persist()

	herdrRestarted(t)
	after := New(cfg)
	if reply := after.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	settle(t, after, here, 6, there)

	// The terminal is still on the machine and could be mirrored. It is not,
	// because somebody closed it, and a restart is not them changing their
	// mind.
	if got := panesFor(here(), "bot"); got != 1 {
		t.Errorf("%d mirrors after the restart, want 1: a tab somebody closed came back", got)
	}
	if got := len(there().Panes); got != 2 {
		t.Errorf("the machine has %d terminals, want the 2 it had", got)
	}
}

// TestTheMenuAnswersWhileAMachineIsBeingWaitedFor is the thing all of that was
// for.
//
//	HRP_TIMING=1 go test ./internal/syncd/ -run MenuAnswersWhile -v
//
// The daemon answers the menu, the status listing and every command on d.mu.
// It used to hold that lock across the SSH round trip to each machine, so a
// machine that had stopped answering -- one that swallows packets rather than
// refusing it, which takes the operating system's own timeout to fail -- took
// the menu with it for as long as it lasted.
//
// Measured against two machines whose every call takes two seconds: the menu
// waited 3.71s before, and does not wait at all now.
//
// Opt-in with the other timing measurement, and generous for the same reason:
// it is meant to tell "does not wait" from "waits for a machine", not to
// police jitter.
func TestTheMenuAnswersWhileAMachineIsBeingWaitedFor(t *testing.T) {
	if os.Getenv("HRP_TIMING") == "" {
		t.Skip("set HRP_TIMING=1 to run; it sleeps to make the difference visible")
	}

	here := withFakeHerdr(t)
	withRemoteHerdrRunning(t, true)

	cfg := machineConfig("bot", "prod")
	for i := range cfg.Hosts {
		cfg.Hosts[i].Mode = "attach"
	}
	d := New(cfg)
	for _, target := range []string{"bot", "prod"} {
		if reply := d.dispatch(Command{Cmd: "connect", Host: target}); !reply.OK {
			t.Fatalf("connect %s: %s", target, reply.Message)
		}
	}
	settle(t, d, here, 2)

	// From here every call to a machine takes two seconds: both dropping out.
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	path := filepath.Join(bin, "ssh")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	slow := strings.Replace(string(raw), "#!/bin/sh\n", "#!/bin/sh\nsleep 2\n", 1)
	if err := os.WriteFile(path, []byte(slow), 0o755); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); d.reconcileAll() }()
	// Long enough that the pass is inside the round trip rather than still
	// setting up, and far short of the two seconds that round trip takes.
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	reply := d.dispatch(Command{Cmd: "status"})
	waited := time.Since(start)
	wg.Wait()

	if !reply.OK {
		t.Fatalf("status while a machine was being waited for: %s", reply.Message)
	}
	t.Logf("the menu waited %v", waited.Round(10*time.Millisecond))
	if waited > time.Second {
		t.Errorf("the menu waited %v while a machine was being polled, so the pass "+
			"is holding the daemon across the round trip again -- a machine that "+
			"stops answering takes the menu with it",
			waited.Round(10*time.Millisecond))
	}
}

// TestASlowPassNamesTheMachineItWasWaitingOn holds the join between measuring
// and reporting.
//
//	HRP_TIMING=1 go test ./internal/syncd/ -run SlowPassNames -v
//
// reportIfSlow is told which machine was slowest, and is tested on its own by
// being handed one. What that leaves untested is the half that matters: that a
// pass measures each machine and hands over the right name. Handing over the
// wrong one is worse than handing over none -- it points at a machine that is
// fine, and the advice is to stop mirroring it.
func TestASlowPassNamesTheMachineItWasWaitingOn(t *testing.T) {
	if os.Getenv("HRP_TIMING") == "" {
		t.Skip("set HRP_TIMING=1 to run; it sleeps to make one machine the slow one")
	}

	here := withFakeHerdr(t)
	withRemoteHerdrRunning(t, true)

	cfg := machineConfig("bot", "prod")
	for i := range cfg.Hosts {
		cfg.Hosts[i].Mode = "attach"
	}
	// Short enough that one slow machine puts a pass over it.
	cfg.PollInterval = "500ms"
	d := New(cfg)
	for _, target := range []string{"bot", "prod"} {
		if reply := d.dispatch(Command{Cmd: "connect", Host: target}); !reply.OK {
			t.Fatalf("connect %s: %s", target, reply.Message)
		}
	}
	settle(t, d, here, 2)

	// Only prod is slow. The destination is the argument before the command.
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	path := filepath.Join(bin, "ssh")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	slow := strings.Replace(string(raw), "#!/bin/sh\n",
		"#!/bin/sh\nfor a in \"$@\"; do prev=\"$last\"; last=\"$a\"; done\n"+
			"[ \"$prev\" = prod ] && sleep 1\n", 1)
	if err := os.WriteFile(path, []byte(slow), 0o755); err != nil {
		t.Fatal(err)
	}

	var said strings.Builder
	log.SetOutput(&said)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	d.reconcileAll()

	if !strings.Contains(said.String(), "longer than the") {
		t.Fatalf("the pass was not reported as slow, so this is testing "+
			"nothing:\n%s", said.String())
	}
	if !strings.Contains(said.String(), "prod was the slowest") {
		t.Errorf("the slow machine was prod and the report says otherwise:\n%s",
			said.String())
	}
	if strings.Contains(said.String(), "bot was the slowest") {
		t.Errorf("it blamed the machine that was answering promptly:\n%s", said.String())
	}
}

// TestAPassCostsEveryMachineAddedTogether measures what a reconcile pass costs
// as machines are added.
//
//	HRP_TIMING=1 go test ./internal/syncd/ -run PassCostsEveryMachine -v
//
// A goroutine is started per machine, and each gives up the daemon's lock for
// the round trip that asks its machine for panes -- so the round trips overlap
// and a pass costs about what the slowest machine costs.
//
// It did not. Each goroutine took d.mu and held it across its own round trip,
// so they ran strictly one after another and a pass cost every machine added
// together: three at 300ms cost 910ms, where one cost 610ms. That lock is what
// answers the menu, so that was the menu's wait.
//
// Kept because the property is worth holding rather than assuming: this is the
// measurement that says whether the round trips still overlap.
//
// Opt-in: it sleeps in a stand-in ssh to make the difference legible, so it
// takes seconds, and timing on a shared CI runner is not a thing to fail a
// build over.
func TestAPassCostsEveryMachineAddedTogether(t *testing.T) {
	if os.Getenv("HRP_TIMING") == "" {
		t.Skip("set HRP_TIMING=1 to run; it sleeps to make the difference visible")
	}

	pass := func(t *testing.T, targets ...string) time.Duration {
		t.Helper()
		withFakeHerdr(t)
		withRemoteHerdrRunning(t, true)

		cfg := machineConfig(targets...)
		for i := range cfg.Hosts {
			cfg.Hosts[i].Mode = "attach"
		}
		d := New(cfg)
		for _, target := range targets {
			if reply := d.dispatch(Command{Cmd: "connect", Host: target}); !reply.OK {
				t.Fatalf("connect %s: %s", target, reply.Message)
			}
		}

		// Every call to a machine takes about this long, from here on.
		bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
		path := filepath.Join(bin, "ssh")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		slow := strings.Replace(string(raw), "#!/bin/sh\n", "#!/bin/sh\nsleep 0.3\n", 1)
		if err := os.WriteFile(path, []byte(slow), 0o755); err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		d.reconcileAll()
		return time.Since(start)
	}

	var one, three time.Duration
	t.Run("one machine", func(t *testing.T) { one = pass(t, "bot") })
	t.Run("three machines", func(t *testing.T) { three = pass(t, "bot", "prod", "web") })

	t.Logf("one machine: %v", one.Round(10*time.Millisecond))
	t.Logf("three machines: %v", three.Round(10*time.Millisecond))
	t.Logf("polled together this is about %v; one after another it was about %v",
		one.Round(10*time.Millisecond), (3 * one).Round(10*time.Millisecond))

	// Held now, rather than only reported. Three machines costing appreciably
	// more than one is the pass having gone back to waiting on each in turn.
	//
	// Generous, because it is a timing test: meant to tell three-times from
	// one-times, not to police jitter on a busy machine.
	if three > 2*one {
		t.Errorf("three machines cost %v against one machine's %v, so the round "+
			"trips are no longer overlapping -- a pass is back to costing every "+
			"machine added together, which is what the menu waits for",
			three.Round(10*time.Millisecond), one.Round(10*time.Millisecond))
	}
}

// relabelPane renames a pane inside the local Herdr, standing in for the pane
// going and its id being handed to something else.
func relabelPane(t *testing.T, paneID, label string) {
	t.Helper()
	statePath := os.Getenv(fakeHerdrState)
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var held fakeHerdr
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	pane, ok := held.Panes[paneID]
	if !ok {
		t.Fatalf("no pane %s to rename, so this test is about nothing", paneID)
	}
	pane["label"] = label
	out, _ := json.Marshal(held)
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAStrayIsCheckedAgainBeforeItIsClosed(t *testing.T) {
	// Moving a stray onto the machine opens a terminal there first -- an ssh
	// round trip -- and closes the local pane by id afterwards. Herdr reuses
	// pane ids. In that gap the pane can be closed by hand and its id handed
	// to something somebody has since opened, and closing by id closed that.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)
	withConfigFile(t, `{"hosts":[{"target":"bot","mode":"attach"}]}`)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	workspace := ""
	for id := range here().Workspaces {
		workspace = id
		break
	}
	if workspace == "" {
		t.Fatal("no space to put a pane in")
	}

	// The one that changed hands: planned as "notes", something else by the
	// time the close comes round.
	changed := addLeftoverPane(t, workspace, "notes")
	relabelPane(t, changed, "somebody-elses-work")
	// And one that did not, so this cannot pass by closing nothing at all.
	kept := addLeftoverPane(t, workspace, "notes")

	d.captureStrayPanes(cfg.Hosts[0], []strayPane{
		{PaneID: changed, Placement: "split", Label: "notes"},
		{PaneID: kept, Placement: "split", Label: "notes"},
	})

	if _, open := here().Panes[changed]; !open {
		t.Error("the pane holding the id now was closed, and nobody asked for that")
	}
	if _, open := here().Panes[kept]; open {
		t.Error("the pane that was still the stray was left open, so nothing was moved")
	}
}

func TestReplacingAPaneSaysWhichPaneItWas(t *testing.T) {
	// The one branch that closes a pane on the strength of a remembered id
	// alone. The identity check beside it compares the terminal a running
	// mirror reports, and this branch runs precisely when nothing is running
	// there to ask -- so if a reused id ever brings back somebody else's pane,
	// the log is the only record of what was closed. An id on its own is not
	// one: nothing afterwards can say what "w1:p5" was.
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)
	withConfigFile(t, `{"hosts":[{"target":"bot","mode":"attach"}]}`)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)
	d.persist()

	// A second daemon over the same Herdr: the mirrors are remembered, and
	// nothing is running in those panes -- which is what Herdr restoring a
	// plugin pane as a plain shell leaves behind.
	logged := captureLog(t)
	second := New(cfg)
	if reply := second.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	settle(t, second, here, 2, there)

	line := ""
	for _, l := range strings.Split(logged.String(), "\n") {
		if strings.Contains(l, "replacing pane") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("no pane was replaced, so this test is about nothing:\n%s", logged.String())
	}
	if !strings.Contains(line, "@bot") {
		t.Errorf("the line names no pane, only an id: %q", line)
	}
	if !strings.Contains(line, "terminal ") {
		t.Errorf("the line does not say which terminal's mirror was missing: %q", line)
	}
}

func TestConnectingAgainPicksUpAModeThatChangedUnderneath(t *testing.T) {
	// Connecting a machine that is already connected takes the branch that
	// updates the state in place, and what it updates is the mode: whether
	// this machine mirrors or is a plain SSH terminal, which seven places read
	// and which decides what the whole pass does with it.
	//
	// Nothing held it. The set-mode tests do not reach this line -- toggling
	// from the menu disconnects first, so the reconnect builds a fresh state
	// through the other branch -- and deleting it broke none of them. What is
	// left is a machine whose config says one thing while the daemon goes on
	// doing another, until it is disconnected or the daemon is restarted.
	withFakeHerdr(t)
	withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	cfg.Hosts[0].Label = "before"
	// The real constructor: a mirroring connect reaches the maps a pass fills
	// in, which an empty daemon leaves nil.
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	hosts := d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 || hosts[0].SSHOnly {
		t.Fatalf("a machine set to mirror reads as ssh-only: %+v", hosts)
	}

	// The file changed underneath, as editing it and pressing connect does.
	// Not through set-mode, which disconnects first and so never reaches the
	// line this is about.
	ssh := machineConfig("bot")
	ssh.Hosts[0].Mode = "ssh"
	ssh.Hosts[0].Label = "after"
	d.setConfig(ssh)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connecting again: %s", reply.Message)
	}
	hosts = d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 {
		t.Fatalf("status = %+v, want the machine listed", hosts)
	}
	if !hosts[0].SSHOnly {
		t.Error("the config says this machine is a plain SSH terminal and the " +
			"daemon is still mirroring it")
	}
	// The settings beside the mode are updated by their own line, and go
	// stale the same way: a label is what the machine is called everywhere it
	// appears.
	if hosts[0].Label != "after" {
		t.Errorf("the machine is listed as %q; the config renamed it to %q",
			hosts[0].Label, "after")
	}
	// Not asserted: whether Herdr is there, which is the third line here.
	// Reaching it needs a machine that gains or loses Herdr between two
	// connects, which the stand-in cannot do, so that one is still open.
}

func TestAConfigFaultThatComesBackIsSaidAgain(t *testing.T) {
	// A half-written file is the ordinary case here: saving is not atomic in
	// every editor and a pass comes round every couple of seconds, so the
	// complaint is said once rather than once per pass. What makes that "once
	// per distinct complaint" rather than "once ever" is forgetting it when
	// the file reads again.
	//
	// Nothing held the forgetting. Without it the first fault is the last one
	// ever mentioned: the same mistake made again next week is kept in use
	// silently, and the log somebody is sent to read says nothing about the
	// file they have just broken.
	said := captureLog(t)
	path := withConfigFile(t, `{"placement":"tab","hosts":[{"target":"bot"}]}`)
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	d := New(loaded)

	// The same fault twice, with a good read in between. The same text both
	// times on purpose: "distinct" is about the complaint, so a different
	// error would be said again whether it was forgotten or not.
	const broken = `{"placement":`
	good := `{"placement":"split","hosts":[{"target":"bot"}]}`

	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		// Every reread is gated on the file having changed, and a test can
		// write twice inside one filesystem timestamp.
		when := time.Now().Add(time.Duration(len(body)) * time.Second)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
		d.rereadConfig()
	}

	write(broken)
	if n := strings.Count(said.String(), "could not reread"); n != 1 {
		t.Fatalf("a broken config was complained about %d times, want once", n)
	}
	write(good)
	write(broken)

	if n := strings.Count(said.String(), "could not reread"); n != 2 {
		t.Errorf("the same fault came back and was complained about %d times in "+
			"total, want twice: the first one is being remembered for ever", n)
	}
}

func TestTheConfigIsRereadOnceAndThenLeftAlone(t *testing.T) {
	// The tests around this one set configStamp by hand before calling --
	// "as the daemon would have stamped it on the pass that read it" -- so
	// what the daemon stamps is simulated rather than exercised. Deleting the
	// line that writes it broke none of them.
	//
	// Without it nothing on disk ever matches what was last read, so every
	// pass rereads the file, reloads it into the daemon and says "the config
	// changed on disk and has been reread". A pass comes round every couple of
	// seconds, and this is the log somebody is told to go and read.
	said := captureLog(t)
	path := withConfigFile(t, `{"placement":"tab","hosts":[{"target":"bot"}]}`)
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	d := New(loaded)
	// Deliberately no stamp set here. The daemon has to write its own down.

	// The first pass reads it, since nothing has been stamped yet.
	d.rereadConfig()
	// And these change nothing on disk, so they must do nothing at all.
	d.rereadConfig()
	d.rereadConfig()

	if n := strings.Count(said.String(), "has been reread"); n != 1 {
		t.Errorf("a config that did not change was reread %d times, want once", n)
	}

	// And a real change is still picked up: this must not be a daemon that
	// simply stopped looking.
	if err := os.WriteFile(path, []byte(`{"placement":"split","hosts":[{"target":"bot"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	newer := time.Now().Add(time.Second)
	if err := os.Chtimes(path, newer, newer); err != nil {
		t.Fatal(err)
	}
	d.rereadConfig()

	if got := d.config().Placement; got != "split" {
		t.Errorf("the file says placement %q and the daemon is using %q", "split", got)
	}
	if n := strings.Count(said.String(), "has been reread"); n != 2 {
		t.Errorf("a config that did change was reread %d times, want twice", n)
	}
}

func TestAConfigRestoredFromABackupIsRead(t *testing.T) {
	// The file is watched by its modification time, and a restored one is
	// older than what replaced it. `cp -p` an earlier copy back, or check one
	// out of git, and the file on disk says one thing while the daemon goes on
	// using another -- with nothing to say why, until Herdr is restarted.
	//
	// The same shape as a clock that steps back, which a machine waking from
	// sleep can do.
	path := withConfigFile(t, `{"placement":"tab","hosts":[{"target":"bot"}]}`)
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	d := New(loaded)
	if got := d.config().Placement; got != "tab" {
		t.Fatalf("the daemon started with placement %q", got)
	}
	// As the daemon would have stamped it on the pass that read it.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	d.configStamp = info.ModTime()

	// The backup goes back, keeping its own older timestamp.
	if err := os.WriteFile(path, []byte(`{"placement":"split","hosts":[{"target":"bot"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	older := info.ModTime().Add(-time.Hour)
	if err := os.Chtimes(path, older, older); err != nil {
		t.Fatal(err)
	}

	d.rereadConfig()

	if got := d.config().Placement; got != "split" {
		t.Errorf("the file on disk says placement %q and the daemon is using %q",
			"split", got)
	}
}

func TestAConfigThatHasNotMovedIsNotRereadEveryPass(t *testing.T) {
	// The other half of the same check: a pass comes round every couple of
	// seconds, and rereading a file nobody touched would log that it changed
	// on every one of them.
	path := withConfigFile(t, `{"placement":"tab","hosts":[{"target":"bot"}]}`)
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	d := New(loaded)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	d.configStamp = info.ModTime()

	logged := captureLog(t)
	for i := 0; i < 3; i++ {
		d.rereadConfig()
	}
	if strings.Contains(logged.String(), "changed on disk") {
		t.Errorf("a file nobody touched was reread:\n%s", logged.String())
	}
}

func TestAHalfWrittenConfigKeepsTheOneInUseAndSaysSoOnce(t *testing.T) {
	// Saving is not atomic in every editor, and a pass comes round every
	// couple of seconds: the file can be read between the truncate and the
	// write. Falling back to the defaults because somebody was mid-keystroke
	// would change every setting for as long as the save took -- and the
	// complaint is asked on every pass, so saying it every time fills the log
	// that would explain it.
	path := withConfigFile(t, `{"placement":"tab","hosts":[{"target":"bot"}]}`)
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	d := New(loaded)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	d.configStamp = info.ModTime()

	logged := captureLog(t)
	if err := os.WriteFile(path, []byte(`{"placement": "spl`), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		d.rereadConfig()
	}

	if got := d.config().Placement; got != "tab" {
		t.Errorf("a half-written file changed the placement in use to %q", got)
	}
	if n := strings.Count(logged.String(), "keeping the one in use"); n != 1 {
		t.Errorf("the complaint was made %d times, and a pass asks every couple "+
			"of seconds:\n%s", n, logged.String())
	}

	// And the finished save is picked up, rather than the daemon being stuck
	// on a complaint it already made.
	if err := os.WriteFile(path, []byte(`{"placement":"split","hosts":[{"target":"bot"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d.rereadConfig()
	if got := d.config().Placement; got != "split" {
		t.Errorf("the finished file says split and the daemon is using %q", got)
	}
}

func TestATerminalThatWillNotMirrorIsGivenUpOnAndCounted(t *testing.T) {
	// The whole chain from a pane that will not open to the number the menu
	// shows. Each link had a test and the joins had none: backOff is called
	// directly in one, the abandoned set is written by hand in another, and
	// the line that reaches backOff when opening a mirror fails -- which is
	// the only way a real terminal gets there -- was never once run.
	//
	// What it costs when it breaks is quiet: the machine goes on holding a
	// terminal that nothing here shows, and the listing says a smaller number
	// than the machine has with nothing to say why.
	withFakeHerdr(t)
	withRemoteHerdr(t)
	withConfigFile(t, `{"hosts":[{"target":"bot","mode":"attach"}]}`)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)

	// Herdr will not open a pane here, which is what a mirror needs.
	refuseOnMachine(t, os.Getenv(fakeHerdrState), "plugin pane open")

	logged := captureLog(t)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	// More passes than the limit, with the wait between attempts already
	// served. backOff schedules the next try for later, so a loop that does
	// not let time pass makes exactly one attempt and then skips the terminal
	// as backed off -- which is right, and is not what this is about.
	for i := 0; i < maxMirrorAttempts+3; i++ {
		d.reconcileAll()
		d.mu.Lock()
		if state := d.hosts["bot"]; state != nil {
			for terminalID := range state.retryAt {
				state.retryAt[terminalID] = time.Now().Add(-time.Minute)
			}
		}
		d.mu.Unlock()
	}

	said := logged.String()
	if !strings.Contains(said, "giving up on terminal") {
		t.Fatalf("a terminal that never mirrored was not given up on:\n%s", said)
	}
	// And it says what to do, since the terminal is still there on the machine.
	if !strings.Contains(said, "connect again to try mirroring it") {
		t.Errorf("giving up says nothing about what would try again:\n%s", said)
	}

	// The count the menu draws from. Without it the machine simply shows fewer
	// terminals than it has.
	d.mu.Lock()
	state := d.hosts["bot"]
	abandoned := len(state.abandoned)
	d.mu.Unlock()
	if abandoned == 0 {
		t.Fatal("nothing was recorded as given up on, so the listing counts none")
	}

	for _, info := range d.status() {
		if info.Target != "bot" {
			continue
		}
		if info.Unmirrored != abandoned {
			t.Errorf("%d terminals were given up on and the listing reports %d",
				abandoned, info.Unmirrored)
		}
		return
	}
	t.Fatal("the listing has no entry for the machine at all")
}

func TestMirroringIntoASpaceThisDidNotNameSaysSo(t *testing.T) {
	// The space on the machine is found by the name this gives it, or, failing
	// that, by a looser match that takes markers off the front. The loose one
	// keeps the terminals in a space made before remote_workspace_format
	// changed -- and it is also the one way into another hub's space, since
	// "☁  ☁laptop" comes down to "laptop" and answers to a hub of that name.
	//
	// The warning beside it needs two spaces sharing a name. This needs one,
	// so nothing said anything at all.
	here := withFakeHerdr(t)
	there, thereState := withRemoteHerdr(t)
	withConfigFile(t, `{"hosts":[{"target":"bot","mode":"attach"}]}`)

	// A space on the machine carrying a decorated form of this hub's name,
	// and none carrying the name this would give it.
	addWorkspaceOn(t, thereState, "wOther", "☁  ☁"+config.HubName())

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)

	logged := captureLog(t)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	said := logged.String()
	if !strings.Contains(said, "which is not") {
		t.Fatalf("a space this did not name was used and nothing said so:\n%s", said)
	}
	if !strings.Contains(said, "another hub's") {
		t.Errorf("the line does not say what it would mean:\n%s", said)
	}
	// Once, not on every pass.
	if n := strings.Count(said, "which is not"); n != 1 {
		t.Errorf("said %d times, and a pass comes round every couple of seconds", n)
	}
}

func TestMirroringIntoTheSpaceThisNamedSaysNothing(t *testing.T) {
	// The other half. The line above is about a space this did not name, and
	// the ordinary case is a space it did -- either one it made or one it
	// found under exactly that name. Saying it there would put a line in every
	// log on every machine, which is how a log stops being read.
	here := withFakeHerdr(t)
	there, thereState := withRemoteHerdr(t)
	withConfigFile(t, `{"hosts":[{"target":"bot","mode":"attach"}]}`)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)

	// A space already on the machine under exactly the name this gives it,
	// which is what a second connect meets. Without one there is nothing to
	// find, the space is created instead, and the line this is about is never
	// reached -- so the test would pass while checking nothing.
	addWorkspaceOn(t, thereState, "wMine", cfg.RemoteWorkspaceLabel())

	logged := captureLog(t)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	if !strings.Contains(logged.String(), "bot") {
		t.Fatalf("nothing happened for the machine at all:\n%s", logged.String())
	}
	if said := logged.String(); strings.Contains(said, "which is not") {
		t.Errorf("the space this named was reported as one it did not:\n%s", said)
	}
}

// TestDisconnectingClosesTheConnectionAsWell holds the other end of a machine's
// life.
//
// What disconnect visibly does is close the panes here and leave the work
// running there, and that half is tested from several directions. The
// connection itself is not visible: an SSH ControlMaster is a process holding
// a socket, and it lives until something tells it to exit. One left behind
// fails nothing and shows up nowhere -- it is a process and a socket per
// disconnect, for as long as the session lasts.
//
// TestTheOldConnectionIsTornDownWhenSettingsChange holds this same call on the
// settings-changed path, which happens when somebody edits a config. This one
// is the path somebody takes over and over: d in the menu to put a machine
// away, enter to bring it back.
func TestDisconnectingClosesTheConnectionAsWell(t *testing.T) {
	here := withFakeHerdr(t)
	there, _ := withRemoteHerdr(t)

	cfg := machineConfig("bot")
	cfg.Hosts[0].Mode = "attach"
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	settle(t, d, here, 2, there)

	// There is a connection to tear down, and connecting did not tear one down
	// on the way: without both of these the count below would prove nothing.
	if len(asked(t)) == 0 {
		t.Fatal("the machine was never asked anything, so there is no connection here")
	}
	if before := teardownsOf(t, "bot"); before != 0 {
		t.Fatalf("connecting tore down %d connections before the test began", before)
	}

	if reply := d.dispatch(Command{Cmd: "disconnect", Host: "bot"}); !reply.OK {
		t.Fatalf("disconnect: %s", reply.Message)
	}

	if got := teardownsOf(t, "bot"); got != 1 {
		t.Errorf("disconnecting tore down %d connections, want 1: the ControlMaster "+
			"is a process and a socket that nothing will close now", got)
	}
}
