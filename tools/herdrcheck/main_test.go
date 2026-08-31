package main

import "testing"

// paneSplitHelp is what `herdr pane split --help` prints, abridged to the part
// that matters: a flag whose possible values are followed by a flag with none.
//
// Kept verbatim rather than reduced to the shape being tested. What this has to
// read is Herdr's help as Herdr writes it -- the indentation, the blank lines
// and the values on their own line under the flag -- and a fixture tidied into
// what the parser expects is one that agrees with the parser about a format
// neither of them decides.
const paneSplitHelp = `Split a pane

Usage: herdr pane split [OPTIONS] [PANE_ID]

Options:
      --direction <DIRECTION>
          [possible values: right, down]

      --env <KEY=VALUE>
          Set an environment variable for the launched process

      --right-click <TARGET>
          [possible values: herdr, pane]

      --focus
          

      --no-focus
`

func TestAValueIsReadUnderItsOwnFlag(t *testing.T) {
	// The mistake this is here for. Reading every "possible values" line in a
	// help and asking whether the value is in any of them says --focus takes
	// "herdr" and "pane", which belong to --right-click two lines above. Done
	// by hand while auditing, and it is the failure that matters: a checker
	// that finds a value wherever it looks is one that never reports drift.
	if takesValue(paneSplitHelp, "--focus", "herdr") {
		t.Error("--focus was read as taking a value belonging to --right-click")
	}
	if takesValue(paneSplitHelp, "--focus", "pane") {
		t.Error("--focus was read as taking a value belonging to --right-click")
	}

	// And the values that are its own are found.
	for _, value := range []string{"right", "down"} {
		if !takesValue(paneSplitHelp, "--direction", value) {
			t.Errorf("--direction takes %q and it was not found", value)
		}
	}
	if takesValue(paneSplitHelp, "--direction", "sideways") {
		t.Error("--direction was read as taking a value it does not list")
	}
	for _, value := range []string{"herdr", "pane"} {
		if !takesValue(paneSplitHelp, "--right-click", value) {
			t.Errorf("--right-click takes %q and it was not found", value)
		}
	}
	// A flag that is not in the help at all takes nothing, rather than
	// borrowing the values of whatever is listed first.
	if takesValue(paneSplitHelp, "--mood", "right") {
		t.Error("a flag the help does not have was read as taking a value")
	}
}

func TestAFlagIsFoundOnlyWhereItIsDeclared(t *testing.T) {
	for _, flag := range []string{"--direction", "--env", "--right-click", "--focus", "--no-focus"} {
		if !hasFlag(paneSplitHelp, flag) {
			t.Errorf("the help declares %s and it was not found", flag)
		}
	}
	// Not a flag of this command, though every letter of it is in the help:
	// --focus is a prefix of --focus-mode, and "--no-focus" contains "--no".
	for _, flag := range []string{"--focus-mode", "--no", "--dir", "--plugin"} {
		if hasFlag(paneSplitHelp, flag) {
			t.Errorf("%s is not declared here and was found anyway", flag)
		}
	}
	// The command's name in the usage line is not a flag declaration: "herdr
	// pane split [OPTIONS]" holds neither, but a looser match on the whole
	// text would find --plugin inside "plugin pane open" in other helps.
	if hasFlag(paneSplitHelp, "--options") {
		t.Error("a word from the usage line was read as a flag")
	}
}
