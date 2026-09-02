// Command deletions reports statements that can be removed without any test
// noticing.
//
// `make mutants` flips operators and drops negations: && for ||, > for >=, a
// guard inverted. That finds a decision nothing checks. It cannot find a side
// effect nothing checks, because it never removes one — and a side effect
// whose absence leaves the happy path working is invisible to every other
// check here as well. The tests still pass, the package still builds, and the
// thing that was supposed to happen quietly does not.
//
// Thirteen were found this way before this tool existed, by hand:
//
//   - capped's c.Stop(), so an overrunning command ran to its timeout instead
//     of being cancelled.
//   - logfile's Close in rotate, leaking one descriptor per rotation until the
//     process reached its limit.
//   - mirror's frames.Buffer, so the scanner kept bufio's 64KB default and a
//     screen repaint ended the mirror.
//   - mirror's --takeover, so a terminal mirrored once and then never again.
//   - sshconfig's seen[host] across includes, listing the same machine twice.
//   - text's `previous = r`, measuring a run of emoji selectors a cell each.
//
// Every one of them reads as thoroughly tested code. The shape that keeps
// recurring is a well-tested helper nothing checks the wiring of: takeover was
// tested and the flag was never sent; maxFrameBytes was pinned and the line
// applying it was not; RunError was tested in every combination and the stream
// it formats never reached it.
//
//	go run ./tools/deletions ./internal/capped
//	go run ./tools/deletions ./internal/syncd 180
//
// Not part of `make check`: it builds and tests the package once per statement,
// which is minutes for a small one and hours for the daemon. A survivor is
// something to read rather than a failure — plenty are logging, where a test
// would fit the message rather than the behaviour, and some are equivalent:
// sshconfig's `started` flags produce an empty field that the caller discards
// three lines later, so deleting both changes nothing observable.
//
// The working tree is mutated in place and put back, so run it against a
// worktree when the sweep is long enough that you would rather keep committing:
//
//	git worktree add --detach /tmp/sweep HEAD
//	go run ./tools/deletions -root /tmp/sweep ./internal/syncd 180
//
// The flag comes first: Go stops reading them at the first argument that is
// not one, so a -root after the package is a word nothing looks at.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// effect matches a whole statement that does something and returns nothing to
// the line it is on: an assignment, or a call standing alone.
//
// skip takes back the ones that only look like that: a short declaration,
// whose deletion leaves every later use of the name undefined, and control
// flow, whose deletion leaves a brace with nothing to open. Both stop the
// build for a reason of their own, which is a compiler error rather than
// anything about the tests.
//
// Anchored to the start of the statement, because a keyword is only structural
// where the statement begins with it. Matching the words anywhere on the line
// reads them inside strings as well, and this project's messages are English:
// "mirroring off for ", "no space of its own to go to", "if it is installed
// elsewhere". Eight real statements were being passed over for a word in a
// sentence, and the only line the loose spelling caught that this one does not
// is a func literal, which begins with func and is caught here too.
var (
	effect = regexp.MustCompile(`^\s*(?:[\w\.\[\]\(\)]+\s*(?:=|\+=|-=)\s*.+|[\w\.]+\(.*\))\s*$`)
	skip   = regexp.MustCompile(`^\s*(?:return|if|for|func|defer|go|case|switch|//)\b|^\s*[\w\.,\s]+:=`)
)

// deletable reports whether a line is a statement worth removing on its own.
//
// Apart from the sweep so the judgement can be read without running anything,
// and because what it lets through decides what the whole run is about.
func deletable(line string) bool {
	return effect.MatchString(line) && !skip.MatchString(line)
}

// memoryCeiling bounds one test run.
//
// Deleting a loop's increment makes a loop that never ends, and one that
// appends as it goes takes the machine rather than the run: `i += size` in the
// mirror's mouse gate did exactly that, and the sweep died with the file still
// mutated. Under a ceiling the test is killed instead, and the file goes back.
const memoryCeiling = "2G"

// verdictFor reads what happened to one deletion.
//
// The order matters. Every way a run can end badly exits non-zero and only one
// of them means a test objected, so "caught" is the answer never to invent:
// a build that never ran, a process the kernel stopped, and a run that never
// finished are all answered before it.
func verdictFor(out string, failed, timedOut bool) string {
	switch {
	case timedOut:
		return "hung"
	case strings.Contains(out, "signal: killed"):
		return "hung"
	case strings.Contains(out, "build failed"):
		return "would not build"
	case failed:
		return "caught"
	}
	return "SURVIVED"
}

// inFlight is the file a deletion is applied to right now, kept so a signal can
// put it back. The sweep clears it once the file is restored.
var inFlight struct {
	sync.Mutex
	path     string
	original string
}

// putBack restores the file a deletion is applied to, and says which it was.
//
// Nothing in flight is not an error: it is what the moment between two
// statements looks like. Both callers come through here so the write and the
// forgetting happen together, and a second call does nothing.
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

// restoreOnSignal puts the file back when the run is interrupted.
//
// The deferred restore covers a normal return and a panic, and not a signal:
// SIGTERM and ctrl-c end the process where it stands. A run given up on halfway
// then leaves a statement missing from the tree, which builds, and which the
// next commit carries.
func restoreOnSignal() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
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
		os.Exit(1)
	}()
}

func main() {
	root := flag.String("root", ".", "the tree to sweep, so a long run can leave yours alone")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: deletions ./internal/pkg [per-test seconds] [-root dir]")
		os.Exit(2)
	}
	pkg := flag.Arg(0)
	limit := 120 * time.Second
	if flag.NArg() > 1 {
		var secs int
		if _, err := fmt.Sscanf(flag.Arg(1), "%d", &secs); err == nil && secs > 0 {
			limit = time.Duration(secs) * time.Second
		}
	}

	if exec.Command("systemd-run", "--user", "--scope", "-q",
		"-p", "MemoryMax="+memoryCeiling, "true").Run() != nil {
		fmt.Fprintln(os.Stderr, "no memory ceiling available here, and a deleted loop "+
			"increment would take the machine rather than the run")
		os.Exit(2)
	}
	restoreOnSignal()

	dir := filepath.Join(*root, strings.TrimPrefix(pkg, "./"))
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "no Go files under %s\n", dir)
		os.Exit(2)
	}

	todo, err := candidatesIn(entries)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("%d statements to try in %s\n", len(todo), pkg)

	counts := map[string]int{}
	var loose, hung []string
	started, lastReport := time.Now(), time.Now()
	for i, c := range todo {
		verdict := sweep(c.path, c.original, c.lines, c.at, *root, pkg, limit)
		counts[verdict]++
		where := fmt.Sprintf("%s:%d  %s", c.path, c.at+1, strings.TrimSpace(c.lines[c.at]))
		switch verdict {
		case "SURVIVED":
			loose = append(loose, where)
		case "hung":
			hung = append(hung, where)
		}
		// Every 25, or every minute, whichever comes first. A sweep of the
		// daemon is one build and one test run per statement for three hundred
		// statements, and silence from something that is working reads exactly
		// like silence from something that is stuck.
		if n := i + 1; n%25 == 0 || time.Since(lastReport) >= time.Minute {
			lastReport = time.Now()
			left := time.Duration(float64(time.Since(started)) / float64(n) * float64(len(todo)-n))
			fmt.Printf("... %d/%d, %d survived, about %s left\n",
				n, len(todo), counts["SURVIVED"], left.Round(time.Second))
		}
	}

	fmt.Printf("\n%s: %d caught, %d survived, %d hung, %d would not build\n",
		pkg, counts["caught"], counts["SURVIVED"], counts["hung"], counts["would not build"])
	if len(loose) > 0 {
		fmt.Print("\nNothing failed when these were removed. Read each one and decide\n" +
			"which it is: a side effect nothing checks, a line whose absence cannot\n" +
			"be observed, or logging where a test would fit the message rather than\n" +
			"the behaviour.\n\n")
		for _, one := range loose {
			fmt.Println("  " + one)
		}
	}
	for _, one := range hung {
		fmt.Println("  hung or killed: " + one)
	}
}

// sweep removes one line, runs the package's tests, and puts the file back.
func sweep(path, original string, lines []string, i int, root, pkg string, limit time.Duration) string {
	// Recorded before the file is touched, so an interrupt between the write
	// and the restore still knows what to put back.
	inFlight.Lock()
	inFlight.path, inFlight.original = path, original
	inFlight.Unlock()
	defer func() {
		if put, err := putBack(); err != nil {
			fmt.Fprintf(os.Stderr, "could not put %s back: %v\n", put, err)
			os.Exit(2)
		}
	}()

	if err := os.WriteFile(path, []byte(withoutLine(lines, i)), 0o644); err != nil {
		return "would not build"
	}
	if buildCmd(root, pkg).Run() != nil {
		return "would not build"
	}

	cmd := testCmd(root, pkg)
	done := make(chan struct{})
	var out []byte
	var failed bool
	go func() {
		raw, err := cmd.CombinedOutput()
		out, failed = raw, err != nil
		close(done)
	}()
	select {
	case <-done:
		return verdictFor(string(out), failed, false)
	case <-time.After(limit):
		_ = cmd.Process.Kill()
		<-done
		return verdictFor(string(out), failed, true)
	}
}

// candidate is one statement the sweep will try, and the file it lives in.
type candidate struct {
	path     string
	original string
	lines    []string
	at       int
}

// candidatesIn is every statement worth deleting in these files, in the order
// they will be tried.
//
// Gathered before any of them is tried, so the run can say how far along it is
// and how long is left. Counting them costs one read of each file against
// hours of building and testing, and the alternative is a sweep that says
// nothing until it is finished.
//
// Test files are left out: what a test does to itself is not what this asks
// about. A line that appears twice in its file is left out as well, since the
// report could not say which of them it meant.
func candidatesIn(paths []string) ([]candidate, error) {
	var out []candidate
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		original := string(raw)
		lines := strings.Split(original, "\n")
		for i, line := range lines {
			if !deletable(line) || strings.Count(original, line+"\n") != 1 {
				continue
			}
			out = append(out, candidate{path: path, original: original, lines: lines, at: i})
		}
	}
	return out, nil
}

// buildCmd compiles the package in the tree being swept.
//
// In that tree and not this one. The deletion is written into the copy, so a
// build run anywhere else compiles source nothing has touched and answers for
// the wrong file -- it says every deletion compiles, including the ones that
// do not, and the run reports on a tree it never changed.
func buildCmd(root, pkg string) *exec.Cmd {
	cmd := exec.Command("go", "build", pkg)
	cmd.Dir = root
	return cmd
}

// testCmd runs one package's tests in the tree being swept, under the ceiling.
//
// -count=1 because a cached result is a pass reporting on the file as it was
// before the deletion, which is the answer this whole tool is trying not to
// give.
func testCmd(root, pkg string) *exec.Cmd {
	cmd := exec.Command("systemd-run", "--user", "--scope", "-q",
		"-p", "MemoryMax="+memoryCeiling, "-p", "MemorySwapMax=0",
		"go", "test", pkg, "-count=1")
	cmd.Dir = root
	return cmd
}

// withoutLine is the file with one line taken out of it.
func withoutLine(lines []string, i int) string {
	kept := make([]string, 0, len(lines)-1)
	kept = append(kept, lines[:i]...)
	return strings.Join(append(kept, lines[i+1:]...), "\n")
}
