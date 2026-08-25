// Command mutants reports the changes that can be made to a package's code
// without any test noticing.
//
// Coverage says which lines a test ran. It does not say whether anything would
// have failed had those lines been wrong, and the difference is most of what
// this project's tests were worth when they were written: four tests here could
// not fail at all, and every one of them was in a covered line.
//
// So each operator is flipped in turn -- && for ||, == for !=, > for >=, a
// negation dropped -- and the package's own tests are run against it. A change
// nothing catches is a survivor, and the survivors are a map of what is being
// taken on trust. Most of them are equivalent or unreachable, which is worth
// knowing too; the ones that are neither are where the bugs have been.
//
//	go run ./tools/mutants ./internal/config
//	go run ./tools/mutants ./internal/syncd daemon.go
//
// Only lines the tests actually reach are mutated: a change to a line nothing
// runs survives by definition and says nothing that the coverage report has not
// already said.
//
// The working tree is never touched. Everything happens in a copy under the
// system temp directory, which is removed afterwards.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// flip is every operator this rewrites, and what it becomes. Chosen because
// each one is a decision somebody made: a boundary, a direction, a conjunction.
var flip = map[token.Token]token.Token{
	token.LAND: token.LOR,
	token.LOR:  token.LAND,
	token.EQL:  token.NEQ,
	token.NEQ:  token.EQL,
	token.LSS:  token.LEQ,
	token.LEQ:  token.LSS,
	token.GTR:  token.GEQ,
	token.GEQ:  token.GTR,
}

type mutation struct {
	file     string // relative to the module root
	offset   int
	old, new string
	line     int
	column   int
	source   string
	clamp    bool // the boundary maps to itself, so both spellings agree
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/mutants <package> [file.go ...]")
		os.Exit(2)
	}
	pkg := os.Args[1]
	only := map[string]bool{}
	for _, name := range os.Args[2:] {
		only[name] = true
	}

	root, err := os.Getwd()
	check(err)

	// A copy, so an interrupted run cannot leave a mutation in the tree this
	// was started from. Restoring by hand is what git checkout is for, and
	// reaching for that in a directory with uncommitted work is how work gets
	// lost.
	work, err := os.MkdirTemp("", "mutants-")
	check(err)
	defer os.RemoveAll(work)
	check(copyTree(root, work))

	covered, err := coveredLines(work, pkg)
	check(err)

	muts, skipped, err := mutationsIn(work, pkg, only, covered)
	check(err)
	if len(muts) == 0 {
		if len(only) > 0 {
			fmt.Printf("nothing to mutate: %s has no covered lines, or no file of that name\n",
				strings.Join(os.Args[2:], ", "))
		} else {
			fmt.Println("nothing to mutate: no covered lines in that package")
		}
		return
	}
	fmt.Printf("%d mutations on covered lines (%d skipped as unreached)\n\n", len(muts), skipped)

	var survived []mutation
	caught, unusable := 0, 0
	started := time.Now()

	for i, m := range muts {
		outcome, err := try(work, pkg, m)
		check(err)
		switch outcome {
		case "survived":
			survived = append(survived, m)
			why := ""
			if class := survivorClass(m); class != "" {
				why = "  -- " + class
			}
			fmt.Printf("SURVIVED  %s:%d:%d  %s -> %s%s\n%s\n",
				m.file, m.line, m.column, m.old, m.new, why, pointAt(m))
		case "caught":
			caught++
		default:
			unusable++
		}
		if n := i + 1; n%25 == 0 {
			left := time.Duration(float64(time.Since(started)) / float64(n) * float64(len(muts)-n))
			fmt.Printf("... %d/%d, %d survived, about %s left\n",
				n, len(muts), len(survived), left.Round(time.Second))
		}
	}

	fmt.Printf("\n%d mutations: %d caught, %d survived, %d would not build, in %s\n",
		len(muts), caught, len(survived), unusable, time.Since(started).Round(time.Second))
	if len(survived) == 0 {
		return
	}

	// Error branches are counted apart because they answer differently. A
	// change to "if err != nil" survives whenever nothing in the tests makes
	// that call fail, which is a question about fault injection rather than
	// about the decision on that line -- and on a package that talks to
	// something else for a living they are most of the list. Separating them
	// is the difference between a report worth reading and a wall.
	onErrors, onClamps := 0, 0
	for _, m := range survived {
		switch survivorClass(m) {
		case classErrorBranch:
			onErrors++
		case classBound:
			onClamps++
		}
	}
	if note := survivorNote(onErrors, onClamps, len(survived)-onErrors-onClamps); note != "" {
		fmt.Println(note)
	}
	fmt.Println("\nA survivor is a change nothing would have failed on. Read each one and" +
		"\ndecide which it is: equivalent, unreachable, or untested.")
}

// try applies one mutation, runs the package's tests, and puts the file back.
func try(work, pkg string, m mutation) (string, error) {
	path := filepath.Join(work, m.file)
	original, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.WriteFile(path, original, 0o600) }()

	if got := string(original[m.offset : m.offset+len(m.old)]); got != m.old {
		// The offsets came from this same copy, so this cannot happen without
		// something else having written to the file underneath.
		return "unusable", nil
	}
	mutated := append([]byte{}, original[:m.offset]...)
	mutated = append(mutated, m.new...)
	mutated = append(mutated, original[m.offset+len(m.old):]...)
	if err := os.WriteFile(path, mutated, 0o600); err != nil {
		return "", err
	}

	// Built first and separately. Piping a build into anything else takes the
	// pipe's status, and a mutation that does not compile then reads as one the
	// tests caught -- which is the opposite of what it is.
	if out, err := run(work, "go", "build", "./..."); err != nil {
		_ = out
		return "unusable", nil
	}
	// Bounded: a mutation can make the code loop rather than fail, and a sweep
	// that waits for it tells you nothing while looking busy. Hanging counts as
	// caught -- the test would never have finished.
	if _, err := run(work, "go", "test", pkg, "-count=1", "-timeout", "120s"); err != nil {
		return "caught", nil
	}
	return "survived", nil
}

// mutationsIn lists what can be changed in a package's own files.
func mutationsIn(work, pkg string, only map[string]bool, covered map[string]map[int]bool) ([]mutation, int, error) {
	dir := filepath.Join(work, filepath.Clean(strings.TrimPrefix(pkg, "./")))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}

	var out []mutation
	skipped := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if len(only) > 0 && !only[name] {
			continue
		}
		rel, err := filepath.Rel(work, filepath.Join(dir, name))
		if err != nil {
			return nil, 0, err
		}
		found, err := mutationsInFile(filepath.Join(dir, name), rel)
		if err != nil {
			return nil, 0, err
		}
		for _, m := range found {
			if covered[rel][m.line] {
				out = append(out, m)
			} else {
				skipped++
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].offset < out[j].offset
	})
	return out, skipped, nil
}

// mutationsInFile parses rather than matching text: a "&&" inside a string
// literal or a comment is not an operator, and a sweep that treats it as one
// spends its time proving that comments do not affect behaviour.
func mutationsInFile(path, rel string) ([]mutation, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(src), "\n")

	// What a node is written as, so two of them can be compared as somebody
	// reading the line would compare them.
	textOf := func(n ast.Node) string {
		return strings.TrimSpace(string(src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset]))
	}
	clampAt := map[token.Pos]bool{}

	var out []mutation
	add := func(pos token.Pos, old, new string) {
		at := fset.Position(pos)
		source := ""
		if at.Line-1 < len(lines) {
			source = strings.TrimSpace(lines[at.Line-1])
		}
		out = append(out, mutation{
			file: rel, offset: at.Offset, old: old, new: new,
			line: at.Line, column: at.Column, source: source,
			clamp: clampAt[pos],
		})
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.IfStmt:
			// Visited before its own condition, so what is recorded here is
			// there to be read when the comparison inside it comes round.
			if cond, ok := e.Cond.(*ast.BinaryExpr); ok && isClamp(textOf, cond, e.Body) {
				clampAt[cond.OpPos] = true
			}
		case *ast.BinaryExpr:
			if to, ok := flip[e.Op]; ok {
				add(e.OpPos, e.Op.String(), to.String())
			}
		case *ast.UnaryExpr:
			// Dropping a negation: the usual way a guard stops guarding.
			if e.Op == token.NOT {
				add(e.OpPos, "!", " ")
			}
		}
		return true
	})
	return out, nil
}

// coveredLines runs the package's tests once and reports which of its own lines
// they reached, by file.
func coveredLines(work, pkg string) (map[string]map[int]bool, error) {
	profile := filepath.Join(work, "mutants.cover")
	out, err := run(work, "go", "test", pkg, "-count=1", "-coverprofile", profile)
	if err != nil {
		// What go test said, not just that it failed. A package that does not
		// exist and a package whose tests are red are the same exit status and
		// very different problems, and being told the wrong one sends somebody
		// looking at tests that are fine.
		return nil, fmt.Errorf("cannot start from a package whose tests do not pass:\n%s", firstLines(out, 5))
	}
	raw, err := os.ReadFile(profile)
	if err != nil {
		return nil, err
	}

	module, err := moduleName(work)
	if err != nil {
		return nil, err
	}
	covered := map[string]map[int]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		// path/file.go:startLine.col,endLine.col statements count
		fields := strings.Fields(line)
		if len(fields) != 3 || !strings.HasPrefix(fields[0], module) {
			continue
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil || count == 0 {
			continue
		}
		where := strings.TrimPrefix(fields[0], module+"/")
		file, span, ok := strings.Cut(where, ":")
		if !ok {
			continue
		}
		from, to, ok := strings.Cut(span, ",")
		if !ok {
			continue
		}
		start, err1 := strconv.Atoi(strings.SplitN(from, ".", 2)[0])
		end, err2 := strconv.Atoi(strings.SplitN(to, ".", 2)[0])
		if err1 != nil || err2 != nil {
			continue
		}
		if covered[file] == nil {
			covered[file] = map[int]bool{}
		}
		for n := start; n <= end; n++ {
			covered[file][n] = true
		}
	}
	return covered, nil
}

func moduleName(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", fmt.Errorf("no module line in go.mod")
}

// copyTree copies the module, leaving out what a build does not need. .git
// especially: a sweep has no business anywhere near it.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch rel {
			case ".git", "bin":
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), raw, info.Mode().Perm())
	})
}

func run(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// pointAt shows the line with a caret under the operator that was changed. A
// line can hold several -- "a <= b || c < d" has three -- and a report that
// gives them all the same line number leaves them indistinguishable, which is
// how four survivors on one line took a separate run each to tell apart.
func pointAt(m mutation) string {
	trimmed := strings.TrimLeft(m.source, " \t")
	lost := len(m.source) - len(trimmed)
	under := m.column - 1 - lost
	if under < 0 || under > len(trimmed) {
		return "            " + trimmed
	}
	return "            " + trimmed + "\n            " +
		strings.Repeat(" ", under) + strings.Repeat("^", len(m.old))
}

// survivorNote says how the survivors divide, in a sentence rather than in
// numbers glued to a phrase: "the other 1 are decisions" makes a reader stop
// and reread a line that had nothing to say.
func survivorNote(onErrors, onClamps, rest int) string {
	var clauses []string
	switch onErrors {
	case 0:
	case 1:
		clauses = append(clauses, "1 is an error branch, surviving until something makes that call fail")
	default:
		clauses = append(clauses, fmt.Sprintf(
			"%d are error branches, surviving until something makes those calls fail", onErrors))
	}
	switch onClamps {
	case 0:
	case 1:
		clauses = append(clauses, "1 holds a value to a bound, where both spellings of the boundary agree")
	default:
		clauses = append(clauses, fmt.Sprintf(
			"%d hold a value to a bound, where both spellings of the boundary agree", onClamps))
	}
	switch {
	case rest == 1 && len(clauses) > 0:
		clauses = append(clauses, "the other is a decision with nothing holding it")
	case rest > 1 && len(clauses) > 0:
		clauses = append(clauses, fmt.Sprintf("the other %d are decisions with nothing holding them", rest))
	}
	if len(clauses) == 0 {
		return ""
	}
	return strings.Join(clauses, ";\n") + "."
}

// The reasons a survivor is likely to be one without anything being wrong. Both
// are named rather than spelled out twice, because the listing and the count
// below it are read together and a listing that disagrees with its own summary
// is worse than one that says nothing.
const (
	classErrorBranch = "an error branch"
	classBound       = "a bound held to itself"
)

// survivorClass says why a survivor is likely to be one, or "" for a decision
// with nothing holding it -- which is the only kind worth reading closely.
func survivorClass(m mutation) string {
	switch {
	case isErrorBranch(m.source):
		return classErrorBranch
	case m.clamp:
		return classBound
	}
	return ""
}

// isClamp reports whether an if-statement holds a value to a bound, in the shape
//
//	if width < 8 { width = 8 }
//	if w > widest { widest = w }
//	if next < 0 { return 0 }
//
// These make up a large share of any survivor list and none of them is worth
// reading twice: the only mutations on a comparison are the two spellings of
// its boundary, and at the boundary the branch assigns the value that is
// already there. Whichever way it is written the result is the same, so the
// change is equivalent by construction rather than by anything a test could
// have said.
//
// Deliberately narrow. It asks that the branch be one statement naming the two
// things compared and nothing else, so that "if first+visible > count { first =
// count - visible }" -- equivalent too, but by an argument about arithmetic
// rather than about shape -- is left in the list to be read.
func isClamp(textOf func(ast.Node) string, cond *ast.BinaryExpr, body *ast.BlockStmt) bool {
	switch cond.Op {
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
	default:
		// Only a boundary can map to itself. Flipping == to != changes which
		// branch runs at every value, including this one.
		return false
	}
	if body == nil || len(body.List) != 1 {
		return false
	}
	x, y := textOf(cond.X), textOf(cond.Y)
	switch st := body.List[0].(type) {
	case *ast.AssignStmt:
		// Plain assignment: := would declare something else of the same name.
		if st.Tok != token.ASSIGN || len(st.Lhs) != 1 || len(st.Rhs) != 1 {
			return false
		}
		lhs, rhs := textOf(st.Lhs[0]), textOf(st.Rhs[0])
		return (lhs == x && rhs == y) || (lhs == y && rhs == x)
	case *ast.ReturnStmt:
		if len(st.Results) != 1 {
			return false
		}
		got := textOf(st.Results[0])
		return got == x || got == y
	}
	return false
}

// isErrorBranch reports whether a line is testing an error rather than
// deciding something. Read from the source line rather than the syntax tree:
// what is wanted is the shape somebody recognises when skimming a list, and
// "if err != nil" is that shape whatever it parses to.
func isErrorBranch(source string) bool {
	for _, shape := range []string{"err != nil", "err == nil", "Err != nil", "Err == nil"} {
		if strings.Contains(source, shape) {
			return true
		}
	}
	return false
}

// firstLines keeps a command's output to something readable.
func firstLines(out []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "...")
	}
	return "  " + strings.Join(lines, "\n  ")
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "mutants:", err)
		os.Exit(1)
	}
}
