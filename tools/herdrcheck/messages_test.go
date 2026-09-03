package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// source is this plugin's messages written the way it writes them: the
// instruction in backticks, one of them split across two literals to fit the
// line, one built around a value, and among them the sentences that begin with
// the word "herdr" without naming a command at all.
//
// Kept in those shapes rather than reduced to one of each. What this reads is
// Go source as the plugin writes it, and a fixture tidied into what the reader
// expects agrees with the reader about a format neither of them decides.
const source = "" +
	"package sample\n" +
	"\n" +
	"var advice = []string{\n" +
	"\t\"no herdr session on the machine — run `herdr session attach` \" +\n" +
	"\t\t\"there, or turn auto_start on and this will\",\n" +
	"\t\"%s not tried again on %s own: run `herdr plugin action \" +\n" +
	"\t\t\"invoke %s.connect` for every machine\",\n" +
	"\t\"no running daemon (is the plugin enabled? check `herdr plugin log list --plugin %s`): %w\",\n" +
	"\t\"herdr not found on the machine\",\n" +
	"\t\"no herdr on the remote host\",\n" +
	"\t\"herdr %s: unreadable response: %s\",\n" +
	"\t\" · herdr not found\",\n" +
	"}\n" +
	"\n" +
	"var built = \"Check `herdr plugin log list --plugin \" + pluginID + \"`.\"\n" +
	"\n" +
	"var pluginID = \"poorplebs.remote-panes\"\n"

func messagesIn(t *testing.T, text string) []toldCommand {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := messageCommands(dir)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestTheCommandsTheMessagesGiveAreRead(t *testing.T) {
	got := map[string]bool{}
	for _, c := range messagesIn(t, source) {
		got[strings.Join(c.command, " ")] = true
	}

	for _, want := range []string{
		"session attach",       // in backticks, mid-sentence
		"plugin action invoke", // split across two literals to fit the line
		"plugin log list",      // in backticks, with a flag after it
	} {
		if !got[want] {
			t.Errorf("a message says to run `herdr %s` and it was not read", want)
		}
		delete(got, want)
	}
	// What is left is prose read as an instruction: it reports drift against
	// this plugin's own words, every run, until nobody reads the report.
	for leftover := range got {
		t.Errorf("`herdr %s` is not a command any message tells somebody to run", leftover)
	}
}

func TestProseInAMessageIsNotACommand(t *testing.T) {
	// A message begins with the word where a page would begin with a command:
	// "herdr not found on the machine" is a machine's row, and asking Herdr
	// whether it has `not found on the machine` is this checker complaining
	// about the thing it is meant to be checking.
	for _, text := range []string{
		"herdr not found on the machine",
		"no herdr on the remote host",
		"herdr %s: timed out after %s",
		" · herdr not found",
		"%s: no herdr on the machine's PATH, so plain ssh terminals; ",
	} {
		for _, got := range commandsInMessage(text) {
			t.Errorf("read %q as the command `herdr %s`", text, got.command)
		}
	}
}

func TestAnInstructionSplitAcrossLiteralsIsReadWhole(t *testing.T) {
	// The failure this is here for: a message is wrapped where the line runs
	// out, not where the command ends. Reading the halves apart finds `plugin
	// action` -- a real command, so nothing complains -- and loses the word
	// that says which one, which is the word a rename would take.
	var names []string
	for _, c := range messagesIn(t, source) {
		names = append(names, strings.Join(c.command, " "))
	}
	if !slices.Contains(names, "plugin action invoke") {
		t.Errorf("the split instruction was not read whole; got %v", names)
	}
}

func TestAComputedWordDoesNotBecomeACommand(t *testing.T) {
	// A message that builds its own verb says nothing about what Herdr takes,
	// and joining the literals across the gap would invent one.
	text := "package sample\n\nvar s = \"run `herdr \" + verb + \" now`\"\n\nvar verb = \"open\"\n"
	for _, c := range messagesIn(t, text) {
		t.Errorf("a computed word was read as the command `herdr %s`", strings.Join(c.command, " "))
	}
}

func TestTheFlagsTheMessagesGiveAreRead(t *testing.T) {
	for _, c := range messagesIn(t, source) {
		if strings.Join(c.command, " ") != "plugin log list" {
			continue
		}
		if _, ok := c.flags["--plugin"]; !ok {
			t.Error("a message passes --plugin to `herdr plugin log list` and it was not read")
		}
		return
	}
	t.Error("`herdr plugin log list` was not read at all, so its flags were not either")
}

func TestWhatIsNotAMessageIsNotRead(t *testing.T) {
	// The skip has to be proved on a tree that would otherwise trip it. In
	// this repository nothing under tools/ writes a command in backticks, so
	// an assertion about the real tree passes whether the skip works or not --
	// it did, and only a mutation removing the skip said so.
	//
	// What lives there matters: a checker's own strings are about commands
	// rather than instructions to run them, and the fake Herdr's exist to be
	// matched by tests. Holding this plugin to either is holding it to a
	// stand-in written from the same belief as the code.
	dir := t.TempDir()
	shown := "package sample\n\nvar s = \"run `herdr session attach` there\"\n"
	hidden := "package sample\n\nvar s = \"run `herdr worktree prune` first\"\n"
	for path, text := range map[string]string{
		"shown.go":                 shown,
		"tools/checker/sample.go":  hidden,
		"internal/x/testdata/y.go": hidden,
	} {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	found, err := messageCommands(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range found {
		names = append(names, strings.Join(c.command, " "))
	}
	if !slices.Contains(names, "session attach") {
		t.Errorf("the message that is shown to somebody was not read; got %v", names)
	}
	if slices.Contains(names, "worktree prune") {
		t.Errorf("a string under tools/ or testdata/ was read as a message; got %v", names)
	}
}

func TestTheRealMessagesStillNameCommands(t *testing.T) {
	// The fixture proves the reader understands these shapes; this proves the
	// messages are still written in them. It also stands for the walk: a skip
	// that grew to cover internal/ would leave every test above passing while
	// the checker looked at nothing.
	found, err := messageCommands(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 3 {
		t.Errorf("found %d commands in this plugin's messages, which is fewer than there are; "+
			"either the walk stopped reaching them or they stopped being written in backticks", len(found))
	}
	// Neither this checker's own strings nor the fake Herdr's are messages
	// anybody is shown, and reading them would hold the plugin to a stand-in.
	for _, c := range found {
		for _, where := range c.where {
			if strings.HasPrefix(where, "tools/") || strings.Contains(where, "testdata") {
				t.Errorf("`herdr %s` was read from %s, which is not a message anybody is shown",
					strings.Join(c.command, " "), where)
			}
		}
	}
}
