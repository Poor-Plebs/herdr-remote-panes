package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryCommandTheDocsGiveStillWorks holds the commands in the
// documentation to the tree they are meant to be run against.
//
// A test name that no longer exists is the worst kind of stale: `go test -run
// NoSuchTest` prints "no tests to run" and exits nought, so somebody following
// the instructions is told everything is fine by a command that did nothing.
// The same for a make target, which is at least loud about it.
func TestEveryCommandTheDocsGiveStillWorks(t *testing.T) {
	inRoot(t)

	docs := []string{"README.md"}
	pages, err := filepath.Glob(filepath.Join("docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	docs = append(docs, pages...)

	// Every test the tests know about, by name.
	out, err := exec.Command("go", "test", "-list", ".*", "./...").Output()
	if err != nil {
		t.Fatalf("listing tests: %v", err)
	}
	known := strings.Split(string(out), "\n")

	runs := regexp.MustCompile(`-run ([A-Za-z][A-Za-z0-9_]*)`)
	fuzzes := regexp.MustCompile(`-fuzz ([A-Za-z][A-Za-z0-9_]*)`)
	targets := regexp.MustCompile(`(?m)^\s*make ([a-z-]+)`)

	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, page := range docs {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)

		for _, m := range append(runs.FindAllStringSubmatch(text, -1),
			fuzzes.FindAllStringSubmatch(text, -1)...) {
			pattern := m[1]
			if pattern == "XXX" {
				// The idiom for running no tests, which is how a fuzz target
				// is run on its own. Naming nothing is the point of it.
				continue
			}
			checked++
			found := false
			for _, name := range known {
				if strings.Contains(strings.TrimSpace(name), pattern) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s tells somebody to run %q and no test matches it; "+
					"that command says \"no tests to run\" and exits nought",
					page, pattern)
			}
		}

		for _, m := range targets.FindAllStringSubmatch(text, -1) {
			checked++
			if !strings.Contains(string(makefile), "\n"+m[1]+":") {
				t.Errorf("%s tells somebody to run `make %s`, which the Makefile "+
					"does not have", page, m[1])
			}
		}
	}
	if checked < 4 {
		t.Fatalf("found %d commands in the documentation, which is fewer than "+
			"there are -- the patterns have stopped matching", checked)
	}
}
