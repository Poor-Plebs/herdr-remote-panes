package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/version"
)

// TestARunningDaemonReportsTheBuildItIsRunning holds the half of the
// stale-daemon warning that lives in the daemon.
//
// Reply.Revision's own comment is the specification: "Revision is the build the
// daemon itself is running. Installing an update leaves the running daemon
// alone, so this is how a stale one is spotted." The COMPARING half was fixed
// twice (1593be8, then 8acb3fc for the menu) and is held in internal/cli and
// internal/picker. The half that fills the field was held by nothing:
// neutralising `Revision: version.Short()` in daemon.go's status arm leaves the
// WHOLE GATE green, because every test that exercises the warning reaches a
// daemon through answerWith -- a stand-in that fills the field in by hand, and
// so cannot show whether the real one sets it.
//
// What that costs is not silence, which is what made it worth holding.
// StaleMessageFor answers an empty revision with "the running daemon does not
// report which build it is ... restart Herdr to be sure that is the one
// running", so every user of a perfectly current daemon would be told to
// restart it, every time, and restarting would not stop it.
//
// Held HERE rather than beside the line because inside a test binary
// version.Short() is "unknown" whatever the code does -- a test binary is not
// built from a checkout and carries no vcs.* settings -- so `Revision:
// "unknown"` would satisfy anything a test in internal/syncd could assert. A
// BUILT plugin carries a real revision, and that is what makes the answer below
// evidence rather than a tautology.
func TestARunningDaemonReportsTheBuildItIsRunning(t *testing.T) {
	inRoot(t)

	binary := filepath.Join(t.TempDir(), "herdr-remote-panes")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}

	// What this build says it is, ASKED of the binary rather than worked out
	// from git here: the daemon below is this same binary, so whatever it
	// answers is the build it runs, by construction.
	out, err := exec.Command(binary, "version").Output()
	if err != nil {
		t.Fatalf("asking the build what it is: %v", err)
	}
	installed := ""
	if fields := strings.Fields(strings.SplitN(string(out), "\n", 2)[0]); len(fields) == 2 {
		installed = fields[1]
	}
	// The fixture's own check, and what it is worth exactly: with no revision
	// to compare against, the first assertion below still fails but blames the
	// daemon, and the second passes vacuously, since StaleMessageFor goes
	// quiet whenever the installed build is empty or unknown. This names the
	// fixture as the thing that broke instead.
	if installed == "" || installed == "unknown" {
		t.Fatalf("a plugin built from this checkout reports no revision of its own, so "+
			"nothing below can tell a daemon that answers from one that does not:\n%s", out)
	}

	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "reporting")

	said := &saidWhat{}
	daemon := exec.Command(binary, "daemon")
	daemon.Env = os.Environ()
	daemon.Stderr = said
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	done := exits(daemon)
	// Asked to stop rather than killed, and waited for. A daemon that dies
	// where it stands leaves its control socket behind, and where the state
	// directory is long enough for the hashed fallback that socket lands in
	// the system temp directory -- which upgrade_test.go scans, so a kill here
	// would fail a sibling test rather than this one.
	defer func() {
		_ = daemon.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = daemon.Process.Kill()
		}
	}()

	// The line the daemon writes once it is listening, and not the socket
	// appearing: stop_test.go says why, and talking to it needs the same
	// moment that signalling it does.
	up := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if strings.Contains(said.String(), "listening on") {
			up = true
			break
		}
		if gone(done) {
			t.Fatalf("the daemon stopped before it was listening:\n%s", said)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !up {
		t.Fatalf("the daemon never said it was listening:\n%s", said)
	}

	reply, err := syncd.Ask(syncd.Command{Cmd: "status"})
	if err != nil {
		t.Fatalf("asking the daemon for status: %v\n%s", err, said)
	}
	if !reply.OK {
		t.Fatalf("status: %s", reply.Message)
	}

	if reply.Revision != installed {
		t.Errorf("the daemon is running %s and reports its build as %q", installed, reply.Revision)
	}
	// The consequence, which is what the field is for: somebody holding this
	// build, talking to a daemon running it, is told nothing.
	if stale := version.StaleMessageFor(reply.Revision, installed); stale != "" {
		t.Errorf("a daemon running the installed build is called stale: %q", stale)
	}
	// The control. Without it the line above passes for a comparison that can
	// never speak at all -- StaleMessageFor goes quiet on several inputs, and
	// one of them is an installed build it does not recognise. A DIFFERENT
	// build must produce a sentence, and it must name what the daemon said.
	stale := version.StaleMessageFor(reply.Revision, "0000000")
	if stale == "" || !strings.Contains(stale, reply.Revision) {
		t.Errorf("a daemon on a build other than the one installed is described as %q", stale)
	}
}
