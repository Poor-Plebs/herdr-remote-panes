package syncd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	after := New(cfg)
	if reply := after.dispatch(Command{Cmd: "connect", Host: "bot"}); !reply.OK {
		t.Fatalf("reconnect: %s", reply.Message)
	}
	// Terminals come back one per pass, so give it more passes than it needs
	// and let it settle.
	for i := 0; i < want+3; i++ {
		after.reconcileAll()
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

	dropped := "bot is not reachable over ssh: exit status 255: Connection reset by peer"

	// The first loss is made good at once: a dropped link is the ordinary case
	// and the terminal is where somebody was working.
	terminalDied(t, onlyPane(t, held()), dropped)
	d.reconcileAll()
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

	terminalDied(t, onlyPane(t, held()),
		"bot is not reachable over ssh: exit status 255: Connection reset by peer")
	for i := 0; i < 3; i++ {
		d.reconcileAll()
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

	terminalDied(t, onlyPane(t, held()),
		"bot is not reachable over ssh: exit status 255: REMOTE HOST IDENTIFICATION HAS CHANGED")
	for i := 0; i < 4; i++ {
		d.reconcileAll()
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
