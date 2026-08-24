package syncd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"os/exec"
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
