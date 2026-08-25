package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// markdownLink matches [text](target). Targets do not contain a space or a
// closing bracket, which is enough to leave prose using brackets alone.
var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)

// TestEveryLinkBetweenTheDocsPointsAtSomething follows the relative links.
//
// A page nothing links to is invisible, so the docs link to each other; and a
// link that has gone stale is worse than no link, because it reads as a promise
// that the thing is there. Renaming or moving a file breaks these silently:
// nothing about markdown is checked by a compiler, and the person who notices
// is a reader who wanted the page.
//
// Only relative links are followed. A URL would need the network, which would
// make the suite fail for reasons that have nothing to do with the change.
func TestEveryLinkBetweenTheDocsPointsAtSomething(t *testing.T) {
	pages, err := filepath.Glob("docs/*.md")
	if err != nil {
		t.Fatal(err)
	}
	pages = append(pages, "README.md")

	checked := 0
	for _, page := range pages {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range markdownLink.FindAllStringSubmatch(string(raw), -1) {
			target := m[1]
			switch {
			case strings.Contains(target, "://"), strings.HasPrefix(target, "mailto:"):
				continue
			// A fragment alone points within the page itself.
			case strings.HasPrefix(target, "#"):
				continue
			}
			// Links may carry a heading: the file is the part before the #.
			if i := strings.Index(target, "#"); i >= 0 {
				target = target[:i]
			}
			if target == "" {
				continue
			}
			checked++
			path := filepath.Join(filepath.Dir(page), target)
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s links to %q, which is not there", page, target)
			}
		}
	}

	// A test that follows no links passes for the wrong reason: it would go on
	// passing if the pattern stopped matching anything at all.
	if checked == 0 {
		t.Error("no relative links were found to follow, so this test proved nothing")
	}
}
