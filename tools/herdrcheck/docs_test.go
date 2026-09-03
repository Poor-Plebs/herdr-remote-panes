package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// page is documentation written the way these pages are written: commands in
// backticks in a sentence, commands on their own in a fenced block, commands
// inside an ssh quote, a command inside a path substitution -- and, in among
// them, the two sentences where "herdr" is a word rather than a command.
//
// Kept in the shapes the real pages use rather than reduced to one of each,
// because the mistake this catches is a shape being read wrongly, and a
// fixture written from the same idea as the reader agrees with it by
// construction.
const page = "" +
	"Install it with `herdr plugin install Poor-Plebs/herdr-remote-panes --ref v0.4.11`.\n" +
	"\n" +
	"`herdr plugin log list` takes `--limit` as well, and reads the newest first.\n" +
	"\n" +
	"```bash\n" +
	"herdr config check\n" +
	"$ herdr plugin log list --plugin poorplebs.remote-panes  # --limit shortens it\n" +
	"ssh workbox 'herdr pane list'          # what is still open there\n" +
	"ssh workbox 'herdr pane close wG:p3'   # close one you are done with\n" +
	"```\n" +
	"\n" +
	"    herdr pane run <pane-id> <command>\n" +
	"\n" +
	"All optional, in `$(herdr plugin config-dir poorplebs.remote-panes)/config.json`:\n" +
	"\n" +
	"A machine says `connected · 2 open · herdr not found` in the menu.\n" +
	"\n" +
	"```\n" +
	"  bot  2 ssh  mirroring off: no herdr found on the machine — set herdr_bin\n" +
	"```\n" +
	"\n" +
	"herdr-remote-panes: 2026/08/27 09:02:41 could not accept on the control socket\n"

func TestTheCommandsTheDocsGiveAreRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := docCommands(dir)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, c := range found {
		got[strings.Join(c.command, " ")] = true
	}

	// Every shape the pages use, and the command as far as its arguments and
	// no further.
	for _, want := range []string{
		"plugin install",    // in backticks, in a sentence
		"config check",      // on its own in a fenced block
		"plugin log list",   // after a prompt, and stopping at --plugin
		"pane list",         // inside an ssh quote, stopping at the quote
		"pane close",        // the same, with an argument after it
		"pane run",          // an indented block, stopping at <pane-id>
		"plugin config-dir", // inside $(...), stopping at the plugin name
	} {
		if !got[want] {
			t.Errorf("the docs give `herdr %s` and it was not read as a command", want)
		}
		delete(got, want)
	}

	// And nothing else. What is left over here is prose read as a command,
	// which is the failure that matters: it reports drift that is not there,
	// every run, until the report stops being read at all.
	for leftover := range got {
		t.Errorf("`herdr %s` is not a command anybody is told to run", leftover)
	}
}

func TestProseIsNotReadAsACommand(t *testing.T) {
	// Both of these are this plugin's own words about Herdr being missing, and
	// both put "herdr" in front of lowercase words that look exactly like
	// subcommands.
	for _, line := range []string{
		"A machine says `connected · 2 open · herdr not found` in the menu.",
		"  bot  2 ssh  mirroring off: no herdr found on the machine",
		"herdr-remote-panes: 2026/08/27 09:02:41 could not accept",
		"Set herdr_bin if it is installed elsewhere there.",
	} {
		for _, got := range commandsIn(line) {
			t.Errorf("read %q as the command `herdr %s`", line, got.command)
		}
	}
}

func TestAnEmptyReadIsAnError(t *testing.T) {
	// A page with no commands in it stands for the two ways this goes quiet:
	// run outside the tree, or run against pages that stopped being written
	// the way it reads them. Both otherwise print the same line as a run that
	// checked a dozen commands and was satisfied.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("Nothing to run here.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := docCommands(dir); err == nil {
		t.Error("finding no commands at all was reported as success")
	}
}

func TestTheRealPagesStillGiveCommands(t *testing.T) {
	// The fixture above proves the reader understands those shapes. This
	// proves the pages are still written in them: a rewrite that moved every
	// invocation into a shape this does not read would leave the fixture test
	// passing and check nothing at all.
	found, err := docCommands(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 8 {
		t.Errorf("found %d commands in the documentation, which is fewer than there are; "+
			"the shapes have stopped matching how the pages are written", len(found))
	}
	for _, c := range found {
		if len(c.where) == 0 {
			t.Errorf("`herdr %s` was found with nowhere to look at", strings.Join(c.command, " "))
		}
	}
}

func TestTheFlagsThePagesPassAreRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := docCommands(dir)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string][]string{}
	for _, c := range found {
		for flag := range c.flags {
			got[strings.Join(c.command, " ")] = append(got[strings.Join(c.command, " ")], flag)
		}
	}

	// Both shapes the pages use: a flag after the argument, and a flag after
	// the subcommands with nothing between.
	for command, flag := range map[string]string{
		"plugin install":  "--ref",
		"plugin log list": "--plugin",
	} {
		if !slices.Contains(got[command], flag) {
			t.Errorf("the pages pass %s to `herdr %s` and it was not read", flag, command)
		}
	}

	// A flag named beside an invocation is not one it passes. The pages
	// annotate their commands -- `# what is still open there` -- and a comment
	// that names a flag is prose sitting on a command line; so is the rest of
	// a sentence after the invocation's closing backtick. Asking Herdr about
	// `--limit` from either would report drift against a paragraph that is
	// right, every run, until the report stops being read.
	for command, flags := range got {
		if slices.Contains(flags, "--limit") {
			t.Errorf("`--limit` is named in prose and was read as passed to `herdr %s`", command)
		}
	}
}

func TestAnArgumentIsNotAFlag(t *testing.T) {
	// What follows a command in these pages: placeholders, a pane id, a plugin
	// name, and the `...` that says a placeholder repeats.
	for _, word := range []string{"<pane-id>", "<key>...", "wG:p3", "poorplebs.remote-panes", "-", "--", "v0.4.11"} {
		if isFlag(word) {
			t.Errorf("%q was read as a flag", word)
		}
	}
	for _, word := range []string{"--ref", "--plugin", "--no-focus", "--fuzztime"} {
		if !isFlag(word) {
			t.Errorf("%q is a flag and was not read as one", word)
		}
	}
}
