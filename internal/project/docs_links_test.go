package project

import (
	"os"
	"path/filepath"
	"reflect"
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

// TestALogSampleIsShownAsComingFromTheLogThatHasIt holds the documentation's
// examples of log output to the process that writes them.
//
// Two files are kept and which holds what is not a detail: the whole use of
// these entries is telling somebody where to look. mirror.log is written by the
// pane process and holds why a terminal would not open. Everything the daemon
// says -- a machine's Herdr version, a pass running long, a terminal it could
// not close -- goes through the daemon's logger into daemon.log.
//
// Three entries named mirror.log for messages the daemon writes. Nothing
// minded, because a file name in prose is a string and the sample under it was
// real -- just from the other file.
func TestALogSampleIsShownAsComingFromTheLogThatHasIt(t *testing.T) {
	inRoot(t)

	// Which package writes each file, and so whose messages a sample from it
	// has to be one of.
	owners := map[string][]string{
		"mirror.log": {"internal/mirror"},
		"daemon.log": {"internal/syncd", "internal/cli"},
	}
	wording := map[string][]string{}
	for file, dirs := range owners {
		for _, dir := range dirs {
			wording[file] = append(wording[file], staticRuns(t, dir)...)
		}
		if len(wording[file]) < 5 {
			t.Fatalf("found %d things %s could say, which is fewer than it says",
				len(wording[file]), file)
		}
	}

	pages, err := DocPages()
	if err != nil {
		t.Fatal(err)
	}
	// A log file named, then a fenced block within the next few lines. The
	// prose between them is the claim; the sample is what it is about.
	claim := regexp.MustCompile("(?s)`(mirror\\.log|daemon\\.log)`[^`]{0,400}?\n```\n(.+?)\n```")
	confirmed := 0
	for _, page := range pages {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range claim.FindAllStringSubmatch(string(raw), -1) {
			file, sample := m[1], flattened(m[2])
			if says(wording[file], sample) {
				confirmed++
				continue
			}
			// Not something the named file says. Something the other one says
			// is the mistake this is for; nothing either says is a sample with
			// too little wording of its own to look for -- a line that is
			// almost all values -- and is evidence of neither.
			for other := range owners {
				if other != file && says(wording[other], sample) {
					t.Errorf("%s shows %q as coming from %s, and it is %s that writes it",
						filepath.Base(page), sample, file, other)
				}
			}
		}
	}
	// Confirmed rather than merely looked at: a run where every sample was too
	// templated to match would otherwise pass while checking nothing.
	//
	// Two, because that is how many of the samples have wording of their own.
	// "bot: herdr 0.8.2" comes from "%s: %s" and could be either file saying
	// anything; the settings report is "config: %s = %s" and the same. Both
	// were among the entries that named the wrong file, so this catches two of
	// those three rather than all of them -- and a message worth an entry in
	// the troubleshooting page usually has a sentence in it, which is the half
	// this can hold.
	if confirmed < 2 {
		t.Fatalf("only %d log samples were matched to the source that writes them, "+
			"which is fewer than the docs show; this is checking nothing", confirmed)
	}
}

// says reports whether a sample is one of these messages, by looking for the
// parts of them that do not change.
func says(runs []string, sample string) bool {
	for _, run := range runs {
		if strings.Contains(sample, run) {
			return true
		}
	}
	return false
}

// staticRuns is every stretch of wording a package's messages hold verbatim.
//
// Taken from the source rather than guessed at from the sample. A sample is one
// message with its values filled in, and from the page there is no telling a
// value from a word: "bot" and "split" look like prose. From the format string
// there is -- what is not a verb is wording, and wording is what has to appear.
func staticRuns(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		joined.Write(raw)
	}
	// Adjacent literals joined, so a message split across lines to fit the
	// column reads as the one string it becomes. Every one of these is written
	// that way, and searching the source as it is finds none of them.
	source := regexp.MustCompile(`"\s*\+\s*"`).ReplaceAllString(joined.String(), "")

	var runs []string
	for _, literal := range regexp.MustCompile(`"((?:[^"\\\n]|\\.)*)"`).FindAllStringSubmatch(source, -1) {
		for _, run := range regexp.MustCompile(`%[-+# 0-9.*]*[a-zA-Z]`).Split(literal[1], -1) {
			if run = flattened(run); len(run) >= 16 {
				runs = append(runs, run)
			}
		}
	}
	return runs
}

// flattened puts a message on one line with single spaces, so wrapping in the
// documentation does not stop it matching the source.
func flattened(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// TestWhatTheDocsSayAMachineSaysIsSomethingItCanSay holds each entry's heading
// to the message it is about.
//
// The troubleshooting page is a list of things this prints, each quoted so it
// can be recognised and searched for. A quotation that has drifted is worse
// than no entry at all: somebody looks for it in their own output, does not
// find it, and concludes the page is about some other problem.
//
// One had. The entry for two spaces sharing a name quoted "more than one space
// has this machine's name" and the message is "more than one space on the
// machine has this machine's name" -- three words that are not a substring, so
// searching for the documented phrase found nothing.
func TestWhatTheDocsSayAMachineSaysIsSomethingItCanSay(t *testing.T) {
	inRoot(t)

	var source strings.Builder
	for _, dir := range []string{"internal/cli", "internal/syncd", "internal/picker", "internal/mirror"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			source.Write(raw)
		}
	}
	// Adjacent literals joined, as elsewhere: every message long enough to
	// quote is written across several lines.
	said := regexp.MustCompile(`"\s*\+\s*"`).ReplaceAllString(source.String(), "")

	pages, err := DocPages()
	if err != nil {
		t.Fatal(err)
	}
	claim := regexp.MustCompile("(?m)^\\*\\*[^*]*? says `([^`]+)`")
	checked := 0
	for _, page := range pages {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range claim.FindAllStringSubmatch(string(raw), -1) {
			quoted := m[1]
			// The page writes a count as "n" where the message has a verb, so
			// that the entry reads as a sentence rather than a format string.
			// Both spellings are tried rather than one being preferred.
			if strings.Contains(said, quoted) ||
				strings.Contains(said, strings.Replace(quoted, "n ", "%d ", 1)) {
				checked++
				continue
			}
			t.Errorf("%s says a machine says %q, and nothing here says that",
				filepath.Base(page), quoted)
		}
	}
	if checked < 5 {
		t.Fatalf("matched %d quoted messages, which is fewer than the page quotes; "+
			"this is checking nothing", checked)
	}
}

// TestTheMenuAndTheListingBlameTheSameThingFirst holds two orderings to each
// other.
//
// A machine can mirror fewer terminals than it has for four reasons at once: a
// name shared with another space, the mirror cap, terminals the scope leaves
// alone, and terminals that would not mirror. Both the listing and the menu
// pick one to show -- the listing has room for a sentence, the menu for two
// words -- and the wording differs on purpose. The choice must not.
//
// A machine blamed on the scope in one and on the cap in the other sends
// somebody to change the wrong thing, and nobody sees both at once to notice.
// They did differ: the listing had these first, in the order it explains, and
// the menu's copy swapped two and left the fourth out. Nothing compared them.
//
// Read rather than run, because the menu's rendering is unexported and the
// package that could call it is the one it imports.
func TestTheMenuAndTheListingBlameTheSameThingFirst(t *testing.T) {
	inRoot(t)

	causes := regexp.MustCompile(`SharedName|AtCapacity|OutsideShared|Unmirrored`)
	order := func(file, from string) []string {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		start := strings.Index(body, from)
		if start < 0 {
			t.Fatalf("%s no longer has %q, so this is checking nothing", file, from)
		}
		body = body[start:]
		if end := strings.Index(body[1:], "\nfunc "); end >= 0 {
			body = body[:end]
		}
		// Comments name these too, and in prose rather than in order.
		var code strings.Builder
		for _, line := range strings.Split(body, "\n") {
			if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "//") {
				code.WriteString(line + "\n")
			}
		}
		var seen []string
		for _, name := range causes.FindAllString(code.String(), -1) {
			if len(seen) == 0 || seen[len(seen)-1] != name {
				seen = append(seen, name)
			}
		}
		return seen
	}

	listing := order(filepath.Join("internal", "cli", "client.go"), "func statusLines(")
	menu := order(filepath.Join("internal", "picker", "picker.go"), "func whyTheCountIsLow(")
	if len(menu) < 4 {
		t.Fatalf("the menu weighs %d of these, and there are four: %v", len(menu), menu)
	}
	if !reflect.DeepEqual(listing, menu) {
		t.Errorf("the listing blames these in the order %v and the menu %v; a machine "+
			"is then sent to two different settings depending on where it is read",
			listing, menu)
	}
}
