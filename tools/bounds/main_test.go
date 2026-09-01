package main

import (
	"errors"
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
