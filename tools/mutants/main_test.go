package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
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

		// A slice held to a maximum length. At the boundary the slice
		// expression is the whole slice, so the branch assigns what is
		// already there -- the same argument as a clamp, one step along.
		{"truncated to a maximum", "s, n := \"ab\", 1\nif len(s) > n {\ns = s[:n]\n}\n_ = s", true},
		{"kept to its last n", "s, n := \"ab\", 1\nif len(s) > n {\ns = s[len(s)-n:]\n}\n_ = s", true},
		{"the bound written the other way round", "s, n := \"ab\", 1\nif n < len(s) {\ns = s[:n]\n}\n_ = s", true},

		// Not that. Each keeps a different amount at the boundary, which is
		// exactly what the two spellings of the comparison disagree about.
		{"truncated to one short of the bound", "s, n := \"ab\", 2\nif len(s) > n {\ns = s[:n-1]\n}\n_ = s", false},
		{"the front dropped rather than the end", "s, n := \"ab\", 1\nif len(s) > n {\ns = s[n:]\n}\n_ = s", false},
		{"a different bound in the slice", "s, n, m := \"ab\", 1, 2\nif len(s) > n {\ns = s[:m]\n}\n_ = s", false},
		{"another slice truncated", "s, o, n := \"ab\", \"cd\", 1\nif len(s) > n {\no = o[:n]\n}\n_ = o", false},

		// Two things assigned at once. The branch is not holding one value to
		// one bound, and reading only the first of them would call a swap a
		// clamp -- which is a survivor somebody would then skip past.
		{"a swap", "a, b := 1, 2\nif a > b {\na, b = b, a\n}\n_ = a", false},
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
	read := map[string]string{triageKey(m) + "\x00": ""}

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

func TestStaleTriageNamesJudgementsThatNoLongerApply(t *testing.T) {
	// read.tsv is the record of survivors somebody read and decided to leave.
	// It can only ever grow, and two ordinary things silently retire an entry:
	// the line gets edited, or a test is added and the mutation stops
	// surviving. Neither is visible, so the record ends up describing code
	// that is not there any more.
	path := filepath.Join(t.TempDir(), "read.tsv")
	record := strings.Join([]string{
		"# a survivor that is still a survivor",
		"picker.go\t< -> <=\tif room < 1 {",
		"",
		"# read once, but a test now catches it",
		"picker.go\t! ->\treturn a.Configured && !b.Configured",
		"",
		"# a line that has since been edited away",
		"picker.go\t> -> >=\tif gone > 0 {",
		"",
		"# another package, not swept this time",
		"daemon.go\t== -> !=\tif kind == \"ssh\" {",
	}, "\n")
	if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}

	swept := []mutation{
		{file: "picker.go", old: "<", new: "<=", source: "\tif room < 1 {"},
		{file: "picker.go", old: "!", new: "", source: "\treturn a.Configured && !b.Configured"},
	}
	survived := []mutation{swept[0]}

	got := staleTriage(path, swept, survived)
	want := []string{
		"picker.go  ! ->  return a.Configured && !b.Configured",
		"picker.go  > -> >=  if gone > 0 {",
	}
	if !slices.Equal(got, want) {
		t.Errorf("stale entries\n%v\nwant\n%v", got, want)
	}
}

func TestEveryTriagedLineIsStillInTheTree(t *testing.T) {
	// staleTriage reports this too, but only for the package being swept and
	// only after the several minutes a sweep takes. A judgement about a line
	// that has been deleted or rewritten is wrong whether or not anyone is
	// sweeping, and this says so in milliseconds.
	raw, err := os.ReadFile("read.tsv")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for n, line := range strings.Split(string(raw), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 && len(parts) != 4 {
			t.Errorf("read.tsv:%d is not file<TAB>change<TAB>line[<TAB>function]: %q", n+1, line)
			continue
		}
		file := parts[0]
		if _, ok := sources[file]; !ok {
			body, err := os.ReadFile(filepath.Join("..", "..", file))
			if err != nil {
				t.Errorf("read.tsv:%d names a file that is not there: %v", n+1, err)
				sources[file] = ""
				continue
			}
			sources[file] = string(body)
		}
		source := strings.TrimSpace(parts[2])
		if source == "" {
			continue
		}
		// Exactly one, not merely at least one. A survivor is keyed by the
		// line as written, so two identical lines in a file share a key and
		// one judgement silences both -- and they are not always the same
		// decision. "if state.restoreShells > 0" appears twice in daemon.go:
		// on one of them a count of zero means there is nothing to do, and on
		// the other it means creating a workspace and closing husks for a
		// machine that needs neither. Recording either would hide the other.
		matches := 0
		for _, line := range strings.Split(sources[file], "\n") {
			if strings.TrimSpace(line) == source {
				matches++
			}
		}
		// A fourth field names the function the judgement was made in, which
		// is how a line that appears more than once is spoken about without
		// speaking about the others.
		if len(parts) == 4 {
			if matches == 0 {
				t.Errorf("read.tsv:%d was decided about a line %s no longer has:\n  %s", n+1, file, source)
			}
			continue
		}
		switch matches {
		case 1:
		case 0:
			t.Errorf("read.tsv:%d was decided about a line %s no longer has:\n  %s", n+1, file, source)
		default:
			t.Errorf("read.tsv:%d matches %d lines in %s, so it settles more than it was written about:\n  %s",
				n+1, matches, file, source)
		}
	}
}

func TestATriagedDeletionMatchesItsEntry(t *testing.T) {
	// A mutation can delete an operator rather than replace one, and the
	// change then reads "! -> " with nothing after it. A hand-edited file
	// cannot be relied on to carry that trailing space -- an editor that
	// strips them is enough -- so both sides trim.
	//
	// Without it every entry for a deletion matched nothing at all: the
	// judgement stayed in the file, the survivor came back unlabelled on every
	// sweep, and nothing said the two had stopped meeting.
	path := filepath.Join(t.TempDir(), "read.tsv")
	if err := os.WriteFile(path, []byte("picker.go\t! ->\treturn a && !b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := mutation{file: "picker.go", old: "!", new: "", source: "\treturn a && !b"}
	if got := survivorClass(m, readSurvivors(path)); got != classRead {
		t.Errorf("a deletion that was read and left classified as %q, want %q", got, classRead)
	}
	if stale := staleTriage(path, []mutation{m}, []mutation{m}); len(stale) != 0 {
		t.Errorf("an entry that matches was called stale: %v", stale)
	}
}

func TestACaseExpressionIsAskedAboutAtItsSwitch(t *testing.T) {
	// A coverage profile records the body of a case and never the line the
	// case is written on, so every "case n > 0:" in the tree read as a line
	// nothing runs and was skipped. Sweeping internal/text went from 14
	// mutations with 40 skipped to 54 with none the day this was fixed, and
	// the decisions it then reached had never been mutation-tested anywhere.
	//
	// Which makes this worth holding: the failure is silent, and what it costs
	// is a whole class of survivor never being reported.
	const src = `package p

func f(n int) string {
	switch {
	case n > 0:
		return "up"
	case n < 0:
		return "down"
	default:
		return "flat"
	}
}

func g(n int) bool {
	return n > 0
}

func h(ok bool) string {
	switch {
	case !ok:
		return "no"
	}
	return "yes"
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := caseCoverLines(fset, file)

	// The first character of a case expression, which is where the span
	// begins. "case !ok:" puts the operator that would be mutated exactly
	// there, and a span that excludes its own first character sends that
	// mutation back to its own line -- where the profile has nothing, so it is
	// skipped as unreached and the case goes unmutated again.
	var firstInCase token.Pos
	var itsSwitch int
	ast.Inspect(file, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		for _, stmt := range sw.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok || len(clause.List) == 0 {
				continue
			}
			// Both taken from the tree rather than counted out of the snippet,
			// so that editing it above cannot quietly make this check nothing.
			if unary, ok := clause.List[0].(*ast.UnaryExpr); ok {
				firstInCase, itsSwitch = unary.OpPos, fset.Position(sw.Switch).Line
			}
		}
		return true
	})
	if firstInCase == 0 {
		t.Fatal("the snippet no longer has a case starting with an operator")
	}
	if got := at(firstInCase, fset.Position(firstInCase).Line); got != itsSwitch {
		t.Errorf("an operator at the very start of a case expression is asked about "+
			"at line %d, want its switch at %d", got, itsSwitch)
	}

	// Where each operator actually is, found rather than counted out by hand.
	var found []struct {
		line, cover int
		what        string
	}
	ast.Inspect(file, func(n ast.Node) bool {
		binary, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		line := fset.Position(binary.OpPos).Line
		found = append(found, struct {
			line, cover int
			what        string
		}{line, at(binary.OpPos, line), src[binary.Pos()-1 : binary.End()-1]})
		return true
	})

	if len(found) != 3 {
		t.Fatalf("found %d comparisons in the snippet, want 3: %+v", len(found), found)
	}
	// The switch is on line 4; its two cases are on 5 and 7. Both are asked
	// about at the switch.
	for _, want := range []struct {
		what        string
		line, cover int
	}{
		{"n > 0", 5, 4},
		{"n < 0", 7, 4},
		// Not in a switch at all, so it answers for itself.
		{"n > 0", 15, 15},
	} {
		var got *struct {
			line, cover int
			what        string
		}
		for i := range found {
			if found[i].line == want.line {
				got = &found[i]
			}
		}
		if got == nil {
			t.Errorf("no comparison found on line %d", want.line)
			continue
		}
		if got.what != want.what {
			t.Errorf("line %d holds %q, want %q", want.line, got.what, want.what)
		}
		if got.cover != want.cover {
			t.Errorf("%q on line %d is asked about at line %d, want %d",
				got.what, got.line, got.cover, want.cover)
		}
	}
}

func TestAJudgementCanBeAboutOneOfTwoIdenticalLines(t *testing.T) {
	// The same line twice in one file, meaning different things. In daemon.go
	// "if state.restoreShells > 0" is equivalent in one function and caught in
	// the other, and with only the line to go on, recording the first said the
	// second had been read too -- so the record could not hold it at all, and
	// the survivor came back unlabelled on every sweep.
	//
	// A fourth field names the function. Without one the entry is about that
	// line wherever it appears, which is what nearly every entry means.
	path := filepath.Join(t.TempDir(), "read.tsv")
	record := strings.Join([]string{
		"# read in one function only",
		"daemon.go\t> -> >=\tif n > 0 {\tinnocent",
		"# and this one is about the line wherever it is",
		"picker.go\t< -> <=\tif room < 1 {",
	}, "\n")
	if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	read := readSurvivors(path)

	inNamed := mutation{file: "daemon.go", old: ">", new: ">=", source: "\tif n > 0 {", function: "innocent"}
	inOther := mutation{file: "daemon.go", old: ">", new: ">=", source: "\tif n > 0 {", function: "guilty"}
	anywhere := mutation{file: "picker.go", old: "<", new: "<=", source: "\tif room < 1 {", function: "whatever"}

	if got := survivorClass(inNamed, read); got != classRead {
		t.Errorf("the line in the function it was read in classified as %q, want %q", got, classRead)
	}
	if got := survivorClass(inOther, read); got == classRead {
		t.Error("reading one of two identical lines settled the other as well, " +
			"which is the thing the fourth field is for")
	}
	if got := survivorClass(anywhere, read); got != classRead {
		t.Errorf("an entry with no function named should be about that line wherever "+
			"it is, and classified as %q", got)
	}

	// And neither is called stale while it still matches something.
	if stale := staleTriage(path, []mutation{inNamed, anywhere}, []mutation{inNamed, anywhere}); len(stale) != 0 {
		t.Errorf("entries that still match were called stale: %v", stale)
	}
	// The one whose function no longer has that line is.
	stale := staleTriage(path, []mutation{inOther}, []mutation{inOther})
	if len(stale) != 1 || !strings.Contains(stale[0], "innocent") {
		t.Errorf("stale = %v, want the entry naming a function nothing survived in", stale)
	}
}

func TestASweepSaysWhichFilesItDidNotLookAt(t *testing.T) {
	// Sweeping a package file by file is how a package comes to be called
	// swept while two of its files have never been looked at. That has
	// happened twice here, both times to somebody who had the file list in
	// front of them a moment earlier, so the run says which ones it left.
	work := t.TempDir()
	pkg := filepath.Join(work, "internal", "thing")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.go", "two.go", "three.go", "one_test.go"} {
		body := "package thing\n\nfunc f" + strings.TrimSuffix(name, ".go") + "(n int) bool { return n > 0 }\n"
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, _, untouched, err := mutationsIn(work, "./internal/thing",
		map[string]bool{"one.go": true}, map[string]map[int]bool{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"three.go", "two.go"}
	if !slices.Equal(untouched, want) {
		t.Errorf("left %v, want %v", untouched, want)
	}

	// A whole-package sweep leaves nothing, and saying so on every run would
	// be noise on the runs that need none.
	_, _, untouched, err = mutationsIn(work, "./internal/thing", nil, map[string]map[int]bool{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(untouched) != 0 {
		t.Errorf("a sweep of the whole package says it left %v", untouched)
	}
}

func TestASweepCanBeRestrictedToWhatChanged(t *testing.T) {
	// Sweeping a large file to look at the lines somebody wrote this morning
	// costs an hour and spends nearly all of it re-deciding mutations read
	// months ago -- which is why the code written in a release went out
	// unswept. Restricting a run to changed lines is what makes asking cheap
	// enough to bother.
	work := t.TempDir()
	pkg := filepath.Join(work, "internal", "thing")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	// Three decisions, one per line, so a line can be named without naming
	// the others.
	body := "package thing\n" + // 1
		"\n" + // 2
		"func a(n int) bool { return n > 0 }\n" + // 3
		"func b(n int) bool { return n > 1 }\n" + // 4
		"func c(n int) bool { return n > 2 }\n" // 5
	if err := os.WriteFile(filepath.Join(pkg, "one.go"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	covered := map[string]map[int]bool{
		"internal/thing/one.go": {3: true, 4: true, 5: true},
	}

	all, _, _, err := mutationsIn(work, "./internal/thing", nil, covered, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("unrestricted, the sweep found %d mutations, want one per line", len(all))
	}

	// Only the line that changed.
	changed := map[string]map[int]bool{"internal/thing/one.go": {4: true}}
	some, skipped, _, err := mutationsIn(work, "./internal/thing", nil, covered, changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(some) != 1 {
		t.Fatalf("restricted to one line, the sweep found %d mutations: %+v", len(some), some)
	}
	if some[0].line != 4 {
		t.Errorf("the sweep took line %d, which is not the one that changed", some[0].line)
	}
	if skipped != 2 {
		t.Errorf("%d mutations were skipped, want the 2 on lines that did not change", skipped)
	}

	// A file with no changed lines at all contributes nothing, rather than
	// everything: getting this backwards sweeps the whole tree while saying it
	// is sweeping what changed.
	none := map[string]map[int]bool{"internal/thing/other.go": {1: true}}
	empty, _, _, err := mutationsIn(work, "./internal/thing", nil, covered, none)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("with nothing changed in the file, the sweep found %d mutations", len(empty))
	}
}

func TestChangedLinesReadsWhatADiffSays(t *testing.T) {
	// The lines come out of git, and reading the hunk headers wrongly is not
	// visible in the result -- a sweep of the wrong lines looks exactly like a
	// sweep of the right ones, and reports just as confidently.
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if _, err := run(repo, args[0], args[1:]...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "user.email", "t@example.com")
	run("git", "config", "user.name", "t")

	first := "package thing\n\nfunc a() int { return 1 }\nfunc b() int { return 2 }\nfunc c() int { return 3 }\n"
	if err := os.WriteFile(filepath.Join(repo, "one.go"), []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "-A")
	run("git", "commit", "-qm", "first")

	// One line rewritten in the middle, and one added at the end.
	second := "package thing\n\nfunc a() int { return 1 }\nfunc b() int { return 22 }\nfunc c() int { return 3 }\nfunc d() int { return 4 }\n"
	if err := os.WriteFile(filepath.Join(repo, "one.go"), []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := changedLines(repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	got := changed["one.go"]
	if got == nil {
		t.Fatalf("no lines reported for the file that changed: %+v", changed)
	}
	for _, line := range []int{4, 6} {
		if !got[line] {
			t.Errorf("line %d changed and is not in %v", line, got)
		}
	}
	for _, line := range []int{1, 2, 3, 5} {
		if got[line] {
			t.Errorf("line %d did not change and is in %v", line, got)
		}
	}
}

func TestARestrictedSweepIsNotRecordedAsThePackage(t *testing.T) {
	// This record exists to say what was looked at, so a run that looked at
	// part of a package must not claim the package. Getting that wrong made it
	// state the opposite of the truth: a sweep of the four lines that had
	// changed in a package overwrote its entry and left it reading "1
	// mutation", which says the whole of it was tried and found to be almost
	// nothing.
	path := filepath.Join(t.TempDir(), "swept.tsv")
	for _, tt := range []struct {
		what  string
		since string
		files []string
		named string
	}{
		{"the whole package", "", nil, "./internal/thing"},
		{"some of its files", "", []string{"one.go"}, "./internal/thing (one.go)"},
		{"only what changed", "v1.2.3", nil, "./internal/thing (since v1.2.3)"},
		{"both at once", "v1.2.3", []string{"one.go"}, "./internal/thing (one.go, since v1.2.3)"},
	} {
		recordSweep(path, "./internal/thing", tt.since, tt.files, 7, 6, 1, 0)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), tt.named+"\t") {
			t.Errorf("%s was recorded as something other than %q:\n%s",
				tt.what, tt.named, raw)
		}
	}

	// And each is its own line: a restricted run replacing the package's entry
	// is how the record came to overstate what had been done.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "./internal/thing") {
			lines++
		}
	}
	if lines != 4 {
		t.Errorf("four different runs left %d lines; each says something the "+
			"others do not:\n%s", lines, raw)
	}
}

func TestTheRecordKeepsItsHeaderAsAHeader(t *testing.T) {
	// Rewriting the file means reading back what is in it and keeping the
	// lines that are entries. Getting the test for "not an entry" wrong turns
	// the explanation at the top into data: every comment and blank line
	// written back as though it were a package that had been swept.
	path := filepath.Join(t.TempDir(), "swept.tsv")
	recordSweep(path, "./internal/one", "", nil, 5, 5, 0, 0)
	recordSweep(path, "./internal/two", "", nil, 3, 3, 0, 0)
	recordSweep(path, "./internal/one", "", nil, 6, 6, 0, 0)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	comments, entries, blanks := 0, 0, 0
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "#"):
			comments++
		case line == "":
			blanks++
		default:
			entries++
		}
	}
	if entries != 2 {
		t.Errorf("%d entries after three runs over two packages; want one each:\n%s",
			entries, raw)
	}
	if blanks != 0 {
		t.Errorf("%d blank lines were written back as entries:\n%s", blanks, raw)
	}
	// The header is written fresh each time, so it must not also be kept from
	// the last one.
	if comments < 5 || comments > 20 {
		t.Errorf("%d comment lines: the header is being kept as well as written:\n%s",
			comments, raw)
	}
}

func TestALineIsAttributedToTheFunctionItIsIn(t *testing.T) {
	// read.tsv names the function for a line that appears more than once in a
	// file and does not mean the same thing both times -- with only the line to
	// go on, deciding one of them decides the other. So attributing a position
	// to the wrong function quietly settles a judgement about code nobody read.
	src := `package thing

func first() bool {
	return true
}

func second() bool {
	return false
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "thing.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	within := enclosingFuncs(fset, file)

	var firstPos, secondPos token.Pos
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fn.Name.Name {
		case "first":
			firstPos = fn.Body.Pos() + 2
		case "second":
			secondPos = fn.Body.Pos() + 2
		}
	}

	if got := within(firstPos); got != "first" {
		t.Errorf("a line inside first is attributed to %q", got)
	}
	if got := within(secondPos); got != "second" {
		t.Errorf("a line inside second is attributed to %q", got)
	}
	// Outside any of them -- the package clause -- belongs to none.
	if got := within(file.Package); got != "" {
		t.Errorf("a line outside every function is attributed to %q", got)
	}
	// And the very edges, since the range is what decides this.
	for _, decl := range file.Decls {
		fn := decl.(*ast.FuncDecl)
		if got := within(fn.Pos()); got != fn.Name.Name {
			t.Errorf("the first position of %s is attributed to %q", fn.Name.Name, got)
		}
		if got := within(fn.End()); got == fn.Name.Name {
			t.Errorf("the position just past %s is still attributed to it", fn.Name.Name)
		}
	}
}

func TestTheReportIsGroupedByFileAndInOrder(t *testing.T) {
	// A sweep of a package reports dozens of mutations and the list is read
	// from the top. Grouped by file and in the order they appear in it, that
	// reads as a walk through the source; ungrouped, the same lines arrive
	// interleaved and somebody has to sort them by eye every run.
	work := t.TempDir()
	pkg := filepath.Join(work, "internal", "thing")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two files, each with several decisions, written in an order that is not
	// the order they should come back in.
	for _, name := range []string{"zebra.go", "alpha.go"} {
		body := "package thing\n\n" +
			"func a" + strings.TrimSuffix(name, ".go") + "(n int) bool { return n > 0 }\n" +
			"func b" + strings.TrimSuffix(name, ".go") + "(n int) bool { return n > 1 }\n" +
			"func c" + strings.TrimSuffix(name, ".go") + "(n int) bool { return n > 2 }\n"
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	covered := map[string]map[int]bool{
		"internal/thing/zebra.go": {3: true, 4: true, 5: true},
		"internal/thing/alpha.go": {3: true, 4: true, 5: true},
	}

	muts, _, _, err := mutationsIn(work, "./internal/thing", nil, covered, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(muts) < 6 {
		t.Fatalf("found %d mutations across two files, which is too few to be "+
			"testing an order", len(muts))
	}

	// Each file's mutations arrive together.
	seen := map[string]bool{}
	file := ""
	for _, m := range muts {
		if m.file == file {
			continue
		}
		if seen[m.file] {
			t.Errorf("%s comes back in more than one run of lines; the list is "+
				"not grouped by file", m.file)
		}
		seen[m.file] = true
		file = m.file
	}
	// And within a file, in the order the lines are in it.
	at := map[string]int{}
	for _, m := range muts {
		if last, ok := at[m.file]; ok && m.offset < last {
			t.Errorf("%s: offset %d comes after %d", m.file, m.offset, last)
		}
		at[m.file] = m.offset
	}
}

func TestSurvivorsAreKeptWhereATerminalCannotLoseThem(t *testing.T) {
	// A sweep of a large package is hours, and the survivors are the whole of
	// what it produced. Piping it through anything that keeps the tail throws
	// them away and leaves the count -- which says how many there are and not
	// one word about which. That has cost this repository a two and a half
	// hour run.
	survived := []mutation{
		{file: "internal/thing/one.go", line: 12, column: 4, old: ">", new: ">=", source: "\tif n > 0 {", function: "f"},
		{file: "internal/thing/two.go", line: 30, column: 9, old: "!=", new: "==", source: "\tif a != b {", function: "g"},
	}

	path := keepSurvivors(survived, "./internal/thing", "")
	if path == "" {
		t.Fatal("nothing was kept, so a truncated run loses everything it found")
	}
	defer os.Remove(path)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	kept := string(raw)
	for _, want := range []string{
		"internal/thing/one.go:12", "> -> >=", "if n > 0 {", "f",
		"internal/thing/two.go:30", "!= -> ==", "if a != b {", "g",
	} {
		if !strings.Contains(kept, want) {
			t.Errorf("what was kept does not mention %q:\n%s", want, kept)
		}
	}

	// A restricted run says so, or the file reads as a sweep of the package.
	restricted := keepSurvivors(survived, "./internal/thing", "v1.2.3")
	defer os.Remove(restricted)
	raw, err = os.ReadFile(restricted)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "since v1.2.3") {
		t.Errorf("a run restricted to what changed does not say so:\n%s", raw)
	}

	// Nothing to keep is not a file worth writing.
	if got := keepSurvivors(nil, "./internal/thing", ""); got != "" {
		t.Errorf("a clean sweep wrote %q", got)
	}
}

// TestTryTellsCaughtFromSurvived holds the judgement every sweep rests on.
//
// try decides whether a mutation was caught, and every "0 unexplained
// survivors" reported by this tool is that decision repeated a few hundred
// times. It had no test: a version of it that called everything caught would
// have swept clean all week and said nothing was wrong anywhere.
//
// Against a module of its own rather than this one, so the mutations are
// small enough to reason about and the run is seconds rather than minutes.
func TestTryTellsCaughtFromSurvived(t *testing.T) {
	work := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module probe\n\ngo 1.25\n")
	// Bigger is tested at 2>1 and 1>2 and not at equality, which is what makes
	// one of the mutations below survive and the other not.
	const source = "package probe\n\nfunc Bigger(a, b int) bool { return a > b }\n"
	write("probe.go", source)
	write("probe_test.go", "package probe\n\nimport \"testing\"\n\n"+
		"func TestBigger(t *testing.T) {\n"+
		"\tif !Bigger(2, 1) || Bigger(1, 2) {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n")

	at := strings.Index(source, "a > b") + len("a ")
	if at < len("a ") {
		t.Fatal("the probe source no longer contains the operator this mutates")
	}

	for _, tt := range []struct {
		what string
		m    mutation
		want string
	}{
		{
			// Reverses the comparison, which the test notices at once.
			what: "a change the tests fail on",
			m:    mutation{file: "probe.go", offset: at, old: ">", new: "<"},
			want: "caught",
		},
		{
			// True at equality instead of false, and nothing asks about equality.
			what: "a change nothing asks about",
			m:    mutation{file: "probe.go", offset: at, old: ">", new: ">="},
			want: "survived",
		},
		{
			// Shifting two ints is not a bool, so the package does not build.
			// This is the one that matters most: a build failure read as a pass
			// would report a mutation as caught by tests that never ran.
			what: "a change that does not compile",
			m:    mutation{file: "probe.go", offset: at, old: ">", new: ">>"},
			want: "unusable",
		},
		{
			// The offsets come from the copy being mutated, so this means
			// something else wrote to the file underneath.
			//
			// The replacement is one that compiles and that the tests fail on,
			// so refusing it is the only way to reach "unusable". Written the
			// other way first -- replacing with something that does not build
			// -- this passed with the offset check taken out, because the
			// answer arrived from the build instead.
			what: "an offset that no longer says what it did",
			m:    mutation{file: "probe.go", offset: at, old: "+", new: "<"},
			want: "unusable",
		},
	} {
		t.Run(tt.what, func(t *testing.T) {
			got, err := try(work, "./...", tt.m)
			if err != nil {
				t.Fatalf("try: %v", err)
			}
			if got != tt.want {
				t.Errorf("try said %q, want %q", got, tt.want)
			}
			// Put back afterwards, always: a sweep runs hundreds of these
			// against one copy, and one left mutated poisons every later
			// answer about that file.
			raw, err := os.ReadFile(filepath.Join(work, "probe.go"))
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != source {
				t.Errorf("the file was left mutated:\n%s", raw)
			}
		})
	}
}

// TestCoveredLinesTellsRunFromNotRun holds what the sweep is allowed to try.
//
// Every mutation is checked against this first, and a line it does not name is
// skipped as unreached. So a fault here does not produce a wrong answer, it
// produces a smaller sweep -- quietly, with the same "0 survived" at the end.
// The count beside that result is the only place it would show, and this tool
// reported a third of itself unreached for exactly that reason.
//
// The two directions matter differently. Missing a covered line loses a
// mutation nobody hears about; naming an uncovered one adds a mutation that
// survives by definition, and a survivor is at least looked at.
func TestCoveredLinesTellsRunFromNotRun(t *testing.T) {
	work := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module probe\n\ngo 1.25\n")
	// Run is called by the test; Skipped is not. Their bodies are on lines the
	// test below names, so a change to this source wants the numbers changed.
	write("probe.go", "package probe\n"+ // 1
		"\n"+ // 2
		"func Run(a, b int) bool {\n"+ // 3
		"\treturn a > b\n"+ // 4
		"}\n"+ // 5
		"\n"+ // 6
		"func Skipped(a, b int) bool {\n"+ // 7
		"\treturn a < b\n"+ // 8
		"}\n") // 9
	write("probe_test.go", "package probe\n\nimport \"testing\"\n\n"+
		"func TestRun(t *testing.T) {\n"+
		"\tif !Run(2, 1) {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n")

	covered, err := coveredLines(work, "./...")
	if err != nil {
		t.Fatalf("coveredLines: %v", err)
	}
	lines := covered["probe.go"]
	if lines == nil {
		t.Fatalf("nothing at all was reported for probe.go: %+v", covered)
	}
	if !lines[4] {
		t.Error("the body of a function the tests call is not reported as run, " +
			"so every mutation in it would be skipped as unreached")
	}
	if lines[8] {
		t.Error("the body of a function nothing calls is reported as run, so " +
			"mutations there survive by definition and fill the report")
	}
}

// TestCoveredLinesSaysWhyItCannotStart holds the message rather than the exit
// status: a package that does not exist and a package whose tests are red fail
// the same way, and being told the wrong one sends somebody reading tests that
// are fine.
func TestCoveredLinesSaysWhyItCannotStart(t *testing.T) {
	work := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":        "module probe\n\ngo 1.25\n",
		"probe.go":      "package probe\n\nfunc Run() bool { return false }\n",
		"probe_test.go": "package probe\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) {\n\tt.Fatal(\"red\")\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, err := coveredLines(work, "./...")
	if err == nil {
		t.Fatal("a package whose tests fail was accepted as a place to start")
	}
	if !strings.Contains(err.Error(), "tests do not pass") {
		t.Errorf("the error is %q, which does not say the tests are the problem", err)
	}
	if !strings.Contains(err.Error(), "red") {
		t.Errorf("the error is %q and does not carry what go test said, so "+
			"somebody has to run it again to find out", err)
	}
}

// TestAProfileSpanCoversBothOfItsEnds holds the reading of a coverage profile.
//
// Every line this gets wrong is a line the sweep then takes on trust. A line
// it believes was not reached is a line it does not mutate, so a mistake here
// is the tool doing less and reporting a clean sweep of it -- and there is
// nothing in the output that would look different.
func TestAProfileSpanCoversBothOfItsEnds(t *testing.T) {
	const module = "example.com/mod"

	got := linesFromProfile(strings.Join([]string{
		"mode: set",
		module + "/pkg/kept.go:10.2,12.16 2 1",
		// Run by nothing, so none of it counts.
		module + "/pkg/cold.go:30.2,32.16 2 0",
		// Another module's profile, which is not this package's business.
		"other.example/elsewhere/x.go:1.1,9.9 1 1",
	}, "\n"), module)

	for _, n := range []int{10, 11, 12} {
		if !got["pkg/kept.go"][n] {
			t.Errorf("line %d is inside the span 10.2,12.16 and is not covered", n)
		}
	}
	for _, n := range []int{9, 13} {
		if got["pkg/kept.go"][n] {
			t.Errorf("line %d is outside the span 10.2,12.16 and is covered", n)
		}
	}
	if len(got["pkg/cold.go"]) != 0 {
		t.Errorf("a block run zero times is covered: %v", got["pkg/cold.go"])
	}
	if len(got) != 1 {
		t.Errorf("files read: %v, want only this module's", got)
	}
}

// TestABlockWhoseNumbersWillNotParseIsDroppedWhole guards the half-read case.
//
// Reading the half that parsed is worse than reading none of it. A start that
// will not parse leaves zero, and a span from zero to the block's real end
// marks the whole top of the file as covered -- lines nothing ran, which then
// get mutated and reported as survivors. The tool would be inventing work
// rather than skipping it, and the report gives no sign.
func TestABlockWhoseNumbersWillNotParseIsDroppedWhole(t *testing.T) {
	const module = "example.com/mod"

	for _, tt := range []struct {
		what string
		span string
	}{
		{"the start will not parse", "oops.2,12.16"},
		{"the end will not parse", "10.2,oops.16"},
	} {
		got := linesFromProfile(module+"/pkg/f.go:"+tt.span+" 2 1", module)
		if len(got["pkg/f.go"]) != 0 {
			t.Errorf("%s (%s): covered %v, want nothing taken from it",
				tt.what, tt.span, got["pkg/f.go"])
		}
	}
}

// TestOutputIsOnlyMarkedCutWhenSomethingWasCut holds the boundary.
func TestOutputIsOnlyMarkedCutWhenSomethingWasCut(t *testing.T) {
	line := func(n int) []byte {
		out := ""
		for i := 0; i < n; i++ {
			out += fmt.Sprintf("line %d\n", i)
		}
		return []byte(out)
	}

	for _, tt := range []struct {
		what string
		have int
		keep int
		cut  bool
	}{
		{"fewer than it keeps", 3, 5, false},
		// Exactly the limit: nothing was left out, so saying so is a lie
		// about the command's output.
		{"exactly what it keeps", 5, 5, false},
		{"one more than it keeps", 6, 5, true},
	} {
		got := firstLines(line(tt.have), tt.keep)
		if cut := strings.Contains(got, "..."); cut != tt.cut {
			t.Errorf("%s: %d lines kept to %d reads as cut = %v, want %v:\n%s",
				tt.what, tt.have, tt.keep, cut, tt.cut, got)
		}
	}
}
