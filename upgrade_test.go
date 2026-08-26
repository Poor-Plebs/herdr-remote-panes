package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// Opt-in: it builds the binary and waits on real processes.
//
//	HRP_UPGRADE=1 go test -run AnUpgradeHandsTheSocketOver .
func TestAnUpgradeHandsTheSocketOver(t *testing.T) {
	if os.Getenv("HRP_UPGRADE") == "" {
		t.Skip("set HRP_UPGRADE=1 to run; it builds the binary and runs two daemons")
	}

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

	daemon := func() (*exec.Cmd, *strings.Builder) {
		said := &strings.Builder{}
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
