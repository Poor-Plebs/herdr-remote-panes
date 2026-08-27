package project

import (
	"os"
	"path/filepath"
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
