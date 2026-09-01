package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestAVerdictIsNotInventedFromAnExitCode holds the one judgement this tool
// makes.
//
// Every way a run can end badly exits non-zero, and only one of them means a
// test objected to the raised bound. Reading the exit code alone answers
// "held" for all of them -- which is the verdict never to invent, since it is
// the one claiming a test stands behind the bound.
//
// This is not hypothetical. capped.Max raised a thousandfold asked for eight
// gigabytes, the kernel stopped the test binary at twenty, and the tool called
// the bound held: the one bound in the tree that nothing held reported clean,
// and the machine went down doing it. The output below is what that run
// actually printed.
func TestAVerdictIsNotInventedFromAnExitCode(t *testing.T) {
	failed := errors.New("exit status 1")

	for _, tt := range []struct {
		what string
		out  string
		err  error
		want string
	}{
		{
			"a test objected, which is the bound being held",
			"--- FAIL: TestTheLimitIsEightMegabytes (0.00s)\n" +
				"    capped_test.go:24: Max = 8388608000, want 8388608\n" +
				"FAIL\tinternal/capped\t0.010s\nFAIL\n",
			failed, "held",
		},
		{
			"the kernel stopped it, which says nothing about the bound",
			"signal: killed\nFAIL\tinternal/capped\t0.054s\nFAIL\n",
			failed, "killed",
		},
		{
			"the mutation did not compile, so nothing was tested",
			"# internal/capped [build failed]\nFAIL\tinternal/capped [build failed]\n",
			failed, "would not build",
		},
		{
			"everything passed with the bound raised, so nothing holds it",
			"ok  \tinternal/capped\t0.009s\n",
			nil, "NOT HELD",
		},
		{
			// Both at once: a build failure is the more useful thing to say,
			// and neither is an answer about the bound.
			"it neither built nor survived",
			"# internal/capped [build failed]\nsignal: killed\n",
			failed, "would not build",
		},
		{
			// The exit code is not what decides a kill. A run stopped after
			// its output was already written can still come back zero, and
			// "NOT HELD" would be a claim about a test that never finished.
			"stopped, but exited zero anyway",
			"signal: killed\n",
			nil, "killed",
		},
	} {
		if got := verdictFor(tt.out, tt.err); got != tt.want {
			t.Errorf("%s: verdict %q, want %q", tt.what, got, tt.want)
		}
	}
}

// TestTheScannerSeesEveryBoundThatIsOne guards the names it reads.
//
// A bound the scanner walks past reports as nothing at all -- not as unheld,
// not as anything. `max[A-Za-z]+` read only unexported names carrying a
// suffix, so it missed `const Max` in capped, which was the tree's one unheld
// bound, and a function-local `const max` in herdrcli.
func TestTheScannerSeesEveryBoundThatIsOne(t *testing.T) {
	for _, tt := range []struct {
		what  string
		line  string
		value string // empty means it must not match at all
	}{
		{"exported, no suffix", "const Max = 8 * 1024 * 1024", "8 * 1024 * 1024"},
		{"unexported, no suffix", "\tconst max = 200", "200"},
		{"unexported with a suffix", "const maxRetryLines = 4", "4"},
		{"inside a const block, aligned", "\tmaxSaid      = 4 << 10", "4 << 10"},
		{"a bound taken from another package", "const maxFrameBytes = capped.Max", "capped.Max"},
		{"a trailing comment is not the value", "const maxFoo = 5 // why five", "5"},

		// Not bounds: a field and a literal key are neither declarations nor
		// values, and raising them would be raising nothing.
		{"a struct field", "\tMax       int", ""},
		{"a key in a composite literal", "\t\tMax:       d.config().MaxMirrors,", ""},
		{"a name that merely starts the same way", "const maximum = 3", "3"},
	} {
		m := bound.FindStringSubmatch(tt.line)
		if tt.value == "" {
			if m != nil {
				t.Errorf("%s: %q matched and should not have", tt.what, tt.line)
			}
			continue
		}
		if m == nil {
			t.Errorf("%s: %q did not match", tt.what, tt.line)
			continue
		}
		if m[2] != tt.value {
			t.Errorf("%s: value is %q, want %q", tt.what, m[2], tt.value)
		}
	}
}

// TestTheRaiseIsSomethingGoWillStillCompile guards the mutation itself.
//
// A raise that does not compile reports "would not build", which is the answer
// that means nothing was tested. Made silently, for every bound of a shape
// nobody tried by hand, that is a sweep of clean-looking verdicts about
// nothing at all.
//
// The brackets are the whole of it. `4 << 10` raised without them is
// `4 << 10 * 1000`, which Go reads as `4 << 10000`.
func TestTheRaiseIsSomethingGoWillStillCompile(t *testing.T) {
	for _, tt := range []struct {
		what string
		src  string
		want string
	}{
		{
			"a product",
			"package p\n\nconst Max = 8 * 1024 * 1024\n\nfunc f() {}\n",
			"package p\n\nconst Max = (8 * 1024 * 1024) * 1000\n\nfunc f() {}\n",
		},
		{
			// The one that brackets exist for.
			"a shift",
			"package p\n\nconst maxSaid = 4 << 10\n",
			"package p\n\nconst maxSaid = (4 << 10) * 1000\n",
		},
		{
			"a bound taken from another package",
			"package p\n\nconst maxFrameBytes = capped.Max\n",
			"package p\n\nconst maxFrameBytes = (capped.Max) * 1000\n",
		},
		{
			"a trailing comment stays where it was",
			"package p\n\nconst max = 200 // characters\n",
			"package p\n\nconst max = (200) * 1000 // characters\n",
		},
		{
			"an aligned entry inside a const block",
			"package p\n\nconst (\n\tmaxSaid      = 4 << 10\n\tmaxSaidWidth = 200\n)\n",
			"package p\n\nconst (\n\tmaxSaid      = (4 << 10) * 1000\n\tmaxSaidWidth = 200\n)\n",
		},
	} {
		m := bound.FindStringSubmatchIndex(tt.src)
		if m == nil {
			t.Errorf("%s: nothing matched in %q", tt.what, tt.src)
			continue
		}
		value := strings.TrimSpace(tt.src[m[4]:m[5]])
		if got := raisedSource(tt.src, m, value); got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.what, got, tt.want)
		}
	}
}

// inFlightFile puts a mutated file on disk and records it the way check does.
func inFlightFile(t *testing.T, original, mutated string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capped.go")
	if err := os.WriteFile(path, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
	inFlight.Lock()
	inFlight.path, inFlight.original = path, original
	inFlight.Unlock()
	t.Cleanup(func() {
		inFlight.Lock()
		inFlight.path, inFlight.original = "", ""
		inFlight.Unlock()
	})
	return path
}

// TestAMutationIsPutBackAndThenLetGoOf holds what stands between a run that is
// interrupted and a raised constant left in somebody's tree.
//
// The mutation builds and most tests still pass, so nothing points at it, and
// `git status` shows one modified file sitting beside the work in progress.
// That is three commands from being committed, and it has happened here.
func TestAMutationIsPutBackAndThenLetGoOf(t *testing.T) {
	const was = "const Max = 8 * 1024 * 1024\n"
	path := inFlightFile(t, was, "const Max = (8 * 1024 * 1024) * 1000\n")

	put, err := putBack()
	if err != nil {
		t.Fatalf("putting %s back: %v", put, err)
	}
	if put != path {
		t.Errorf("said it put back %q, want %q", put, path)
	}
	if raw, _ := os.ReadFile(path); string(raw) != was {
		t.Errorf("the file holds %q, want %q", raw, was)
	}

	// Let go of, so that the second caller does nothing. The signal handler
	// and check's own defer both come through here, and the handler can arrive
	// after the defer has already finished -- at which point the file may
	// belong to the next bound, or to somebody editing it.
	if err := os.WriteFile(path, []byte("someone else's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if put, err := putBack(); put != "" || err != nil {
		t.Errorf("a second put back reported %q, %v; want nothing to do", put, err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "someone else's work\n" {
		t.Errorf("a second put back overwrote the file with %q", raw)
	}
}

// TestNothingInFlightIsNothingToPutBack covers the gap between two bounds.
func TestNothingInFlightIsNothingToPutBack(t *testing.T) {
	inFlight.Lock()
	inFlight.path, inFlight.original = "", ""
	inFlight.Unlock()

	if put, err := putBack(); put != "" || err != nil {
		t.Errorf("with nothing in flight: %q, %v; want nothing to do", put, err)
	}
}

// TestAFileThatCannotBePutBackIsNotForgotten keeps the record honest.
//
// Forgetting a file the write failed on says the tree is clean when a raised
// constant is still sitting in it, and the caller that wanted to complain
// loudly no longer has the name to complain about.
func TestAFileThatCannotBePutBackIsNotForgotten(t *testing.T) {
	// A directory cannot be written as a file, which is a real failure rather
	// than a contrived one: the path came from a tree walk.
	dir := t.TempDir()
	inFlight.Lock()
	inFlight.path, inFlight.original = dir, "const Max = 8 * 1024 * 1024\n"
	inFlight.Unlock()
	t.Cleanup(func() {
		inFlight.Lock()
		inFlight.path, inFlight.original = "", ""
		inFlight.Unlock()
	})

	put, err := putBack()
	if err == nil {
		t.Fatal("writing a file over a directory was reported as success")
	}
	if put != dir {
		t.Errorf("the failure names %q, want the file it could not write, %q", put, dir)
	}

	inFlight.Lock()
	still := inFlight.path
	inFlight.Unlock()
	if still != dir {
		t.Errorf("a file that was not put back was forgotten anyway (now %q)", still)
	}
}

// TestATestRunIsFreshAndBoundedWhereItCanBe reads the command without running
// it.
//
// Two things in here decide whether a verdict means anything.
//
// A cached result is a pass reporting on the code as it was before the
// mutation, and this project has had one: a subprocess test answered
// "ok (cached)" after the daemon it drives had been edited, because nothing
// the subprocess read was recorded as an input. `-count=1` is what makes the
// answer be about the file that is on disk now.
//
// And a ceiling that is not really there is worse than a missing one, because
// the run looks bounded. Where one is claimed, the properties that impose it
// have to actually be in the command.
func TestATestRunIsFreshAndBoundedWhereItCanBe(t *testing.T) {
	was := bounded
	t.Cleanup(func() { bounded = was })

	const pkg = "./internal/capped/"
	for _, tt := range []struct {
		what    string
		ceiling bool
	}{
		{"where a ceiling can be imposed", true},
		{"where none can be", false},
	} {
		bounded = tt.ceiling
		args := testCmd(pkg).Args
		joined := strings.Join(args, " ")

		if !slices.Contains(args, "-count=1") {
			t.Errorf("%s: no -count=1, so a cached pass can answer for the "+
				"file as it was before the mutation: %s", tt.what, joined)
		}
		if !slices.Contains(args, pkg) {
			t.Errorf("%s: the package under test is not in the command: %s", tt.what, joined)
		}
		if !strings.Contains(joined, "go test") {
			t.Errorf("%s: this does not run the tests at all: %s", tt.what, joined)
		}

		imposed := slices.Contains(args, "MemoryMax="+memoryCeiling)
		if imposed != tt.ceiling {
			t.Errorf("%s: a ceiling is imposed = %v, want %v: %s",
				tt.what, imposed, tt.ceiling, joined)
		}
		if tt.ceiling && !slices.Contains(args, "MemorySwapMax=0") {
			t.Errorf("%s: memory is capped and swap is not, so the run leans on "+
				"swap instead of failing: %s", tt.what, joined)
		}
	}
}
