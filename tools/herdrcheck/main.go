// Command herdrcheck asks the installed Herdr whether it and this plugin still
// agree -- about the commands and values the plugin sends it, and about the
// values the plugin's manifest declares to it.
//
// Every command, flag and restricted value the plugin uses is written down in
// internal/herdrcli.Dependencies, and none of it is checked by anything that
// builds: a renamed flag or a value Herdr stopped accepting compiles perfectly
// and fails at the far end, one action at a time. The stand-in the tests run
// against cannot catch it either, being written from the same belief as the
// code -- it accepted `--placement popup` with a workspace, which is the pair
// Herdr refuses, and nothing opened. The placement itself is fine: Herdr's own
// PluginPanePlacement declares popup, and only its --help leaves it out, which
// is why the values here are checked against the schema and not against the
// help text.
//
// The manifest is the other contract and the earlier one: herdr-plugin.toml is
// what Herdr reads when it LOADS the plugin, and three of its settings take
// values Herdr itself defines -- an action's contexts, a pane's placement and
// the platforms. A value Herdr no longer knows is not refused in a way anybody
// sees, because the schema gives placement a default of "overlay": the machine
// menu would open as an overlay rather than the session-modal popup it needs in
// order to receive Escape, with every action still working.
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
	"slices"
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
		askHerdrSchema(bin), manifestValues(".")))
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
	asked, declared, recorded string, schema herdrSchema, manifest map[string][]string) int {
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
				if !accepts(schema, text, flag, value) {
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

	problems += askTheSchema(w, schema.PaneFields, schema.EnvelopeFields, schema.Enums)
	problems += askTheManifest(w, manifest, schema.Enums)

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
			"from %s, and re-capturing against %s is no longer the only way to see a field "+
			"move: the fields a pane carries and the ones the envelope is read by are "+
			"checked against this Herdr's schema above, every run. What only the recordings "+
			"hold now is the workspace and tab fields the parsers read -- workspace_id, "+
			"label, tab_id -- which the schema describes as WorkspaceInfo and TabInfo and "+
			"nothing here asks about yet.\n", recorded, asked)
	}
	return 0
}

// enumFor names the schema type that says which values a flag takes.
//
// Two vocabularies meet here -- what the command line calls a flag and what the
// schema calls its type -- so the pairing has to be written down once, and a
// pairing written down is a pairing that can go stale.
//
// askTheSchema reports a name the schema does not define. That is not
// decoration: without it, a type Herdr renames sends accepts quietly back to
// the help text, which is the source this whole check exists to stop trusting
// -- and the first thing that would happen is `--placement popup` being called
// drift again, by a check that had silently stopped asking the authority.
var enumFor = map[string]string{
	"--placement": "PluginPanePlacement",
	"--state":     "PaneAgentState",
	"--direction": "SplitDirection",
}

// manifestEnumFor names the schema type behind each manifest setting whose
// values Herdr declares.
//
// The manifest is this plugin's OTHER contract with Herdr, and the earlier one.
// Everything else here asks whether the commands and values the plugin SENDS at
// run time still exist; nothing had ever looked at the file Herdr READS at load
// time to decide which actions there are, where a pane opens and on which
// platforms. A value Herdr does not know is not refused loudly: the schema
// gives placement a default of "overlay", so the machine menu would open as an
// overlay rather than the session-modal popup it needs in order to get Escape,
// and nothing anywhere would say why.
//
// Paired by hand like enumFor, and carrying the same trap: a pairing whose type
// the schema stops defining is REPORTED rather than quietly skipped, or this
// check would go on saying ok while asking nothing.
var manifestEnumFor = map[string]string{
	"platforms": "PluginPlatform",
	"contexts":  "PluginActionContext",
	"placement": "PluginPanePlacement",
}

// manifestSetting matches one `key = ...` line of the manifest and
// manifestQuoted the strings on it, which together read both forms this file
// uses: a bare value and an array of them.
//
// Enough TOML for the question and no more. The forms it cannot read -- a value
// spread over several lines, a quoted or dotted key -- are ones this manifest
// does not use, and a manifest that adopted one would leave a paired setting
// with nothing found for it, which is what the scan's own test holds against
// the real file.
var (
	manifestSetting = regexp.MustCompile(`(?m)^[ \t]*([a-z_]+)[ \t]*=[ \t]*(.*)$`)
	manifestQuoted  = regexp.MustCompile(`"([^"]*)"`)
)

// manifestValues reads the paired settings out of the manifest, as the setting
// name against every value the file gives it. A manifest that cannot be read at
// all answers nil, which askTheManifest reports as unchecked rather than as
// drift.
func manifestValues(root string) map[string][]string {
	raw, err := os.ReadFile(filepath.Join(root, "herdr-plugin.toml"))
	if err != nil {
		return nil
	}
	found := map[string][]string{}
	for _, line := range manifestSetting.FindAllStringSubmatch(string(raw), -1) {
		key := line[1]
		if _, paired := manifestEnumFor[key]; !paired {
			continue
		}
		for _, value := range manifestQuoted.FindAllStringSubmatch(line[2], -1) {
			found[key] = append(found[key], value[1])
		}
	}
	return found
}

// askTheManifest holds what the manifest declares against what Herdr says it
// accepts.
func askTheManifest(w io.Writer, values, enums map[string][]string) int {
	switch {
	case values == nil:
		fmt.Fprintf(w, "\n%-24s could not be read, so the values it declares went "+
			"unchecked\n", "plugin manifest")
		return 0
	case enums == nil:
		fmt.Fprintf(w, "\n%-24s the schema could not be read, so the values the "+
			"manifest declares went unchecked\n", "plugin manifest")
		return 0
	}

	stale, wrong, silent, checked := []string{}, []string{}, []string{}, 0
	for setting, def := range manifestEnumFor {
		declared, known := enums[def]
		if !known {
			stale = append(stale, setting+" -> "+def)
			continue
		}
		if len(values[setting]) == 0 {
			silent = append(silent, setting)
		}
		for _, value := range values[setting] {
			checked++
			if !slices.Contains(declared, value) {
				wrong = append(wrong, fmt.Sprintf("%s = %q", setting, value))
			}
		}
	}
	sort.Strings(stale)
	sort.Strings(wrong)
	sort.Strings(silent)

	if len(stale) > 0 {
		fmt.Fprintf(w, "\n%-24s does not define %s, so what the manifest declares "+
			"there is checked against nothing\n", "plugin manifest",
			strings.Join(stale, ", "))
		return len(stale)
	}
	if len(wrong) > 0 {
		fmt.Fprintf(w, "\n%-24s declares %s, which this Herdr does not accept\n",
			"plugin manifest", strings.Join(wrong, ", "))
		return len(wrong)
	}
	// A count of nought is not an answer to this question. The manifest was
	// READ -- an unreadable one is nil and reported above -- and none of the
	// settings this knows about were found in it, which for this repository can
	// only mean the scan has stopped matching the file rather than that the file
	// stopped declaring anything. Reported, because the alternative is the
	// sentence below with a nought in it, and success and total failure would
	// then differ by one character in a line that opens "ok". `make check`
	// catches this through the scan's own test; `make herdr` is what somebody
	// runs when they suspect drift, and it has to be able to say it found none
	// because there was none rather than because it looked at nothing.
	if checked == 0 {
		fmt.Fprintf(w, "\n%-24s was read and none of the %d settings this checks were "+
			"found in it, so the scan is no longer matching what the file says\n",
			"plugin manifest", len(manifestEnumFor))
		return 1
	}
	if len(silent) > 0 {
		fmt.Fprintf(w, "\n%-24s ok, all %d values it declares are ones Herdr declares "+
			"too -- but nothing was found for %s, so that setting is unchecked\n",
			"plugin manifest", checked, strings.Join(silent, ", "))
		return 0
	}
	fmt.Fprintf(w, "\n%-24s ok, all %d values it declares are ones Herdr declares too\n",
		"plugin manifest", checked)
	return 0
}

// accepts reports whether Herdr takes this value for this flag.
//
// The SCHEMA first, because it is the authority and the help text is not: Herdr
// 0.9.0's `plugin pane open --help` lists overlay, split, tab and zoomed as the
// possible placements, and its own PluginPanePlacement declares those four and
// popup -- which the binary accepts and this plugin's menu has always sent. A
// check reading the help alone called that drift and had to be given an
// exception; reading the schema, there is nothing to except.
//
// Help remains the answer for every flag with no enum behind it, which is most
// of them.
func accepts(schema herdrSchema, help, flag, value string) bool {
	if def, ok := enumFor[flag]; ok {
		if values, known := schema.Enums[def]; known {
			for _, one := range values {
				if one == value {
					return true
				}
			}
			return false
		}
	}
	return takesValue(help, flag, value)
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
func askTheSchema(w io.Writer, declared, envelope map[string]bool, enums map[string][]string) int {
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
	// And the envelope every answer arrives in. Checked here rather than left
	// to the recordings, which are a capture of one version: a rename at the
	// far end would empty every error code, so every refusal would read as one
	// this plugin does not recognise -- and ignoreNotFound would stop treating
	// "already gone" as success, which is the whole of how a reconciling pass
	// tolerates a pane somebody closed by hand.
	//
	// Nil is "not checked" rather than "nothing is missing", as above.
	if envelope != nil {
		gone := []string{}
		for _, field := range envelopeJSONFields() {
			if !envelope[field] {
				gone = append(gone, field)
			}
		}
		if len(gone) > 0 {
			fmt.Fprintf(w, "\n%-24s does not declare %s on the response envelope, and the "+
				"parsers read them\n", "api schema", strings.Join(gone, ", "))
			return len(gone)
		}
	}
	// And every schema type this checker pairs a flag with is one the schema
	// still defines. A pairing that has gone stale sends accepts back to the
	// help text without saying so.
	stale := []string{}
	if enums != nil {
		for flag, def := range enumFor {
			if _, ok := enums[def]; !ok {
				stale = append(stale, flag+" -> "+def)
			}
		}
		sort.Strings(stale)
	}
	if len(stale) > 0 {
		fmt.Fprintf(w, "\n%-24s does not define %s, so those values fall back to the "+
			"help text, which is not the authority\n", "api schema", strings.Join(stale, ", "))
		return len(stale)
	}

	// And every status Herdr says it can REPORT is one AgentState knows by
	// name. It maps what a remote pane says onto the four states pane
	// report-agent accepts, and anything it does not recognise becomes
	// "unknown" -- which is right for a value that means nothing here and
	// wrong for one Herdr has just started using. Herdr reports five today and
	// accepts four; "done" is the one that needs a decision, and AgentState
	// makes it, calling a finished agent idle.
	//
	// A status that maps to "unknown" without BEING "unknown" fell through the
	// default, which is how a new one would arrive: silently, as a machine
	// whose agent shows nothing in the sidebar.
	unnamed := []string{}
	for _, status := range enums["AgentStatus"] {
		if status != "unknown" && herdrcli.AgentState(status) == "unknown" {
			unnamed = append(unnamed, status)
		}
	}
	sort.Strings(unnamed)
	if len(unnamed) > 0 {
		fmt.Fprintf(w, "\n%-24s reports %s and herdrcli.AgentState does not name %s, so "+
			"a machine's agent in that state shows as unknown here\n",
			"api schema", strings.Join(unnamed, ", "),
			map[bool]string{true: "it", false: "them"}[len(unnamed) == 1])
		return len(unnamed)
	}

	fmt.Fprintf(w, "\n%-24s ok, all %d fields herdrcli.Pane reads are declared and all %d "+
		"the envelope is read by, every flag with an enum has one, and every status Herdr "+
		"reports has a name\n",
		"api schema", len(paneJSONFields()), len(envelopeJSONFields()))
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

// herdrSchema is what the bundled schema says that this plugin depends on: the
// fields a pane carries, and the values each flag takes.
//
// Zero when it could not be had -- an older Herdr with no `api schema` -- and
// every reader has to treat that as "not checked" rather than as "nothing is
// declared".
type herdrSchema struct {
	PaneFields     map[string]bool
	EnvelopeFields map[string]bool
	Enums          map[string][]string
}

// askHerdrSchema asks the installed Herdr for its bundled schema.
func askHerdrSchema(bin string) herdrSchema {
	return herdrSchema{
		PaneFields:     paneSchemaFields(bin),
		EnvelopeFields: envelopeSchemaFields(bin),
		Enums:          schemaEnums(bin),
	}
}

// schemaEnums is every named type in the schema that lists the strings it
// accepts, by name.
func schemaEnums(bin string) map[string][]string {
	out, err := exec.Command(bin, "api", "schema", "--json").Output()
	if err != nil {
		return nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil
	}
	found := map[string][]string{}
	var walk func(node json.RawMessage, name string)
	walk = func(node json.RawMessage, name string) {
		var obj map[string]json.RawMessage
		if json.Unmarshal(node, &obj) != nil {
			return
		}
		if raw, ok := obj["enum"]; ok {
			var values []string
			if json.Unmarshal(raw, &values) == nil && name != "" {
				found[name] = values
			}
		}
		for key, child := range obj {
			// Under $defs the key is the type's name; anywhere else it is a
			// field and the name above it still applies.
			next := name
			if name == "$defs" || key == "$defs" {
				next = key
			}
			if name == "$defs" {
				next = key
			}
			walk(child, next)
		}
	}
	for key, child := range doc {
		walk(child, key)
	}
	return found
}

// envelopeSchemaFields is what the installed Herdr declares about the envelope
// it wraps every answer in: the two top-level names, and the error body's own.
//
// Read because the parsers depend on these and nothing was asking. The pane
// fields have been checked here since the schema arrived; the envelope was
// left to the RECORDINGS, which are captures of one version and cannot notice
// the real thing changing shape. The schema can, and it is printed offline by
// the binary already installed.
func envelopeSchemaFields(bin string) map[string]bool {
	out, err := exec.Command(bin, "api", "schema", "--json").Output()
	if err != nil {
		return nil
	}
	var doc struct {
		Schemas struct {
			Success struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"success_response"`
			Error struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Defs       struct {
					ErrorBody struct {
						Properties map[string]json.RawMessage `json:"properties"`
					} `json:"ErrorBody"`
				} `json:"$defs"`
			} `json:"error_response"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil
	}
	found := map[string]bool{}
	for name := range doc.Schemas.Success.Properties {
		found[name] = true
	}
	for name := range doc.Schemas.Error.Properties {
		found[name] = true
	}
	for name := range doc.Schemas.Error.Defs.ErrorBody.Properties {
		found["error."+name] = true
	}
	return found
}

// envelopeJSONFields is what this plugin reads off an envelope.
//
// "result" and "error" are the two the unwrapping asks for by name; the rest
// come off ErrorBody, so a field added there is checked without anybody
// remembering to add it here.
func envelopeJSONFields() []string {
	fields := []string{"result", "error"}
	t := reflect.TypeOf(herdrcli.ErrorBody{})
	for i := 0; i < t.NumField(); i++ {
		if tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
			fields = append(fields, "error."+tag)
		}
	}
	sort.Strings(fields)
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
