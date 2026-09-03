package main

import (
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
)

// paneSplit is a help as Herdr prints one, enough to answer about a flag.
const paneSplit = `Split a pane

Usage: herdr pane split [OPTIONS] [PANE_ID]

Options:
      --direction <DIRECTION>
          [possible values: right, down]
`

// answering is a Herdr that has exactly these commands, so a test can take one
// away without a Herdr on the machine and without editing the pages.
func answering(known map[string]string) asker {
	return func(command []string) (string, bool) {
		help, ok := known[strings.Join(command, " ")]
		return help, ok
	}
}

func aRun(t *testing.T, ask asker) (string, int) {
	t.Helper()
	var out strings.Builder
	code := report(&out, ask,
		[]herdrcli.Dependency{
			// One with a restricted value and one without. Both kinds are in
			// the real list, and they have to be kept apart here: with only
			// the first, a help missing the flag fails the value check too, so
			// the flag check could be removed with nothing noticing.
			{
				Command: []string{"pane", "split"},
				Flags:   []string{"--direction"},
				Values:  map[string][]string{"--direction": {"right"}},
			},
			{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}},
		},
		[]toldCommand{{
			command: []string{"plugin", "log", "list"},
			where:   []string{"troubleshooting.md:49"},
			flags:   map[string][]string{"--plugin": {"troubleshooting.md:49"}},
		}},
		[]toldCommand{{
			command: []string{"session", "attach"},
			where:   []string{"plan.go:628"},
		}},
	)
	return out.String(), code
}

// everything is a Herdr that has all three of them.
func everything() map[string]string {
	return map[string]string{
		"pane split":      paneSplit,
		"pane close":      "Usage: herdr pane close [PANE_ID]\n\nOptions:\n      --plugin <ID>\n",
		"plugin log list": "Usage: herdr plugin log list\n\nOptions:\n      --plugin <ID>\n",
		"session attach":  "Usage: herdr session attach <NAME>\n",
	}
}

func TestARunWithNothingWrongExitsNought(t *testing.T) {
	out, code := aRun(t, answering(everything()))
	if code != 0 {
		t.Errorf("nothing was wrong and it exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "take what this plugin sends") {
		t.Errorf("a clean run does not say so:\n%s", out)
	}
	// And it says what it asked about. A run that printed only the summary
	// would be a checker nobody could tell had looked at the right things --
	// which is how `--placement popup` was sent for as long as it was.
	for _, named := range []string{"pane split", "pane close", "plugin log list", "session attach"} {
		if !strings.Contains(out, named) {
			t.Errorf("a clean run does not name %q among what it checked:\n%s", named, out)
		}
	}
	if n := strings.Count(out, "ok"); n < 4 {
		t.Errorf("four commands were asked about and %d are reported ok:\n%s", n, out)
	}
}

func TestAValueHerdrStoppedTakingGatesTheRun(t *testing.T) {
	// The other half of what Dependencies records. A flag can still be there
	// while the value this plugin sends for it is gone, which is exactly what
	// happened to `--placement popup`: accepted by the stand-in for as long as
	// the code sent it, refused by the real thing, and nothing opened.
	known := everything()
	known["pane split"] = `Usage: herdr pane split

Options:
      --direction <DIRECTION>
          [possible values: down]
`

	out, code := aRun(t, answering(known))
	if code == 0 {
		t.Errorf("a value Herdr stopped taking exited nought:\n%s", out)
	}
	if !strings.Contains(out, "--direction=right") {
		t.Errorf("the value that went is not named:\n%s", out)
	}
	if strings.Contains(out, "take what this plugin sends") {
		t.Errorf("a value that went still says everything is well:\n%s", out)
	}
}

func TestAnythingWrongGatesTheRun(t *testing.T) {
	// The failure this is here for. A statement-deletion sweep removed the
	// `os.Exit(1)` and every test still passed, because nothing ran this at
	// all: the checker printed what was wrong, printed the line saying all was
	// well underneath it, and told make it had succeeded. Removing either line
	// that adds the pages' or the messages' problems to the count was just as
	// quiet -- those would be listed and then not counted.
	//
	// So each source is taken away in turn, and each has to do two things: end
	// the run non-zero, and not print the reassuring line afterwards.
	for _, gone := range []string{"pane split", "pane close", "plugin log list", "session attach"} {
		known := everything()
		delete(known, gone)

		out, code := aRun(t, answering(known))
		if code == 0 {
			t.Errorf("%q is missing and the run exited nought, so `make herdr` goes green:\n%s",
				gone, out)
		}
		if strings.Contains(out, "take what this plugin sends") {
			t.Errorf("%q is missing and the run still says everything is well:\n%s", gone, out)
		}
		if !strings.Contains(out, "no such command in this Herdr") {
			t.Errorf("%q is missing and nothing says which:\n%s", gone, out)
		}
	}
}

func TestWhatIsWrongIsNamedWithWhereItCameFrom(t *testing.T) {
	// Naming the command is half of it. A page or a message that sends
	// somebody to a command Herdr no longer has has to say which page and
	// which line, or somebody has to grep for it.
	for gone, where := range map[string]string{
		"plugin log list": "troubleshooting.md:49",
		"session attach":  "plan.go:628",
	} {
		known := everything()
		delete(known, gone)
		out, _ := aRun(t, answering(known))
		if !strings.Contains(out, where) {
			t.Errorf("%q is missing and the run does not say it comes from %s:\n%s", gone, where, out)
		}
	}
}

func TestAFlagThatWentGatesTheRunToo(t *testing.T) {
	// A command that still exists but stopped taking what is passed to it, on
	// both sides: what the plugin sends, and what a page passes.
	for _, one := range []struct{ what, help, says string }{
		{"pane close", "Usage: herdr pane close\n", "no longer takes: --plugin"},
		{"plugin log list", "Usage: herdr plugin log list\n", "no longer takes what it is given: --plugin"},
	} {
		known := everything()
		known[one.what] = one.help

		out, code := aRun(t, answering(known))
		if code == 0 {
			t.Errorf("%q lost a flag and the run exited nought:\n%s", one.what, out)
		}
		if !strings.Contains(out, one.says) {
			t.Errorf("%q lost a flag and the run does not say so:\n%s", one.what, out)
		}
		if strings.Contains(out, "take what this plugin sends") {
			t.Errorf("%q lost a flag and the run still says everything is well:\n%s", one.what, out)
		}
	}
}
