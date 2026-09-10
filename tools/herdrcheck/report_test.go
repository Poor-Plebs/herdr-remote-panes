package main

import (
	"path/filepath"
	"slices"
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
		theAskedVersion, theDeclaredMinimum, theRecordedVersion, aSchemaThatDeclaresEverything(), theManifest(),
	)
	return out.String(), code
}

// The two versions a run names: the one it asked, and the one the manifest
// says this plugin supports. Written out rather than read from anywhere, so
// the assertions below cannot pass by comparing a value with itself.
const (
	theAskedVersion    = "herdr 0.9.0"
	theDeclaredMinimum = "herdr 0.8.0"
	theRecordedVersion = "herdr 0.8.2"
)

// TestACleanRunSaysWhichHerdrItAsked holds the scope of the sentence it prints.
//
// "all N commands take what this plugin sends" reads as a claim about the
// plugin and is a claim about ONE Herdr: the one installed on the machine that
// ran it. The manifest declares a minimum, and nothing asks that Herdr
// anything, so a command or flag added after it passes here and fails for
// somebody on the version the plugin says it supports. The report says both
// now, which is the difference between a check and a check somebody can read
// the scope of.
func TestACleanRunSaysWhichHerdrItAsked(t *testing.T) {
	out, code := aRun(t, answering(everything()))
	if code != 0 {
		t.Fatalf("a clean run exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, theAskedVersion) {
		t.Errorf("the report does not say which Herdr it asked (%q):\n%s",
			theAskedVersion, out)
	}
	if !strings.Contains(out, theDeclaredMinimum) {
		t.Errorf("the report does not say the minimum the manifest declares (%q), so "+
			"its sentence reads as a claim about every supported Herdr:\n%s",
			theDeclaredMinimum, out)
	}
	// And it says the minimum is not what was asked, rather than printing two
	// versions and leaving them to be read as one checked range.
	if !strings.Contains(out, "nothing here asks that one") {
		t.Errorf("the report names both versions without saying which of them was "+
			"actually asked:\n%s", out)
	}
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
	// And a clean run carries NONE of the drift sentences, which is the zero
	// edge these checks were missing. Each decides with `len(x) > 0`, which
	// reads as obviously right; a mutation sweep of this package found that
	// all of them could be `>= 0` with nothing failing. That prints a sentence
	// about an empty list on every clean run -- "does not name them" with
	// nothing before it -- and returns the same nought, so everything watching
	// the exit code goes on passing and the line is the only place it shows.
	schema := reportLine(t, out, "api schema")
	if !strings.Contains(schema, "ok,") {
		t.Errorf("a clean run's api schema line does not say ok: %q", schema)
	}
	if strings.Contains(schema, "does not name") {
		t.Errorf("a clean run reports a status the mapping does not name: %q", schema)
	}
	manifest := reportLine(t, out, "plugin manifest")
	if !strings.Contains(manifest, "ok,") {
		t.Errorf("a clean run's plugin manifest line does not say ok: %q", manifest)
	}
	if strings.Contains(manifest, "nothing was found for") {
		t.Errorf("a clean run says one of the manifest's settings went "+
			"unchecked: %q", manifest)
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
	// The other half of what Dependencies records: a flag can still be there
	// while the value this plugin sends for it is gone.
	//
	// Modelled by taking it out of the SCHEMA rather than out of the help,
	// because the schema is what says which values a flag takes. This test
	// used to remove it from the help and to cite `--placement popup` as the
	// case -- on the belief that Herdr refused popup, which it does not: its
	// own PluginPanePlacement declares popup, the binary accepts it, and only
	// the help text leaves it out.
	enums := theEnums()
	enums["SplitDirection"] = []string{"down"}

	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{
			Command: []string{"pane", "split"},
			Flags:   []string{"--direction"},
			Values:  map[string][]string{"--direction": {"right"}},
		}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		herdrSchema{PaneFields: everyPaneField(), Enums: enums}, theManifest())

	if code == 0 {
		t.Errorf("a value Herdr stopped taking exited nought:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "--direction=right") {
		t.Errorf("the value that went is not named:\n%s", out.String())
	}
	if strings.Contains(out.String(), "take what this plugin sends") {
		t.Errorf("a value that went still says everything is well:\n%s", out.String())
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

// TestARunSaysWhenTheRecordingsAreDueARefresh holds the one kind of drift this
// tool cannot ask Herdr about.
//
// The parsers in internal/herdrcli are held against RECORDINGS of what Herdr
// sent, and a recording cannot notice the real thing changing shape -- their
// own comment says refreshing them against a newer Herdr is the point. Nothing
// said when that was due, so they sat at 0.8.2 while the Herdr on the machine
// became 0.9.0 and every test went on passing.
//
// It is a note and not a problem: a moved field is possible rather than proven.
// So the exit code stays nought and the run still says everything else was fine.
//
// It used to say only re-capturing could tell, and that is no longer true --
// the fields a pane carries and the ones the envelope is read by are checked
// against the installed Herdr's schema on every run. So this holds what the
// note IDENTIFIES rather than how it is phrased: both versions, so a reader
// can see what is stale against what, and the names that really are held only
// by the recordings. Matching the old imperative was what broke here when the
// sentence stopped overstating.
func TestARunSaysWhenTheRecordingsAreDueARefresh(t *testing.T) {
	out, code := aRun(t, answering(everything()))
	if code != 0 {
		t.Fatalf("a clean run exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, theRecordedVersion) {
		t.Errorf("the report does not say which Herdr the recordings came from (%q):\n%s",
			theRecordedVersion, out)
	}
	if !strings.Contains(out, theAskedVersion) {
		t.Errorf("the report does not name the Herdr it asked (%q), so a reader cannot "+
			"tell what the recordings are stale against:\n%s", theAskedVersion, out)
	}
	// And what is genuinely left to them, which is the actionable half.
	for _, name := range []string{"workspace_id", "tab_id"} {
		if !strings.Contains(out, name) {
			t.Errorf("the note does not say %q is still held only by the recordings:\n%s",
				name, out)
		}
	}
}

// TestRecordingsMatchingTheHerdrAskedAreNotMentioned is the other half: a
// version that agrees is not something to tell anybody about, and a note that
// appears whatever the versions are would be read as noise and then not read.
func TestRecordingsMatchingTheHerdrAskedAreNotMentioned(t *testing.T) {
	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil,
		theAskedVersion, theDeclaredMinimum, theAskedVersion, aSchemaThatDeclaresEverything(), theManifest())
	if code != 0 {
		t.Fatalf("a clean run exited %d:\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "Re-capture") {
		t.Errorf("the recordings match the Herdr asked and the report still asks for a "+
			"refresh:\n%s", out.String())
	}
}

// everyPaneField is a schema that declares everything herdrcli.Pane reads,
// built from the struct so a field added to the parser is added here too.
func everyPaneField() map[string]bool {
	declared := map[string]bool{}
	for _, f := range paneJSONFields() {
		declared[f] = true
	}
	return declared
}

// theEnums is what Herdr 0.9.0 declares for the types this check asks about,
// written out rather than read from the binary: a test that asks the installed
// Herdr says nothing about what the check does with an answer.
//
// The first three are the flags this plugin sends values with. AgentStatus is
// not a flag at all -- it is what Herdr REPORTS a pane's agent to be, and it is
// here because the check holds AgentState against it. Note that it has five
// values and PaneAgentState has four: that gap is the whole reason AgentState
// exists. The last two are not run-time values either: they are what the
// MANIFEST declares, read by Herdr when it loads the plugin.
func theEnums() map[string][]string {
	return map[string][]string{
		"PluginPanePlacement": {"overlay", "popup", "split", "tab", "zoomed"},
		"PaneAgentState":      {"blocked", "idle", "unknown", "working"},
		"SplitDirection":      {"down", "right"},
		"AgentStatus":         {"blocked", "done", "idle", "unknown", "working"},
		"PluginPlatform":      {"linux", "macos", "windows"},
		"PluginActionContext": {"global", "workspace", "tab", "pane", "selection"},
	}
}

// theManifest is what herdr-plugin.toml declares for the settings whose values
// Herdr's schema constrains, written out so a test says what it means rather
// than depending on the real file. TestTheManifestScanReadsTheRealFile is what
// keeps the two in step.
func theManifest() map[string][]string {
	return map[string][]string{
		"platforms": {"linux", "macos"},
		"contexts":  {"global", "workspace", "selection"},
		"placement": {"popup", "tab"},
	}
}

// everyEnvelopeField is a schema that declares the whole envelope.
//
// Built from what the parsers read, like everyPaneField: a fixture written out
// by hand would be a second copy of the same list, and the check would pass
// against whichever of the two was wrong.
func everyEnvelopeField() map[string]bool {
	declared := map[string]bool{}
	for _, f := range envelopeJSONFields() {
		declared[f] = true
	}
	return declared
}

func aSchemaThatDeclaresEverything() herdrSchema {
	return herdrSchema{
		PaneFields:     everyPaneField(),
		EnvelopeFields: everyEnvelopeField(),
		Enums:          theEnums(),
	}
}

// TestAnEnvelopeFieldTheSchemaDropsIsDrift holds the response envelope the way
// the pane fields have been held since the schema arrived.
//
// The envelope was left to the RECORDINGS, which are a capture of one version
// and cannot notice the real thing changing shape. What a rename there costs is
// not a parse error: Code is what IsNotFound reads, so every refusal would stop
// being recognised as "already gone" and a reconciling pass would treat a pane
// somebody closed by hand as a failure.
func TestAnEnvelopeFieldTheSchemaDropsIsDrift(t *testing.T) {
	short := everyEnvelopeField()
	delete(short, "error.code")

	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		herdrSchema{PaneFields: everyPaneField(), EnvelopeFields: short, Enums: theEnums()},
		theManifest())

	if code == 0 {
		t.Errorf("a schema that no longer declares error.code passed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "error.code") {
		t.Errorf("the report does not name the field that went:\n%s", out.String())
	}
	// The control: with it declared, nothing is said and the run is clean.
	var fine strings.Builder
	if code := report(&fine, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		aSchemaThatDeclaresEverything(), theManifest()); code != 0 {
		t.Errorf("a schema declaring the whole envelope exited %d:\n%s", code, fine.String())
	}
	if strings.Contains(fine.String(), "on the response envelope") {
		t.Errorf("a complete envelope was reported as drift:\n%s", fine.String())
	}
}

// TestAValueTheSchemaDoesNotDeclareIsDrift holds the value check against the
// authority rather than against the help text.
//
// Herdr's `plugin pane open --help` lists four placements and its schema
// declares five: popup is accepted, and is what this plugin's menu sends. A
// check reading the help called that drift and had to be given an exception.
func TestAValueTheSchemaDoesNotDeclareIsDrift(t *testing.T) {
	deps := []herdrcli.Dependency{{
		Command: []string{"pane", "split"},
		Flags:   []string{"--direction"},
		Values:  map[string][]string{"--direction": {"sideways"}},
	}}
	var out strings.Builder
	code := report(&out, answering(everything()), deps, nil, nil,
		theAskedVersion, theDeclaredMinimum, theAskedVersion, aSchemaThatDeclaresEverything(), theManifest())
	if code == 0 {
		t.Errorf("--direction=sideways is in no enum and the run exited nought:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "--direction=sideways") {
		t.Errorf("the report does not name the value:\n%s", out.String())
	}
}

// TestAValueOnlyTheSchemaKnowsIsAccepted is the case the help text got wrong.
func TestAValueOnlyTheSchemaKnowsIsAccepted(t *testing.T) {
	deps := []herdrcli.Dependency{{
		Command: []string{"pane", "split"},
		Flags:   []string{"--direction"},
		// paneSplit's help declares "right" and says nothing about "down";
		// the schema declares both.
		Values: map[string][]string{"--direction": {"down"}},
	}}
	var out strings.Builder
	code := report(&out, answering(everything()), deps, nil, nil,
		theAskedVersion, theDeclaredMinimum, theAskedVersion, aSchemaThatDeclaresEverything(), theManifest())
	if code != 0 {
		t.Errorf("--direction=down is in the schema's enum and was reported as drift:\n%s",
			out.String())
	}
}

// TestAFieldTheParserReadsAndTheSchemaDoesNotIsDrift holds the check the
// recordings cannot make.
//
// A recording answers "does the parser read what Herdr wrote once", for ever.
// The schema comes from the binary installed now, so a field renamed on Herdr's
// side is visible here and nowhere else in this repository.
func TestAFieldTheParserReadsAndTheSchemaDoesNotIsDrift(t *testing.T) {
	if len(paneJSONFields()) < 5 {
		t.Fatalf("herdrcli.Pane reads %d fields, which is too few for this to be "+
			"checking anything: %v", len(paneJSONFields()), paneJSONFields())
	}

	short := everyPaneField()
	gone := paneJSONFields()[0]
	delete(short, gone)

	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		herdrSchema{PaneFields: short, Enums: theEnums()}, theManifest())

	if code == 0 {
		t.Errorf("the schema does not declare %q and the run exited nought:\n%s",
			gone, out.String())
	}
	if !strings.Contains(out.String(), gone) {
		t.Errorf("the report does not name the field that is gone (%q):\n%s",
			gone, out.String())
	}
}

// TestASchemaTypeThisPairsWithAndCannotFindIsReported holds the claim enumFor's
// comment makes about itself.
//
// accepts asks the schema for the values a flag takes, and falls back to the
// help text when the schema has no such type. That fallback is right -- an
// older Herdr has no schema at all -- and silent it is a trap: the help is the
// source this whole check exists to stop trusting, and the first thing that
// would happen is `--placement popup` being called drift again by a checker
// that had quietly stopped asking the authority.
//
// The comment said the missing name was reported. It was not, until this.
func TestASchemaTypeThisPairsWithAndCannotFindIsReported(t *testing.T) {
	if len(enumFor) == 0 {
		t.Fatal("no flag is paired with a schema type, so this holds nothing")
	}

	short := theEnums()
	delete(short, "PluginPanePlacement")

	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		herdrSchema{PaneFields: everyPaneField(), Enums: short}, theManifest())

	if code == 0 {
		t.Errorf("the schema no longer defines PluginPanePlacement and the run exited "+
			"nought:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "PluginPanePlacement") {
		t.Errorf("the report does not name the type that is gone:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "--placement") {
		t.Errorf("the report does not name the flag left without an authority:\n%s",
			out.String())
	}
}

// TestASchemaWithNoEnumsIsNotAStalePairing is the other side, and it has to
// hand the pane fields IN to reach the question.
//
// A schema that could not be read at all is answered earlier, by the line that
// says the fields went unchecked -- so passing an empty herdrSchema tests that
// line and not this one. The case this is about is a schema that parsed and
// carries no enums, where reporting every pairing as gone would be three
// problems invented out of one absence.
//
// Written this way because the first version passed for the wrong reason:
// blinding the guard it is named for left it green.
func TestASchemaWithNoEnumsIsNotAStalePairing(t *testing.T) {
	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		herdrSchema{PaneFields: everyPaneField(), Enums: nil}, theManifest())

	if code != 0 {
		t.Errorf("a schema carrying no enums was reported as having stale pairings:\n%s",
			out.String())
	}
	if strings.Contains(out.String(), "does not define") {
		t.Errorf("the report complains about pairings when the schema has no enums:\n%s",
			out.String())
	}
}

// TestNoSchemaAtAllSaysTheFieldsWentUnchecked is the case that empty schema
// really tests, kept apart so each says what it holds.
func TestNoSchemaAtAllSaysTheFieldsWentUnchecked(t *testing.T) {
	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion, herdrSchema{}, theManifest())

	if code != 0 {
		t.Errorf("a Herdr with no readable schema was reported as drift:\n%s", out.String())
	}
	// Scoped to the line the schema check itself wrote. Read across the whole
	// report, this passed on the MANIFEST's line, which says "could not be
	// read" about the very same missing schema -- so the sentence naming the
	// fields could be replaced by anything and both of the tests that were
	// here went on passing. Measured, not supposed.
	said := reportLine(t, out.String(), "api schema")
	if !strings.Contains(said, "could not be read") {
		t.Errorf("the api schema line does not say the schema could not be read: %q", said)
	}
	if !strings.Contains(said, "fields") {
		t.Errorf("the api schema line does not say what went unchecked: %q", said)
	}
}

// withAgentStatus is theEnums with a different set of reported statuses, for
// the two tests below. Everything else stays as Herdr declares it so a failure
// is about the statuses and nothing else.
func withAgentStatus(statuses ...string) map[string][]string {
	enums := theEnums()
	enums["AgentStatus"] = statuses
	return enums
}

// TestAStatusHerdrReportsAndTheMappingDoesNotNameIsDrift holds the input side
// of herdrcli.AgentState.
//
// AgentState turns what a remote pane says its agent is doing into one of the
// four states `pane report-agent` accepts, and its default is "unknown". That
// default is right for a value that means nothing here. It is wrong for one
// Herdr has just started reporting: the mapping goes on working, the gate stays
// green, and the only sign is a machine whose agent shows nothing in the
// sidebar while it is plainly busy. Herdr adding a status is the likeliest way
// this plugin goes subtly wrong, and it is the sort of wrong nobody files.
func TestAStatusHerdrReportsAndTheMappingDoesNotNameIsDrift(t *testing.T) {
	// "waiting" stands in for whatever Herdr adds next. Checked here so the
	// test cannot pass by the mapping having grown to cover it.
	if herdrcli.AgentState("waiting") != "unknown" {
		t.Fatalf("herdrcli.AgentState now names \"waiting\", so this test no longer " +
			"describes a status the mapping has never heard of")
	}

	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		herdrSchema{
			PaneFields: everyPaneField(),
			Enums:      withAgentStatus("blocked", "done", "idle", "unknown", "waiting", "working"),
		}, theManifest())

	if code == 0 {
		t.Errorf("Herdr reports a status the mapping does not name and the run "+
			"exited nought:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "waiting") {
		t.Errorf("the report does not name the status that has no name here:\n%s",
			out.String())
	}
	// One is "it" and several are "them". The sentence reads as a fault in the
	// checker otherwise, and the choice was held by nothing: the sweep flipped
	// `len(unnamed) == 1` and no test minded.
	if said := reportLine(t, out.String(), "api schema"); !strings.Contains(said, "does not name it") {
		t.Errorf("one unnamed status is not called \"it\": %q", said)
	}
}

// TestSeveralStatusesWithNoNameAreCalledThemRatherThanIt is the other half of
// that choice, and the reason it is a separate row: with one fixture the two
// spellings cannot be told apart, which is how the decision came to be held by
// nothing at all.
func TestSeveralStatusesWithNoNameAreCalledThemRatherThanIt(t *testing.T) {
	var out strings.Builder
	report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		herdrSchema{
			PaneFields: everyPaneField(),
			Enums:      withAgentStatus("idle", "waiting", "pausing"),
		}, theManifest())

	said := reportLine(t, out.String(), "api schema")
	if !strings.Contains(said, "does not name them") {
		t.Errorf("two unnamed statuses are not called \"them\": %q", said)
	}
}

// TestTheStatusThatMeansUnknownIsNotItselfDrift is the trap in the check above.
//
// It spots a fall-through by the mapping answering "unknown", which is exactly
// what the mapping answers for the status "unknown" -- correctly, by name, not
// by default. A check that did not except it would report drift against a Herdr
// that had changed nothing, on every run, for as long as Herdr reports a status
// meaning it does not know either.
func TestTheStatusThatMeansUnknownIsNotItselfDrift(t *testing.T) {
	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		herdrSchema{PaneFields: everyPaneField(), Enums: withAgentStatus("unknown")}, theManifest())

	if code != 0 {
		t.Errorf("the status \"unknown\" was reported as a status with no name:\n%s",
			out.String())
	}
}

// TestEveryStatusHerdrReportsTodayHasAName is the check run against what Herdr
// 0.9.0 actually declares, so the five values in theEnums are held one by one
// and "done" cannot quietly stop being handled.
func TestEveryStatusHerdrReportsTodayHasAName(t *testing.T) {
	statuses := theEnums()["AgentStatus"]
	if len(statuses) < 5 {
		t.Fatalf("theEnums declares %d statuses, too few to be checking anything: %v",
			len(statuses), statuses)
	}
	for _, status := range statuses {
		if status == "unknown" {
			continue
		}
		if got := herdrcli.AgentState(status); got == "unknown" {
			t.Errorf("Herdr reports %q and herdrcli.AgentState answers %q, so it "+
				"fell through to the default", status, got)
		}
	}
}

// manifestDeclaring is theManifest with one setting given different values, so
// a failure is about that setting and nothing else.
func manifestDeclaring(setting string, values ...string) map[string][]string {
	declared := theManifest()
	declared[setting] = values
	return declared
}

// TestAManifestValueHerdrDoesNotDeclareIsDrift holds the load-time contract.
//
// The manifest is read by Herdr when it loads the plugin, and a value it does
// not know does not come back as an error the plugin could report: the schema
// gives placement a default of "overlay", so a menu declared with a placement
// Herdr had dropped would simply open as an overlay -- not session-modal, not
// receiving Escape -- with every command still working and nothing to read.
func TestAManifestValueHerdrDoesNotDeclareIsDrift(t *testing.T) {
	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		aSchemaThatDeclaresEverything(), manifestDeclaring("placement", "popup", "sidebar"))

	if code == 0 {
		t.Errorf("the manifest declares a placement Herdr does not and the run "+
			"exited nought:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "sidebar") {
		t.Errorf("the report does not name the value Herdr will not take:\n%s",
			out.String())
	}
	if strings.Contains(out.String(), `"popup"`) {
		t.Errorf("the report names popup, which this Herdr does declare:\n%s",
			out.String())
	}
}

// TestAManifestSettingPairedWithATypeTheSchemaDropsIsReported is the same trap
// enumFor carries: the pairing is written by hand, and a type Herdr renames
// would leave the setting checked against nothing while the run still said ok.
func TestAManifestSettingPairedWithATypeTheSchemaDropsIsReported(t *testing.T) {
	short := theEnums()
	delete(short, "PluginActionContext")

	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		herdrSchema{PaneFields: everyPaneField(), Enums: short}, theManifest())

	if code == 0 {
		t.Errorf("a setting is paired with a type the schema does not define and "+
			"the run exited nought:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "contexts -> PluginActionContext") {
		t.Errorf("the report does not name the setting left without an authority:\n%s",
			out.String())
	}
}

// TestAManifestThatCouldNotBeReadIsNotDrift is the other side. A run from
// somewhere with no manifest under it has nothing to say about the manifest,
// which is different from having found something wrong in one.
func TestAManifestThatCouldNotBeReadIsNotDrift(t *testing.T) {
	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		aSchemaThatDeclaresEverything(), nil)

	if code != 0 {
		t.Errorf("an unreadable manifest was reported as drift:\n%s", out.String())
	}
	if said := reportLine(t, out.String(), "plugin manifest"); !strings.Contains(said, "went unchecked") {
		t.Errorf("the plugin manifest line does not say it went unchecked: %q", said)
	}
}

// TestTheManifestScanReadsTheRealFile is the scope assertion under the scan.
//
// It reads enough TOML for the question and no more, so a manifest that adopted
// a form it cannot see -- a value over several lines, a quoted key -- would
// leave a paired setting with nothing found for it, and a check over an empty
// list passes. This is the denominator that says the check is asking about
// something.
func TestTheManifestScanReadsTheRealFile(t *testing.T) {
	found := manifestValues(filepath.Join("..", ".."))
	if found == nil {
		t.Fatal("herdr-plugin.toml could not be read from the repository root")
	}
	for setting := range manifestEnumFor {
		if len(found[setting]) == 0 {
			t.Errorf("the scan found no %q in the manifest, so what the check says "+
				"about it is said over nothing", setting)
		}
	}
	// The two placements this plugin cannot do without: the menu is a popup
	// because a popup is session-modal and gets Escape, and a mirror is a tab.
	for _, want := range []string{"popup", "tab"} {
		if !slices.Contains(found["placement"], want) {
			t.Errorf("the manifest declares a %q pane and the scan did not find it: %v",
				want, found["placement"])
		}
	}
	// And it returns the paired settings and nothing else, so a manifest key
	// that happens to share a name with none of them cannot arrive as a value
	// to check.
	if len(found) != len(manifestEnumFor) {
		t.Errorf("the scan returned %d settings and %d are paired: %v",
			len(found), len(manifestEnumFor), found)
	}
}

// TestAManifestNothingWasFoundInIsNotOk holds the difference between finding no
// drift and having looked at nothing.
//
// The scan reads enough TOML for the question and no more, so a manifest that
// adopted a form it cannot see leaves it with nothing to check -- and the
// sentence it printed for that was the success one with a nought in it: "ok,
// all 0 values it declares are ones Herdr declares too". Measured before this
// was written: the package caught it through the scan's own test, and the
// COMMAND exited nought and said ok. `make herdr` is what somebody runs when
// they suspect drift, and it has to be able to tell them it found none because
// there was none.
func TestAManifestNothingWasFoundInIsNotOk(t *testing.T) {
	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		aSchemaThatDeclaresEverything(), map[string][]string{})

	if code == 0 {
		t.Errorf("the scan found nothing in the manifest and the run exited "+
			"nought:\n%s", out.String())
	}
	// The whole point is the sentence, not only the status: an empty scan must
	// not read like a clean one.
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "plugin manifest") && strings.Contains(line, "ok,") {
			t.Errorf("an empty scan still says ok:\n%s", line)
		}
	}
}

// TestASettingWithNoValuesIsNamedAsUnchecked is the same claim one setting at a
// time. A manifest that declared no panes at all would have no placement in it,
// which is legitimate -- and the count would then quietly cover two settings
// while the line read as though it covered three.
func TestASettingWithNoValuesIsNamedAsUnchecked(t *testing.T) {
	partial := theManifest()
	delete(partial, "placement")

	var out strings.Builder
	code := report(&out, answering(everything()),
		[]herdrcli.Dependency{{Command: []string{"pane", "close"}, Flags: []string{"--plugin"}}},
		nil, nil, theAskedVersion, theDeclaredMinimum, theAskedVersion,
		aSchemaThatDeclaresEverything(), partial)

	if code != 0 {
		t.Errorf("a manifest declaring no panes was reported as drift:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "nothing was found for placement") {
		t.Errorf("the report does not say placement went unchecked:\n%s", out.String())
	}
}

// reportLine is the line the report wrote for one check, found by its label.
//
// The report is a column of "<label>  <what it found>" lines, and an assertion
// made with strings.Contains over the WHOLE of it is satisfied by any line at
// all. That is not a hypothetical worry here: two checks say "could not be
// read" about a missing schema, and the one the manifest check added kept the
// schema's own tests passing with the schema's sentence replaced by anything.
// Ask which line said it.
func reportLine(t *testing.T, out, label string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, label) {
			return line
		}
	}
	t.Fatalf("the report has no %q line in it:\n%s", label, out)
	return ""
}
