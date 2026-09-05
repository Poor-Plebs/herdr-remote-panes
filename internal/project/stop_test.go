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

	for _, tt := range []struct {
		what      string
		sig       syscall.Signal
		stillBusy bool // a machine to reach, and an ssh that takes its time
	}{
		{"interrupt", syscall.SIGINT, false},
		{"terminated", syscall.SIGTERM, false},
		{"interrupt while still starting", syscall.SIGINT, true},
	} {
		t.Run(tt.what, func(t *testing.T) {
			sig := tt.sig
			config := t.TempDir()
			t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", config)
			t.Setenv("HERDR_SESSION", "stopping")

			// The third row keeps the daemon inside the window the ordering
			// is about. restoreConnections reaches every configured machine
			// before the loop starts, so one machine and an ssh that sleeps
			// holds it there -- and the margin runs the safe way round, since
			// load keeps it there longer rather than less long.
			if tt.stillBusy {
				if err := os.WriteFile(filepath.Join(config, "config.json"),
					[]byte(`{"hosts":[{"target":"slowbox"}]}`), 0o600); err != nil {
					t.Fatal(err)
				}
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "ssh"),
					[]byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			}

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

			// Wait for the line the daemon writes once it is up, NOT for the
			// socket to appear. Run installs the signal handler between
			// binding and saying this, so "listening on" is the first moment
			// a signal is certain to be handled rather than fatal; the socket
			// exists a moment earlier, and signalling then raced the
			// registration -- which is how this test failed CI on the slowest
			// runner while passing everywhere else.
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
			// The control: the socket has to be TAKEN before its absence
			// afterwards means anything, or a build that never bound passes
			// the check below by having nothing to leave behind.
			if _, err := os.Stat(socket); err != nil {
				t.Fatalf("the daemon is listening and %s is not there: %v", socket, err)
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
