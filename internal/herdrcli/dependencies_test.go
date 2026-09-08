package herdrcli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// packageStrings is every string literal handed to a call that runs a Herdr
// command, anywhere under internal/.
//
// Derived from the CALL SITES rather than from a list of packages, because a
// list of packages is a thing to keep correct and this one was wrong twice. It
// read this package alone while internal/mirror ran two commands over ssh --
// `terminal session observe` and `terminal attach`, the whole mirroring half.
// Widened to those two, it was still wrong: internal/syncd runs four more from
// the daemon, `tab create` and three `workspace` commands among them. Both
// times the list was fixed and the question was not.
//
// Two sources, and both are needed. A file that calls Run, RunJSON or Argv is
// a file that runs Herdr commands, and its literals are the words and flags it
// asks for -- the whole file, because mirror and syncd build the argv as a
// slice several lines above the call. This package calls none of them: it
// builds argv slices for its callers to send, so its own literals are where
// its commands are written. Taking only the call sites loses `notification
// show` and the agent commands; taking only this package loses everything
// syncd and mirror run.
//
// Neither source picks up ssh's options or this plugin's own usage text, which
// a sweep of every "--" string in the tree would.
func packageStrings(t *testing.T) []string {
	t.Helper()

	runners := map[string]bool{"Run": true, "RunJSON": true, "Argv": true}
	found := ownStrings(t)
	files := 0
	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Not the root of the walk, whose own name is "..", which begins
			// with a dot and would prune everything before it started. The
			// denominator below said so on the first run.
			if name := info.Name(); path != ".." &&
				(strings.HasPrefix(name, ".") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		files++
		// A file that runs a Herdr command, and then every literal in it --
		// not only the arguments at the call. Both mirror and syncd build the
		// argv as a slice first and hand it on, so the words are in a
		// composite literal several lines above the call that sends them.
		runsHerdr := false
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && runners[sel.Sel.Name] {
					runsHerdr = true
				}
			}
			return true
		})
		if !runsHerdr {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if value, err := strconv.Unquote(lit.Value); err == nil {
				found = append(found, value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if files < 10 {
		t.Fatalf("only %d source files were read under internal/, so this is "+
			"looking at almost nothing", files)
	}
	if len(found) == 0 {
		t.Fatal("no Herdr command was found being run, so this checks nothing")
	}
	return found
}

// ownStrings is every string literal in this package's own code, where its argv
// builders spell the commands out.
func ownStrings(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// Not the list itself. Reading it back in makes every word in it a
		// word "this plugin says", so the list would vouch for itself.
		if name == "dependencies.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if value, err := strconv.Unquote(lit.Value); err == nil {
				found = append(found, value)
			}
			return true
		})
	}
	return found
}

func TestEveryFlagThisSendsIsOneMakeHerdrChecks(t *testing.T) {
	// The list exists to be checked against the Herdr on the machine, and a
	// flag missing from it is a flag nothing asks about -- which is the state
	// this was written to leave. Read from the source rather than kept beside
	// it: a second copy of a list is a copy that drifts, and the one that
	// drifts silently is always the one nothing runs.
	listed := map[string]bool{}
	for _, dep := range Dependencies {
		for _, flag := range dep.Flags {
			listed[flag] = true
		}
	}

	for _, value := range packageStrings(t) {
		if !strings.HasPrefix(value, "--") || value == "--" {
			// A bare "--" is the separator that keeps a machine's name from
			// being read as an option, not a flag anything declares.
			continue
		}
		// Herdr's own two, which belong to no command: this plugin runs
		// `herdr --version` to see what a machine has, and `make herdr` asks
		// every command with `--help`. Both are exercised by the checker
		// itself on every run -- it cannot ask a command anything without
		// them -- so a Dependencies entry would be a second, weaker copy.
		if value == "--version" || value == "--help" {
			continue
		}
		if !listed[value] {
			t.Errorf("this package sends %q and no entry in Dependencies names it, "+
				"so `make herdr` never asks Herdr whether it still takes it", value)
		}
	}
}

func TestEveryCommandInTheListIsOneThisSends(t *testing.T) {
	// The other way round. A command that stops being sent should leave the
	// list, or `make herdr` reports on a Herdr surface this no longer uses --
	// and a check that fails for something that does not matter is a check
	// people learn to ignore.
	said := map[string]bool{}
	for _, value := range packageStrings(t) {
		said[value] = true
	}
	for _, dep := range Dependencies {
		for _, word := range dep.Command {
			if !said[word] {
				t.Errorf("Dependencies lists %q, and this package never says %q",
					strings.Join(dep.Command, " "), word)
			}
		}
	}
	if len(Dependencies) == 0 {
		t.Fatal("nothing is listed, so make herdr checks nothing")
	}
}

// statesChecked is the values `make herdr` asks Herdr to still accept for
// pane report-agent --state.
func statesChecked(t *testing.T) map[string]bool {
	t.Helper()
	for _, dep := range Dependencies {
		if len(dep.Command) == 2 && dep.Command[1] == "report-agent" {
			listed := map[string]bool{}
			for _, value := range dep.Values["--state"] {
				listed[value] = true
			}
			return listed
		}
	}
	t.Fatal("pane report-agent is not listed, so nothing checks the states it sends")
	return nil
}

func TestEveryStateThisReportsIsOneMakeHerdrChecks(t *testing.T) {
	// AgentState decides what goes on the wire, and the list decides what is
	// asked about. A state this can produce and the list does not name is one
	// Herdr is never asked about -- and Herdr restricts this flag, so a state
	// it does not take is a report that fails rather than one it ignores.
	listed := statesChecked(t)

	// What Herdr can report, from the AgentStatus enum in its own API schema,
	// and then some things it cannot: the mapping has a default and the far
	// side is a machine that may be running a different version.
	for _, status := range []string{
		"idle", "working", "blocked", "done", "unknown",
		"", "DONE", "thinking", "idle ", "unknown\n", "🙂",
	} {
		if got := AgentState(status); !listed[got] {
			t.Errorf("a pane reporting %q is reported on as %q, which no entry "+
				"in Dependencies names: %v", status, got, listed)
		}
	}
}

func FuzzEveryStateThisReportsIsOneMakeHerdrChecks(f *testing.F) {
	for _, seed := range []string{"idle", "done", "", "working", "\x00", "unknown"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, status string) {
		// The status comes off another machine, so the table above is a
		// sample and this is the property: whatever arrives, what leaves is
		// something Herdr was asked about.
		listed := statesChecked(t)
		if got := AgentState(status); !listed[got] {
			t.Fatalf("a pane reporting %q is reported on as %q, which no entry "+
				"in Dependencies names", status, got)
		}
	})
}
