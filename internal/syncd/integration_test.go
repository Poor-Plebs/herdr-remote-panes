package syncd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
)

// These drive the daemon against a stand-in for the Herdr CLI, so the paths
// that only exist as a conversation with Herdr -- opening a pane, finding the
// space it belongs to, working out what a restart left behind -- are exercised
// rather than described.
//
// The stand-in is this test binary run again with fakeHerdrEnv set, which is
// the usual way to get a helper process in Go: no interpreter to depend on, no
// second binary to build, and the same behaviour on every platform.

const (
	fakeHerdrEnv   = "HRP_TEST_FAKE_HERDR"
	fakeHerdrState = "HRP_TEST_FAKE_HERDR_STATE"
)

// fakeHerdr is the whole of the Herdr CLI this plugin needs, over a JSON file.
type fakeHerdr struct {
	Panes      map[string]map[string]any `json:"panes"`
	Workspaces map[string]map[string]any `json:"workspaces"`
	Next       int                       `json:"next"`
}

func (f *fakeHerdr) id(prefix string) string {
	f.Next++
	return fmt.Sprintf("%s%d", prefix, f.Next)
}

// runFakeHerdr answers one CLI call and exits, as the real binary would.
func runFakeHerdr(args []string) {
	path := os.Getenv(fakeHerdrState)
	state := fakeHerdr{
		Panes:      map[string]map[string]any{},
		Workspaces: map[string]map[string]any{},
	}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	save := func() {
		raw, _ := json.Marshal(state)
		_ = os.WriteFile(path, raw, 0o600)
	}
	ok := func(result any) {
		out, _ := json.Marshal(map[string]any{"result": result, "id": "cli:fake"})
		fmt.Println(string(out))
		os.Exit(0)
	}
	fail := func(code, message string) {
		out, _ := json.Marshal(map[string]any{
			"error": map[string]string{"code": code, "message": message}, "id": "cli:fake"})
		fmt.Println(string(out))
		os.Exit(1)
	}
	flag := func(name string) string {
		for i, a := range args {
			if a == name && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	values := func(m map[string]map[string]any) []map[string]any {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		// Sorted, so a listing does not come back in a different order each
		// time and make a test depend on map iteration.
		sort.Strings(keys)
		out := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, m[k])
		}
		return out
	}

	join := strings.Join(args, " ")
	switch {
	case join == "pane list":
		ok(map[string]any{"panes": values(state.Panes)})

	case join == "workspace list":
		ok(map[string]any{"workspaces": values(state.Workspaces)})

	case strings.HasPrefix(join, "workspace create"):
		id := state.id("w")
		state.Workspaces[id] = map[string]any{"workspace_id": id, "label": flag("--label")}
		save()
		ok(map[string]any{"workspace": state.Workspaces[id]})

	case strings.HasPrefix(join, "workspace "):
		id := args[2]
		if _, live := state.Workspaces[id]; !live {
			fail("workspace_not_found", "workspace "+id+" not found")
		}
		if args[1] == "rename" {
			state.Workspaces[id]["label"] = args[3]
			save()
		}
		ok(map[string]any{"workspace_id": id})

	case strings.HasPrefix(join, "plugin pane open"):
		workspace := flag("--workspace")
		tab := state.id("t")
		// A split lands beside its target, in that pane's space and tab, and
		// carries no --workspace of its own.
		if target := flag("--target-pane"); target != "" {
			pane, live := state.Panes[target]
			if !live {
				fail("pane_not_found", "pane "+target+" not found")
			}
			workspace, _ = pane["workspace_id"].(string)
			tab, _ = pane["tab_id"].(string)
		}
		if workspace != "" {
			if _, live := state.Workspaces[workspace]; !live {
				fail("workspace_not_found", "workspace "+workspace+" not found")
			}
		}
		id := state.id("p")
		state.Panes[id] = map[string]any{
			"pane_id": id, "tab_id": tab, "workspace_id": workspace,
			"terminal_id": state.id("term_"), "label": "",
		}
		save()
		ok(map[string]any{"plugin_pane": map[string]any{"pane": state.Panes[id]}})

	case strings.HasPrefix(join, "plugin pane close"), strings.HasPrefix(join, "pane close"):
		id := args[len(args)-1]
		if _, live := state.Panes[id]; !live {
			fail("pane_not_found", "pane "+id+" not found")
		}
		delete(state.Panes, id)
		save()
		ok(map[string]any{"pane_id": id})

	case strings.HasPrefix(join, "pane rename"):
		id := args[2]
		if _, live := state.Panes[id]; !live {
			fail("pane_not_found", "pane "+id+" not found")
		}
		state.Panes[id]["label"] = args[3]
		save()
		ok(map[string]any{"pane_id": id})

	default:
		ok(map[string]any{})
	}
}

// withFakeHerdr points the plugin at the stand-in and at an ssh that answers,
// and returns a look at what Herdr is holding.
func withFakeHerdr(t *testing.T) func() fakeHerdr {
	t.Helper()

	dir := t.TempDir()
	state := filepath.Join(dir, "herdr.json")
	t.Setenv(fakeHerdrEnv, "1")
	t.Setenv(fakeHerdrState, state)
	t.Setenv("HERDR_BIN_PATH", os.Args[0])
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

// TestMain lets this binary stand in for the Herdr CLI when asked to.
func TestMain(m *testing.M) {
	if os.Getenv(fakeHerdrEnv) != "" && len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-test.") {
		runFakeHerdr(os.Args[1:])
		return
	}
	os.Exit(m.Run())
}
