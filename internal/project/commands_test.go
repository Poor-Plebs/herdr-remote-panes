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
// The same for a make target, which is at least loud about it. And the same
// again for the HRP_* variable some of these commands set, which is why that is
// checked here too: a test left gated on a name the pages no longer set just
// skips, and a skip prints "ok" and exits nought exactly like naming no test.
//
// HONEST LIMIT on that half: it asks whether the PACKAGE the command names has
// a test reading the variable, not whether the test the -run pattern selects
// does -- HRP_UPGRADE_FROM is read by a helper rather than by the test the page
// names, so per-function matching would report the page and be wrong. Tying the
// two together properly means running the command, and these are opt-in
// precisely because they sleep and stress the machine; putting them in the gate
// would add that load beside a timing test that has already failed CI twice.
func TestEveryCommandTheDocsGiveStillWorks(t *testing.T) {
	inRoot(t)

	// The names of tests come from `go test -list`, a subprocess, so nothing
	// the tool records ties this result to the tests it is about: renaming one
	// the docs name left this passing from cache.
	cacheDependsOnTheTree(t, ".", func(name string) bool {
		return strings.HasSuffix(name, "_test.go")
	})

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
	optIns := regexp.MustCompile(`\b(HRP_[A-Z0-9_]+)=`)
	packages := regexp.MustCompile(`\./[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*/`)

	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}

	// Every test file in the tree, so that a command naming no package can
	// still be answered rather than reported for having nothing beside it.
	var testFiles []string
	if err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, "_test.go") {
			testFiles = append(testFiles, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	checked, optional := 0, 0
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

		// The variable in front of a command is its other half, and a test that
		// SKIPS is exactly as quiet as one that was never selected: `go test`
		// prints "ok" and exits nought either way. Renaming HRP_TIMING,
		// HRP_STRESS or HRP_UPGRADE_FROM in the tests that read them left every
		// page still naming the old one, with the whole gate green -- measured,
		// all three survived.
		for _, line := range strings.Split(text, "\n") {
			m := optIns.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			optional++
			variable, where := m[1], "the tree"
			dir := ""
			if p := packages.FindString(line); p != "" {
				dir = filepath.Clean(p)
				where = dir
			}
			read, looked := false, 0
			for _, file := range testFiles {
				if dir != "" && filepath.Dir(file) != dir {
					continue
				}
				looked++
				body, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(body), `"`+variable+`"`) {
					read = true
					break
				}
			}
			switch {
			case looked == 0:
				t.Errorf("%s runs something in %s, which has no test files at all",
					page, where)
			case !read:
				t.Errorf("%s tells somebody to set %s to run something in %s, and "+
					"no test there reads it; that command skips, prints \"ok\" and "+
					"exits nought, which is the same silence as naming no test at all",
					page, variable, where)
			}
		}
	}
	if checked < 4 {
		t.Fatalf("found %d commands in the documentation, which is fewer than "+
			"there are -- the patterns have stopped matching", checked)
	}
	if optional < 3 {
		t.Fatalf("found %d opt-in variables in the documentation, which is fewer "+
			"than there are -- the pattern has stopped matching", optional)
	}
}
