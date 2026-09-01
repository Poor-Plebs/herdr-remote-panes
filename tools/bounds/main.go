// Command bounds asks whether each bound in the tree is held by anything.
//
// Every max* constant is raised a thousandfold in turn and that package's own
// tests are run. A bound whose loss nothing notices is a bound with no test
// behind it -- and the reason to look for those mechanically is that they do
// not look like gaps. Four were found this way, and every one had a test that
// read as though it held the bound:
//
//	if n := len([]rune(long.SafeAgent())); n > maxAgentName {
//
// Raise the constant and the threshold rises with it, so the test passes for
// any value the bound could take, including one that lets a machine put five
// hundred characters in a sidebar. Measuring against the bound under test is
// the shape; a number written out is the fix.
//
// Not part of `make check`: it builds and tests each package once per bound,
// which is minutes rather than seconds. An unheld bound is something to read
// rather than a failure -- some are not observable at all, and a report that
// fails the build for those is one people stop running.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// bound matches a max* constant declaration and splits it so the value can be
// replaced without disturbing the name or the comment after it.
var bound = regexp.MustCompile(`(?m)^(\s*(?:const )?max[A-Za-z]+\s*=\s*)([^/\n]+?)(\s*(?://.*)?)$`)

// raise is how much bigger the bound is made. Large enough that no realistic
// input is bounded by it any more, so a test that still passes is a test that
// was never about the bound.
const raise = 1000

func main() {
	root := "internal"
	if len(os.Args) > 1 {
		root = os.Args[1]
	}

	var held, unheld, unbuildable int
	var loose []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		original := string(source)
		pkg := "./" + filepath.Dir(path) + "/"
		for _, m := range bound.FindAllStringSubmatchIndex(original, -1) {
			name := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(original[m[2]:m[3]]), "="))
			name = strings.TrimPrefix(name, "const ")
			value := strings.TrimSpace(original[m[4]:m[5]])
			line := strings.Count(original[:m[0]], "\n") + 1

			verdict := check(path, original, m, value, pkg)
			switch verdict {
			case "held":
				held++
			case "would not build":
				unbuildable++
			default:
				unheld++
				loose = append(loose, fmt.Sprintf("%s:%d  %s = %s", path, line, name, value))
			}
			fmt.Printf("%-16s %s:%d  %s = %s\n", verdict, path, line, name, value)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	fmt.Printf("\n%d held, %d not, %d would not build\n", held, unheld, unbuildable)
	if len(loose) > 0 {
		fmt.Print("\nNothing noticed these growing a thousandfold. Read each one and\n" +
			"decide which it is: a bound nothing can observe, or one whose test\n" +
			"measures against the bound itself and so cannot fail.\n\n")
		for _, one := range loose {
			fmt.Println("  " + one)
		}
	}
}

// check raises one bound and reports what the package's tests made of it. The
// file is put back whatever happens, since a run that is interrupted has left
// a mutation behind before.
func check(path, original string, m []int, value, pkg string) (verdict string) {
	raised := original[:m[2]] + original[m[2]:m[3]] + "(" + value + ") * " +
		fmt.Sprint(raise) + original[m[6]:m[7]] + original[m[1]:]
	if err := os.WriteFile(path, []byte(raised), 0o644); err != nil {
		return "could not write"
	}
	defer func() {
		if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "could not put %s back: %v\n", path, err)
			os.Exit(2)
		}
	}()

	out, err := exec.Command("go", "test", pkg, "-count=1").CombinedOutput()
	if strings.Contains(string(out), "build failed") {
		return "would not build"
	}
	if err != nil {
		return "held"
	}
	return "NOT HELD"
}
