package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
)

// TestADaemonAskedToStopTakesItsSocketWithIt holds both signals the daemon
// registers, because only one of them was held by anything.
//
// Run's own comment says what the registration is for: "Herdr stops a plugin's
// startup process with a signal, and the default action is to die on the spot:
// the deferred cleanup above never ran, so the control socket was left behind
// on every shutdown." That cleanup is `defer listener.Close()`, and closing a
// Unix listener unlinks the path it bound.
//
// MEASURED at 52e6db2, dropping one signal at a time from
// `signal.Notify(stopping, syscall.SIGINT, syscall.SIGTERM)`: SIGTERM is
// caught, by TestAnUpgradeFromTheLastReleaseHandsTheSocketOver in this package
// rather than by anything in internal/syncd -- and SIGINT SURVIVES the whole
// gate. A statement-deletion sweep never proposes dropping one ARGUMENT, so
// the tool called the line settled while half of it was held by nothing.
//
// Ctrl-c is the half nobody held and the one somebody actually types: running
// `herdr-remote-panes daemon` in a terminal to see what it says is what both
// pages suggest when a daemon will not come up. Without the registration that
// takes the default disposition, so the process dies where it stands and the
// socket file stays. The next daemon recovers -- listenControl dials, finds
// nothing answering, and clears it -- but where the state directory is long
// enough that the socket lives in the temp directory instead, nothing tidies
// it ever, which is the leak this package already scans for.
func TestADaemonAskedToStopTakesItsSocketWithIt(t *testing.T) {
	inRoot(t)

	binary := filepath.Join(t.TempDir(), "herdr-remote-panes")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}

	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
			t.Setenv("HERDR_SESSION", "stopping")

			// Where it will bind, ASKED of the code rather than worked out
			// here: socketPathFor has two branches, and which one this state
			// directory takes is not something to restate.
			socket, err := syncd.ControlSocket()
			if err != nil {
				t.Fatal(err)
			}

			said := &saidWhat{}
			daemon := exec.Command(binary, "daemon")
			daemon.Env = os.Environ()
			daemon.Stderr = said
			if err := daemon.Start(); err != nil {
				t.Fatal(err)
			}
			done := exits(daemon)
			defer func() { _ = daemon.Process.Kill() }()

			// The control, and the synchronisation in one: the socket has to
			// be TAKEN before its absence afterwards means anything. A build
			// that never bound would otherwise pass the check below by having
			// nothing to leave behind.
			taken := false
			for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
				if _, err := os.Stat(socket); err == nil {
					taken = true
					break
				}
				if gone(done) {
					t.Fatalf("the daemon stopped before it bound anything:\n%s", said)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !taken {
				t.Fatalf("the daemon never bound %s:\n%s", socket, said)
			}

			if err := daemon.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatalf("the daemon did not stop on %s:\n%s", sig, said)
			}

			if _, err := os.Stat(socket); !os.IsNotExist(err) {
				t.Errorf("%s is still there after the daemon stopped on %s, so the "+
					"next one starts against a stale socket and a temp-directory "+
					"one is never tidied at all: %v", socket, sig, err)
			}
		})
	}
}
