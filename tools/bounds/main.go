// Command bounds asks whether each bound in the tree is held by anything.
//
// Every max constant is raised a thousandfold in turn -- exported or not, with
// a suffix or without -- and that package's own tests are run. A bound whose
// loss nothing notices is a bound with no test behind it, and the reason to
// look for those mechanically is that they do not look like gaps. Four were
// found this way, and every one had a test that read as though it held the
// bound:
//
//	if n := len([]rune(long.SafeAgent())); n > maxAgentName {
//
// Raise the constant and the threshold rises with it, so the test passes for
// any value the bound could take, including one that lets a machine put five
// hundred characters in a sidebar. Measuring against the bound under test is
// the shape; a number written out is the fix.
//
// Raising a bound is not always cheap. Where a test sizes its own input from
// the constant -- []int{Max}, or a payload repeated maxFrameBytes times -- a
// thousandfold bound allocates a thousandfold with it. capped.Max at eight
// gigabytes took twenty gigabytes of memory and the machine with it, and the
// killed process exited non-zero, which this tool used to read as "held": the
// one verdict it must never invent. Killed is now its own answer, and the
// tests run under a memory ceiling where the machine can impose one.
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
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
)

// bound matches a max constant declaration and splits it so the value can be
// replaced without disturbing the name or the comment after it.
//
// Both cases and no required suffix: `max[A-Za-z]+` read only unexported names
// that carried one, so it walked past `const Max` in capped -- the tree's one
// exported bound, and an unheld one -- and past a function-local `const max`
// in herdrcli. A bound the scanner does not see reports as nothing at all.
//
// The LEADING class is `[ \t]` and not `\s`, which is what makes the reported
// line the line the bound is on. `\s` matches a newline, so with `(?m)` the
// match could begin at the empty line ABOVE the declaration and swallow it --
// and the line is counted from where the match begins, so every bound with a
// blank line over it was named one line early. Nothing in this tree shows it,
// because every bound here has its doc comment directly above; the fixtures
// have the blank line, and asserting on it is what found this.
//
// Only the leading one narrows. The `\s*` around the `=` still matches a
// newline on purpose: a constant split across two lines is unusual and still a
// bound, and one the scanner does not see reports as nothing at all.
var bound = regexp.MustCompile(`(?m)^([ \t]*(?:const )?[Mm]ax[A-Za-z]*\s*=\s*)([^/\n]+?)(\s*(?://.*)?)$`)

// raise is how much bigger the bound is made. Large enough that no realistic
// input is bounded by it any more, so a test that still passes is a test that
// was never about the bound.
const raise = 1000

// inFlight is the file a mutation is applied to right now, kept so that a
// signal can put it back. check clears it once it has restored the file.
var inFlight struct {
	sync.Mutex
	path     string
	original string
}

// restoreOnSignal puts the mutated file back when the run is interrupted.
//
// The deferred restore in check covers a normal return and a panic, and not a
// signal: SIGTERM and ctrl-c end the process where it stands, deferred
// functions and all. A run given up on halfway then left a constant multiplied
// by a thousand in the tree -- which builds, and which the next commit carries.
// This tool's whole job is to put things back.
func restoreOnSignal() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-stop
		if path, err := putBack(); path != "" {
			if err != nil {
				fmt.Fprintf(os.Stderr, "could not put %s back: %v\n", path, err)
			} else {
				fmt.Fprintf(os.Stderr, "\nput %s back before stopping\n", path)
			}
		}
		if sig, ok := s.(syscall.Signal); ok {
			os.Exit(128 + int(sig))
		}
		// Unreachable as things stand, and kept rather than deleted. Every
		// value os/signal can deliver here is a syscall.Signal, because the two
		// registered above are os.Interrupt -- which IS syscall.SIGINT -- and
		// syscall.SIGTERM, so the assertion never turns anything away.
		// Measured: exiting 99 here instead SURVIVES both rows of
		// TestAnInterruptedRunPutsTheFileBack, which is what says nothing
		// arrives to reach it.
		os.Exit(1)
	}()
}

// putBack restores the file a mutation is applied to, and says which it was.
//
// Nothing in flight is not an error: it is what a run between one bound and the
// next looks like, and a signal arriving then has nothing to undo.
//
// Both callers come through here. The handler and check's own defer can be
// putting the same file back at the same moment, so the write and the
// forgetting happen together under the one lock -- and once it is forgotten a
// second call does nothing, which is what makes the two safe to both run.
//
// A file that could not be written is not forgotten. Whoever asked is told
// which one, and the record still says a mutation is out there, because it is.
func putBack() (string, error) {
	inFlight.Lock()
	defer inFlight.Unlock()

	path := inFlight.path
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, []byte(inFlight.original), 0o644); err != nil {
		return path, err
	}
	inFlight.path, inFlight.original = "", ""
	return path, nil
}

func main() {
	restoreOnSignal()

	root := "internal"
	if len(os.Args) > 1 {
		root = os.Args[1]
	}

	bounded = ceiling()
	if !bounded {
		fmt.Fprint(os.Stderr, "No memory ceiling available here. A bound whose test sizes its own\n"+
			"input from it can take the machine down rather than report.\n\n")
	}

	var held, unheld, unbuildable, noAnswer int
	var loose, unanswered []string
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
			case "killed", "could not write":
				noAnswer++
				unanswered = append(unanswered,
					fmt.Sprintf("%s:%d  %s = %s  -- %s", path, line, name, value, verdict))
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

	fmt.Printf("\n%d held, %d not, %d would not build, %d no answer\n",
		held, unheld, unbuildable, noAnswer)
	if len(loose) > 0 {
		fmt.Print("\nNothing noticed these growing a thousandfold. Read each one and\n" +
			"decide which it is: a bound nothing can observe, or one whose test\n" +
			"measures against the bound itself and so cannot fail.\n\n")
		for _, one := range loose {
			fmt.Println("  " + one)
		}
	}
	if len(unanswered) > 0 {
		fmt.Print("\nThese answered nothing: the process was killed rather than failed, so\n" +
			"whether anything holds the bound is still unknown. A test that sizes its\n" +
			"own input from the constant allocates a thousandfold along with it -- read\n" +
			"the test, and raise these by hand by a little rather than by a lot.\n\n")
		for _, one := range unanswered {
			fmt.Println("  " + one)
		}
	}
}

// memoryCeiling bounds one test process, where the machine can impose it.
const memoryCeiling = "2G"

// bounded records whether that ceiling is actually available.
var bounded bool

// ceiling reports whether a memory ceiling can be put on a test, by imposing
// one once rather than by looking for the binary. systemd-run exists on
// machines whose user session it cannot talk to, and a ceiling that is not
// really there is worse than a missing one: the run looks bounded.
func ceiling() bool {
	return exec.Command("systemd-run", "--user", "--scope", "-q",
		"-p", "MemoryMax="+memoryCeiling, "-p", "MemorySwapMax=0", "true").Run() == nil
}

// testCmd runs one package's tests, under the ceiling where there is one.
func testCmd(pkg string) *exec.Cmd {
	if bounded {
		return exec.Command("systemd-run", "--user", "--scope", "-q",
			"-p", "MemoryMax="+memoryCeiling, "-p", "MemorySwapMax=0",
			"go", "test", pkg, "-count=1")
	}
	return exec.Command("go", "test", pkg, "-count=1")
}

// raisedSource returns the file with the one bound at m multiplied by raise,
// and everything else exactly as it was.
//
// The value is bracketed because it is an expression and not always a literal.
// `4 << 10` multiplied without brackets is `4 << 10 * 1000`, which is `4 <<
// 10000` and not a shift Go will compile -- so every shift-valued bound in the
// tree would report "would not build" and be quietly skipped, which is the
// answer that means nothing was tested.
//
// Apart from check so it can be read without writing to anybody's tree.
func raisedSource(original string, m []int, value string) string {
	return original[:m[3]] + "(" + value + ") * " + fmt.Sprint(raise) +
		original[m[6]:m[7]] + original[m[1]:]
}

// check raises one bound and reports what the package's tests made of it. The
// file is put back whatever happens, since a run that is interrupted has left
// a mutation behind before.
func check(path, original string, m []int, value, pkg string) (verdict string) {
	raised := raisedSource(original, m, value)
	// Recorded before the file is touched, so an interrupt between the write
	// and the defer below still knows what to put back.
	inFlight.Lock()
	inFlight.path, inFlight.original = path, original
	inFlight.Unlock()

	if err := os.WriteFile(path, []byte(raised), 0o644); err != nil {
		return "could not write"
	}
	defer func() {
		if put, err := putBack(); err != nil {
			fmt.Fprintf(os.Stderr, "could not put %s back: %v\n", put, err)
			os.Exit(2)
		}
	}()

	out, err := testCmd(pkg).CombinedOutput()
	return verdictFor(string(out), err)
}

// verdictFor reads what `go test` made of a raised bound.
//
// Apart from check so that it can be tested without running anything. It is
// the judgement this whole tool exists to make, and it was wrong once in the
// direction that matters: every way a run can end badly exits non-zero, and
// only one of them means a test objected.
//
// The order is the point. "held" is the verdict never to invent, since it is
// the one claiming a test stands behind the bound, so everything that ends a
// run without a test having failed is answered before it -- a build that never
// ran, and a process the kernel stopped. capped.Max raised to eight gigabytes
// was killed at twenty and read as held, which is how the tree's one unheld
// bound reported clean.
func verdictFor(out string, err error) string {
	switch {
	case strings.Contains(out, "build failed"):
		return "would not build"
	case strings.Contains(out, "signal: killed"):
		return "killed"
	case err != nil:
		return "held"
	}
	return "NOT HELD"
}
