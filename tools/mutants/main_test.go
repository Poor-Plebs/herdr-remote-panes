package main

import (
	"strings"
	"testing"
)

func TestTheCaretLandsOnTheOperatorThatChanged(t *testing.T) {
	// A line can hold several operators, and reporting them all as one line
	// number leaves them indistinguishable — which is how four survivors on one
	// line came to need a separate run each to tell apart. The caret is the
	// whole of the fix, so it had better be under the right characters.
	//
	// The arithmetic is the fiddly part: a column counts from the start of the
	// line as written, tabs and all, while the line is printed with its
	// indentation stripped.
	const source = "\t\tif width <= 0 || indent+w <= width {"

	for _, tt := range []struct {
		what   string
		column int
		old    string
	}{
		// "if width " is nine characters after two tabs, so the first
		// comparison starts at column twelve.
		{"the first comparison", 12, "<="},
		{"the conjunction", 17, "||"},
		{"the second comparison", 29, "<="},
	} {
		t.Run(tt.what, func(t *testing.T) {
			out := pointAt(mutation{source: source, column: tt.column, old: tt.old})
			lines := strings.Split(out, "\n")
			if len(lines) != 2 {
				t.Fatalf("want the line and a caret under it, got %q", out)
			}
			at := strings.Index(lines[1], "^")
			if at < 0 {
				t.Fatalf("no caret in %q", lines[1])
			}
			if got := lines[0][at : at+len(tt.old)]; got != tt.old {
				t.Errorf("the caret is under %q, not the %q it is about", got, tt.old)
			}
			if n := strings.Count(lines[1], "^"); n != len(tt.old) {
				t.Errorf("%d carets for an operator of %d characters", n, len(tt.old))
			}
		})
	}
}

func TestACaretThatCannotBePlacedStillShowsTheLine(t *testing.T) {
	// The column comes from the parser and the source line from the file, and
	// they are read separately. If they ever disagree, showing the line without
	// a caret beats a caret in the wrong place or a panic.
	for _, tt := range []struct {
		what   string
		column int
	}{
		{"a column before the line starts", -5},
		{"a column past the end of it", 500},
	} {
		t.Run(tt.what, func(t *testing.T) {
			out := pointAt(mutation{source: "\tif a == b {", column: tt.column, old: "=="})
			if strings.Contains(out, "^") {
				t.Errorf("a caret was placed anyway: %q", out)
			}
			if !strings.Contains(out, "if a == b {") {
				t.Errorf("the line itself went missing: %q", out)
			}
		})
	}
}

func TestTheSurvivorsAreCountedInASentence(t *testing.T) {
	// The report is read by somebody deciding where to look next, so it has to
	// read. "the other 1 are decisions" makes them stop on a line that had
	// nothing to say.
	for _, tt := range []struct {
		onErrors, rest int
		want           string
	}{
		{1, 0, "1 is an error branch, surviving until something makes that call fail."},
		{3, 0, "3 are error branches, surviving until something makes those calls fail."},
		{1, 1, "1 is an error branch, surviving until something makes that call fail;\n" +
			"the other is a decision with nothing holding it."},
		{24, 29, "24 are error branches, surviving until something makes those calls fail;\n" +
			"the other 29 are decisions with nothing holding them."},
	} {
		if got := errorBranchNote(tt.onErrors, tt.rest); got != tt.want {
			t.Errorf("errorBranchNote(%d, %d) =\n%q\nwant\n%q", tt.onErrors, tt.rest, got, tt.want)
		}
	}
}

func TestAnErrorBranchIsRecognisedByItsShape(t *testing.T) {
	// Read off the line rather than the syntax tree: what is wanted is the
	// shape somebody recognises when skimming, and these are that shape
	// whatever they parse to.
	for _, source := range []string{
		"if err != nil {",
		"\t\tif err := f(); err != nil {",
		"if _, err := herdrcli.Run(\"pane\", \"list\"); err != nil {",
		"if rmErr := os.Remove(socket); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {",
		"if err == nil {",
	} {
		if !isErrorBranch(source) {
			t.Errorf("%q was not read as an error branch", source)
		}
	}

	// And a decision is not one, however much it looks like a guard.
	for _, source := range []string{
		"if placement == \"\" && !focus {",
		"if len(state.shellPanes) == 0 {",
		"if selected < 0 || selected >= count {",
		"if state.workspaceID == \"\" {",
	} {
		if isErrorBranch(source) {
			t.Errorf("%q was counted as an error branch", source)
		}
	}
}
