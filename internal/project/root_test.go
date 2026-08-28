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
