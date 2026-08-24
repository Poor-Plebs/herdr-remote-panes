package version

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestShortIsAlwaysSomethingPrintable(t *testing.T) {
	// Tests are built outside a checkout, so this exercises the fallback: it
	// must still return something rather than an empty column in the status.
	got := Short()
	if got == "" {
		t.Fatal("Short() is empty")
	}
	if strings.ContainsAny(got, " \n\t") {
		t.Errorf("Short() = %q, want a single printable token", got)
	}
	if len(got) > 20 {
		t.Errorf("Short() = %q, too long to sit in a status line", got)
	}
}

func TestShortIsStable(t *testing.T) {
	// It is read on every status, so it must not recompute or drift.
	if a, b := Short(), Short(); a != b {
		t.Errorf("Short() returned %q then %q", a, b)
	}
}

func TestStaleMessage(t *testing.T) {
	// Installing an update leaves the running daemon alone, so the new build
	// sits on disk while the old one keeps answering. Nothing said so, which
	// made it possible to watch an old build behave like an old build and
	// conclude the update had not worked.
	installed := Short()

	if got := StaleMessage(installed); got != "" {
		t.Errorf("a matching build should say nothing, got %q", got)
	}

	// Built outside a checkout there is nothing to compare, so it stays quiet
	// rather than warning on every status.
	if installed == "unknown" {
		for _, running := range []string{"", "427e2ad"} {
			if got := StaleMessage(running); got != "" {
				t.Errorf("StaleMessage(%q) = %q, want silence from an unknown build", running, got)
			}
		}
		return
	}

	got := StaleMessage("427e2ad")
	if !strings.Contains(got, "427e2ad") || !strings.Contains(got, installed) {
		t.Errorf("StaleMessage = %q, want it to name both builds", got)
	}
	if !strings.Contains(got, "restart") {
		t.Errorf("StaleMessage = %q, want it to say what to do", got)
	}

	if got := StaleMessage(""); !strings.Contains(got, "older build") {
		t.Errorf("a daemon too old to report a build should still be named: %q", got)
	}
}

func TestHowABuildIsNamed(t *testing.T) {
	for _, tt := range []struct {
		what, recorded string
		modified       bool
		want           string
	}{
		{"a commit is cut to something readable", "9fcc667abc123def456", false, "9fcc667"},
		{"and marked when the tree was not clean", "9fcc667abc123def456", true, "9fcc667-dirty"},
		{"a short revision is kept whole", "9fcc667", false, "9fcc667"},
		{"no revision means no checkout, which is not a dirty build", "", true, "unknown"},
		{"nor is it when nothing was modified", "", false, "unknown"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			if got := shortRevision(tt.recorded, tt.modified); got != tt.want {
				t.Errorf("shortRevision(%q, %v) = %q, want %q", tt.recorded, tt.modified, got, tt.want)
			}
		})
	}
}

func TestWhenTheRunningDaemonIsWorthMentioning(t *testing.T) {
	// The warning exists because an update is invisible otherwise: the new
	// build sits on disk while the old one keeps answering. It has to stay
	// quiet whenever it cannot actually tell -- a warning that appears every
	// time teaches people to ignore the one time it matters.
	for _, tt := range []struct {
		what, running, installed string
		wantQuiet                bool
		mentions                 []string
	}{
		{what: "the same build is nothing to say", running: "9fcc667", installed: "9fcc667", wantQuiet: true},
		{what: "an installed build with no revision cannot be compared", running: "427e2ad", installed: "unknown", wantQuiet: true},
		{what: "nor can an empty one", running: "427e2ad", installed: "", wantQuiet: true},
		{
			what:    "a daemon on an older build is worth saying, with both named",
			running: "427e2ad", installed: "9fcc667",
			mentions: []string{"427e2ad", "9fcc667", "restart Herdr"},
		},
		{
			// The daemon answers, but is old enough not to say which build it
			// is. Silence there would be the update-is-invisible case again.
			what:    "a daemon too old to say still gets mentioned",
			running: "", installed: "9fcc667",
			mentions: []string{"an older build", "9fcc667", "restart Herdr"},
		},
	} {
		t.Run(tt.what, func(t *testing.T) {
			got := staleMessage(tt.running, tt.installed)
			if tt.wantQuiet {
				if got != "" {
					t.Errorf("want nothing said, got %q", got)
				}
				return
			}
			if got == "" {
				t.Fatal("want a warning, got nothing")
			}
			for _, want := range tt.mentions {
				if !strings.Contains(got, want) {
					t.Errorf("the warning does not mention %q: %q", want, got)
				}
			}
		})
	}
}

func TestTheREADMEQuotesTheWarningTheCodeActuallyPrints(t *testing.T) {
	// The README shows this warning word for word, which is how somebody
	// recognises it when it appears. Written out by hand it agrees with the
	// code until the wording changes, and then it describes a message nobody
	// has ever seen.
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	const marker = "warning: the running daemon is "
	i := strings.Index(string(readme), marker)
	if i < 0 {
		t.Fatalf("the README no longer quotes %q", strings.TrimSpace(marker))
	}
	block := string(readme)[i:]
	block = block[:strings.Index(block, "```")]

	// The README wraps it across two lines to fit the page; the message is one.
	shown := strings.TrimPrefix(strings.Join(strings.Fields(block), " "), "warning: ")

	m := regexp.MustCompile(`daemon is (\S+) but (\S+) is installed`).FindStringSubmatch(shown)
	if m == nil {
		t.Fatalf("cannot read the two builds out of the README's example: %q", shown)
	}
	if want := staleMessage(m[1], m[2]); shown != want {
		t.Errorf("the README quotes\n\t%q\nbut the code prints\n\t%q", shown, want)
	}
}
