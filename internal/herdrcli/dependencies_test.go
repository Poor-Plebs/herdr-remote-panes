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

// packageStrings is every string literal in this package's own code, which is
// where the commands and flags it sends to Herdr are written.
func packageStrings(t *testing.T) []string {
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
		// word "this package says", so the list vouches for itself: a command
		// nothing sends is found in the line that lists it, and the check
		// passes for exactly the entry it was meant to catch. It did.
		if name == "dependencies.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err == nil {
				found = append(found, value)
			}
			return true
		})
	}
	if len(found) == 0 {
		t.Fatal("no string literals found in this package, so this checks nothing")
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
		if !strings.HasPrefix(value, "--") {
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
