package main

import (
	"go/ast"
	"go/parser"
	"go/token"
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
		onErrors, onClamps, rest int
		want                     string
	}{
		{1, 0, 0, "1 is an error branch, surviving until something makes that call fail."},
		{3, 0, 0, "3 are error branches, surviving until something makes those calls fail."},
		{1, 0, 1, "1 is an error branch, surviving until something makes that call fail;\n" +
			"the other is a decision with nothing holding it."},
		{24, 0, 29, "24 are error branches, surviving until something makes those calls fail;\n" +
			"the other 29 are decisions with nothing holding them."},
		// Bounds on their own, which is what a layout package looks like.
		{0, 1, 0, "1 holds a value to a bound, where both spellings of the boundary agree."},
		{0, 9, 2, "9 hold a value to a bound, where both spellings of the boundary agree;\n" +
			"the other 2 are decisions with nothing holding them."},
		{2, 3, 1, "2 are error branches, surviving until something makes those calls fail;\n" +
			"3 hold a value to a bound, where both spellings of the boundary agree;\n" +
			"the other is a decision with nothing holding it."},
		// Nothing to divide up: the caller prints no line rather than a
		// sentence about none of anything.
		{0, 0, 4, ""},
		{0, 0, 0, ""},
	} {
		if got := survivorNote(tt.onErrors, tt.onClamps, tt.rest); got != tt.want {
			t.Errorf("survivorNote(%d, %d, %d) =\n%q\nwant\n%q",
				tt.onErrors, tt.onClamps, tt.rest, got, tt.want)
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

// clampsIn parses a function body and reports, for each if-statement in it,
// whether it reads as holding a value to a bound.
func clampsIn(t *testing.T, body string) []bool {
	t.Helper()
	src := "package p\nfunc f() {\n" + body + "\n}\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parsing the snippet: %v", err)
	}
	textOf := func(n ast.Node) string {
		return strings.TrimSpace(src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset])
	}
	var got []bool
	ast.Inspect(file, func(n ast.Node) bool {
		if stmt, ok := n.(*ast.IfStmt); ok {
			cond, _ := stmt.Cond.(*ast.BinaryExpr)
			got = append(got, cond != nil && isClamp(textOf, cond, stmt.Body))
		}
		return true
	})
	return got
}

func TestABoundHeldToItselfIsRecognised(t *testing.T) {
	// A survivor list from anything that lays out a screen is mostly these,
	// and none of them is worth a second reading: at the boundary the branch
	// assigns the value that is already there, so both spellings agree.
	for _, tt := range []struct {
		what string
		body string
		want bool
	}{
		{"a floor", "width := 3\nif width < 8 {\nwidth = 8\n}\n_ = width", true},
		{"a ceiling", "width := 3\nif width > 40 {\nwidth = 40\n}\n_ = width", true},
		{"a running maximum", "w, widest := 1, 2\nif w > widest {\nwidest = w\n}\n_ = widest", true},
		{"a bound returned rather than assigned", "next := 1\nif next < 0 {\nreturn\n}\n_ = next", false},

		// Not bounds. Each of these is a decision, and a change to it means
		// something -- so leaving them in the list is the point.
		{"a comparison of two other things", "a, b, c := 1, 2, 3\nif a < b {\nc = a\n}\n_ = c", false},
		{"a branch that does more than clamp", "w, m := 1, 2\nif w > m {\nm = w\nw = 0\n}\n_ = w", false},
		{"equality, where no boundary maps to itself", "a, b := 1, 2\nif a == b {\na = b\n}\n_ = a", false},
		{"a declaration rather than an assignment", "a, b := 1, 2\nif a < b {\nc := b\n_ = c\n}\n_ = a", false},
		{"arithmetic on the compared side", "f, v, c := 1, 2, 3\nif f+v > c {\nf = c - v\n}\n_ = f", false},
	} {
		got := clampsIn(t, tt.body)
		if len(got) != 1 {
			t.Fatalf("%s: found %d if-statements in the snippet, want 1", tt.what, len(got))
		}
		if got[0] != tt.want {
			t.Errorf("%s: read as a bound = %v, want %v", tt.what, got[0], tt.want)
		}
	}
}

func TestASurvivorSaysWhyItIsLikelyToBeOne(t *testing.T) {
	// The listing and the summary under it are read together: the summary says
	// how many are error branches and how many are bounds, and without a mark
	// on each line the reader still has to work out which is which for every
	// one of them. That is the whole of what the counting was meant to save.
	//
	// One function behind both, so a line and the number above it cannot
	// disagree about the same mutation.
	for _, tt := range []struct {
		what string
		m    mutation
		want string
	}{
		{"an error branch", mutation{source: "if err != nil {"}, classErrorBranch},
		{"a bound", mutation{source: "if width < 8 {", clamp: true}, classBound},
		{"a decision", mutation{source: "if entry.Connected {"}, ""},
		// An error branch that is also a bound is called an error branch:
		// nothing here fails the tests until a call fails, whatever shape the
		// line has, and that is the more useful thing to say.
		{"both at once", mutation{source: "if err != nil {", clamp: true}, classErrorBranch},
	} {
		if got := survivorClass(tt.m, nil); got != tt.want {
			t.Errorf("%s: survivorClass = %q, want %q", tt.what, got, tt.want)
		}
	}
}

func TestASurvivorReadBeforeIsNotRaisedAgain(t *testing.T) {
	// Every sweep of a package this size turns up the same dozen equivalents.
	// Reading them again costs more than the sweep does, and a judgement kept
	// only in a commit message is one nobody finds.
	m := mutation{file: "internal/syncd/plan.go", old: "<", new: "<=", source: "\t\treturn na < nb"}
	read := map[string]bool{triageKey(m): true}

	if got := survivorClass(m, read); got != classRead {
		t.Errorf("a survivor already read came back as %q, want %q", got, classRead)
	}
	// Being in the record says nothing about the others.
	other := mutation{file: "internal/syncd/plan.go", old: "<", new: "<=", source: "\t\treturn a.PaneID < b.PaneID"}
	if got := survivorClass(other, read); got != "" {
		t.Errorf("a survivor not in the record came back as %q, want a decision", got)
	}

	// The line as written is part of what was judged. Edit it and the
	// judgement no longer applies, because it was about the line that was
	// there -- which is the whole reason this is not keyed by line number.
	edited := m
	edited.source = "\t\treturn na <= nb // reordered"
	if got := survivorClass(edited, read); got != "" {
		t.Errorf("an edited line kept its old judgement (%q)", got)
	}
	// Whitespace is not an edit, though: gofmt moves it and nobody decided
	// anything different.
	spaced := m
	spaced.source = "    return na < nb   "
	if got := survivorClass(spaced, read); got != classRead {
		t.Errorf("the same line differently indented came back as %q, want %q", got, classRead)
	}
}
