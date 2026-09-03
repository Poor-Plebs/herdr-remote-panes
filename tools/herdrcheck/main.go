// Command herdrcheck asks the installed Herdr whether it still takes what this
// plugin sends it.
//
// Every command, flag and restricted value the plugin uses is written down in
// internal/herdrcli.Dependencies, and none of it is checked by anything that
// builds: a renamed flag or a value Herdr stopped accepting compiles perfectly
// and fails at the far end, one action at a time. The stand-in the tests run
// against cannot catch it either, being written from the same belief as the
// code -- it accepted `--placement popup` for as long as the code sent it,
// while the real thing refused it and nothing opened.
//
// Not part of `make check`: it needs Herdr on the machine, and a check that
// cannot run everywhere is one that gets ignored where it can.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
)

func main() {
	bin := herdrcli.Bin()
	version, err := exec.Command(bin, "--version").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s --version did not run, so there is nothing to ask: %v\n", bin, err)
		os.Exit(2)
	}
	fmt.Printf("asking %s\n\n", strings.TrimSpace(string(version)))

	problems := 0
	for _, dep := range herdrcli.Dependencies {
		name := strings.Join(dep.Command, " ")

		text, ok := helpFor(bin, dep.Command)
		if !ok {
			problems++
			fmt.Printf("%-24s no such command in this Herdr\n", name)
			continue
		}

		missing := []string{}
		for _, flag := range dep.Flags {
			// Word-bounded: --focus is a prefix of --focus-something, and
			// --plugin appears inside "plugin pane open" in the usage line.
			if !hasFlag(text, flag) {
				missing = append(missing, flag)
			}
		}
		for flag, values := range dep.Values {
			for _, value := range values {
				if !takesValue(text, flag, value) {
					missing = append(missing, flag+"="+value)
				}
			}
		}
		if len(missing) > 0 {
			problems++
			fmt.Printf("%-24s no longer takes: %s\n", name, strings.Join(missing, ", "))
			continue
		}
		fmt.Printf("%-24s ok\n", name)
	}

	// The pages send a reader to Herdr commands this plugin never runs, so
	// they are not in Dependencies and nothing above has looked at them. Only
	// the commands: a flag the documentation passes is not checked here, and
	// that gap is worth knowing about rather than being papered over by a
	// summary line that says everything was looked at.
	docs, err := docCommands(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	fmt.Println()
	for _, doc := range docs {
		name := strings.Join(doc.command, " ")
		if _, ok := helpFor(bin, doc.command); !ok {
			problems++
			fmt.Printf("%-24s no such command in this Herdr, and %s sends somebody to it\n",
				name, strings.Join(doc.where, ", "))
			continue
		}
		fmt.Printf("%-24s ok, given in %s\n", name, strings.Join(doc.where, ", "))
	}

	fmt.Println()
	if problems > 0 {
		fmt.Printf("%d of %d commands are not what this plugin and its pages expect\n",
			problems, len(herdrcli.Dependencies)+len(docs))
		os.Exit(1)
	}
	fmt.Printf("all %d commands take what this plugin sends, and all %d the pages give exist\n",
		len(herdrcli.Dependencies), len(docs))
}

// helpFor asks a command for its own help, and reports whether this Herdr has
// it at all.
//
// --help rather than running it: this must not change anything, and a plugin's
// checker that opened a pane to find out would be worse than the drift it
// looks for.
//
// Two ways Herdr says it has no such command, and neither is an exit status on
// its own. An unknown subcommand under a known parent exits non-zero and
// prints the parent's list of commands; a command that is one unknown word
// prints the top-level help and exits ZERO. What both have in common is that
// the output does not name the command, which is the first thing a command's
// own help does.
//
// Herdr itself is proved to run by --version before any of this, so a command
// that does not answer here is a command this Herdr does not have.
func helpFor(bin string, command []string) (string, bool) {
	name := strings.Join(command, " ")
	out, err := exec.Command(bin, append(append([]string{}, command...), "--help")...).CombinedOutput()
	text := string(out)
	return text, err == nil && strings.Contains(text, "herdr "+name)
}

// hasFlag reports whether the help declares this flag, rather than merely
// containing those letters. Herdr lists one per line as "      --flag <VALUE>".
func hasFlag(help, flag string) bool {
	for _, line := range strings.Split(help, "\n") {
		field := strings.TrimSpace(line)
		if field == flag || strings.HasPrefix(field, flag+" ") || strings.HasPrefix(field, flag+"=") {
			return true
		}
	}
	return false
}

// takesValue reports whether the possible values printed under a flag include
// this one.
//
// The values are on a line of their own after the flag, so the flag has to be
// found first: reading every "possible values" line in the help and asking
// whether the value is in any of them accepts a value that belongs to a
// different flag entirely -- which is how --focus looked like it took "herdr"
// and "pane", values that belong to --right-click two lines above.
func takesValue(help, flag, value string) bool {
	lines := strings.Split(help, "\n")
	for i, line := range lines {
		field := strings.TrimSpace(line)
		if field != flag && !strings.HasPrefix(field, flag+" ") && !strings.HasPrefix(field, flag+"=") {
			continue
		}
		for _, following := range lines[i+1:] {
			following = strings.TrimSpace(following)
			if strings.HasPrefix(following, "--") {
				break // the next flag: this one had no values
			}
			if !strings.HasPrefix(following, "[possible values:") {
				continue
			}
			list := strings.TrimSuffix(strings.TrimPrefix(following, "[possible values:"), "]")
			for _, allowed := range strings.Split(list, ",") {
				if strings.TrimSpace(allowed) == value {
					return true
				}
			}
			return false
		}
	}
	return false
}
