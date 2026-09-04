package version

import (
	"regexp"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/project"
)

// docsText is every page of documentation joined together: the README and the
// pages under docs/. These tests are about something the documentation shows
// agreeing with what the code does, and which page shows it is a decision that
// has already changed twice -- the troubleshooting and contributor sections
// both moved out of the README. Reading the set rather than one file keeps the
// check about the agreement rather than about where the prose currently sits.
func docsText(t *testing.T) string {
	t.Helper()
	text, err := project.DocsText()
	if err != nil {
		t.Fatal(err)
	}
	return text
}

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

func TestWhichOfGosBuildSettingsNameTheBuild(t *testing.T) {
	// shortRevision below is held thoroughly and reads nothing: the pair it
	// decides from has to be found first, and which keys they come from was held
	// by nothing at all. It cannot be reached through Short either -- a test
	// binary is built without a checkout, so ReadBuildInfo answers with no vcs
	// settings whatever the code does with them, and every existing test here
	// passes on the "unknown" that falls out.
	//
	// What that hides is not small. A key read wrong names every real build
	// "unknown", and "unknown" is the single input that makes StaleMessageFor say
	// nothing, so the update warning this package exists for would go quiet on
	// every machine with the suite still green.
	info := func(pairs ...string) *debug.BuildInfo {
		built := &debug.BuildInfo{}
		for i := 0; i < len(pairs); i += 2 {
			built.Settings = append(built.Settings,
				debug.BuildSetting{Key: pairs[i], Value: pairs[i+1]})
		}
		return built
	}

	for _, tt := range []struct {
		what string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"the commit Go stamped in",
			info("vcs.revision", "9fcc667abc123def456"), true, "9fcc667"},
		{"and the mark for a tree that was not clean",
			info("vcs.revision", "9fcc667abc123def456", "vcs.modified", "true"), true,
			"9fcc667-dirty"},
		{"a clean tree is not marked",
			info("vcs.revision", "9fcc667abc123def456", "vcs.modified", "false"), true,
			"9fcc667"},
		// The control, and the one that catches a key read from the wrong name:
		// settings that say plenty and nothing about a checkout. This is what a
		// test binary itself looks like.
		{"settings that say nothing about a checkout",
			info("GOARCH", "amd64", "-race", "true", "vcs", "git"), true, "unknown"},
		// And the other way a build can be unnameable: Go recorded nothing at
		// all. Without the guard for it there is no info to read the settings
		// out of.
		{"nothing recorded about the build at all", nil, false, "unknown"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			if got := buildRevision(tt.info, tt.ok); got != tt.want {
				t.Errorf("buildRevision(%v, %v) = %q, want %q",
					tt.info, tt.ok, got, tt.want)
			}
		})
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
			// The daemon answers, but does not say which build it is.
			// Silence there would be the update-is-invisible case again --
			// and so would claiming to know which build it is, which is what
			// the daemon column two lines above has just said it does not.
			what:    "a daemon that names no build still gets mentioned",
			running: "", installed: "9fcc667",
			mentions: []string{"does not report which build", "9fcc667", "restart Herdr"},
		},
	} {
		t.Run(tt.what, func(t *testing.T) {
			got := StaleMessageFor(tt.running, tt.installed)
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
	readme := docsText(t)
	const marker = "warning: the running daemon is "
	i := strings.Index(readme, marker)
	if i < 0 {
		t.Fatalf("the documentation no longer quotes %q", strings.TrimSpace(marker))
	}
	block := readme[i:]
	block = block[:strings.Index(block, "```")]

	// The README wraps it across two lines to fit the page; the message is one.
	shown := strings.TrimPrefix(strings.Join(strings.Fields(block), " "), "warning: ")

	m := regexp.MustCompile(`daemon is (\S+) but (\S+) is installed`).FindStringSubmatch(shown)
	if m == nil {
		t.Fatalf("cannot read the two builds out of the README's example: %q", shown)
	}
	if want := StaleMessageFor(m[1], m[2]); shown != want {
		t.Errorf("the README quotes\n\t%q\nbut the code prints\n\t%q", shown, want)
	}
}
