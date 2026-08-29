package config

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestASettingsFileThatIsNotOneStopsNothing holds the daemon's ability to run.
//
// Reading a named pipe waits for somebody to write to it, and this file is
// read on a timer: the daemon rereads it every pass so an edit takes effect
// without a restart. So a settings file that is not a file is not a slow menu,
// it is the whole daemon stopped -- no menu, no reconcile, and nothing
// anywhere saying why.
//
// The same hazard as the SSH config, found by asking where else a path is read
// that this plugin did not choose the contents of.
func TestASettingsFileThatIsNotOneStopsNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	if err := syscall.Mkfifo(filepath.Join(dir, "config.json"), 0o600); err != nil {
		t.Skipf("cannot make a named pipe here: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := Load()
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a settings file that cannot be read was accepted")
		}
		// Said in terms of the file, because "unexpected end of JSON" about a
		// pipe sends somebody looking for a syntax error they cannot see.
		if !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("the error is %q, which does not say what is wrong with it", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reading the settings did not come back, so the daemon would " +
			"stop on its next pass")
	}
}
