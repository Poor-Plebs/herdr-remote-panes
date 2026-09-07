package project

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
	inRoot(t)

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
		// What each declaration is called, and the comment above it. Types,
		// constants and variables as well as functions: the rule is the same
		// for all of them, and reading only functions let a comment about
		// something else settle above a type, where `go doc` shows it as that
		// type's first paragraph.
		//
		// Only declarations naming one thing. A grouped `var (...)` may
		// reasonably carry a comment about the group rather than any member.
		type documented struct {
			name string
			doc  *ast.CommentGroup
			what string
		}
		var named []documented
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				named = append(named, documented{d.Name.Name, d.Doc, "func"})
			case *ast.GenDecl:
				if len(d.Specs) != 1 {
					continue
				}
				switch spec := d.Specs[0].(type) {
				case *ast.TypeSpec:
					named = append(named, documented{spec.Name.Name, d.Doc, "type"})
				case *ast.ValueSpec:
					if len(spec.Names) == 1 {
						named = append(named, documented{spec.Names[0].Name, d.Doc, "declaration"})
					}
				}
			}
		}

		for _, fn := range named {
			if fn.doc == nil || len(fn.doc.List) == 0 {
				continue
			}
			checked++
			// Go's own convention: a doc comment opens with the name of what
			// it documents. Held to here because it is what makes this
			// checkable at all.
			opening := strings.Fields(strings.TrimPrefix(fn.doc.List[0].Text, "//"))
			if len(opening) == 0 {
				continue
			}
			first := strings.TrimRight(opening[0], ",.")
			if first != fn.name {
				t.Errorf("%s: the comment above %s %s opens with %q.\n"+
					"A doc comment starts with the name of what it documents. If %q is a "+
					"real name, its comment has been stranded here and belongs above it.",
					fset.Position(fn.doc.Pos()), fn.what, fn.name, first, first)
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

func TestNoDocCommentIsStrandedAwayFromWhatItDocuments(t *testing.T) {
	// A comment separated from what it documents by a blank line is attached
	// to nothing at all, and the check above cannot see it: that one reads
	// each declaration's own doc comment, and a stranded comment is nobody's.
	// `go doc` will not show it either, which is the whole reason it was
	// written where it was.
	//
	// Both instances in this tree were the same shape -- an explanation
	// written as documentation, filed where the thing it explains is not. One
	// opened with saveSnapshot, a function that had been split in two and no
	// longer existed anywhere; the other explained the pane's entrypoint from
	// inside its test file, while the entrypoint itself carried no comment at
	// all.
	//
	// HONEST LIMIT: this sees only a comment opening with a LOWERCASE word. Go
	// names unexported things that way and an English sentence does not begin
	// that way, which is what separates a stranded doc comment from the
	// ordinary paragraphs of prose this repository keeps at file level. A
	// stranded comment for an EXPORTED name opens with a capital and cannot be
	// told from prose here, so it is not caught.
	inRoot(t)

	examined := 0
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
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

		attached := map[*ast.CommentGroup]bool{file.Doc: true}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				attached[d.Doc] = true
			case *ast.GenDecl:
				attached[d.Doc] = true
			}
		}

		for _, group := range file.Comments {
			if attached[group] {
				continue
			}
			// A comment that does not start its own line is a remark about
			// that line, and one inside a declaration is commentary on the
			// code around it. Neither is documentation of anything.
			if fset.Position(group.Pos()).Column != 1 {
				continue
			}
			within := false
			for _, decl := range file.Decls {
				if group.Pos() > decl.Pos() && group.End() < decl.End() {
					within = true
					break
				}
			}
			if within {
				continue
			}
			examined++

			opening := strings.Fields(strings.TrimPrefix(group.List[0].Text, "//"))
			if len(opening) == 0 {
				continue
			}
			first := strings.TrimRight(opening[0], ",.:")
			if first == "" || first[0] < 'a' || first[0] > 'z' {
				continue
			}
			if strings.ContainsAny(first, "`\"'()[]{}*/;!?") {
				continue
			}
			t.Errorf("%s: this comment opens with %q and is attached to nothing.\n"+
				"A blank line between a doc comment and its declaration leaves the "+
				"comment documenting nothing: go doc will not show it, and what it "+
				"describes reads as undocumented. Move it against what it documents, "+
				"or reword it so it does not open with a name.",
				fset.Position(group.Pos()), first)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// Guards against the walk finding no free-floating comments at all, which
	// is how a check like this stops meaning anything.
	if examined < 10 {
		t.Fatalf("only %d file-level comments stand apart from a declaration; "+
			"the walk is not reaching the source", examined)
	}
	t.Logf("examined %d file-level comments attached to no declaration", examined)
}

func TestEveryPackageSaysWhatItIsFor(t *testing.T) {
	// A package comment is the first thing anybody reads about a package, and
	// the only part of it `go doc` shows without being asked for a name. It has
	// to sit immediately above the package clause with no blank line, and one
	// that does not is not a package comment at all -- it is a floating comment
	// that reads exactly like one, in a file that renders with no
	// documentation whatever.
	//
	// This package had that: the words were written, in the right file, saying
	// the right thing, and separated from the clause by the import block.
	inRoot(t)

	seen := map[string]bool{}
	documented := map[string]bool{}

	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// testdata is not built by the go tool, and bin and the dot
			// directories are not ours.
			if name := entry.Name(); path != "." && (strings.HasPrefix(name, ".") ||
				name == "bin" || name == "vendor" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		dir := filepath.Dir(path)
		seen[dir] = true
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if file.Doc != nil {
			documented[dir] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(seen) == 0 {
		t.Fatal("no packages were found to check, so this proved nothing")
	}
	for dir := range seen {
		if !documented[dir] {
			t.Errorf("no file in %s opens with a package comment, so `go doc %s` "+
				"says nothing about what it is for", dir, dir)
		}
	}
}
