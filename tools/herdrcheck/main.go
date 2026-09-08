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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
)

// declaredMinimum is the Herdr the manifest says this plugin supports, or a
// note in its place. Read rather than compared: the report says both versions
// and leaves the reading to a person, because a semver comparison here would
// be a second opinion about what "supported" means.
func declaredMinimum(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, "herdr-plugin.toml"))
	if err != nil {
		return "a minimum the manifest could not be read for"
	}
	m := regexp.MustCompile(`(?m)^min_herdr_version = "([^"]+)"`).FindSubmatch(raw)
	if m == nil {
		return "no minimum the manifest declares"
	}
	return "herdr " + string(m[1])
}

// recordedVersion is the Herdr the wire-format recordings were captured from,
// taken from their own file names, or "" when they cannot be read or do not
// agree with each other.
//
// Empty rather than a guess: this only exists to say a refresh is due, and a
// wrong version would send somebody re-capturing against nothing.
func recordedVersion(root string) string {
	entries, err := os.ReadDir(filepath.Join(root, "internal", "herdrcli", "testdata"))
	if err != nil {
		return ""
	}
	from := regexp.MustCompile(`-([0-9]+\.[0-9]+\.[0-9]+)\.json$`)
	seen := ""
	for _, e := range entries {
		m := from.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if seen != "" && seen != m[1] {
			return ""
		}
		seen = m[1]
	}
	if seen == "" {
		return ""
	}
	return "herdr " + seen
}

// asker says what a command's help prints, and whether this Herdr has it at
// all. A seam: the real one runs Herdr, and the tests stand in for it, because
// what has to be held below is the counting and the exit status rather than
// the asking.
type asker func(command []string) (help string, ok bool)

func main() {
	bin := herdrcli.Bin()
	version, err := exec.Command(bin, "--version").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s --version did not run, so there is nothing to ask: %v\n", bin, err)
		os.Exit(2)
	}
	fmt.Printf("asking %s\n\n", strings.TrimSpace(string(version)))

	// Two more sources of commands, neither of them in Dependencies. The pages
	// send a reader to Herdr directly, and so do the plugin's own messages --
	// what to run when a machine's session is not up, where to look when the
	// daemon cannot be reached. The plugin runs none of those; it prints them
	// for somebody else to type, which is exactly why nothing was watching.
	docs, err := docCommands(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	said, err := messageCommands(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}

	ask := func(command []string) (string, bool) { return helpFor(bin, command) }
	os.Exit(report(os.Stdout, ask, herdrcli.Dependencies, docs, said,
		strings.TrimSpace(string(version)), declaredMinimum("."), recordedVersion("."),
		paneSchemaFields(bin)))
}

// report asks about everything and says what it found, returning what this
// should exit with.
//
// Away from where Herdr is actually run, so `make check` can hold the counting
// and the exit status on a machine with no Herdr on it -- and they were held by
// nothing at all. A statement-deletion sweep of this package removed the
// `os.Exit(1)` and every test still passed: a checker that lists what is wrong
// and then tells make everything is fine, which is `gofmt -l` exiting nought
// while printing the files it objects to. Removing either line that adds the
// pages' and the messages' problems to the count passed just as quietly.
func report(w io.Writer, ask asker, deps []herdrcli.Dependency, docs, said []toldCommand,
	asked, declared, recorded string, paneFields map[string]bool) int {
	problems := 0
	for _, dep := range deps {
		name := strings.Join(dep.Command, " ")

		text, ok := ask(dep.Command)
		if !ok {
			problems++
			fmt.Fprintf(w, "%-24s no such command in this Herdr\n", name)
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
			fmt.Fprintf(w, "%-24s no longer takes: %s\n", name, strings.Join(missing, ", "))
			continue
		}
		fmt.Fprintf(w, "%-24s ok\n", name)
	}

	fmt.Fprintln(w)
	problems += askAbout(w, ask, docs, "the pages give it at")
	fmt.Fprintln(w)
	problems += askAbout(w, ask, said, "a message gives it at")

	problems += askTheSchema(w, paneFields)

	fmt.Fprintln(w)
	total := len(deps) + len(docs) + len(said)
	if problems > 0 {
		fmt.Fprintf(w, "%d of %d commands are not what this plugin, its pages and its messages expect\n",
			problems, total)
		return 1
	}
	fmt.Fprintf(w, "all %d commands take what this plugin sends, and all %d it tells somebody "+
		"to run exist and take the %d flags given with them\n",
		len(deps), len(docs)+len(said), passedFlags(docs)+passedFlags(said))
	// The claim above is about ONE Herdr: the one installed here. The manifest
	// declares a minimum, and nothing asks that Herdr anything -- so a command
	// or a flag added after it would pass this and fail for somebody on the
	// version the plugin says it supports. Said here rather than left for a
	// reader to work out, because the sentence above reads like a claim about
	// the plugin and is a claim about this machine.
	fmt.Fprintf(w, "\nasked of %s. The manifest declares %s as the minimum this plugin "+
		"supports, and nothing here asks that one: install it to check.\n",
		asked, declared)
	// The parsers are held against RECORDINGS of what Herdr sent, and a
	// recording cannot notice the real thing changing shape. Their own comment
	// says refreshing them against a newer Herdr is the point -- and nothing
	// said when that was due, so they sat at one version while the machine
	// moved to another. Not a problem to fail on: a moved field is possible,
	// not proven, and only re-capturing says which.
	if recorded != "" && recorded != asked {
		fmt.Fprintf(w, "\nthe wire-format recordings in internal/herdrcli/testdata are "+
			"from %s. Re-capture them against %s: a field that moved shows up there and "+
			"nowhere else.\n", recorded, asked)
	}
	return 0
}

// askTheSchema holds the fields this plugin's parsers read against the ones
// Herdr's own bundled schema declares.
//
// The recordings in internal/herdrcli cannot do this. They are captures of what
// Herdr sent once, so they answer "does the parser read what 0.8.2 wrote" and
// go on answering it for ever -- a renamed field looks the same to them. The
// schema comes from the binary that is installed, offline and without a server,
// which is what makes this the check the recordings were standing in for.
//
// The field list is REFLECTED off the struct rather than written out again: a
// field added to the parser and not to this list would otherwise be unheld, and
// two lists of the same thing is the mistake this repository keeps finding.
//
// A field the schema no longer declares is a PROBLEM and not a note. Unlike the
// version numbers above, this is drift proven: the parser reads a name that the
// Herdr installed here says nothing about.
func askTheSchema(w io.Writer, declared map[string]bool) int {
	if declared == nil {
		fmt.Fprintf(w, "\n%-24s the schema could not be read, so the fields the "+
			"parsers depend on were not checked\n", "api schema")
		return 0
	}
	missing := []string{}
	for _, field := range paneJSONFields() {
		if !declared[field] {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(w, "\n%-24s does not declare %s, and herdrcli.Pane reads them\n",
			"api schema", strings.Join(missing, ", "))
		return len(missing)
	}
	fmt.Fprintf(w, "\n%-24s ok, all %d fields herdrcli.Pane reads are declared\n",
		"api schema", len(paneJSONFields()))
	return 0
}

// paneJSONFields is what herdrcli.Pane reads, taken from the struct itself.
func paneJSONFields() []string {
	var fields []string
	t := reflect.TypeOf(herdrcli.Pane{})
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			fields = append(fields, tag)
		}
	}
	return fields
}

// paneSchemaFields asks the installed Herdr for its bundled schema and returns
// the properties of the pane object a listing is made of, or nil when it cannot
// be had -- an older Herdr with no `api schema`, or one that answers something
// this cannot read.
func paneSchemaFields(bin string) map[string]bool {
	out, err := exec.Command(bin, "api", "schema", "--json").Output()
	if err != nil {
		return nil
	}
	var doc struct {
		Schemas struct {
			Success struct {
				Defs map[string]struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"$defs"`
			} `json:"success_response"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil
	}
	// The pane object a listing carries. Named rather than searched for: if
	// Herdr renames it, that is drift worth reporting rather than papering
	// over by finding whatever else looks pane-shaped.
	def, ok := doc.Schemas.Success.Defs["PaneInfo"]
	if !ok {
		return nil
	}
	fields := map[string]bool{}
	for name := range def.Properties {
		fields[name] = true
	}
	return fields
}

// askAbout asks Herdr about every command something tells somebody to run, and
// returns how many of them it no longer has or no longer takes.
func askAbout(w io.Writer, ask asker, told []toldCommand, gives string) int {
	problems := 0
	for _, one := range told {
		name := strings.Join(one.command, " ")
		help, ok := ask(one.command)
		if !ok {
			problems++
			fmt.Fprintf(w, "%-24s no such command in this Herdr, and %s sends somebody to it\n",
				name, strings.Join(one.where, ", "))
			continue
		}
		gone := []string{}
		for _, flag := range sortedFlags(one.flags) {
			if !hasFlag(help, flag) {
				gone = append(gone, fmt.Sprintf("%s (%s)", flag, strings.Join(one.flags[flag], ", ")))
			}
		}
		if len(gone) > 0 {
			problems++
			fmt.Fprintf(w, "%-24s no longer takes what it is given: %s\n", name, strings.Join(gone, ", "))
			continue
		}
		fmt.Fprintf(w, "%-24s ok, %s %s\n", name, gives, strings.Join(one.where, ", "))
	}
	return problems
}

// passedFlags counts the flags given with these commands.
func passedFlags(told []toldCommand) int {
	n := 0
	for _, one := range told {
		n += len(one.flags)
	}
	return n
}

// sortedFlags names the flags an invocation passes, in a fixed order so two
// runs against the same pages report the same thing.
func sortedFlags(flags map[string][]string) []string {
	out := make([]string, 0, len(flags))
	for flag := range flags {
		out = append(out, flag)
	}
	sort.Strings(out)
	return out
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
