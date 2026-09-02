package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestOnlyStatementsWorthRemovingAreOffered holds what the sweep is about.
//
// What this lets through decides what the whole run reports on. Too narrow and
// the side effects it exists to find are never tried; too wide and every run
// is mostly "would not build", which is not a verdict about anything and takes
// a test run each to say so.
func TestOnlyStatementsWorthRemovingAreOffered(t *testing.T) {
	for _, tt := range []struct {
		what string
		line string
		want bool
	}{
		{"a call standing alone", "\tc.Stop()", true},
		{"a field being set", "\tcmd.Stderr = &stderr", true},
		{"an element being marked", "\t\t\tseen[host] = true", true},
		{"a counter moving on", "\tf.written += int64(n)", true},
		{"an aligned assignment in a block", "\tmaxSaid      = 4 << 10", true},

		// Left alone: deleting these stops the build for a reason of its own,
		// which is a compiler error rather than anything about the tests.
		{"a short declaration", "\tcfg, err := config.Load()", false},
		{"a return", "\treturn path, nil", false},
		{"a guard", "\tif path == \"\" {", false},
		{"a loop", "\tfor _, host := range cfg.Hosts {", false},
		{"a deferred call", "\tdefer f.Close()", false},
		{"a goroutine", "\tgo func() {", false},
		{"a comment that reads like an assignment", "\t// path = clean(path)", false},
		{"a case in a switch", "\tcase \"daemon\":", false},
		{"a func literal handed to a call", "\t\tfunc(s place) bool { return s.id == id })", false},

		// The keywords are structural only where the statement starts with
		// one. These are ordinary statements whose strings happen to be
		// English, and reading the words anywhere on the line passed over
		// eight real ones in this tree.
		{"a string with for in it", "\tchanged = \"mirroring off for \" + cmd.Host", true},
		{"a message with go in it", "\tlog.Printf(\"no space of its own to go to\", target)", true},
		{"a message with if in it", "\tr.state = \"set herdr_bin if it is installed\"", true},
	} {
		if got := deletable(tt.line); got != tt.want {
			t.Errorf("%s: deletable(%q) = %v, want %v", tt.what, tt.line, got, tt.want)
		}
	}
}

// TestAVerdictIsNotInventedFromAnExitCode holds the judgement the run rests on.
//
// Every way a run can end badly exits non-zero and only one of them means a
// test objected. Reading the exit code alone answers "caught" for all of them,
// which is the answer that says a test stands behind the statement — and a
// build that never ran, a process the kernel stopped and a run that never
// finished say nothing of the kind.
func TestAVerdictIsNotInventedFromAnExitCode(t *testing.T) {
	for _, tt := range []struct {
		what     string
		out      string
		failed   bool
		timedOut bool
		want     string
	}{
		{"a test objected", "--- FAIL: TestX\nFAIL\n", true, false, "caught"},
		{"everything still passed", "ok  \tpkg\t0.1s\n", false, false, "SURVIVED"},
		{"the deletion did not compile", "# pkg [build failed]\n", true, false, "would not build"},
		{"the kernel stopped it", "signal: killed\nFAIL\n", true, false, "hung"},
		{"it never finished", "", false, true, "hung"},
		// A run that was killed after it had already written a failure still
		// says nothing: the exit code is not what decides this.
		{"killed with a failure already printed", "--- FAIL: TestX\nsignal: killed\n", true, false, "hung"},
		{"timed out with output that looks like a pass", "ok  \tpkg\t0.1s\n", false, true, "hung"},
	} {
		if got := verdictFor(tt.out, tt.failed, tt.timedOut); got != tt.want {
			t.Errorf("%s: verdict %q, want %q", tt.what, got, tt.want)
		}
	}
}

// TestTheFileGoesBackWhateverHappened holds the promise the tree depends on.
func TestTheFileGoesBackWhateverHappened(t *testing.T) {
	const was = "package p\n\nfunc F() { g() }\n"
	path := filepath.Join(t.TempDir(), "p.go")
	if err := os.WriteFile(path, []byte("package p\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inFlight.Lock()
	inFlight.path, inFlight.original = path, was
	inFlight.Unlock()

	put, err := putBack()
	if err != nil || put != path {
		t.Fatalf("putBack() = %q, %v; want %q, nil", put, err, path)
	}
	if raw, _ := os.ReadFile(path); string(raw) != was {
		t.Errorf("the file holds %q, want %q", raw, was)
	}

	// Let go of, so a second caller does nothing: the signal handler and the
	// sweep's own defer both come through here, and the handler can arrive
	// after the defer has finished, when the file belongs to the next
	// statement or to somebody editing it.
	if err := os.WriteFile(path, []byte("someone else's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if put, err := putBack(); put != "" || err != nil {
		t.Errorf("a second putBack reported %q, %v; want nothing to do", put, err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "someone else's work\n" {
		t.Errorf("a second putBack overwrote the file with %q", raw)
	}
}

// TestALineIsTakenOutWithoutDisturbingTheRest keeps the deletion to one line.
func TestALineIsTakenOutWithoutDisturbingTheRest(t *testing.T) {
	source := "package p\n\nfunc F() {\n\ta()\n\tb()\n\tc()\n}\n"
	lines := strings.Split(source, "\n")
	got := withoutLine(lines, 4) // the b() line
	want := "package p\n\nfunc F() {\n\ta()\n\tc()\n}\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Count(got, "\n") != strings.Count(source, "\n")-1 {
		t.Errorf("the file lost %d lines, want one",
			strings.Count(source, "\n")-strings.Count(got, "\n"))
	}
}

// TestBothCommandsRunInTheTreeBeingSwept holds what -root is for.
//
// The deletion is written into the tree named by -root. A command run anywhere
// else answers for a file nothing has touched: the build says every deletion
// compiles, including the ones that do not, and the tests pass because they are
// testing source that was never changed. The run reports on a tree it never
// altered, and the tree it did alter is somebody's working copy.
//
// The build was doing exactly that. It set no directory at all, so with -root
// it compiled the real tree while the deletion sat in the copy.
func TestBothCommandsRunInTheTreeBeingSwept(t *testing.T) {
	const root, pkg = "/tmp/sweep", "./internal/capped/"

	build := buildCmd(root, pkg)
	if build.Dir != root {
		t.Errorf("the build runs in %q, want the tree being swept, %q", build.Dir, root)
	}
	if !slices.Contains(build.Args, pkg) {
		t.Errorf("the build does not name the package: %v", build.Args)
	}

	run := testCmd(root, pkg)
	if run.Dir != root {
		t.Errorf("the tests run in %q, want the tree being swept, %q", run.Dir, root)
	}
	if !slices.Contains(run.Args, "-count=1") {
		t.Errorf("no -count=1, so a cached pass can answer for the file as it was "+
			"before the deletion: %v", run.Args)
	}
	if !slices.Contains(run.Args, "MemoryMax="+memoryCeiling) {
		t.Errorf("no memory ceiling, and a deleted loop increment takes the machine "+
			"rather than the run: %v", run.Args)
	}
	if !slices.Contains(run.Args, "MemorySwapMax=0") {
		t.Errorf("memory is capped and swap is not, so a runaway grinds instead of "+
			"failing: %v", run.Args)
	}
	if !strings.Contains(strings.Join(run.Args, " "), "go test") {
		t.Errorf("this does not run the tests at all: %v", run.Args)
	}
}
