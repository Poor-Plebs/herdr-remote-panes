package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

// TestNoHerdrToAskIsSaidRatherThanReportedAsDrift holds what this says when it
// has no Herdr to ask, which is the one situation it can answer nothing in.
//
// The exit code is three-way and the distinction is the point: 0 means Herdr
// takes everything this plugin sends, 1 means it was asked and something has
// drifted, and 2 means it could not be asked at all. A run that gave up
// reporting 1 would tell somebody their pages name a command Herdr does not
// have, when what happened is that Herdr is not there.
//
// The exit was held and the TELLING was not: a statement-deletion sweep at
// 2858a85 removed the line naming what it tried and every test still passed,
// so `make herdr` failed with nothing said. That is the first thing somebody
// following docs/development.md on a fresh machine would meet.
//
// TWO WAYS, because they leave different errors behind and only one of them
// says the path by itself. Bin() returns HERDR_BIN_PATH verbatim, so both are
// reachable by setting it: a path with nothing at it fails before exec, and
// the error carries the path; a path that RUNS and exits non-zero gives back
// "exit status 1" and nothing else, and there the message naming the binary is
// the whole of what the reader gets. The second row is what makes that half
// load-bearing -- with only the first, dropping the name from the message
// survives, because the error underneath it is already carrying the path.
//
// Built and run rather than reached through a seam: what is held is the
// program giving up before it asks anything, which main decides and hands to
// make as an exit status.
func TestNoHerdrToAskIsSaidRatherThanReportedAsDrift(t *testing.T) {
	built := filepath.Join(t.TempDir(), "herdrcheck")
	if out, err := exec.Command("go", "build", "-o", built, ".").CombinedOutput(); err != nil {
		t.Fatalf("building the command: %v\n%s", err, out)
	}

	nothingThere := filepath.Join(t.TempDir(), "herdr")

	runsAndFails := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(runsAndFails, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		what      string
		bin       string
		namesPath bool // whether the error underneath already gives it away
	}{
		{"nothing at that path", nothingThere, true},
		{"something that is not Herdr", runsAndFails, false},
	} {
		t.Run(tt.what, func(t *testing.T) {
			// The error the command itself will get, taken from exec rather
			// than written out here, so this pins the reason being passed on
			// and no wording for it.
			_, why := exec.Command(tt.bin, "--version").Output()
			if why == nil {
				t.Fatalf("%s ran, so this asks nothing", tt.bin)
			}
			// And the fixture says which kind it is, because the row exists
			// for that difference.
			if named := strings.Contains(why.Error(), tt.bin); named != tt.namesPath {
				t.Fatalf("exec said %q, which names the path = %v, want %v",
					why, named, tt.namesPath)
			}

			// Away from the repository: with no Herdr this must stop on
			// Herdr, not go on to read the pages and fail about those.
			var out, errs bytes.Buffer
			run := exec.Command(built)
			run.Dir = t.TempDir()
			run.Env = append(os.Environ(), "HERDR_BIN_PATH="+tt.bin)
			run.Stdout, run.Stderr = &out, &errs
			ran := run.Run()

			var exit *exec.ExitError
			if !errors.As(ran, &exit) {
				t.Fatalf("a run with no Herdr to ask ended %v, want a non-zero exit", ran)
			}
			if exit.ExitCode() != 2 {
				t.Errorf("a run that could not ask exited %d, want 2; 1 is what it "+
					"returns having asked and found drift", exit.ExitCode())
			}

			said := errs.String()
			if !strings.Contains(said, tt.bin) {
				t.Errorf("what it could not run is not named, so there is nothing to "+
					"fix or to check HERDR_BIN_PATH against:\n%s", said)
			}
			if !strings.Contains(said, why.Error()) {
				t.Errorf("the reason it could not run is not passed on.\nexec said: %v\n"+
					"and it said:\n%s", why, said)
			}

			// The control: it stopped rather than carrying on with a binary
			// that does not work. Anything it went on to ask about would be
			// printed here, starting with the line naming the Herdr it asks.
			if out.Len() != 0 {
				t.Errorf("it carried on after finding no Herdr to ask:\n%s", out.String())
			}
		})
	}
}
