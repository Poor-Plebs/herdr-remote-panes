package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
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
	quoted := regexp.MustCompile("`make ([a-z-]+)[^`]*`")
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

	checked, optional, targeted := 0, 0, 0
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

		// Both the commands a page sets out on a line of its own and the ones it
		// names mid-sentence. NEITHER PATTERN IS THE OTHER'S SUPERSET, which is
		// why both are read: of the five targets the pages give, `make bounds`
		// is only ever described in a paragraph and `make mutants` only ever
		// shown in a code block, so each pattern alone sees four. Renaming
		// bounds in the Makefile SURVIVED the whole gate while renaming herdr,
		// beside it in the same block, was caught.
		//
		// Backticks are what tell a command from ordinary English here, the same
		// rule tools/herdrcheck reads messages by. Matching `make (\w+)`
		// anywhere on a line instead would report "no input can make them" and
		// "machine can make this write", both of which are in these pages.
		named := map[string]bool{}
		for _, m := range append(targets.FindAllStringSubmatch(text, -1),
			quoted.FindAllStringSubmatch(text, -1)...) {
			named[m[1]] = true
		}
		for target := range named {
			checked++
			targeted++
			if !strings.Contains(string(makefile), "\n"+target+":") {
				t.Errorf("%s tells somebody to run `make %s`, which the Makefile "+
					"does not have", page, target)
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
	if targeted < 5 {
		t.Fatalf("found %d make targets in the documentation, which is fewer than "+
			"there are; the prose ones are the easy half to stop matching", targeted)
	}
}

// TestEveryPlacementThePluginSendsIsAccountedFor holds the --placement values
// written as literals, which is the sender internal/syncd's own check cannot
// see.
//
// That one holds every placement planPaneTarget produces against
// herdrcli.Dependencies, which is the list `make herdr` asks Herdr about. It
// covers the mirror path and nothing else: internal/cli's menu calls
// herdrcli.Run directly with "--placement", "popup", so a second sender exists
// outside the chain that is held, and a placement changed there would reach
// Herdr with nothing having looked at it.
//
// popup used to be excepted here, because `make herdr` checked declared values
// against Herdr's `--help`, which lists four placements and omits it. That
// exception is gone: the check asks Herdr's bundled schema now, whose
// PluginPanePlacement declares overlay, popup, split, tab and zoomed, so popup
// is simply declared in Dependencies like any other value. An exception that
// existed because a check read the wrong source is worth removing when the
// check learns to read the right one.
func TestEveryPlacementThePluginSendsIsAccountedFor(t *testing.T) {
	inRoot(t)

	accounted := map[string]string{}
	for _, dep := range herdrcli.Dependencies {
		if len(dep.Command) == 3 && dep.Command[2] == "open" {
			for _, value := range dep.Values["--placement"] {
				accounted[value] = "declared in herdrcli.Dependencies, where `make herdr` checks it"
			}
		}
	}
	if len(accounted) == 0 {
		t.Fatal("no placements are declared for `plugin pane open`, so this holds nothing")
	}

	sends := regexp.MustCompile(`"--placement",\s*"([a-zA-Z]+)"`)
	found := 0
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); path != "." && (strings.HasPrefix(name, ".") ||
				name == "bin" || name == "vendor" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range sends.FindAllStringSubmatch(string(source), -1) {
			found++
			if why, ok := accounted[m[1]]; !ok {
				t.Errorf("%s sends --placement %q, and nothing accounts for it. Either "+
					"add it to herdrcli.Dependencies, where `make herdr` will check it "+
					"against Herdr, or account for it here with what you measured against "+
					"the binary. Accounted for now: %v", path, m[1], accounted)
			} else if why == "" {
				t.Errorf("%s sends --placement %q with an empty reason", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// Self-verifying: the walk has to have found the menu's literal, or a
	// renamed flag would leave this passing over a tree it read nothing in.
	if found == 0 {
		t.Fatal("no --placement literal was found in the tree, so this holds nothing: " +
			"the flag has been renamed, or it is no longer written as a literal")
	}
}
