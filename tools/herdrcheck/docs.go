package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// toldCommand is a `herdr ...` invocation something in this repository tells
// somebody to run, every place it is given, and the flags passed with it
// against the places that pass them. The pages give some of these and the
// plugin's own messages give the rest; both are read the same way, because
// both are somebody being told to type a command.
type toldCommand struct {
	command []string
	where   []string
	flags   map[string][]string
}

// docInvocation is one `herdr ...` as a single line writes it.
type docInvocation struct {
	command string
	flags   []string
}

// docCommands reads the documentation for the Herdr commands it hands a reader.
//
// These are not in herdrcli.Dependencies, which is what this plugin runs
// itself. `herdr plugin log list` is how the troubleshooting page has somebody
// find out why a daemon would not start, and `herdr terminal attach` is how it
// has them watch a stream by hand; the plugin runs neither, so neither is
// written down there. Nothing else held them either: the test that checks the
// commands in the documentation knows about `make` targets and test names,
// both of which are in this tree, and stops there. So Herdr renaming one of
// these leaves every check green and leaves the page sending somebody to a
// command that does not exist -- which is worse than no instruction, because
// they will believe the page and doubt their machine.
func docCommands(root string) ([]toldCommand, error) {
	pages := []string{filepath.Join(root, "README.md")}
	found, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		return nil, err
	}
	pages = append(pages, found...)

	at := map[string][]string{}
	flags := map[string]map[string][]string{}
	for _, page := range pages {
		raw, err := os.ReadFile(page)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(page)
		for i, line := range strings.Split(string(raw), "\n") {
			for _, found := range commandsIn(line) {
				place := fmt.Sprintf("%s:%d", name, i+1)
				at[found.command] = append(at[found.command], place)
				for _, flag := range found.flags {
					if flags[found.command] == nil {
						flags[found.command] = map[string][]string{}
					}
					flags[found.command][flag] = append(flags[found.command][flag], place)
				}
			}
		}
	}
	// Nothing found is not a clean bill of health. The documentation gives a
	// dozen of these, so an empty answer means this ran somewhere other than
	// the tree, or the shapes below stopped matching how the pages are
	// written -- and either way a checker that looks at nothing prints the
	// same reassuring line as one that looked and was satisfied.
	if len(at) == 0 {
		return nil, fmt.Errorf("no herdr commands found in %s or its docs, and the pages give several: "+
			"either this ran outside the tree or they are no longer written the way this reads them", root)
	}

	names := make([]string, 0, len(at))
	for name := range at {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]toldCommand, 0, len(names))
	for _, name := range names {
		out = append(out, toldCommand{command: strings.Fields(name), where: at[name], flags: flags[name]})
	}
	return out, nil
}

// commandsIn pulls the herdr invocations out of one line of documentation.
//
// Only where a command can start: the beginning of a line, after a prompt,
// inside the quotes of `ssh box 'herdr pane list'`, or in the `$(...)` of a
// path. In prose the word is preceded by another word, and those are not
// commands -- the menu says "herdr not found" against a machine and a listing
// says "no herdr found on the machine". Asking Herdr whether it still has
// `herdr found on the machine` reports drift that is not there, and a checker
// that cries about its own error messages is one whose output gets skimmed.
func commandsIn(line string) []docInvocation {
	return invocationsIn(line, startsHere)
}

// commandsInMessage pulls the herdr invocations out of a message this plugin
// prints, where a command is written in backticks and nothing else is.
//
// The rule the pages use does not carry over. There, a line beginning with the
// word is a command, because an indented block is how a page shows one. In a
// message the beginning of the string is where the sentence starts, and this
// plugin's sentences begin with the word: "herdr not found on the machine" is
// what a machine's row says, and reading it as a command asks Herdr whether it
// still has `not found on the machine`. Every instruction in these messages
// puts the command in backticks -- run `herdr session attach`, check `herdr
// plugin log list` -- because that is how a reader is shown where to stop
// typing, and it is the difference between the two.
func commandsInMessage(text string) []docInvocation {
	return invocationsIn(text, quotedHere)
}

func invocationsIn(line string, canStart func(before string) bool) []docInvocation {
	var out []docInvocation
	for i := range line {
		rest, ok := strings.CutPrefix(line[i:], "herdr")
		if !ok || !canStart(line[:i]) {
			continue
		}
		// The word has to be herdr and not merely begin with it:
		// herdr-remote-panes is this plugin's own log prefix, and herdr_bin is
		// a configuration key that appears in the same sentences.
		if !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
			continue
		}
		if words := subcommandsIn(rest); len(words) > 0 {
			out = append(out, docInvocation{
				command: strings.Join(words, " "),
				flags:   flagsIn(rest),
			})
		}
	}
	return out
}

// quotedHere reports whether a command begins here in a message: only just
// inside a backtick.
func quotedHere(before string) bool {
	before = strings.TrimRight(before, " \t")
	return strings.HasSuffix(before, "`")
}

// startsHere reports whether a command could begin after this much of a line.
func startsHere(before string) bool {
	before = strings.TrimRight(before, " \t")
	if before == "" {
		return true // the start of a line, or an indented block
	}
	if strings.HasSuffix(before, "&&") {
		return true
	}
	switch before[len(before)-1] {
	case '$', '(', '\'', '"', '`', '|', ';':
		// A prompt, a substitution, the quotes of an ssh command, a pipe.
		return true
	}
	return false
}

// subcommandsIn takes the leading subcommands of an invocation and stops at the
// first word that is not one.
//
// An argument is not a subcommand and neither is a flag: `herdr pane close
// wG:p3`, `herdr pane run <pane-id>`, `herdr plugin config-dir
// poorplebs.remote-panes` and `herdr plugin log list --plugin ...` are each the
// command only as far as that word. It has to stop at the quote or
// substitution the invocation sits in as well, or `herdr pane list` written
// inside `ssh box '...'` comes back with the closing quote stuck to it and is
// reported as a command Herdr does not have.
func subcommandsIn(rest string) []string {
	if cut := strings.IndexAny(rest, "'\"`)|;#"); cut >= 0 {
		rest = rest[:cut]
	}
	var words []string
	for _, word := range strings.Fields(rest) {
		if !isSubcommand(word) {
			break
		}
		words = append(words, word)
	}
	return words
}

// flagsIn takes the flags an invocation passes.
//
// From inside the invocation and nowhere else. The pages name flags in prose
// as well -- what `--limit` does, what would happen without `--ref` -- and a
// flag mentioned in a sentence is not one anybody is being told to type, so
// asking Herdr about it would report drift against a paragraph. Subcommands
// never begin with a dash, so the whole of what follows can be scanned: what
// is a flag and what is a subcommand cannot be confused.
func flagsIn(rest string) []string {
	if cut := strings.IndexAny(rest, "'\"`)|;#"); cut >= 0 {
		rest = rest[:cut]
	}
	var out []string
	for _, word := range strings.Fields(rest) {
		if name, _, _ := strings.Cut(word, "="); isFlag(name) {
			out = append(out, name)
		}
	}
	return out
}

// isFlag reports whether a word is a long flag: two dashes and then the shape
// of a name. A bare `--` ends the options rather than being one, and `...`
// after a placeholder is not a flag either.
func isFlag(word string) bool {
	name, ok := strings.CutPrefix(word, "--")
	if !ok || name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && !(c == '-' && i > 0) {
			return false
		}
	}
	return true
}

// isSubcommand reports whether a word has the shape of one: lowercase letters
// and hyphens, which is what every Herdr subcommand is and what no argument,
// flag, placeholder or plugin name in these pages is.
func isSubcommand(word string) bool {
	if word == "" {
		return false
	}
	for i := 0; i < len(word); i++ {
		if c := word[i]; (c < 'a' || c > 'z') && !(c == '-' && i > 0) {
			return false
		}
	}
	return true
}
