package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestALongLineDoesNotSwallowTheMachinesAfterIt holds the rest of the config.
//
// bufio.Scanner stops at 64KB by default and reports it only through Err,
// which nothing was asking. A long comment, a long ProxyCommand, or an Include
// that matched something without newlines ended the scan there -- and every
// machine below that line was missing from the menu, which looks exactly like
// somebody having deleted them.
func TestALongLineDoesNotSwallowTheMachinesAfterIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "config")

	// Past the default bound and well inside the one set here.
	body := "Host before\n# " + strings.Repeat("x", 70_000) + "\nHost after\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	hosts := hostsFrom(path, 0)
	if len(hosts) != 2 || hosts[0] != "before" || hosts[1] != "after" {
		t.Errorf("got %v, want both machines: the one after a long line was "+
			"dropped, and nothing anywhere says so", hosts)
	}
}

// TestAConfigThatCannotBeReadThroughSaysSo holds the reporting for the case
// that is still too long: a line past even the raised bound.
//
// It matters because the failure is machines missing rather than an error.
// Somebody looking at a shorter menu than yesterday's needs to be told the
// file could not be read, not left to work it out.
func TestAConfigThatCannotBeReadThroughSaysSo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "Host before\n# " + strings.Repeat("x", maxConfigLine+1024) + "\nHost after\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if why := Unreadable(); why == "" {
		t.Error("a config that cannot be read through was reported as fine, so " +
			"the machines below the line it stopped at are missing silently")
	}
}
