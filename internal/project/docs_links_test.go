package project

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
	inRoot(t)

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

// TestEveryPageUnderDocsIsLinkedFromAnother holds the intent the test above
// states and leaves unchecked.
//
// Its comment says "a page nothing links to is invisible, so the docs link to
// each other", and it then only follows the links that exist -- so a page
// nobody links to passes it by being absent from every page it reads. The two
// pages here that were split out of the README were linked by hand as they
// were written, which is the arrangement that works until somebody adds a
// third.
func TestEveryPageUnderDocsIsLinkedFromAnother(t *testing.T) {
	inRoot(t)

	pages, err := filepath.Glob("docs/*.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 {
		t.Fatal("no pages under docs/, so this test proves nothing")
	}

	linked := map[string]bool{}
	for _, page := range append(append([]string{}, pages...), "README.md") {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range markdownLink.FindAllStringSubmatch(string(raw), -1) {
			target := m[1]
			if i := strings.Index(target, "#"); i >= 0 {
				target = target[:i]
			}
			if target == "" || strings.Contains(target, "://") ||
				strings.HasPrefix(target, "mailto:") {
				continue
			}
			to := filepath.Clean(filepath.Join(filepath.Dir(page), target))
			// A page linking to itself is still a page nobody arrives at.
			if to == filepath.Clean(page) {
				continue
			}
			linked[to] = true
		}
	}

	for _, page := range pages {
		if !linked[filepath.Clean(page)] {
			t.Errorf("%s is linked from no other page, so the only way to it is "+
				"knowing it is there", page)
		}
	}
}

// proseWidth is the column the prose in these pages is wrapped at. Not a rule
// about taste: every paragraph here is already wrapped to roughly this, so one
// that is not stands out as a paragraph somebody edited and did not re-flow,
// and reads differently from the ones around it in an editor that does not
// wrap.
//
// Generous, so that a sentence running a little over is left alone and only a
// paragraph that lost its wrapping altogether is reported.
const proseWidth = 88

func TestTheProseInTheDocsStaysWrapped(t *testing.T) {
	// The line this was written for ran to 117 columns in the middle of a
	// paragraph wrapped at 80: an edit that added a clause and never re-flowed
	// what followed. Nothing reads markdown for shape, so it sat there.
	inRoot(t)

	for _, page := range docPagesFor(t) {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		fenced := false
		for n, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				fenced = !fenced
				continue
			}
			switch {
			case fenced:
				// Quoted output, which has whatever width it has. Wrapping it
				// would be a picture of something the program does not print.
			case strings.HasPrefix(strings.TrimSpace(line), "|"):
				// A table row cannot be wrapped and stay a table row.
			case strings.Contains(line, "](http"):
				// A badge or a link whose target is longer than the line.
			case len([]rune(line)) > proseWidth:
				t.Errorf("%s:%d is %d columns and is prose, so it wants re-wrapping:\n  %s",
					filepath.Base(page), n+1, len([]rune(line)), line)
			}
		}
	}
}

// docPagesFor is every markdown page in the repository.
func docPagesFor(t *testing.T) []string {
	t.Helper()
	pages, err := filepath.Glob(filepath.Join("docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	return append(pages, "README.md")
}
