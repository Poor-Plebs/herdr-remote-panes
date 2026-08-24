// Command fakeherdr stands in for the Herdr CLI while the daemon is tested.
//
// A program of its own rather than the test binary invoked again, because the
// tests run under the race detector and the stand-in does not need to: it is a
// fixture, not code under test, and paying for instrumentation on every one of
// the hundreds of calls a test makes took the package from two seconds to three
// minutes.
//
// It lives under testdata, so the go tool leaves it out of builds and vets of
// the module proper. The tests build it once.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// fakeHerdr is the whole of the Herdr CLI this plugin needs, over a JSON file.
type fakeHerdr struct {
	Panes      map[string]map[string]any `json:"panes"`
	Workspaces map[string]map[string]any `json:"workspaces"`
	Next       int                       `json:"next"`
	// Focused is every space that was brought to the front, in order.
	Focused []string `json:"focused_spaces"`
}

func (f *fakeHerdr) id(prefix string) string {
	f.Next++
	return fmt.Sprintf("%s%d", prefix, f.Next)
}

// main answers one CLI call and exits, as the real binary would.
func main() {
	args := os.Args[1:]
	path := os.Getenv("HRP_TEST_FAKE_HERDR_STATE")
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
		switch args[1] {
		case "rename":
			state.Workspaces[id]["label"] = args[3]
			save()
		case "focus":
			// Recorded because taking the screen, and not taking it, are both
			// promises: picking a machine goes to it, and a pane opening on its
			// own does not.
			state.Focused = append(state.Focused, id)
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
		// Whether the pane was asked for with the focus is recorded, because
		// "open a terminal on this machine and go to it" is a promise the
		// manifest makes and one that has been broken before.
		focused := false
		for _, a := range args {
			if a == "--focus" {
				focused = true
			}
		}
		id := state.id("p")
		state.Panes[id] = map[string]any{
			"pane_id": id, "tab_id": tab, "workspace_id": workspace,
			"terminal_id": state.id("term_"), "label": "", "focused": focused,
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
