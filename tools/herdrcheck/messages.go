package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// messageCommands reads the plugin's own messages for the Herdr commands they
// tell somebody to run.
//
// The pages are not the only place this hands out instructions. A machine whose
// session is not up is listed as "run `herdr session attach` there"; a menu
// that cannot reach the daemon says to check `herdr plugin log list`; the
// advice under a failed connection says to invoke an action by hand. None of
// those are in herdrcli.Dependencies, because the plugin does not run them --
// it prints them for somebody else to type -- and Dependencies is what the
// plugin runs. So they drift with nothing watching, and they drift where it
// hurts most: every one of these messages is shown to somebody who already has
// a problem, and sends them to a command that no longer exists.
func messageCommands(root string) ([]toldCommand, error) {
	at := map[string][]string{}
	flags := map[string]map[string][]string{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// The checkers and the fake Herdr are not messages anybody is
			// shown: one is this, and the other is a stand-in whose strings
			// exist to be matched by tests.
			if name := d.Name(); name == "tools" || name == "testdata" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("reading %s for the commands it names: %w", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		ast.Inspect(file, func(n ast.Node) bool {
			text, line, ok := stringAt(fset, n)
			if !ok {
				return true
			}
			for _, found := range commandsInMessage(text) {
				place := fmt.Sprintf("%s:%d", rel, line)
				at[found.command] = append(at[found.command], place)
				for _, flag := range found.flags {
					if flags[found.command] == nil {
						flags[found.command] = map[string][]string{}
					}
					flags[found.command][flag] = append(flags[found.command][flag], place)
				}
			}
			// A concatenation was read whole; its pieces are not messages of
			// their own, and reading them again would report the same command
			// twice from the same line.
			_, isJoin := n.(*ast.BinaryExpr)
			return !isJoin
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Unlike the pages, no commands here is a possible answer: the messages
	// could stop naming any. It is not the answer today, and the test beside
	// this holds the tree to that, where a floor built into the walk would
	// instead fail the checker on a machine that has the wrong copy of it.

	names := make([]string, 0, len(at))
	for name := range at {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]toldCommand, 0, len(names))
	for _, name := range names {
		out = append(out, toldCommand{command: strings.Fields(name), where: at[name], flags: flags[name]})
	}
	return out, nil
}

// stringAt returns the string an expression is, joining a concatenation into
// the message it produces.
//
// A message is written the way it fits on the screen, not the way it reads:
// "run `herdr plugin action " + "invoke %s.connect`" is one instruction split
// across two literals, and reading them apart finds `plugin action` and loses
// the word that says which action. What is computed rather than written -- a
// plugin id, a machine's name -- becomes "$", which is not a word any command
// or flag is made of, so a message that builds its verb out of a variable ends
// the command there instead of inventing one from whatever follows.
func stringAt(fset *token.FileSet, n ast.Node) (string, int, bool) {
	switch e := n.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", 0, false
		}
		text, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", 0, false
		}
		return text, fset.Position(e.Pos()).Line, true
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", 0, false
		}
		left, _, lok := stringAt(fset, e.X)
		right, _, rok := stringAt(fset, e.Y)
		if !lok && !rok {
			return "", 0, false // not a string concatenation at all
		}
		// The left one is symmetry rather than something a test can see, and
		// measured rather than assumed: a computed leftmost operand puts the
		// placeholder at the very front of the message, where it can neither
		// create nor destroy the backtick that commandsInMessage looks for.
		// Read with "$" and with "" it finds the same commands. It stops being
		// equivalent the moment that rule loosens, which is why it stays.
		if !lok {
			left = "$"
		}
		if !rok {
			right = "$"
		}
		return left + right, fset.Position(e.Pos()).Line, true
	}
	return "", 0, false
}
