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
