package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestAnUpgradeHandsTheSocketOver runs two real daemons, the way an upgrade
// does.
//
// Every release-day failure this plugin has had came from the same gap: the
// check before a release builds from a clean checkout and starts a daemon,
// which passes every time because a clean checkout has no daemon already
// running in it. An upgrade is not an install. What is different about it is
// only visible with two of them at once:
//
//   - the replacing daemon must not exit because the socket is taken, because
//     Herdr does not retry a startup command that exited, and once the old one
//     stops there is nothing serving at all;
//   - the daemon that goes must not take the replacement's socket with it.
//
// It builds the binary and waits on real processes, which is slower than the
// rest of the suite and still under two seconds. It was opt-in at first, and
// that was the wrong side of the trade: the release steps are the only place
// that would have run it, and forgetting the release steps is how the thing it
// guards got out three times.
func TestAnUpgradeHandsTheSocketOver(t *testing.T) {
	inRoot(t)

	dir := t.TempDir()
	binary := filepath.Join(dir, "herdr-remote-panes")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}

	state := filepath.Join(dir, "state")
	config := filepath.Join(dir, "config")
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"HERDR_PLUGIN_STATE_DIR="+state,
		"HERDR_PLUGIN_CONFIG_DIR="+config,
		"HERDR_SESSION=upgrade")

	daemon := func() (*exec.Cmd, *saidWhat) {
		said := &saidWhat{}
		cmd := exec.Command(binary, "daemon")
		cmd.Env = env
		cmd.Stderr = said
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd, said
	}

	// Waits for something to be answering, which is the only thing an action
	// cares about.
	answering := func(within time.Duration) bool {
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			ask := exec.Command(binary, "status")
			ask.Env = env
			if err := ask.Run(); err == nil {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}

	old, oldSaid := daemon()
	defer func() { _ = old.Process.Kill() }()
	if !answering(10 * time.Second) {
		t.Fatalf("the first daemon never answered:\n%s", oldSaid)
	}
	// A daemon that is well says nothing for the rest of its life, so a log
	// with "starting" and nothing after it reads the same whether it came up
	// or the coming up is what failed. This is the line that tells them apart,
	// and it is the first thing to look for in a report of no daemon.
	if !strings.Contains(oldSaid.String(), "listening on") {
		t.Errorf("the daemon never said where it was listening:\n%s", oldSaid)
	}

	// The upgrade: a second one starts while the first is still serving.
	replacing, replacingSaid := daemon()
	defer func() { _ = replacing.Process.Kill() }()
	time.Sleep(time.Second)

	if replacing.ProcessState != nil && replacing.ProcessState.Exited() {
		t.Fatalf("the replacing daemon exited instead of waiting; Herdr does not "+
			"start it again, so nothing would be left serving:\n%s", replacingSaid)
	}

	// The old one goes, as Herdr's restart stops it.
	if err := old.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = old.Wait()

	// And from then on, something answers. This is the whole of what broke:
	// the old daemon stopped, and every action said there was no daemon.
	if !answering(15 * time.Second) {
		t.Errorf("nothing answers after the handover.\nreplacing daemon said:\n%s\nold daemon said:\n%s",
			replacingSaid, oldSaid)
	}
}

// saidWhat collects what a process wrote while it is still writing.
//
// A strings.Builder handed to cmd.Stderr is written by the goroutine copying
// the pipe, and reading it from the test while the process is alive is a race
// -- one the race detector never saw while this test was opt-in, because
// opt-in tests are not the ones CI runs with it.
type saidWhat struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *saidWhat) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *saidWhat) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// previousRelease is the newest release tag that is not the commit under test.
//
// A shallow clone has no tags, and a test that quietly skips when it cannot
// find one is a test that never runs anywhere it matters. So this fails, and
// says what the clone needs.
func previousRelease(t *testing.T) string {
	t.Helper()

	// No repository at all is not the same as a repository without tags, and
	// only the second is a problem. The mutation sweep copies the tree to a
	// temporary directory to mutate it, and copies no .git with it -- so
	// failing on that would mean this package could never be swept, which is
	// a real cost for a check that CI runs on every push regardless.
	//
	// A shallow clone still fails below: it has a .git and no tags, which is
	// a clone that cannot answer the question rather than a tree that was
	// never asked to.
	if err := exec.Command("git", "rev-parse", "--git-dir").Run(); err != nil {
		if os.Getenv("CI") != "" {
			// The claim that CI runs this has to be enforceable, or it is the
			// same silent skip in a better disguise: nothing in a test run
			// without -v distinguishes a check that passed from one that was
			// not run. A checkout there without a repository is the checkout
			// being wrong, not a tree with nothing to answer.
			t.Fatal("no git repository in CI, so the upgrade check did not run; " +
				"actions/checkout needs fetch-depth: 0")
		}
		t.Skip("not a git repository, so there is no release to upgrade from " +
			"(this is the mutation sweep's copy of the tree; CI fails instead)")
	}
	// A particular jump can be asked for, which is how somebody several
	// versions behind can be told whether their upgrade works rather than
	// guessed at.
	if from := os.Getenv("HRP_UPGRADE_FROM"); from != "" {
		return from
	}
	out, err := exec.Command("git", "tag", "--sort=-v:refname").Output()
	if err != nil {
		t.Fatalf("could not list tags: %v", err)
	}
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range strings.Fields(string(out)) {
		at, err := exec.Command("git", "rev-list", "-n1", tag).Output()
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(at)) != strings.TrimSpace(string(head)) {
			return tag
		}
	}
	t.Fatal("no release tag to upgrade from; this needs a clone with tags " +
		"(actions/checkout wants fetch-depth: 0)")
	return ""
}

func TestAnUpgradeFromTheLastReleaseHandsTheSocketOver(t *testing.T) {
	// The test above starts one build twice, which is a daemon replacing
	// itself. A real upgrade is one version replacing another, and the half
	// that has to cope is the new one: it meets a socket held by a daemon
	// built before any of the handover code existed, and that daemon's idea of
	// tidying up on the way out is whatever it was at the time.
	//
	// Three releases went out broken on exactly this difference, and none of
	// them would have been caught by starting the same binary twice.
	inRoot(t)

	// The daemon under test is built from the tree by a subprocess, so nothing
	// here records that the result depends on it: breaking the socket handover
	// on purpose and running this by hand reported "ok (cached)".
	cacheDependsOnTheTree(t, ".", goSource)
	previous := previousRelease(t)

	dir := t.TempDir()
	build := func(from, name string) string {
		t.Helper()
		binary := filepath.Join(dir, name)
		src := "."
		if from != "" {
			src = filepath.Join(dir, "src-"+name)
			if err := os.MkdirAll(src, 0o755); err != nil {
				t.Fatal(err)
			}
			// Checked before it is unpacked, because the failure otherwise
			// lands on tar: git archive writes nothing and exits, tar reads
			// the empty pipe and says "this does not look like a tar
			// archive", and the one thing that went wrong -- a version that
			// is not a tag here -- appears nowhere. HRP_UPGRADE_FROM is set
			// by hand, which is where the typo comes from.
			if out, err := exec.Command("git", "rev-parse", "--verify", from+"^{commit}").CombinedOutput(); err != nil {
				t.Fatalf("%s is not a commit in this repository, so there is "+
					"nothing to upgrade from: %v\n%s", from, err, out)
			}
			archive := exec.Command("git", "archive", from)
			untar := exec.Command("tar", "-x", "-C", src)
			pipe, err := archive.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			untar.Stdin = pipe
			if err := archive.Start(); err != nil {
				t.Fatal(err)
			}
			if out, err := untar.CombinedOutput(); err != nil {
				t.Fatalf("unpacking %s: %v\n%s", from, err, out)
			}
			if err := archive.Wait(); err != nil {
				t.Fatalf("archiving %s: %v", from, err)
			}
		}
		cmd := exec.Command("go", "build", "-o", binary, ".")
		cmd.Dir = src
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", name, err, out)
		}
		return binary
	}

	older := build(previous, "older")
	current := build("", "current")

	state := filepath.Join(dir, "state")
	config := filepath.Join(dir, "config")
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"HERDR_PLUGIN_STATE_DIR="+state,
		"HERDR_PLUGIN_CONFIG_DIR="+config,
		"HERDR_SESSION=upgrade-across")

	start := func(binary string) (*exec.Cmd, *saidWhat) {
		said := &saidWhat{}
		cmd := exec.Command(binary, "daemon")
		cmd.Env = env
		cmd.Stderr = said
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd, said
	}
	answering := func(with string, within time.Duration) bool {
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			ask := exec.Command(with, "status")
			ask.Env = env
			if err := ask.Run(); err == nil {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}

	old, oldSaid := start(older)
	defer func() { _ = old.Process.Kill() }()
	if !answering(older, 15*time.Second) {
		t.Fatalf("the %s daemon never answered:\n%s", previous, oldSaid)
	}

	replacing, replacingSaid := start(current)
	defer func() { _ = replacing.Process.Kill() }()
	time.Sleep(time.Second)
	if replacing.ProcessState != nil && replacing.ProcessState.Exited() {
		t.Fatalf("the new daemon exited rather than waiting for the %s one to go; "+
			"Herdr does not start it again, so nothing would be left serving:\n%s",
			previous, replacingSaid)
	}

	if err := old.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = old.Wait()

	if !answering(current, 20*time.Second) {
		t.Errorf("nothing answers after upgrading from %s.\nnew daemon said:\n%s\n%s daemon said:\n%s",
			previous, replacingSaid, previous, oldSaid)
	}
}

func TestEveryVersionTheDocsNameIsOneThatExists(t *testing.T) {
	// The docs say when things changed -- "it did not until v0.4.0", "requires
	// Herdr 0.8.0+" -- and a release named there is a thing somebody can go and
	// install, or look for in the list of releases to see what else came with
	// it. One that was never cut sends them looking for nothing.
	//
	// This is not hypothetical. The README credited the config reread to
	// v0.3.2, which does not exist and never did; the change shipped in v0.4.0.
	// Nothing minded, because a version in prose is a string.
	inRoot(t)

	// The tags come from git, a subprocess, so nothing ties this result to the
	// documents it is about.
	cacheDependsOnTheTree(t, ".", func(name string) bool {
		return strings.HasSuffix(name, ".md")
	})
	if err := exec.Command("git", "rev-parse", "--git-dir").Run(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("no git repository in CI, so no tag list; actions/checkout needs fetch-depth: 0")
		}
		t.Skip("not a git repository, so there is no list of releases to check against")
	}
	out, err := exec.Command("git", "tag").Output()
	if err != nil {
		t.Fatalf("listing tags: %v", err)
	}
	released := map[string]bool{}
	for _, tag := range strings.Fields(string(out)) {
		released[tag] = true
	}
	if len(released) < 5 {
		t.Fatalf("found %d releases, which is fewer than there are; this is checking nothing",
			len(released))
	}

	// The one in the manifest is allowed whether or not it is tagged: at the
	// moment a release is prepared the version is written down and the tag
	// does not exist yet, which is the same reason the version test holds the
	// README and the manifest to each other rather than to the tag.
	manifest, err := os.ReadFile("herdr-plugin.toml")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(?m)^version = "([^"]+)"`).FindSubmatch(manifest); m != nil {
		released["v"+string(m[1])] = true
	}

	pages, err := DocPages()
	if err != nil {
		t.Fatal(err)
	}
	// Herdr's versions are written bare and are not this project's to have
	// tagged, so only the v-prefixed ones are ours to answer for.
	named := regexp.MustCompile(`\bv[0-9]+\.[0-9]+\.[0-9]+\b`)
	checked := 0
	for _, page := range pages {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, found := range named.FindAllString(string(raw), -1) {
			checked++
			if !released[found] {
				t.Errorf("%s names %s, which was never released",
					filepath.Base(page), found)
			}
		}
	}
	if checked < 3 {
		t.Fatalf("found %d versions named in the docs, which is fewer than there "+
			"are; this is checking nothing", checked)
	}
}
