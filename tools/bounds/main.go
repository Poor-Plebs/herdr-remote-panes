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
// Time is the same cost in the other currency, and it read as held for
// longer. maxObserveAttempts raised a thousandfold makes a retry loop wait
// out four thousand attempts, so internal/mirror never finishes -- and a
// suite stopped by its own deadline prints a panic and no failing test at
// all, which is non-zero like everything else. Timed out is its own answer
// now too.
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
//
// The VALUE excludes only newlines, not `/`. It used to exclude both, which
// read as the way to stop it swallowing a trailing `// comment` -- and the
// non-greedy repeat is what actually does that, since the group after it
// takes the comment and `$` anchors the pair. Excluding `/` bought nothing
// there and hid a whole shape: a bound expressed as a division, like
// `maxHalf = maxWhole / 2`, matched nothing at all. Measured across internal/
// when it widened: 29 bounds before and after, every line, value and raised
// source byte-identical.
//
// The honest limit, since it is the same silence one step along: a `//` INSIDE
// a string still splits the value early, so `const maxURL = "http://x"` is read
// as `"http:`. That raise will not compile, and the run then counts it as
// "would not build" -- which is a non-answer somebody is TOLD about, where the
// old behaviour was to leave it out of the report entirely.
var bound = regexp.MustCompile(`(?m)^([ \t]*(?:const )?[Mm]ax[A-Za-z]*\s*=\s*)([^\n]+?)(\s*(?://.*)?)$`)

// raise is how much bigger the bound is made. Large enough that no realistic
// input is bounded by it any more, so a test that still passes is a test that
// was never about the bound.
const raise = 1000

// raises are the factors tried, in order, until one of them answers.
//
// The thousandfold is the strong signal and settles all but a few at once. It
// cannot settle a bound whose COST grows with its value: a retry count raised
// a thousandfold does not make a test disagree, it makes the suite wait, and
// the run is then stopped rather than failed -- a non-answer, and the report
// has always said to "raise these by hand by a little rather than by a lot".
// This does that little raise instead of asking somebody else to.
//
// Doubling is enough to break a test that pins the value and cheap enough that
// a quadratic wait stays small: maxObserveAttempts at eight is thirty-six
// steps, where at four thousand it is eight million.
var raises = []int{raise, 2}

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

// replaceFile writes contents over path without ever leaving it half written.
//
// os.WriteFile truncates and then fills, so anything reading the file in
// between sees one that is not there yet -- and what reads this file is the
// `go test` running beside the restore that fires when a run is interrupted.
// Measured in tools/deletions, which had the same pair of writes: 10 reads in
// 200 landed on a file that would not parse, and the CI failure it explains is
// a verdict of would-not-build for a bound nothing was wrong with.
//
// A temporary beside it and a rename, which is what internal/config and
// internal/syncd already do for the files they replace. No Sync: what this
// guards against is another process reading mid-write, not a power cut, and
// this file is a working copy of source that is about to be put back anyway.
func replaceFile(path, contents string) error {
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name) // No-op once the rename below succeeds.

	if _, err := temp.WriteString(contents); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// CreateTemp makes it private, and this is source in a tree somebody else
	// is about to read.
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
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
	if err := replaceFile(path, inFlight.original); err != nil {
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
			case "killed", "timed out", "could not write":
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
		fmt.Print("\nThese answered nothing: the run was stopped rather than failed, so\n" +
			"whether anything holds the bound is still unknown. A test that sizes its\n" +
			"own input from the constant allocates a thousandfold along with it, and one\n" +
			"that waits a step per attempt waits a thousandfold too -- read the test, and\n" +
			"raise these by hand by a little rather than by a lot.\n\n")
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

// raisedSourceBy returns the file with the one bound at m multiplied by the
// factor given, and everything else exactly as it was.
//
// The value is bracketed because it is an expression and not always a literal.
// `4 << 10` multiplied without brackets is `4 << 10 * 1000`, which is `4 <<
// 10000` and not a shift Go will compile -- so every shift-valued bound in the
// tree would report "would not build" and be quietly skipped, which is the
// answer that means nothing was tested.
//
// The factor is a parameter rather than the raise constant because a bound the
// thousandfold cannot settle is asked again by a little; raises is the list.
//
// Apart from check so it can be read without writing to anybody's tree.
func raisedSourceBy(original string, m []int, value string, factor int) string {
	return original[:m[3]] + "(" + value + ") * " + fmt.Sprint(factor) +
		original[m[6]:m[7]] + original[m[1]:]
}

// check raises one bound and reports what the package's tests made of it. The
// file is put back whatever happens, since a run that is interrupted has left
// a mutation behind before.
func check(path, original string, m []int, value, pkg string) (verdict string) {
	// Recorded before the file is touched, so an interrupt between the write
	// and the defer below still knows what to put back.
	inFlight.Lock()
	inFlight.path, inFlight.original = path, original
	inFlight.Unlock()

	defer func() {
		if put, err := putBack(); err != nil {
			fmt.Fprintf(os.Stderr, "could not put %s back: %v\n", put, err)
			os.Exit(2)
		}
	}()

	for i, factor := range raises {
		// Written from the original every time rather than from the last
		// mutation, so a second attempt raises the bound once and not twice.
		if err := replaceFile(path, raisedSourceBy(original, m, value, factor)); err != nil {
			return "could not write"
		}
		out, err := testCmd(pkg).CombinedOutput()
		verdict = verdictFor(string(out), err)
		if !askAgain(verdict, len(raises)-1-i) {
			return verdict
		}
	}
	return verdict
}

// askAgain reports whether a verdict is worth asking again with a smaller
// raise, and how many raises are left to ask with.
//
// Only a run that was STOPPED is worth a second question. "held" and "not
// held" are answers; "would not build" is an answer about the source rather
// than about the tests, and raising by less will not make it compile. A
// stopped run is the one verdict that says nothing at all -- and for a bound
// whose cost grows with its value, a thousandfold always stops the run, so
// without this the tool could never answer that class however many times it
// was pointed at it.
//
// Apart from check so the decision can be read without running a suite, which
// is the same reason verdictFor is apart from it.
func askAgain(verdict string, raisesLeft int) bool {
	return verdict == "timed out" && raisesLeft > 0
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
// ran, a process the kernel stopped, and a suite that never finished.
// capped.Max raised to eight gigabytes was killed at twenty and read as held,
// which is how the tree's one unheld bound reported clean.
//
// The third of those was found the same way, by reading which tests actually
// objected: maxObserveAttempts raised a thousandfold makes the retry loop wait
// out four thousand attempts, so internal/mirror never finishes, and a suite
// stopped by its own deadline prints a panic and no `--- FAIL:` line at all.
// It exits non-zero like everything else, and that was enough to be called
// held. Matched on Go's own panic prefix rather than on the words alone, so a
// test whose failure message happens to talk about timing out is still read as
// the objection it is.
func verdictFor(out string, err error) string {
	switch {
	case strings.Contains(out, "build failed"):
		return "would not build"
	case strings.Contains(out, "signal: killed"):
		return "killed"
	case strings.Contains(out, "panic: test timed out"):
		return "timed out"
	case err != nil:
		return "held"
	}
	return "NOT HELD"
}
