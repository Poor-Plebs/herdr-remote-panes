package project

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inRoot puts a test in the top of the repository for as long as it runs.
//
// These tests read files by the names they have at the top -- README.md,
// build.sh, "." meaning the whole tree -- and a test runs in the directory of
// its own package. Going there rather than joining a prefix onto every path
// keeps those names as they read in the repository.
func inRoot(t *testing.T) string {
	t.Helper()
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return root
}

func TestTheTopOfTheRepositoryIsFoundFromInsideIt(t *testing.T) {
	// Everything else here depends on this, and a wrong answer would not look
	// like a wrong answer: the tests would read no files, find nothing to
	// complain about, and pass.
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go.mod", "README.md", "build.sh", "herdr-plugin.toml"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("the top of the repository was reported as %s, which has no %s: %v",
				root, name, err)
		}
	}
}

// cacheDependsOnTheTree makes a cached result stop applying once the tree it
// was about has changed.
//
// The go tool decides whether a cached result still holds from the files the
// test itself opened. These tests reach their subject through a subprocess --
// `go test -list` for the names of tests, a build of the daemon for the
// upgrade -- and nothing a subprocess reads is recorded. So editing the daemon
// and running the upgrade test by hand answers:
//
//	ok  github.com/Poor-Plebs/herdr-remote-panes/internal/project  (cached)
//
// which is a pass reporting on the code as it was before the edit. It happened
// twice in one afternoon, once while deliberately breaking the socket handover
// to check that the test would notice, which it did not appear to.
//
// Reading the files here is what tells the tool they matter. `make check` is
// not affected, because -shuffle=on is not a cacheable flag and disables the
// cache for the whole run -- but that is a property of how it is invoked, not
// of these tests, and it does not help anyone running one by hand.
func cacheDependsOnTheTree(t *testing.T, root string, wanted func(name string) bool) {
	t.Helper()

	read := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			// Not part of what any of this is about, and .git is large enough
			// that walking it is the slowest thing here by far.
			case ".git", "bin", "notes", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !wanted(entry.Name()) {
			return nil
		}
		// Opened rather than stat'd: both are recorded, and opening is the one
		// that says the contents were the input.
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		read++
		return f.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	if read == 0 {
		t.Fatal("no files were read, so a cached result would outlive every change " +
			"this test is about")
	}
}

// goSource is every Go file, which is what a build of the daemon depends on.
func goSource(name string) bool { return strings.HasSuffix(name, ".go") }

// TestRootSaysWhereItLookedFrom holds the thing that function's own comment
// asks for and did not get.
//
// The comment beside the failure says "Worth saying where it started: a test
// failing here is not about what it tests" -- and the message said "above the
// working directory", naming nothing. The reason is visible in the loop: dir
// is walked up to the filesystem root before the search gives up, so by the
// time there is something to say, the only value to hand is "/", which is the
// one directory the answer is certainly not in.
//
// Held from a directory with no go.mod anywhere above it, which is the only
// way this branch is reached at all.
func TestRootSaysWhereItLookedFrom(t *testing.T) {
	start := t.TempDir()
	t.Chdir(start)

	_, err := Root()
	if err == nil {
		t.Fatal("a directory with no go.mod above it should not find a root")
	}
	said := err.Error()

	if !strings.Contains(said, start) {
		t.Errorf("the failure does not say where it looked from, which is the whole "+
			"point of saying anything: looked from %s, said %q", start, said)
	}
	// And not the filesystem root, which is where the walk ends up and what
	// naming the loop variable would have printed.
	if strings.Contains(said, "above /,") || strings.Contains(said, "above / ") {
		t.Errorf("the failure names the filesystem root rather than the starting "+
			"directory: %q", said)
	}
	// It still says what could not be done, or naming the directory has
	// replaced the point rather than added to it.
	if !strings.Contains(said, "top of the") {
		t.Errorf("the failure no longer says what it failed to find: %q", said)
	}
}

// TestTheDocumentationIsEveryPageAndTheREADMEFirst holds what DocPages names.
//
// Five packages read the documentation through this to check that a phrase in
// the prose agrees with what the code does. Which page carries the phrase is
// the thing they are deliberately not about, so returning fewer pages than
// there are would not fail any of them: the phrase would simply be missing,
// and a test looking for it would say the docs no longer make the claim.
func TestTheDocumentationIsEveryPageAndTheREADMEFirst(t *testing.T) {
	inRoot(t)

	pages, err := DocPages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 2 {
		t.Fatalf("DocPages returned %d pages; there is a README and a docs/", len(pages))
	}
	if filepath.Base(pages[0]) != "README.md" {
		t.Errorf("the first page is %q, want the README", filepath.Base(pages[0]))
	}

	under, err := filepath.Glob(filepath.Join("docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != len(under)+1 {
		t.Errorf("DocPages returned %d pages and there are %d under docs/ plus the "+
			"README; a page it does not name is a page nothing checks",
			len(pages), len(under)+1)
	}
	for _, page := range pages {
		if _, err := os.Stat(page); err != nil {
			t.Errorf("DocPages names %q, which is not there", page)
		}
	}
}

// TestTheDocumentationReadsAsOne holds the joining, which is the whole point.
//
// Returning only the README would pass every caller that happens to look for
// something the README still says, and quietly stop covering the pages split
// out of it -- which is how this helper came to exist at all.
func TestTheDocumentationReadsAsOne(t *testing.T) {
	inRoot(t)

	text, err := DocsText()
	if err != nil {
		t.Fatal(err)
	}

	// A heading from the README and one from a page that is not the README, so
	// a join that dropped either half is a failure rather than a shorter
	// string nobody looks at.
	for _, want := range []string{"## Install", "# When something looks wrong"} {
		if !strings.Contains(text, want) {
			t.Errorf("the documentation read as one does not contain %q", want)
		}
	}

	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(text) <= len(readme) {
		t.Errorf("the whole documentation is %d bytes and the README alone is %d, "+
			"so nothing beyond it was read", len(text), len(readme))
	}
}

func TestAPlanIsNotDocumentationOfWhatThisDoes(t *testing.T) {
	// Several packages read DocsText to ask whether something is explained to
	// somebody who went looking: a state the menu can show, a warning the
	// config can produce, a setting with a default. docs/pairing.md is a design
	// for something not built, kept so that decisions already taken are not
	// argued again -- and a phrase found only there is documented nowhere a
	// reader can act on. The guard would pass and the reader would be lost.
	inRoot(t)

	plan, err := os.ReadFile(filepath.Join("docs", "pairing.md"))
	if err != nil {
		t.Skip("no pairing plan to check against; this test needs rewriting")
	}
	if !Planned(plan) {
		t.Fatal("docs/pairing.md is no longer recognised as a plan, so its text " +
			"now counts as documentation of what this does")
	}

	text, err := DocsText()
	if err != nil {
		t.Fatal(err)
	}
	// A phrase from the plan and from nowhere else. If this ever appears in a
	// page about what the plugin does, it stops being a test of anything and
	// wants replacing rather than deleting.
	const fromThePlan = "takeover war"
	if !strings.Contains(string(plan), fromThePlan) {
		t.Fatalf("the plan no longer says %q, so this is checking nothing", fromThePlan)
	}
	if strings.Contains(text, fromThePlan) {
		t.Error("what the documentation says includes a page about something not built")
	}

	// The pages themselves still name it: the links in it are checked like any
	// other page's, and it is linked from the README on purpose.
	pages, err := DocPages()
	if err != nil {
		t.Fatal(err)
	}
	named := false
	for _, page := range pages {
		if filepath.Base(page) == "pairing.md" {
			named = true
		}
	}
	if !named {
		t.Error("DocPages has stopped naming the plan, so nothing checks its links")
	}
}

func TestOnlyAWholePageAboutSomethingUnbuiltCounts(t *testing.T) {
	// Searched for loosely, the word appears in prose about features that do
	// exist -- and a page dropped from the documentation is a page whose
	// promises nothing holds any more.
	for _, page := range [][]byte{
		[]byte("# Mirroring\n\nThis was planned for a while and is now built.\n"),
		[]byte("# Settings\n\nStatus is reported per machine. Planned work is elsewhere.\n"),
	} {
		if Planned(page) {
			t.Errorf("a page about something built is being dropped from the docs:\n%s", page)
		}
	}
	if !Planned([]byte("# Pairing\n\nStatus: **planned, nothing built.**\n")) {
		t.Error("a page declaring itself a plan is being read as documentation")
	}
}

func TestPagesAreSeparatedEvenWhenOneDoesNotEndInANewline(t *testing.T) {
	// DocsText hands every page to whatever is searching it as one string, and
	// nine files in this repository ask questions of that string. The pages
	// are joined with a newline of their own so the last line of one cannot
	// run into the first line of the next.
	//
	// Nothing held that. Every page here happens to end with a newline, so
	// deleting the join changes nothing today -- which is what makes it worth
	// writing down rather than leaving. The first page added without one turns
	// the next page's heading into the tail of somebody else's sentence, and
	// every question asked of this string is a Contains, which still matches a
	// heading glued to the end of a line. Nothing would fail; the docs would
	// just quietly stop having a heading there.
	//
	// Against a repository of its own, since the point is a page this one does
	// not have.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"go.mod": "module probe\n\ngo 1.25\n",
		// Deliberately without a trailing newline.
		"README.md":               "# Readme\n\nthe last line of the readme",
		"docs/troubleshooting.md": "# When something looks wrong\n\nnot much\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	text, err := DocsText()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "\n# When something looks wrong") {
		t.Errorf("the second page's heading does not start a line:\n%q", text)
	}
	if strings.Contains(text, "readme# When something looks wrong") {
		t.Errorf("one page's last line ran into the next page's heading:\n%q", text)
	}
}
