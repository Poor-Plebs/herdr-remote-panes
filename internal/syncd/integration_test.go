package syncd

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/mirror"
)

// These drive the daemon against a stand-in for the Herdr CLI, so the paths
// that only exist as a conversation with Herdr -- opening a pane, finding the
// space it belongs to, working out what a restart left behind -- are exercised
// rather than described.
//
// The stand-in is a small program under testdata, built once by TestMain: no
// interpreter to depend on and the same behaviour on every platform.

// fakeHerdrState names the file the stand-in keeps its panes and spaces in.
const fakeHerdrState = "HRP_TEST_FAKE_HERDR_STATE"

// fakeHerdr is what the stand-in keeps: the panes and spaces Herdr is holding.
// It matches the shape written by testdata/fakeherdr.
type fakeHerdr struct {
	Panes      map[string]map[string]any `json:"panes"`
	Workspaces map[string]map[string]any `json:"workspaces"`
	Next       int                       `json:"next"`
	Focused    []string                  `json:"focused_spaces"`
	Calls      map[string]int            `json:"calls"`
}

// withFakeHerdr points the plugin at the stand-in and at an ssh that answers,
// and returns a look at what Herdr is holding.
func withFakeHerdr(t *testing.T) func() fakeHerdr {
	t.Helper()

	dir := t.TempDir()
	state := filepath.Join(dir, "herdr.json")
	t.Setenv(fakeHerdrState, state)
	t.Setenv("HERDR_BIN_PATH", fakeHerdrBin)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(dir, "plugin-state"))
	t.Setenv("HERDR_SESSION", "hub")

	// An ssh that answers, so a machine looks reachable without one being.
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() fakeHerdr {
		t.Helper()
		var held fakeHerdr
		raw, err := os.ReadFile(state)
		if err != nil {
			return held
		}
		if err := json.Unmarshal(raw, &held); err != nil {
			t.Fatalf("reading what Herdr is holding: %v", err)
		}
		return held
	}
}

// machineConfig is the plugin's defaults with one machine written into them.
func machineConfig(targets ...string) config.Config {
	cfg := config.Defaults()
	for _, target := range targets {
		cfg.Hosts = append(cfg.Hosts, config.Host{Target: target})
	}
	return cfg
}

// panesFor counts the panes Herdr is holding whose label names a machine.
func panesFor(held fakeHerdr, target string) int {
	n := 0
	for _, pane := range held.Panes {
		if label, _ := pane["label"].(string); strings.HasSuffix(label, "@"+target) {
			n++
		}
	}
	return n
}

func TestConnectingOpensATerminalInTheMachinesOwnSpace(t *testing.T) {
	held := withFakeHerdr(t)
	d := New(machineConfig("bot"))

	reply := d.dispatch(Command{Cmd: "connect", Host: "bot"})
	if !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}

	herdr := held()
	if got := panesFor(herdr, "bot"); got != 1 {
		t.Fatalf("connecting opened %d terminals, want 1: %+v", got, herdr.Panes)
	}
	// In a space of its own, named for the machine, so the sidebar says which
	// machine the terminal is on.
	var labels []string
	for _, workspace := range herdr.Workspaces {
		label, _ := workspace["label"].(string)
		labels = append(labels, label)
	}
	if len(labels) != 1 || !strings.Contains(labels[0], "bot") {
		t.Errorf("spaces are %q, want one named for the machine", labels)
	}
}

func TestARestartBringsBackTheTerminalsThatWereOpen(t *testing.T) {
	// The path a person hits most: Herdr restarts, and its own panes come back
	// as plain shells with nothing behind them. Those have to be recognised and
	// closed, and the terminals opened again -- and the count has to come out
	// the same, with nothing left over.
	//
	// It went untested because it is entirely a conversation with Herdr: what
	// was there, what is there now, and what that difference means.
	held := withFakeHerdr(t)
	cfg := machineConfig("bot")

	before := New(cfg)
	if reply := before.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	for i := 0; i < 2; i++ {
		if reply := before.dispatch(Command{Cmd: "open", Host: "bot"}); !reply.OK {
			t.Fatalf("open: %s", reply.Message)
		}
	}
	before.reconcileAll()
	terminalsAreRunning(t, held())
	before.persist()

	const want = 3
	herdrBefore := held()
	if got := panesFor(herdrBefore, "bot"); got != want {
		t.Fatalf("opened %d terminals, want %d", got, want)
	}
	// The panes as they were. Every one of them is a husk after the restart --
	// Herdr keeps the pane and drops what was running in it -- so every one has
	// to be gone by the end. Counting alone cannot tell three fresh terminals
	// from three dead ones, and a test that cannot tell those apart passes with
	// the recognition of husks switched off entirely.
	husks := map[string]bool{}
	for id := range herdrBefore.Panes {
		husks[id] = true
	}

	// A new daemon over the same state, which is what a restart is. The panes
	// from before are still in Herdr and nothing is behind them.
	// Their bridges died with Herdr, which is what makes them husks rather than
	// terminals: a pane whose bridge is running is somebody's live session.
	herdrRestarted(t)
	after := New(cfg)
	if reply := after.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	// Terminals come back one per pass, so give it more passes than it needs
	// and let it settle.
	for i := 0; i < want+3; i++ {
		after.reconcileAll()
		terminalsAreRunning(t, held())
	}

	herdr := held()
	if got := panesFor(herdr, "bot"); got != want {
		t.Errorf("after a restart there are %d terminals, want %d", got, want)
	}
	// Nothing left over: a husk that is neither closed nor reused is a dead
	// pane sitting in the sidebar for the rest of the session.
	if len(herdr.Panes) != want {
		t.Errorf("Herdr is holding %d panes for %d terminals: %+v",
			len(herdr.Panes), want, herdr.Panes)
	}
	for id := range herdr.Panes {
		if husks[id] {
			t.Errorf("pane %s survived the restart with nothing behind it", id)
		}
	}
}

func TestDisconnectingClosesTheTerminalsHere(t *testing.T) {
	held := withFakeHerdr(t)
	d := New(machineConfig("bot"))

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	if got := panesFor(held(), "bot"); got == 0 {
		t.Fatal("connecting opened no terminal")
	}

	if reply := d.dispatch(Command{Cmd: "disconnect", Host: "bot"}); !reply.OK {
		t.Fatalf("disconnect: %s", reply.Message)
	}
	d.reconcileAll()

	if got := panesFor(held(), "bot"); got != 0 {
		t.Errorf("%d terminals were left open after disconnecting", got)
	}
}

// fakeHerdrBin is the stand-in, built once for the whole package.
var fakeHerdrBin string

// TestMain builds the stand-in before any test needs it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hrp-fakeherdr")
	if err != nil {
		fmt.Fprintf(os.Stderr, "building the Herdr stand-in: %v\n", err)
		os.Exit(1)
	}
	fakeHerdrBin = filepath.Join(dir, "herdr")

	// Built rather than run as this test binary invoked again. That works, but
	// the tests run under the race detector and the stand-in does not need to:
	// it is a fixture, not code under test. Paying for the instrumentation on
	// every one of the hundreds of calls the tests make took this package from
	// two seconds to nearly three minutes.
	build := exec.Command("go", "build", "-o", fakeHerdrBin, "./testdata/fakeherdr")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the Herdr stand-in: %v\n%s", err, out)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// sshFails makes every ssh call fail with a given message, so a machine can be
// made unreachable without one being.
func sshFails(t *testing.T, message string) {
	t.Helper()
	dir := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	script := "#!/bin/sh\necho \"" + message + "\" >&2\nexit 255\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// closePaneByHand removes a pane from Herdr, as closing it in the sidebar does.
func closePaneByHand(t *testing.T, id string) {
	t.Helper()
	path := os.Getenv(fakeHerdrState)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var held fakeHerdr
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	delete(held.Panes, id)
	out, _ := json.Marshal(held)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClosingTheLastTerminalLeavesAWayBack(t *testing.T) {
	// Closing the last one is an ordinary thing to do, and it used to be a
	// dead end: the machine still counted as connected, so connecting again
	// answered that it already was and opened nothing. The menu then reported
	// a connection with nothing to show for it and no way back short of
	// editing the config.
	held := withFakeHerdr(t)
	d := New(machineConfig("bot"))

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()
	terminalsAreRunning(t, held())

	var pane string
	for id := range held().Panes {
		pane = id
	}
	if pane == "" {
		t.Fatal("connecting opened no terminal")
	}
	closePaneByHand(t, pane)
	for i := 0; i < 3; i++ {
		d.reconcileAll()
		terminalsAreRunning(t, held())
	}

	// Not reopened behind the user's back: closing a terminal means closing it.
	if got := len(held().Panes); got != 0 {
		t.Errorf("a terminal closed by hand came back: %d panes", got)
	}
	if hosts := d.dispatch(Command{Cmd: "status"}).Hosts; len(hosts) != 1 || hosts[0].Terminals != 0 {
		t.Errorf("status = %+v, want the machine with no terminals", hosts)
	}

	// And connecting again gives one back.
	reply := d.dispatch(Command{Cmd: "connect", Host: "bot"})
	if !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	if got := panesFor(held(), "bot"); got != 1 {
		t.Errorf("connecting again opened %d terminals, want 1", got)
	}
}

func TestAMachineThatIsNotAnsweringGetsNoSpaceAndNoTerminal(t *testing.T) {
	// Nothing is created for a machine that cannot be reached: an empty space
	// named after it would sit in the sidebar looking like somewhere to go.
	held := withFakeHerdr(t)
	sshFails(t, "ssh: connect to host bot port 22: Connection refused")

	d := New(machineConfig("bot"))
	reply := d.dispatch(Command{Cmd: "connect", Host: "bot"})
	if reply.OK {
		t.Fatal("connecting to a machine that refused was reported as working")
	}
	// Said in the machine's terms, not ssh's fifteen lines.
	if reply.Message != "connection refused" {
		t.Errorf("connect said %q", reply.Message)
	}
	d.reconcileAll()

	herdr := held()
	if len(herdr.Panes) != 0 || len(herdr.Workspaces) != 0 {
		t.Errorf("an unreachable machine left %d panes and %d spaces",
			len(herdr.Panes), len(herdr.Workspaces))
	}
	// It is still tracked, so it shows in the listing as unreachable rather
	// than vanishing.
	hosts := d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 || hosts[0].Connected {
		t.Fatalf("status = %+v, want the machine listed and not connected", hosts)
	}
	if hosts[0].LastError == "" {
		t.Error("the machine is listed with nothing said about why it is not connected")
	}
}

func TestEachMachineGetsItsOwnSpace(t *testing.T) {
	// The default: one space per machine, named for it, so the sidebar says
	// which machine a terminal is on.
	held := withFakeHerdr(t)
	d := New(machineConfig("bot", "prod"))

	for _, target := range []string{"bot", "prod"} {
		if reply := d.dispatch(Command{Cmd: "connect", Host: target}); !reply.OK {
			t.Fatalf("connect %s: %s", target, reply.Message)
		}
	}
	d.reconcileAll()

	herdr := held()
	spaces := map[string]string{}
	for id, workspace := range herdr.Workspaces {
		label, _ := workspace["label"].(string)
		spaces[id] = label
	}
	if len(spaces) != 2 {
		t.Fatalf("two machines produced %d spaces: %v", len(spaces), spaces)
	}
	for _, target := range []string{"bot", "prod"} {
		found := false
		for _, label := range spaces {
			if strings.Contains(label, target) {
				found = true
			}
		}
		if !found {
			t.Errorf("no space named for %q: %v", target, spaces)
		}
		if got := panesFor(herdr, target); got != 1 {
			t.Errorf("%s has %d terminals, want 1", target, got)
		}
	}

	// And each terminal is in its own machine's space, not pooled.
	for _, pane := range herdr.Panes {
		label, _ := pane["label"].(string)
		workspace, _ := pane["workspace_id"].(string)
		machine := strings.TrimPrefix(label[strings.Index(label, "@"):], "@")
		if !strings.Contains(spaces[workspace], machine) {
			t.Errorf("terminal %q is in the space named %q", label, spaces[workspace])
		}
	}
}

func TestAFlappingMachineIsLeftAloneRatherThanChurning(t *testing.T) {
	// A machine can fail in a way that lets a terminal open and then drops it
	// at once: a link that keeps going, or a machine under so much load that it
	// accepts a connection and then kills it.
	//
	// This is what the daemon already said it did about that -- "giving up
	// after N dropped terminals; connect again to retry" -- and could not. The
	// tally reset whenever a terminal was up, and a terminal that has just been
	// opened is up, so it never reached anything and such a machine had a pane
	// opened and shut every couple of seconds for as long as the session
	// lasted. Staying up is what counts as recovery now, not merely existing.
	restore := reopenSettled
	reopenSettled = time.Hour
	defer func() { reopenSettled = restore }()

	held := withFakeHerdr(t)
	d := New(machineConfig("bot"))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()
	terminalsAreRunning(t, held())

	dropped := "bot is not reachable over ssh: exit status 255: Connection reset by peer"

	// The first loss is made good at once: a dropped link is the ordinary case
	// and the terminal is where somebody was working.
	terminalDied(t, onlyPane(t, held()), dropped)
	d.reconcileAll()
	terminalsAreRunning(t, held())
	if got := panesFor(held(), "bot"); got != 1 {
		t.Fatalf("the first dropped terminal was not reopened: %d panes", got)
	}

	// Losing them one after another is different. Each round is what actually
	// happens: a terminal is opened, a pass sees it there, and then it dies.
	// That middle pass is the whole point -- it is the one that used to count
	// as the machine having recovered.
	opened := map[string]bool{}
	for i := 0; i < 20; i++ {
		d.reconcileAll() // opens one, if it is going to
		d.reconcileAll() // and this pass sees it there
		herdr := held()
		if len(herdr.Panes) == 0 {
			continue
		}
		pane := onlyPane(t, herdr)
		opened[pane] = true
		terminalDied(t, pane, dropped)
	}
	if len(opened) > maxHostAttempts+1 {
		t.Errorf("%d terminals were opened on a machine losing every one of them",
			len(opened))
	}

	// Left alone, and saying so, with the way back being the same as for any
	// other unreachable machine: pick it from the menu again.
	hosts := d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 || !hosts[0].GaveUp {
		t.Fatalf("status = %+v, want the machine left alone", hosts)
	}
	if !strings.Contains(hosts[0].LastError, "keep dropping") {
		t.Errorf("the machine reads as %q, which does not say what happened", hosts[0].LastError)
	}
}

// terminalDied is what happens when the ssh inside a pane stops: the bridge
// records why it went, and the pane goes with it.
//
// This is the signal the plugin actually runs on. A machine that stops
// answering is not noticed by asking it -- a plain SSH machine is never polled
// -- but by its terminals dying, so nothing about that path could be tested
// until the tests could make one die.
func terminalDied(t *testing.T, paneID, reason string) {
	t.Helper()
	mirror.MarkFailed(paneID, reason)
	closePaneByHand(t, paneID)
}

// onlyPane is the id of the single pane Herdr is holding.
func onlyPane(t *testing.T, held fakeHerdr) string {
	t.Helper()
	if len(held.Panes) != 1 {
		t.Fatalf("expected one pane, found %d: %+v", len(held.Panes), held.Panes)
	}
	for id := range held.Panes {
		return id
	}
	return ""
}

func TestATerminalThatDropsComesBack(t *testing.T) {
	// A connection that dropped can come good on its own, and the terminal is
	// where somebody was working, so it is opened again.
	held := withFakeHerdr(t)
	d := New(machineConfig("bot"))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()
	terminalsAreRunning(t, held())

	terminalDied(t, onlyPane(t, held()),
		"bot is not reachable over ssh: exit status 255: Connection reset by peer")
	for i := 0; i < 3; i++ {
		d.reconcileAll()
		terminalsAreRunning(t, held())
	}

	if got := panesFor(held(), "bot"); got != 1 {
		t.Errorf("a dropped terminal left %d open, want 1", got)
	}
	hosts := d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 || hosts[0].GaveUp {
		t.Errorf("status = %+v, want the machine still being tried", hosts)
	}
}

func TestATerminalThatCannotComeBackIsNotReopened(t *testing.T) {
	// Reopening was once the only way to find out why a terminal went, which
	// is the right guess for a dropped link and the wrong one for a changed
	// host key: the replacement fails in exactly the same way. Two terminals
	// flash open and shut, two more copies of a fifteen-line banner land in the
	// log, and only then does anything say what is actually wrong.
	held := withFakeHerdr(t)
	d := New(machineConfig("bot"))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()
	terminalsAreRunning(t, held())

	terminalDied(t, onlyPane(t, held()),
		"bot is not reachable over ssh: exit status 255: REMOTE HOST IDENTIFICATION HAS CHANGED")
	for i := 0; i < 4; i++ {
		d.reconcileAll()
		terminalsAreRunning(t, held())
	}

	if got := panesFor(held(), "bot"); got != 0 {
		t.Errorf("%d terminals were opened again on a machine whose key changed", got)
	}

	hosts := d.dispatch(Command{Cmd: "status"}).Hosts
	if len(hosts) != 1 {
		t.Fatalf("status = %+v", hosts)
	}
	if !hosts[0].GaveUp {
		t.Error("the machine is still being tried, and every attempt fails the same way")
	}
	if !strings.Contains(hosts[0].LastError, "known_hosts") {
		t.Errorf("the machine reads as %q, which does not say what to fix", hosts[0].LastError)
	}

	// The sidebar says so too, which is where somebody looks first.
	for _, workspace := range held().Workspaces {
		label, _ := workspace["label"].(string)
		if !strings.HasPrefix(label, "⚠") {
			t.Errorf("the machine's space is named %q, with nothing to say it is not answering", label)
		}
	}
}

func TestOpeningATerminalGoesToIt(t *testing.T) {
	// "New terminal on the machine whose space you are in, and go to it" is
	// what the manifest promises for this action. The focus asked for used to
	// stop short of the plain SSH path, so for the default mode -- which is
	// most people -- opening a terminal left you looking at somewhere else.
	held := withFakeHerdr(t)
	d := New(machineConfig("bot"))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}

	before := len(held().Panes)
	if reply := d.dispatch(Command{Cmd: "open", Host: "bot"}); !reply.OK {
		t.Fatalf("open: %s", reply.Message)
	}

	herdr := held()
	if len(herdr.Panes) != before+1 {
		t.Fatalf("open produced %d panes, want one more than %d", len(herdr.Panes), before)
	}
	// The newest pane is the one just opened.
	newest, highest := "", -1
	for id := range herdr.Panes {
		if n := paneNumber(id); n > highest {
			newest, highest = id, n
		}
	}
	if focused, _ := herdr.Panes[newest]["focused"].(bool); !focused {
		t.Errorf("the terminal that was opened was not focused: %+v", herdr.Panes[newest])
	}
}

// paneNumber is the counter at the end of a pane id the stand-in hands out, so
// the newest can be told from the rest. Herdr's ids are scoped to a space --
// "w1:p3" -- so only the digits after the last letter are the counter.
func paneNumber(id string) int {
	digits := 0
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] < '0' || id[i] > '9' {
			break
		}
		digits++
	}
	if digits == 0 {
		return -1
	}
	n := 0
	for _, r := range id[len(id)-digits:] {
		n = n*10 + int(r-'0')
	}
	return n
}

func TestNewTabIsATabEvenWhereTerminalsNormallySplit(t *testing.T) {
	// The manifest offers two actions because the answer is not always the
	// same: terminals land wherever the machine's placement says, and "new tab"
	// is how you get one somewhere else without changing that setting.
	held := withFakeHerdr(t)
	d := New(machineConfig("bot")) // placement defaults to split

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	first := held()
	if len(first.Panes) != 1 {
		t.Fatalf("connecting opened %d panes, want 1", len(first.Panes))
	}
	var firstTab string
	for _, pane := range first.Panes {
		firstTab, _ = pane["tab_id"].(string)
	}

	tabOf := func(before fakeHerdr) string {
		t.Helper()
		after := held()
		for id, pane := range after.Panes {
			if _, existed := before.Panes[id]; !existed {
				tab, _ := pane["tab_id"].(string)
				return tab
			}
		}
		t.Fatal("nothing was opened")
		return ""
	}

	t.Run("an ordinary new terminal splits", func(t *testing.T) {
		before := held()
		if reply := d.dispatch(Command{Cmd: "open", Host: "bot"}); !reply.OK {
			t.Fatalf("open: %s", reply.Message)
		}
		if got := tabOf(before); got != firstTab {
			t.Errorf("the new terminal is in tab %q, not %q: it did not split", got, firstTab)
		}
	})

	t.Run("but new tab is a tab", func(t *testing.T) {
		before := held()
		reply := d.dispatch(Command{Cmd: "open", Host: "bot", Placement: "tab"})
		if !reply.OK {
			t.Fatalf("open-tab: %s", reply.Message)
		}
		if got := tabOf(before); got == firstTab {
			t.Errorf("the new terminal shares tab %q: it split rather than making a tab", got)
		}
	})
}

func TestADisabledMachineIsLeftAlone(t *testing.T) {
	// "Skip it without removing it" is what the settings table promises, and
	// the point of the setting is to stop connecting to a machine you are not
	// using without losing the rest of what you wrote about it. A version of
	// this once did not disable anything.
	held := withFakeHerdr(t)
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{
		{Target: "bot"},
		{Target: "ci", Disabled: true},
	}
	d := New(cfg)

	// Reconnecting everything is the path that decides this. It brings back
	// what a machine had rather than opening anything new, so the check is
	// which machines it touched.
	reply := d.dispatch(Command{Cmd: "connect"})
	if !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	if strings.Contains(reply.Message, "ci") {
		t.Errorf("reconnecting reported %q, which includes the disabled machine", reply.Message)
	}
	d.reconcileAll()

	if got := panesFor(held(), "ci"); got != 0 {
		t.Errorf("the disabled machine got %d terminals", got)
	}
	// The one that is not disabled is reachable as usual.
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect bot: %s", reply.Message)
	}
	if got := panesFor(held(), "bot"); got != 1 {
		t.Errorf("the machine that is not disabled got %d terminals, want 1", got)
	}
	for _, workspace := range held().Workspaces {
		if label, _ := workspace["label"].(string); strings.Contains(label, "ci") {
			t.Errorf("the disabled machine was given a space named %q", label)
		}
	}
	for _, host := range d.dispatch(Command{Cmd: "status"}).Hosts {
		if host.Target == "ci" {
			t.Errorf("the disabled machine is being tracked: %+v", host)
		}
	}
}

func TestOneSpaceForEveryMachineWhenThatIsAsked(t *testing.T) {
	// "Put every machine in one space instead" -- for somebody who would rather
	// have one place with everything in it than a space per machine.
	held := withFakeHerdr(t)
	cfg := config.Defaults()
	cfg.Workspace = "remote"
	cfg.Hosts = []config.Host{{Target: "bot"}, {Target: "prod"}}
	d := New(cfg)

	for _, target := range []string{"bot", "prod"} {
		if reply := d.dispatch(Command{Cmd: "connect", Host: target}); !reply.OK {
			t.Fatalf("connect %s: %s", target, reply.Message)
		}
	}
	d.reconcileAll()

	herdr := held()
	if len(herdr.Workspaces) != 1 {
		labels := []string{}
		for _, workspace := range herdr.Workspaces {
			label, _ := workspace["label"].(string)
			labels = append(labels, label)
		}
		t.Fatalf("two machines produced %d spaces (%q), want one", len(herdr.Workspaces), labels)
	}
	// Named as given, rather than after a machine: it holds several.
	for _, workspace := range herdr.Workspaces {
		if label, _ := workspace["label"].(string); label != "remote" {
			t.Errorf("the shared space is named %q, want %q", label, "remote")
		}
	}
	// And both machines' terminals are in it.
	for _, target := range []string{"bot", "prod"} {
		if got := panesFor(herdr, target); got != 1 {
			t.Errorf("%s has %d terminals in the shared space, want 1", target, got)
		}
	}
}

func TestAMachinesLabelIsWhatItIsCalledHere(t *testing.T) {
	// "How it is named here" -- for a machine whose ssh target is an address or
	// a long name nobody wants to read in a sidebar.
	held := withFakeHerdr(t)
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Target: "10.0.0.7", Label: "build box"}}
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "10.0.0.7"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()

	herdr := held()
	for _, workspace := range herdr.Workspaces {
		label, _ := workspace["label"].(string)
		if !strings.Contains(label, "build box") {
			t.Errorf("the space is named %q, not after the label", label)
		}
	}
	for _, pane := range herdr.Panes {
		label, _ := pane["label"].(string)
		if !strings.HasSuffix(label, "@build box") {
			t.Errorf("the terminal is named %q, not after the label", label)
		}
	}
	// And it can be connected to by the name it is called, since that is what
	// the menu shows.
	if reply := d.dispatch(Command{Cmd: "disconnect", Host: "build box"}); !reply.OK {
		t.Errorf("disconnecting by label: %s", reply.Message)
	}
}

func TestPickingAMachineGoesToItEvenWhenItIsAlreadyThere(t *testing.T) {
	// "Picking a machine takes you to its space, whether it had just been
	// connected or was already there." The second half is the one that broke:
	// a machine already connected needed nothing done to it, so the pass that
	// would have learned where its space is never ran, and picking it from the
	// menu looked like the menu doing nothing.
	held := withFakeHerdr(t)
	cfg := machineConfig("bot")

	before := New(cfg)
	if reply := before.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	before.reconcileAll()
	before.persist()
	if len(held().Focused) == 0 {
		t.Fatal("connecting to a machine did not go to its space")
	}

	// A new daemon over the same state, which is what a restart is. The
	// machine's space is there and this knows nothing about it -- which is the
	// case that broke, and the usual one: most times somebody picks a machine
	// it is one that is already connected.
	after := New(cfg)
	if reply := after.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect again: %s", reply.Message)
	}

	focused := held().Focused
	if len(focused) == 0 {
		t.Fatal("nothing was ever focused")
	}
	// The space that is there, not a new one made to have somewhere to go.
	last := focused[len(focused)-1]
	spaces := held().Workspaces
	if len(spaces) != 1 {
		t.Fatalf("there are %d spaces, want the one that was already there", len(spaces))
	}
	if _, ok := spaces[last]; !ok {
		t.Errorf("went to space %q, which is not the machine's: %v", last, spaces)
	}
	if len(focused) < 2 {
		t.Error("picking a machine that was already connected went nowhere")
	}
}

func TestAPaneThatOpensOnItsOwnDoesNotTakeTheScreen(t *testing.T) {
	// "A pane that opens on its own -- a dropped link coming back, or a
	// terminal appearing on a mirrored machine -- never takes the screen from
	// you." Somebody is working somewhere else when a link comes back, and
	// being moved is worse than the terminal being a moment late.
	held := withFakeHerdr(t)
	d := New(machineConfig("bot"))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()

	before := len(held().Focused)
	terminalDied(t, onlyPane(t, held()),
		"bot is not reachable over ssh: exit status 255: Connection reset by peer")
	for i := 0; i < 3; i++ {
		d.reconcileAll()
	}

	herdr := held()
	if got := panesFor(herdr, "bot"); got != 1 {
		t.Fatalf("the dropped terminal was not reopened: %d panes", got)
	}
	if len(herdr.Focused) != before {
		t.Errorf("a terminal coming back on its own took the screen: focused %v", herdr.Focused)
	}
	for id, pane := range herdr.Panes {
		if focused, _ := pane["focused"].(bool); focused {
			t.Errorf("pane %s was opened with the focus, and nobody asked for it", id)
		}
	}
}

func TestOutsideAMachinesSpaceNewTerminalIsAnOrdinaryOne(t *testing.T) {
	// "Outside a machine's space both actions open an ordinary local pane or
	// tab, so they are safe to bind over your usual keys." Somebody who binds
	// their new-tab key to this has to get a tab wherever they are, or the
	// binding is worse than the one it replaced.
	held := withFakeHerdr(t)
	d := New(machineConfig("bot"))

	// A space that belongs to nothing, which is where most panes are.
	reply := d.dispatch(Command{Cmd: "open", Workspace: "w-elsewhere"})
	if !reply.OK {
		t.Fatalf("open outside a machine's space: %s", reply.Message)
	}
	if !strings.Contains(reply.Message, "local") {
		t.Errorf("open said %q, which does not read as an ordinary pane", reply.Message)
	}
	// Nothing was opened on a machine.
	if got := panesFor(held(), "bot"); got != 0 {
		t.Errorf("%d terminals were opened on a machine nobody was looking at", got)
	}

	tab := d.dispatch(Command{Cmd: "open", Workspace: "w-elsewhere", Placement: "tab"})
	if !tab.OK {
		t.Fatalf("open-tab outside a machine's space: %s", tab.Message)
	}
	if !strings.Contains(tab.Message, "tab") {
		t.Errorf("open-tab said %q, which does not read as an ordinary tab", tab.Message)
	}
}

func TestALocalPaneInAPlainSSHMachinesSpaceIsLeftAlone(t *testing.T) {
	// Herdr's own new-tab key and the plus icon open a local shell, and no
	// plugin can intercept them. On a mirrored machine such a pane is replaced
	// with a terminal on the machine. On a plain SSH one -- the default -- it
	// is not: the capture is skipped for those outright.
	//
	// Written down here because the README promised it in both modes, which is
	// most of what a promise like that costs: somebody presses their new-tab
	// key in a machine's space, gets a local shell that looks like it is on the
	// machine, and reads a document saying it will be corrected.
	//
	// Not a decision this test agrees with, only one it records. Making the
	// capture work here means closing a live SSH session on a judgement about
	// whose pane it is, and there is a window where a terminal just opened is
	// not yet claimed.
	held := withFakeHerdr(t)
	d := New(machineConfig("bot"))
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	d.reconcileAll()

	var workspace string
	for id := range held().Workspaces {
		workspace = id
	}
	stray := addLocalPane(t, workspace)

	for i := 0; i < 4; i++ {
		d.reconcileAll()
	}

	herdr := held()
	if _, there := herdr.Panes[stray]; !there {
		t.Error("a local pane in a plain SSH machine's space was closed; " +
			"if that is now wanted, the README and this test both need saying differently")
	}
	// And the machine's own terminal is untouched by any of it.
	if got := panesFor(herdr, "bot"); got != 1 {
		t.Errorf("the machine has %d terminals, want the one it started with", got)
	}
}

// addLocalPane puts a pane in a space without this plugin having opened it,
// which is what Herdr's own new-tab key and plus icon do.
func addLocalPane(t *testing.T, workspace string) string {
	t.Helper()
	path := os.Getenv(fakeHerdrState)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var held fakeHerdr
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	held.Panes["local-pane"] = map[string]any{
		"pane_id": "local-pane", "tab_id": "local-tab", "workspace_id": workspace,
		"terminal_id": "local-term", "label": "zsh",
	}
	out, _ := json.Marshal(held)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return "local-pane"
}

// terminalsAreRunning marks every plain SSH terminal here as having a live
// bridge. A plain SSH pane runs the same entrypoint a mirror does and leaves
// the same mark, so a test where none of them do is a test where every one of
// them looks like a pane Herdr restored with nothing behind it.
func terminalsAreRunning(t *testing.T, held fakeHerdr) {
	t.Helper()
	forgetGoneBridges(t, held)
	for id, pane := range held.Panes {
		label, _ := pane["label"].(string)
		if !strings.Contains(label, "@") {
			continue
		}
		terminal, _ := pane["terminal_id"].(string)
		mirrorIsRunning(t, id, terminal)
	}
}

// addLeftoverPane puts a pane into the local Herdr wearing a machine's name but
// with nothing behind it -- what a pane left by a previous session looks like.
func addLeftoverPane(t *testing.T, workspace, label string) string {
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
	held.Next++
	id := fmt.Sprintf("%s:p%d", workspace, held.Next)
	held.Panes[id] = map[string]any{
		"pane_id": id, "tab_id": workspace + "-tab", "workspace_id": workspace,
		"terminal_id": fmt.Sprintf("term_%d", held.Next), "label": label,
		"plugin": true,
	}
	out, _ := json.Marshal(held)
	if err := os.WriteFile(statePath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEveryLeftoverInASpaceGoesInTheSamePass(t *testing.T) {
	// Panes wearing a machine's name with nothing behind them are cleared when
	// the machine is adopted, and only then -- nothing revisits them.
	//
	// The loop that clears them walks the space's panes and takes each one it
	// closes out of the same slice it is walking, and that slice was filtered
	// in place: closing one shifted the next pane down into a position the
	// loop had already passed. So it cleared one, skipped the next, and looked
	// at the one after that twice. What was skipped stayed in the space for
	// good, wearing a live terminal's name with nothing behind it.
	held := withFakeHerdr(t)
	cfg := machineConfig("bot")

	// A space with the machine's name on it, and some panes in it.
	first := New(cfg)
	if reply := first.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}
	first.mu.Lock()
	workspace := first.hosts["bot"].workspaceID
	first.mu.Unlock()
	if workspace == "" {
		t.Fatal("the machine has no space")
	}

	// Three, because two is not enough to show it: with two, the one skipped is
	// the last, and a loop that stops early looks like a loop that works.
	leftovers := []string{
		addLeftoverPane(t, workspace, "alpha@bot"),
		addLeftoverPane(t, workspace, "beta@bot"),
		addLeftoverPane(t, workspace, "gamma@bot"),
	}

	// Every pane in the space is a husk once Herdr has restarted, the one the
	// connection opened included, so every one of them has to go. Naming only
	// the ones this test added would let the loop skip the other and pass:
	// which pane gets skipped depends on the order Herdr lists them in, and
	// that is not fixed.
	husks := map[string]bool{}
	for id, pane := range held().Panes {
		if ws, _ := pane["workspace_id"].(string); ws == workspace {
			husks[id] = true
		}
	}
	for _, id := range leftovers {
		if !husks[id] {
			t.Fatalf("leftover %s is not in the machine's space", id)
		}
	}

	// A daemon that has never seen this machine, meeting a space full of names
	// it does not recognise. Nothing is behind any of them.
	herdrRestarted(t)
	adopting := New(cfg)
	if reply := adopting.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	adopting.reconcileAll()

	after := held()
	var survived []string
	for id := range husks {
		if _, still := after.Panes[id]; still {
			survived = append(survived, id)
		}
	}
	sort.Strings(survived)
	if len(survived) > 0 {
		t.Errorf("%d of %d husks are still there: %v -- nothing will revisit them",
			len(survived), len(husks), survived)
	}
}

// withSSHConfig writes a ~/.ssh/config for the test and points HOME at it.
func withSSHConfig(t *testing.T, contents string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
}

func TestALineOfSomebodyElsesOutputIsNotAMachine(t *testing.T) {
	// connect with no machine named falls back to whatever text was selected in
	// the terminal, which is how a line of someone else's output becomes an
	// argument to ssh. A name nobody wrote down therefore has to look like a
	// name, and a line with a space in it is a sentence.
	withFakeHerdr(t)
	withSSHConfig(t, "Host bot\n")
	d := New(config.Defaults())

	for _, selected := range []string{
		"error: could not reach the build server",
		"see https://example.com/docs for help",
		"bot and prod",
	} {
		reply := d.dispatch(Command{Cmd: "connect", Host: selected})
		if reply.OK {
			t.Errorf("connected to %q, which is a line of output rather than a machine", selected)
		}
	}
}

func TestAMachineYouWroteDownIsTakenAtYourWord(t *testing.T) {
	// The other half, and the reason the two checks are held apart: ssh allows
	// `Host "my server"` and connects to it as `ssh "my server"`. Refusing a
	// space outright meant such a machine was read correctly out of the file
	// and then dropped without a word.
	//
	// The space is safe on its own account: the target is one element of an
	// argument list and never goes near a shell. What it means is that a name
	// nobody declared is probably not a name -- and this one was declared.
	withFakeHerdr(t)
	withSSHConfig(t, "Host \"my server\"\nHost bot\n")
	d := New(config.Defaults())

	if reply := d.dispatch(Command{Cmd: "connect", Host: "my server"}); !reply.OK {
		t.Errorf("refused a machine written down in ~/.ssh/config: %s", reply.Message)
	}
}

func TestATargetThatIsAnInstructionIsRefusedHoweverItArrived(t *testing.T) {
	// ssh takes options on the command line and -oProxyCommand=... runs one, so
	// this is not a machine whoever wrote it down. Writing it in ~/.ssh/config
	// buys no leniency here: the lesser check still refuses it.
	withFakeHerdr(t)
	withSSHConfig(t, "Host -oProxyCommand=touch /tmp/hrp-should-not-exist\n")
	d := New(config.Defaults())

	for _, target := range []string{
		"-oProxyCommand=touch /tmp/hrp-should-not-exist",
		"-F/dev/null",
		"bot\x1b[31m",
	} {
		reply := d.dispatch(Command{Cmd: "connect", Host: target})
		if reply.OK {
			t.Errorf("connected to %q, which ssh would read as an instruction", target)
		}
	}
	if _, err := os.Stat("/tmp/hrp-should-not-exist"); err == nil {
		t.Fatal("a ProxyCommand ran")
	}
}

func TestTheWarningAboutTwoMachinesSharingANameReachesYou(t *testing.T) {
	// Reporting it inside the config package is only half of it: what decides
	// whether anybody finds out is whether it comes back with status, which is
	// what the menu draws its warning line from.
	withFakeHerdr(t)
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{
		{Target: "bot", Label: "build"},
		{Target: "ci", Label: "build"},
	}
	d := New(cfg)

	reply := d.dispatch(Command{Cmd: "status"})
	if !reply.OK {
		t.Fatalf("status: %s", reply.Message)
	}
	if reply.Warning == "" {
		t.Fatal("status carries no warning, so nothing in the menu will say the two machines collide")
	}
	for _, want := range []string{"bot", "ci", "build"} {
		if !strings.Contains(reply.Warning, want) {
			t.Errorf("the warning does not name %q: %q", want, reply.Warning)
		}
	}

	// And a config with nothing wrong with it says nothing, or the line becomes
	// furniture that nobody reads.
	quiet := New(machineConfig("bot"))
	if reply := quiet.dispatch(Command{Cmd: "status"}); reply.Warning != "" {
		t.Errorf("an ordinary config produced a warning: %q", reply.Warning)
	}
}

func TestPollingAndCommandsAtTheSameTimeIsSafe(t *testing.T) {
	// This is the shape the daemon actually runs in: a poll every couple of
	// seconds, and commands from the menu arriving whenever somebody presses
	// something. They are not serialised — reconcileOnce takes the lock only
	// to copy the list of machines and lets go of it before it talks to Herdr —
	// so a connect can be halfway through while a poll is walking the same
	// machine's panes.
	//
	// The concurrency tests before this one built a Daemon by hand and pointed
	// the race detector at one map. This runs the real paths against the
	// stand-in, which is where a fault would actually be.
	// SKIPPED, and the reason is a real fault rather than a flaky test.
	//
	// The reconcile path locks correctly: each host's goroutine holds d.mu for
	// its whole body. The command path does not -- openRemotePane and
	// ensureRemotePresence take d.mu only to look the machine up, release it,
	// and then work on the *hostSync unlocked. So a command from the menu and a
	// poll touch the same machine's fields at the same time. The race detector
	// finds it in under a second with this test:
	//
	//   state.workspaceID  written by ensureWorkspace, read by markWorkspaceState
	//   state.labels       written by forgetPane, read and written by retitle
	//
	// The second is worse than stale data: concurrent read and write of a Go
	// map is a runtime throw, which takes the daemon down and every machine
	// with it.
	//
	// Not fixed here because the fix is not local. Hoisting d.mu to the two
	// command entry points means removing the five nested locks below them --
	// openShellPane holds two, rememberPlacement another -- and Go's mutexes do
	// not nest. Getting that wrong deadlocks the daemon, which freezes the menu
	// and every machine for good rather than intermittently. That is a worse
	// failure than the one being fixed, and the call belongs to whoever owns
	// this.
	//
	// Remove the skip to reproduce.
	held := withFakeHerdr(t)
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Target: "bot"}, {Target: "prod"}, {Target: "ci"}}
	d := New(cfg)

	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect: %s", reply.Message)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// The poll, running as fast as it can rather than every two seconds.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				d.reconcileAll()
			}
		}
	}()

	// The menu: status and the machine list, which is what the picker asks for
	// while somebody is looking at it.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				d.dispatch(Command{Cmd: "status"})
			}
		}
	}()

	// Somebody pressing things: connecting, opening terminals, disconnecting,
	// and asking for a refresh. Every one of these changes what the poll is
	// walking while it walks it.
	commands := []Command{
		{Cmd: "connect", Host: "bot"},
		{Cmd: "open", Host: "bot"},
		{Cmd: "connect", Host: "prod"},
		{Cmd: "refresh"},
		{Cmd: "open", Host: "prod", Placement: "tab"},
		{Cmd: "disconnect", Host: "bot"},
		{Cmd: "connect", Host: "ci"},
		{Cmd: "set-mode", Host: "ci", Mode: "ssh"},
		{Cmd: "disconnect", Host: "prod"},
	}
	for i := 0; i < 3; i++ {
		for _, cmd := range commands {
			// The reply is not checked: a command racing a disconnect can
			// legitimately fail. What is being tested is that it answers at all
			// rather than deadlocking, panicking, or tearing the state.
			d.dispatch(cmd)
		}
	}

	close(stop)
	wg.Wait()

	// Still working afterwards, and still coherent.
	//
	// Not "every pane belongs to a machine that is still connected": the last
	// commands above disconnect two of them, and a pane outliving its machine's
	// entry by a moment is the ordinary way that happens rather than damage.
	// What must hold is that nothing is left wearing a name that was never a
	// machine here, which is what torn state would look like.
	status := d.status()
	if len(status) == 0 {
		t.Fatal("no machines left at all")
	}
	configured := map[string]bool{"bot": true, "prod": true, "ci": true}
	for id, pane := range held().Panes {
		label, _ := pane["label"].(string)
		at := strings.LastIndex(label, "@")
		if at < 0 {
			continue
		}
		if machine := label[at+1:]; !configured[machine] {
			t.Errorf("pane %s is named for %q, which was never one of these machines", id, machine)
		}
	}

	// And it still answers, which is the other half of what a lock can get
	// wrong: a deadlock does not race, it simply stops.
	done := make(chan Reply, 1)
	go func() { done <- d.dispatch(Command{Cmd: "status"}) }()
	select {
	case reply := <-done:
		if !reply.OK {
			t.Errorf("status after all that: %s", reply.Message)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon stopped answering: something is holding the lock")
	}
}

// TestPollingAndCommandsUnderRealContention is the test above turned up until
// it hurts, and left out of an ordinary run because of how long it takes.
//
//	HRP_STRESS=1 go test ./internal/syncd/ -race -run UnderRealContention -count=20
//
// The difference that matters is not the number of iterations: it is that the
// commands run concurrently with each other rather than one after another.
// Two goroutines connecting and disconnecting the same machine at the same
// moment is the interleaving that a sequential loop can never produce, and it
// is the one where a lock taken for the lookup and released for the work goes
// wrong.
func TestPollingAndCommandsUnderRealContention(t *testing.T) {
	if os.Getenv("HRP_STRESS") == "" {
		t.Skip("set HRP_STRESS=1 to run; it takes minutes rather than a moment")
	}
	held := withFakeHerdr(t)
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{
		{Target: "bot"}, {Target: "prod"}, {Target: "ci"}, {Target: "staging"},
	}
	d := New(cfg)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	deadline := time.After(8 * time.Second)

	// Pollers. reconcileAll is meant to run one at a time whatever calls it,
	// so several of them is also a test of the guard that ensures that.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					d.reconcileAll()
				}
			}
		}()
	}

	// Readers: the menu, redrawing while somebody looks at it.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					d.dispatch(Command{Cmd: "status"})
					d.status()
				}
			}
		}()
	}

	// Writers, all at once and all over each other. Each takes a machine of
	// its own to start with and then reaches for the others, so the same
	// machine is being connected by one goroutine while another disconnects it.
	machines := []string{"bot", "prod", "ci", "staging"}
	for i, machine := range machines {
		wg.Add(1)
		go func(i int, mine string) {
			defer wg.Done()
			others := machines
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				theirs := others[(i+n)%len(others)]
				switch n % 6 {
				case 0:
					d.dispatch(Command{Cmd: "connect", Host: mine})
				case 1:
					d.dispatch(Command{Cmd: "open", Host: mine})
				case 2:
					d.dispatch(Command{Cmd: "open", Host: theirs, Placement: "tab"})
				case 3:
					d.dispatch(Command{Cmd: "set-mode", Host: mine, Mode: "ssh"})
				case 4:
					d.dispatch(Command{Cmd: "refresh"})
				default:
					d.dispatch(Command{Cmd: "disconnect", Host: theirs})
				}
			}
		}(i, machine)
	}

	<-deadline
	close(stop)

	// Everything has to come back. A deadlock does not race, it stops, and a
	// stopped goroutine here looks exactly like a slow one until it never ends.
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(60 * time.Second):
		t.Fatal("goroutines did not finish: something is holding the lock")
	}

	// And it still answers.
	answered := make(chan Reply, 1)
	go func() { answered <- d.dispatch(Command{Cmd: "status"}) }()
	select {
	case reply := <-answered:
		if !reply.OK {
			t.Errorf("status after all that: %s", reply.Message)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon stopped answering")
	}

	// How much actually happened, so "it passed" can be read against a size
	// rather than taken on trust. A run that deadlocked early would pass every
	// assertion below on almost no work at all.
	total := 0
	for _, n := range held().Calls {
		total += n
	}
	t.Logf("%d calls to Herdr across %d goroutines", total, 10)
	if total < 500 {
		t.Errorf("only %d calls: this did not stress anything", total)
	}

	// Nothing wearing a name that was never a machine here, which is what torn
	// bookkeeping would leave behind.
	configured := map[string]bool{"bot": true, "prod": true, "ci": true, "staging": true}
	for id, pane := range held().Panes {
		label, _ := pane["label"].(string)
		at := strings.LastIndex(label, "@")
		if at < 0 {
			continue
		}
		if machine := label[at+1:]; !configured[machine] {
			t.Errorf("pane %s is named for %q, which was never one of these machines", id, machine)
		}
	}
}

// withSlowMachine replaces ssh with one that takes its time for a named
// destination and answers at once for every other.
func withSlowMachine(t *testing.T, slow string, delay time.Duration) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do case \"$a\" in " + slow + ") sleep " +
		strings.TrimSuffix(delay.String(), "s") + ";; esac; done\n" +
		"exit 0\n"
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestConnectingToASlowMachineDoesNotFreezeTheMenu(t *testing.T) {
	// Connecting talks to the machine before it takes the daemon's lock, and
	// only takes it to write down what it found. That ordering is the whole
	// reason the menu stays usable while somebody connects to a machine that is
	// not answering — an ssh to a blackholed address takes ten seconds to fail
	// and there are several of them.
	//
	// It is also exactly what a well-meaning fix for a data race would undo:
	// the two entry points beside this one had their locks hoisted to cover
	// their whole bodies, and doing the same here would trade one bug for a
	// menu that hangs whenever a machine is slow.
	withFakeHerdr(t)
	withSlowMachine(t, "slowbox", 2*time.Second)

	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Target: "bot"}, {Target: "slowbox"}}
	d := New(cfg)
	if reply := d.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("connect bot: %s", reply.Message)
	}

	connecting := make(chan struct{})
	go func() {
		defer close(connecting)
		d.dispatch(Command{Cmd: "connect", Host: "slowbox"})
	}()
	// Long enough to be inside the slow machine's ssh.
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	reply := d.dispatch(Command{Cmd: "status"})
	took := time.Since(start)

	if !reply.OK {
		t.Errorf("status while connecting to a slow machine: %s", reply.Message)
	}
	// Generous, because this measures a machine under load rather than a
	// deadline. Held against the second the slow machine still has to run: the
	// point is that the menu is not waiting for it.
	if took > time.Second {
		t.Errorf("status took %s while a machine was being connected to; "+
			"the menu is waiting for ssh", took.Round(time.Millisecond))
	}
	<-connecting
}

// withBrokenHerdr replaces the Herdr CLI with one that refuses everything the
// way Herdr does when its server is going away.
func withBrokenHerdr(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\n" +
		"echo '{\"error\":{\"code\":\"server_unavailable\"," +
		"\"message\":\"server is shutting down\"},\"id\":\"cli:fake\"}'\n" +
		"exit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", bin)
}

func TestAFailureIsNotDescribedTwiceInOneLine(t *testing.T) {
	// Every error out of the Herdr CLI already names the command it was:
	// "herdr pane list: server_unavailable: server is shutting down". A caller
	// that puts "local pane list:" in front of that says pane list twice and
	// adds nothing, which is how a log line grows to a paragraph while saying
	// one thing.
	//
	// What a caller is for is the part the error cannot know: what it cost.
	withFakeHerdr(t)
	withBrokenHerdr(t)

	var logged strings.Builder
	saved := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(saved) })

	d := New(machineConfig("bot"))
	d.hosts["bot"] = newTestHost()
	d.reconcileAll()

	out := logged.String()
	if out == "" {
		t.Fatal("a Herdr that refuses everything was not worth a word")
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Count(line, "pane list") > 1 {
			t.Errorf("the line names the same command twice: %q", line)
		}
	}
	// And it still says what actually went wrong.
	if !strings.Contains(out, "shutting down") {
		t.Errorf("the log does not say what Herdr said: %q", out)
	}
}
