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
	"syscall"
)

// fakeHerdr is the whole of the Herdr CLI this plugin needs, over a JSON file.
type fakeHerdr struct {
	Panes      map[string]map[string]any `json:"panes"`
	Workspaces map[string]map[string]any `json:"workspaces"`
	Next       int                       `json:"next"`
	// Focused is every space that was brought to the front, in order.
	Focused []string `json:"focused_spaces"`
	// Calls counts what was asked for, by the verb rather than the whole
	// command. Several things this plugin does are promises not to make a call
	// -- not to rename a pane that already carries the name, not to report an
	// agent that has not changed -- and the outcome of doing the work twice
	// looks exactly like the outcome of doing it once. Only the count differs,
	// and at one poll every two seconds that difference is the whole cost.
	Calls map[string]int `json:"calls"`
}

func (f *fakeHerdr) id(prefix string) string {
	f.Next++
	return fmt.Sprintf("%s%d", prefix, f.Next)
}

// paneID is a pane's id as Herdr writes one: scoped to its space, with a colon
// in it. A stand-in handing out bare "p3" leaves everything that takes an id
// apart, or puts one in a filename, working on a shape Herdr never produces.
func (f *fakeHerdr) paneID(workspace string) string {
	f.Next++
	if workspace == "" {
		return fmt.Sprintf("p%d", f.Next)
	}
	return fmt.Sprintf("%s:p%d", workspace, f.Next)
}

// lockState takes an exclusive lock held for the whole of one call, and returns
// the release. The lock is its own file so that renaming the state into place
// does not pull the lock out from under anybody waiting on it.
func lockState(path string) func() {
	if path == "" {
		return func() {}
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return func() {}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

// main answers one CLI call and exits, as the real binary would.
func main() {
	args := os.Args[1:]
	path := os.Getenv("HRP_TEST_FAKE_HERDR_STATE")

	// One call at a time, across processes. The daemon lets go of its lock
	// before it talks to Herdr, so a poll and a command from the menu reach the
	// CLI at the same moment -- and every call here is read the file, change
	// it, write it back. Without this the later write simply loses whatever the
	// earlier one did, and a test of two things happening at once would be
	// testing this file instead of the daemon. Real Herdr is one process with
	// one copy of the state; this is the nearest a file gets to that.
	unlock := lockState(path)
	// Released by hand in ok and fail below, which end the process rather than
	// returning. Exiting would release it anyway when the descriptor closes,
	// but a lock whose release depends on that reads like a lock nobody holds.
	defer unlock()

	state := fakeHerdr{
		Panes:      map[string]map[string]any{},
		Workspaces: map[string]map[string]any{},
	}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	// Written whole and moved into place, so a reader never sees half a file:
	// os.WriteFile truncates first, and anything reading in that moment gets
	// nothing at all.
	save := func() {
		raw, _ := json.Marshal(state)
		tmp := path + ".writing"
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			return
		}
		_ = os.Rename(tmp, path)
	}
	ok := func(result any) {
		out, _ := json.Marshal(map[string]any{"result": result, "id": "cli:fake"})
		unlock()
		fmt.Println(string(out))
		os.Exit(0)
	}
	fail := func(code, message string) {
		out, _ := json.Marshal(map[string]any{
			"error": map[string]string{"code": code, "message": message}, "id": "cli:fake"})
		unlock()
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

	// Counted before anything is decided, so a refusal counts too: a call that
	// fails is still a call that was made.
	if state.Calls == nil {
		state.Calls = map[string]int{}
	}
	verb := join
	if n := len(args); n > 2 {
		verb = strings.Join(args[:2], " ")
		if args[0] == "plugin" && n > 3 {
			verb = strings.Join(args[:3], " ")
		}
	}
	state.Calls[verb]++
	save()

	// A verb the test wants refused. Herdr can say no to any call -- a pane
	// that went away between the listing and the request, a session that shut
	// down mid-poll -- and what the plugin does about each refusal is a branch
	// that nothing could otherwise reach, because this stand-in succeeds at
	// everything it understands. Named verbs, comma separated, each optionally
	// with the code to refuse with:
	//
	//	tab create
	//	pane split:pane_not_found,tab create
	//
	// Read from a file beside the state rather than from the environment, so
	// that it is scoped to one machine the same way the state is: both ends of
	// a mirroring test run this same program, and a machine refusing a call is
	// not this side refusing it too. An environment variable reached both, and
	// threading a second one through the ssh stand-in's shell depended on how
	// that shell treats an assignment in front of eval -- which differs, and
	// differed between here and CI.
	//
	// Counted above rather than below, because a refused call is still a call
	// the plugin made, and a test about giving up needs to see how often it
	// tried.
	refusals, _ := os.ReadFile(path + ".refuse")
	for _, spec := range strings.Split(string(refusals), ",") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		code := "internal_error"
		if at := strings.LastIndex(spec, ":"); at >= 0 {
			spec, code = strings.TrimSpace(spec[:at]), strings.TrimSpace(spec[at+1:])
		}
		if spec == verb {
			fail(code, "refused: the test named "+verb+" in this machine's .refuse file")
		}
	}

	switch {
	case join == "pane list":
		ok(map[string]any{"panes": values(state.Panes)})

	case join == "workspace list":
		ok(map[string]any{"workspaces": values(state.Workspaces)})

	case strings.HasPrefix(join, "workspace create"):
		// A new space comes with a tab and a shell in it, as Herdr's does. The
		// plugin retires that shell once it has put something of its own there,
		// so a stand-in that made an empty space left that path unexercised and
		// the mirroring path with nowhere for its first pane to come from.
		id := state.id("w")
		state.Workspaces[id] = map[string]any{"workspace_id": id, "label": flag("--label")}
		tab := state.id("t")
		root := state.paneID(id)
		state.Panes[root] = map[string]any{
			"pane_id": root, "tab_id": tab, "workspace_id": id,
			"terminal_id": state.id("term_"), "label": "",
			// A shell sets its own title as a matter of course, and that is
			// what a mirror of it is named after. Leaving it empty left every
			// name in these tests coming from the last fallback there is.
			"terminal_title_stripped": "you@laptop:~", "cwd": "/home/you",
		}
		save()
		ok(map[string]any{"workspace": state.Workspaces[id], "root_pane": state.Panes[root]})

	case strings.HasPrefix(join, "workspace "):
		id := args[2]
		if _, live := state.Workspaces[id]; !live {
			fail("workspace_not_found", "workspace "+id+" not found")
		}
		switch args[1] {
		case "rename":
			state.Workspaces[id]["label"] = args[3]
			save()
		case "report-metadata":
			// The sidebar marker. Which token a space carries is the whole of
			// what somebody sees at a glance about whether the machine behind
			// it is answering, and dropping it here left the choice between
			// the two of them with nothing watching.
			tokens, _ := state.Workspaces[id]["tokens"].(map[string]any)
			if tokens == nil {
				tokens = map[string]any{}
			}
			// Cleared first and set after, which is the order the arguments
			// mean: a call clears the token for the other state and sets its
			// own, and doing it the other way round would clear what it just
			// set if a caller ever named the same one twice.
			for i, a := range args {
				if a == "--clear-token" && i+1 < len(args) {
					delete(tokens, args[i+1])
				}
			}
			for i, a := range args {
				if a == "--token" && i+1 < len(args) {
					name, value, _ := strings.Cut(args[i+1], "=")
					tokens[name] = value
				}
			}
			state.Workspaces[id]["tokens"] = tokens
			save()
		case "focus":
			// Recorded because taking the screen, and not taking it, are both
			// promises: picking a machine goes to it, and a pane opening on its
			// own does not.
			state.Focused = append(state.Focused, id)
			save()
		}
		ok(map[string]any{"workspace_id": id})

	case join == "tab list":
		// Derived from the panes rather than kept separately, so a tab cannot
		// outlive what is in it or go missing while something still is.
		//
		// Two things here are not what Herdr does, checked against a recording
		// from 0.8.2 in internal/herdrcli/testdata. Tab ids there are scoped
		// to their space -- "w1:t1" -- where these are bare, and numbering is
		// per space, so a real listing has "w1:t1" and "w2:t1" both numbered
		// 1 while these are numbered straight through.
		//
		// Left alone deliberately. The plugin takes a tab id as an opaque
		// string, and reads the number only to order panes within one space,
		// where numbering per space and numbering throughout give the same
		// order. The one difference real data makes is a tie between spaces
		// under scope "all", and a tie falls back to the pane id, which was
		// tried against these shapes and is stable.
		//
		// It would start to matter if anything here read a space out of a tab
		// id, or compared numbers across spaces. Either is a reason to make
		// these scoped, as the pane ids already are.
		seen := map[string]bool{}
		var ids []string
		for _, pane := range values(state.Panes) {
			id, _ := pane["tab_id"].(string)
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		tabs := make([]map[string]any, 0, len(ids))
		for i, id := range ids {
			tabs = append(tabs, map[string]any{"tab_id": id, "number": i + 1})
		}
		ok(map[string]any{"tabs": tabs})

	case strings.HasPrefix(join, "tab create"):
		// As with a space: a new tab comes with a shell in it.
		workspace := flag("--workspace")
		if workspace != "" {
			if _, live := state.Workspaces[workspace]; !live {
				fail("workspace_not_found", "workspace "+workspace+" not found")
			}
		}
		tab := state.id("t")
		root := state.paneID(workspace)
		state.Panes[root] = map[string]any{
			"pane_id": root, "tab_id": tab, "workspace_id": workspace,
			"terminal_id": state.id("term_"), "label": "",
			"terminal_title_stripped": "you@laptop:~", "cwd": "/home/you",
		}
		save()
		ok(map[string]any{
			"tab":       map[string]any{"tab_id": tab, "workspace_id": workspace},
			"root_pane": state.Panes[root],
		})

	case strings.HasPrefix(join, "plugin pane open"):
		workspace := flag("--workspace")
		target := flag("--target-pane")
		direction := flag("--direction")

		// Herdr falls back to the placement the manifest declares for the
		// entrypoint, so a request that sends no --placement is not placed
		// "wherever": the mirror pane becomes a tab and the picker a popup.
		//
		// This used to be inferred from --target-pane instead, which meant a
		// --placement that stopped being sent altogether looked exactly like
		// one that was. A stand-in simpler than the real thing does not leave
		// a gap; it manufactures agreement.
		placement := flag("--placement")
		if placement == "" {
			switch flag("--entrypoint") {
			case "picker":
				placement = "popup"
			default:
				placement = "tab"
			}
		}

		// The combinations Herdr refuses outright, with its own messages. A
		// plugin that sends the wrong pair gets invalid_params and no pane,
		// which is not something a test should have to discover in production.
		switch placement {
		case "overlay", "popup":
			if workspace != "" || target != "" || direction != "" {
				fail("invalid_params", "overlay and popup plugin panes target the active pane")
			}
		case "split", "zoomed":
			if workspace != "" {
				fail("invalid_params",
					"split and zoomed plugin panes target an existing pane; use target_pane_id")
			}
		case "tab":
			if target != "" || direction != "" {
				fail("invalid_params",
					"tab plugin panes support workspace_id but not target_pane_id or direction")
			}
		default:
			fail("invalid_params", "unknown placement "+placement)
		}

		tab := state.id("t")
		// A split or a zoom lands on its target, in that pane's space and tab.
		if target != "" {
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
		// What the pane is told. This is the whole of the interface between the
		// daemon and the process in the pane -- which machine, which terminal,
		// which mode, whether it may take over a stale attach -- and none of it
		// was recorded, so a setting that stopped reaching the pane looked
		// exactly like one that arrived.
		env := map[string]any{}
		for i, a := range args {
			if a != "--env" || i+1 >= len(args) {
				continue
			}
			if name, value, found := strings.Cut(args[i+1], "="); found {
				env[name] = value
			}
		}

		id := state.paneID(workspace)
		state.Panes[id] = map[string]any{
			// Marked as this plugin's, because Herdr will not let a plugin
			// close a pane it does not own -- and a stand-in that closes
			// anything for anybody cannot tell a caller using the wrong one of
			// the two close commands.
			"plugin":  true,
			"pane_id": id, "tab_id": tab, "workspace_id": workspace,
			"terminal_id": state.id("term_"), "label": "", "focused": focused,
			"env": env,
		}
		save()
		ok(map[string]any{"plugin_pane": map[string]any{"pane": state.Panes[id]}})

	case strings.HasPrefix(join, "plugin pane close"), strings.HasPrefix(join, "pane close"):
		id := args[len(args)-1]
		pane, live := state.Panes[id]
		if !live {
			fail("pane_not_found", "pane "+id+" not found")
		}
		if strings.HasPrefix(join, "plugin pane close") {
			if mine, _ := pane["plugin"].(bool); !mine {
				fail("not_a_plugin_pane", "pane "+id+" was not opened by a plugin")
			}
		}
		delete(state.Panes, id)
		save()
		ok(map[string]any{"pane_id": id})

	case strings.HasPrefix(join, "pane report-agent"), strings.HasPrefix(join, "pane release-agent"):
		// What the sidebar would show for this pane. Recorded rather than
		// discarded, because an agent running on another machine appearing here
		// under its own name and state is the whole of what this reports.
		id := args[2]
		pane, live := state.Panes[id]
		if !live {
			fail("pane_not_found", "pane "+id+" not found")
		}
		if args[1] == "release-agent" {
			delete(pane, "agent")
			delete(pane, "agent_status")
		} else {
			pane["agent"] = flag("--agent")
			pane["agent_status"] = flag("--state")
		}
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
