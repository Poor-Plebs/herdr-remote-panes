package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
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

// TestTheWorkIsCountedBeforeAnyOfItIsDone holds the pass that makes a long run
// legible.
//
// The count is what "12/300, about 40m left" is measured against, so it has to
// be the same set the sweep then walks -- a count that includes what the sweep
// skips reports a run that never finishes, and one that misses candidates
// reports a run that overshoots its own total.
func TestTheWorkIsCountedBeforeAnyOfItIsDone(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// Two identical lines in one file: neither is offered, because a report
	// naming one of them could not say which.
	code := write("code.go", "package p\n\nfunc F(s *S) {\n\ts.Stop()\n\ts.n = 1\n\ts.n = 1\n\treturn\n}\n")
	// A test file, which the sweep does not ask about at all.
	write("code_test.go", "package p\n\nfunc TestF(t *testing.T) {\n\ts.Stop()\n}\n")

	got, err := candidatesIn([]string{code, filepath.Join(dir, "code_test.go")})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range got {
		if c.path != code {
			t.Errorf("candidate from %s; only non-test files are swept", c.path)
		}
		lines = append(lines, strings.TrimSpace(c.lines[c.at]))
	}
	if want := []string{"s.Stop()"}; !slices.Equal(lines, want) {
		t.Errorf("offered %v, want %v", lines, want)
	}
}

// TestWhatASweepFoundReachesTheReport holds the wiring this tool exists to
// find, in this tool.
//
// Every helper in main.go has a test of its own -- what is offered, what a
// verdict is made of, that the file goes back, that both commands run in the
// swept tree -- and the loop that assembles them lived in main, which nothing
// can call. So the tally and the report were held by nothing at all: the line
// keeping a survivor's location could be deleted and the tool would print
// "17 survived" above an empty list, which is this command's own doc comment
// describing itself ("a well-tested helper nothing checks the wiring of").
func TestWhatASweepFoundReachesTheReport(t *testing.T) {
	var f found
	f.add("caught", "internal/a/a.go:1  caughtLine()")
	f.add("SURVIVED", "internal/a/a.go:2  looseLine()")
	f.add("would not build", "internal/a/a.go:3  brokenLine()")
	f.add("hung", "internal/a/a.go:4  hungLine()")
	f.add("SURVIVED", "internal/b/b.go:5  otherLooseLine()")

	var out strings.Builder
	report(&out, "./internal/a", f)
	got := out.String()

	if want := "./internal/a: 1 caught, 2 survived, 1 hung, 1 would not build"; !strings.Contains(got, want) {
		t.Errorf("the summary is not %q:\n%s", want, got)
	}
	// Both survivors, by where they are: a report naming one of them is a
	// report somebody would act on and still miss a line.
	for _, want := range []string{"looseLine()", "otherLooseLine()"} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not name the survivor %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "hung or killed: internal/a/a.go:4  hungLine()") {
		t.Errorf("the report does not say which statement hung:\n%s", got)
	}
	// The control. Without it a report that listed every statement it was
	// handed would satisfy everything above.
	for _, absent := range []string{"caughtLine()", "brokenLine()"} {
		if strings.Contains(got, absent) {
			t.Errorf("the report lists %q, which nobody has to read:\n%s", absent, got)
		}
	}
	if strings.Contains(got, "hung or killed: internal/a/a.go:2") {
		t.Errorf("a survivor is being reported as hung:\n%s", got)
	}
}

// TestACleanSweepDoesNotExplainAListItDoesNotHave holds the guard around the
// paragraph, which is the half a fixture with survivors in it cannot see.
func TestACleanSweepDoesNotExplainAListItDoesNotHave(t *testing.T) {
	var f found
	f.add("caught", "internal/a/a.go:1  caughtLine()")
	f.add("would not build", "internal/a/a.go:2  brokenLine()")

	var out strings.Builder
	report(&out, "./internal/a", f)
	got := out.String()

	if want := "./internal/a: 1 caught, 0 survived, 0 hung, 1 would not build"; !strings.Contains(got, want) {
		t.Errorf("the summary is not %q:\n%s", want, got)
	}
	if strings.Contains(got, "Read each one and decide") {
		t.Errorf("a sweep with no survivors still explains how to read them:\n%s", got)
	}
}

// probeOriginal is the fixture package's source. One statement worth deleting,
// and what the file must hold again once an interrupted run has put it back.
const probeOriginal = "package probe\n\nvar Ran int\n\nfunc Do() {\n\tRan = 1\n}\n"

// probeDeleted is that file with the one statement taken out, which is exactly
// what the sweep writes before it builds.
//
// Compared whole rather than by the line's absence: os.WriteFile truncates
// before it writes, so a read landing mid-write also fails to contain the line
// and would send the signal while there was nothing yet to put back.
const probeDeleted = "package probe\n\nvar Ran int\n\nfunc Do() {\n}\n"

// probeInTree is how the fixture's file is named in anything the command
// prints. The sweep is given "./pkg" and the command runs with the tree as its
// working directory, so what comes back is relative -- the path somebody who
// ran it can act on, rather than one only this process knows.
const probeInTree = "pkg/probe.go"

// probeTestBody is the fixture package's test: it waits, so the deletion above
// it is still on disk when the signal arrives.
//
// The wait is never actually paid. The sweep writes the file before it even
// builds, so the poll below catches the deletion there and stops the run long
// before anything sleeps; what is left sleeping is an orphan nothing here
// waits for.
const probeTestBody = `import (
	"testing"
	"time"
)

func TestDo(t *testing.T) {
	time.Sleep(20 * time.Second)
	Do()
	if Ran != 1 {
		t.Fatal("the statement this sweep removes is not there")
	}
}
`

// standInCeiling puts a systemd-run on PATH that imposes nothing and runs what
// it was handed.
//
// The command refuses to sweep at all where a memory ceiling cannot be had,
// and it exits before registering anything -- so on a machine with no systemd
// user session, which is two of this project's three CI jobs, building and
// running it would exercise that refusal and nothing else. What is held below
// is the restore, and the ceiling is machinery in the way of reaching it.
//
// It can say nothing about the bounding itself, and is not asked to:
// TestBothCommandsRunInTheTreeBeingSwept holds that by reading the command's
// arguments rather than by running it.
func standInCeiling(t *testing.T) string {
	t.Helper()

	// Its own options first -- --user, --scope, -q, and each -p with the value
	// after it -- and then whatever it was asked to run. That is "true" for
	// the probe in main and `go test` for a statement being tried.
	const script = "#!/bin/sh\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    --user|--scope|-q) shift ;;\n" +
		"    -p) shift 2 ;;\n" +
		"    *) break ;;\n" +
		"  esac\n" +
		"done\n" +
		"exec \"$@\"\n"

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "systemd-run"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// sweptRun starts the command over a fixture tree of one statement and comes
// back once the deletion is actually on disk.
//
// That moment is the whole point: signalling before it is there leaves nothing
// to put back, and a test doing so would pass against a command that never
// installed a handler at all. It answers with the running command, the path of
// the file now missing a statement, a reader for everything said so far, and a
// channel carrying how it ended. The caller sends the signal and decides what
// to make of it.
func sweptRun(t *testing.T) (*exec.Cmd, string, func() string, <-chan error) {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "deletions")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building the command: %v\n%s", err, out)
	}

	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"go.mod":            "module probe\n\ngo 1.25\n",
		"pkg/probe.go":      probeOriginal,
		"pkg/probe_test.go": "package probe\n\n" + probeTestBody,
	} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Its output goes to a file rather than a pipe, so Wait returns when the
	// command exits rather than when the orphaned test process lets go of the
	// other end.
	out := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })

	run := exec.Command(bin, "./pkg")
	run.Dir, run.Stdout, run.Stderr = work, f, f
	run.Env = append(os.Environ(),
		"PATH="+standInCeiling(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	// Waited for HERE rather than by the caller, so the poll below can tell a
	// command that is still working from one that has already stopped. The
	// ending is carried on `done`, which exactly one reader takes; whether it
	// has ended at all is `stopped`, which is CLOSED and so answers any number
	// of readers -- the poll, the caller and the cleanup all ask, and a
	// single-value channel shared between them deadlocks the second asker.
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		done <- run.Wait()
		close(stopped)
	}()
	t.Cleanup(func() {
		_ = run.Process.Kill()
		<-stopped
	})

	said := func() string {
		t.Helper()
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	// The deadline is generous on purpose: load can only make the command
	// slower to reach the deletion, and waiting longer costs nothing when it
	// gets there. What must NOT be waited out is a command that has already
	// stopped -- there is no deletion coming then, and sitting out the
	// deadline turns a fast failure into a timeout somebody has to diagnose.
	probe := filepath.Join(work, "pkg", "probe.go")
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		now, err := os.ReadFile(probe)
		if err != nil {
			t.Fatal(err)
		}
		if string(now) == probeDeleted {
			return run, probe, said, done
		}
		select {
		case <-stopped:
			t.Fatalf("the command stopped without removing anything:\n%s", said())
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no statement was ever removed, so there was never anything to put "+
		"back:\n%s", said())
	return nil, "", nil, nil
}

// TestAnInterruptedSweepPutsTheStatementBack holds the one restore that a
// deferred function cannot do.
//
// restoreOnSignal's own comment is the specification: a signal ends the process
// where it stands, defers and all, so a run given up on halfway "leaves a
// statement missing from the tree, which builds, and which the next commit
// carries". Nothing named that function in any test -- neutralising main's one
// call to it left the whole tree green -- while putBack under it is held both
// ways by TestTheFileGoesBackWhateverHappened, the restore and the letting go
// of what was restored. The helper was held; the handler, its call site and
// every signal it registers were held by nothing.
//
// That test calls putBack directly, which is why it could not see any of this:
// it says the file goes back when something asks, and nothing here asked. This
// is the first test in the package to run the command at all.
//
// ALL THREE SIGNALS, because one statement registering several is a mutation
// no deletion sweep proposes: it offers the whole line, so the line reads as
// settled while any one argument of it can be dropped unnoticed. They are
// answered by a single path, so what the extra rows buy is the STATUS -- a run
// stopped by a signal exits 128 plus that signal, and the right number for
// each says the handler chose it rather than the runtime taking the default
// disposition.
func TestAnInterruptedSweepPutsTheStatementBack(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			run, probe, said, done := sweptRun(t)

			if err := run.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Error("a command stopped by a signal exited nought")
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("waiting for the command: %v", err)
				}
				if want := 128 + int(sig); exit.ExitCode() != want {
					t.Errorf("the interrupted sweep exited %d, want %d", exit.ExitCode(), want)
				}
			}

			back, err := os.ReadFile(probe)
			if err != nil {
				t.Fatal(err)
			}
			if string(back) != probeOriginal {
				t.Errorf("the interrupted sweep left a statement out of the tree:\n%s", back)
			}

			// And it SAYS which file it put back. Somebody who has just
			// interrupted a sweep over their own tree has to know whether
			// anything was left missing from it, and this line is the only
			// place that is answered.
			out := said()
			if want := "put " + probeInTree + " back before stopping"; !strings.Contains(out, want) {
				t.Errorf("the interrupted sweep does not say %q:\n%s", want, out)
			}
			if strings.Contains(out, "could not put") {
				t.Errorf("a restore that worked reported a failure:\n%s", out)
			}
		})
	}
}

// TestASweepThatCannotRestoreSaysWhichFileIsStillShort holds the other arm of
// the same decision, and it is the arm that matters.
//
// putBack's own comment says a file that could not be written is not
// forgotten. When the write fails, that sentence is ALL that is left: the tree
// keeps a statement that was removed, it still builds, and nothing else
// anywhere names which file it is in. The test above drives only the arm where
// the write works, and passes whether either message is printed or not --
// which is exactly how the same pair came to still be survivors one commit
// after tools/bounds held its own restore.
func TestASweepThatCannotRestoreSaysWhichFileIsStillShort(t *testing.T) {
	// Self-check the fixture before building on it: a user who can write a
	// read-only file cannot stage a failing restore at all, and the whole test
	// would then be about nothing.
	guard := filepath.Join(t.TempDir(), "readonly")
	if err := os.WriteFile(guard, []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, []byte("y"), 0o400); err == nil {
		t.Skip("this user can write a read-only file, so a restore cannot be made to fail")
	}

	run, probe, said, done := sweptRun(t)

	// Read-only, so putBack's write fails. The mode goes back afterwards so
	// what was left behind can be read like any other file.
	if err := os.Chmod(probe, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(probe, 0o600) })

	if err := run.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	<-done

	out := said()
	if want := "could not put " + probeInTree + " back"; !strings.Contains(out, want) {
		t.Errorf("a failed restore does not say %q:\n%s", want, out)
	}
	if strings.Contains(out, "back before stopping") {
		t.Errorf("a restore that failed reported success:\n%s", out)
	}

	// The control, and it is about the fixture rather than the code: the write
	// really did fail, so the tree really is still short a statement. Without
	// it this passes against a run that put the file back and printed the
	// failure anyway.
	still, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	if string(still) != probeDeleted {
		t.Errorf("the file was put back after all, so nothing failed:\n%s", still)
	}
}
