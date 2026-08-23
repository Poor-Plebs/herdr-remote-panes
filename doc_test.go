package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// A doc comment that names a different function than the one below it is worse
// than none: it reads as an explanation of that function and is an explanation
// of another. This plugin keeps most of its reasoning in those comments -- why
// a retry exists, what a message used to say and why it changed -- so one
// pointing at the wrong place takes a piece of that reasoning with it.
//
// Nine of them had accumulated, all the same way: a function inserted between a
// doc comment and the function it belonged to, which leaves the doc stranded
// above the newcomer and the original bare. Nothing complains about it. gofmt
// is happy, the compiler is happy, and reading either function in isolation
// looks fine -- the mistake is only visible from the pair.

func TestEveryDocCommentNamesItsOwnFunction(t *testing.T) {
	checked := 0
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// Nothing generated or vendored, which this does not own.
			if name := entry.Name(); path != "." && (strings.HasPrefix(name, ".") || name == "bin" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Doc == nil || len(fn.Doc.List) == 0 {
				continue
			}
			checked++
			// Go's own convention: a doc comment opens with the name of what
			// it documents. Held to here because it is what makes this
			// checkable at all.
			opening := strings.Fields(strings.TrimPrefix(fn.Doc.List[0].Text, "//"))
			if len(opening) == 0 {
				continue
			}
			first := strings.TrimRight(opening[0], ",.")
			if first != fn.Name.Name {
				t.Errorf("%s: the comment above %s opens with %q.\n"+
					"A doc comment starts with the name of what it documents. If %q is a "+
					"real function, its comment has been stranded here and belongs above it.",
					fset.Position(fn.Doc.Pos()), fn.Name.Name, first, first)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// Guards against the walk quietly finding nothing and this passing on an
	// empty set, which is how a check like this stops meaning anything.
	if checked < 200 {
		t.Fatalf("only %d documented functions found; the walk is not reaching the source", checked)
	}
	t.Logf("checked %d documented functions", checked)
}
