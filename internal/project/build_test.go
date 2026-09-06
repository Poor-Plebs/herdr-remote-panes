package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTheBuildSaysWhenGoIsMissing runs the build with nothing to build with.
//
// This is the one path CI cannot take: a runner always has Go, so the message
// that exists for a machine without it is never printed and could rot without
// anything noticing — and it is the whole reason the manifest spawns a script
// rather than the compiler. Herdr spawning `go` on a machine that has none
// fails with "No such file or directory" before anything of ours runs, which
// reads like the plugin is broken rather than like a missing dependency.
func TestTheBuildSaysWhenGoIsMissing(t *testing.T) {
	inRoot(t)

	script, err := os.ReadFile("build.sh")
	if err != nil {
		t.Fatal(err)
	}

	// Every absolute place the script looks, pointed somewhere there is
	// nothing, so that "found nowhere" can be reached on a machine that does
	// have Go. The relative ones fall away with HOME below.
	absolute := regexp.MustCompile(`(?m)^\s*(/\S*/go)\b`)
	if !absolute.Match(script) {
		t.Fatal("the script no longer looks in any absolute location; this test is checking nothing")
	}
	stripped := absolute.ReplaceAllString(string(script), "\t\t/nonexistent-for-this-test$1")

	dir := t.TempDir()
	copied := filepath.Join(dir, "build.sh")
	if err := os.WriteFile(copied, []byte(stripped), 0o755); err != nil {
		t.Fatal(err)
	}

	// A PATH with the shell's own tools and no compiler.
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"sh", "cat"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s to build a stand-in PATH with: %v", tool, err)
		}
		if err := os.Symlink(found, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command(filepath.Join(bin, "sh"), copied)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + bin, "HOME=" + filepath.Join(dir, "no-home")}
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("a build with no Go anywhere reported success:\n%s", out)
	}
	said := string(out)
	// What somebody needs: what is missing, that it is needed, and where to
	// get it. Not "No such file or directory".
	for _, want := range []string{"Go was not found", "go.dev/dl", "install the plugin again"} {
		if !strings.Contains(said, want) {
			t.Errorf("the build does not say %q:\n%s", want, said)
		}
	}
	// And the other reading of the same symptom, which is the commoner one on
	// a desktop: Go is there, and the session Herdr runs in cannot see it.
	if !strings.Contains(said, "PATH") {
		t.Errorf("the build does not mention PATH, which is the other way this happens:\n%s", said)
	}
}

// TestTheFloorJobRunsOnTheFloorTheModuleDeclares holds the version CI calls
// "oldest supported Go" to the one go.mod actually asks for.
//
// The floor is written in FOUR places. go.mod declares it; the README says
// "Go 1.25+", and TestTheREADMERequiresWhatTheProjectRequires holds that
// against go.mod; the matrix job deliberately uses a current toolchain; and
// this job pins the floor by hand. Only the last was tied to nothing.
//
// It is the same change that stranded the README. That test's own comment says
// so -- "the Go floor was raised once already, which is exactly the change
// that would have stranded it" -- and raising it again would leave this job
// pinned below the module, which is not a version anybody supports.
//
// Which way it drifts decides how it is found, and only one way is loud.
// Raising go.mod above the pin fails this job outright, because GOTOOLCHAIN is
// local there on purpose and Go refuses rather than fetching a newer
// toolchain: a real failure, though one that reads as a toolchain error rather
// than as a stale pin. Lowering go.mod is silent -- the job goes on testing a
// version above the floor and passing, and the name of the job is then the
// only thing that says otherwise. This holds both directions, and says which
// file to edit.
func TestTheFloorJobRunsOnTheFloorTheModuleDeclares(t *testing.T) {
	inRoot(t)

	mod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	declared := regexp.MustCompile(`(?m)^go (\S+)$`).FindSubmatch(mod)
	if declared == nil {
		t.Fatal("go.mod names no Go version, so there is no floor to compare")
	}
	floor := string(declared[1])

	workflow, err := os.ReadFile(filepath.Join(".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// From the floor job alone. The matrix job above it says `go-version:
	// stable` on purpose -- what most people build with is what is tested --
	// so reading the first one in the file would compare the wrong thing.
	at := strings.Index(string(workflow), "\n  floor:")
	if at < 0 {
		t.Fatal("the workflow no longer has a floor job; this checks nothing")
	}
	pinned := regexp.MustCompile(`(?m)^\s*go-version: "?([^"\s]+)"?$`).
		FindSubmatch(workflow[at:])
	if pinned == nil {
		t.Fatal("the floor job names no Go version, so it runs on whatever the runner has")
	}
	if got := string(pinned[1]); got != floor {
		t.Errorf("go.mod asks for Go %s and CI's oldest-supported job pins %s, so "+
			"the floor is not the thing being tested: change go-version in "+
			".github/workflows/ci.yml, or the go line in go.mod", floor, got)
	}
}

func TestWhatCheckRunsIsWhatCIRuns(t *testing.T) {
	// `make check` prints "ok — this is what CI runs" when it finishes, which
	// is the sentence everything in this repository is committed on. Nothing
	// held it to anything: a step added to check would be run by nobody else,
	// and a step added to CI would fail on a push after a green run here.
	//
	// That sentence has TWO halves and only one was held: which make targets
	// each side runs, and that every CI step is a make target at all. Both are
	// checked here now.
	//
	// The claim is about the first job. The second builds and tests on the
	// oldest supported Go and deliberately skips gofmt and staticcheck, which
	// are about the source rather than about the toolchain.
	inRoot(t)

	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	declared := regexp.MustCompile(`(?m)^check:(.*)$`).FindSubmatch(makefile)
	if declared == nil {
		t.Fatal("the Makefile no longer has a check target")
	}
	wanted := map[string]bool{}
	for _, step := range strings.Fields(string(declared[1])) {
		wanted[step] = true
	}
	if len(wanted) < 3 {
		t.Fatalf("check runs %d things, which is fewer than it does; this is "+
			"checking nothing", len(wanted))
	}

	workflow, err := os.ReadFile(filepath.Join(".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Up to the second job, which is held to less on purpose.
	first := string(workflow)
	if i := strings.Index(first, "\n  floor:"); i > 0 {
		first = first[:i]
	}
	run := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*run: make (.+)$`).FindAllStringSubmatch(first, -1) {
		for _, step := range strings.Fields(m[1]) {
			run[step] = true
		}
	}

	// And EVERY step, not only the ones beginning with make. The comparison
	// below reads `run: make ...` lines, so a step running anything else is
	// invisible to it -- which is the failure this test exists to prevent,
	// arriving by the one door it was not watching. Measured: adding `run: go
	// run ./tools/bounds` to the first job survived the whole gate, and so did
	// a block scalar running two commands. The workflow's own comment claims
	// each step IS a Makefile target, and this is that half.
	steps := regexp.MustCompile(`(?m)^\s*run: (.+)$`).FindAllStringSubmatch(first, -1)
	if len(steps) < 3 {
		t.Fatalf("the first job runs %d steps, which is fewer than it does; the "+
			"pattern has stopped matching", len(steps))
	}
	for _, m := range steps {
		switch what := strings.TrimSpace(m[1]); {
		case what == "|" || what == ">":
			t.Error("a CI step runs a block of several commands, so no single " +
				"Makefile target is what it runs and `make check` cannot be the " +
				"whole of it")
		case !strings.HasPrefix(what, "make "):
			t.Errorf("CI runs %q, which is not a Makefile target, so `make check` "+
				"is no longer the whole of what CI does and a push can fail after "+
				"a green run here", what)
		}
	}

	for step := range wanted {
		if !run[step] {
			t.Errorf("check runs %q and CI does not, so a push can go green on "+
				"less than was run here", step)
		}
	}
	for step := range run {
		if !wanted[step] {
			t.Errorf("CI runs %q and check does not, so a push can fail after a "+
				"green run here", step)
		}
	}
}
