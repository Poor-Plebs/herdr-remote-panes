package sshconfig

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestReportingAnUnreadableConfigDoesNotWaitEither is the same hazard in the
// function that exists to explain it.
//
// Unreadable opens the config to find out whether it can be read, and opening
// a pipe waits for a writer. So a config that is a pipe hung the menu inside
// the check meant to say why the menu is empty -- which is the worst place for
// it, and was left behind when the reading path was fixed.
func TestReportingAnUnreadableConfigDoesNotWaitEither(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(home, ".ssh", "config"), 0o600); err != nil {
		t.Skipf("cannot make a named pipe here: %v", err)
	}

	done := make(chan string, 1)
	go func() { done <- Unreadable() }()

	select {
	case why := <-done:
		if why == "" {
			t.Error("a config that cannot be read was reported as fine, so the " +
				"menu is empty with nothing said about it")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the check that explains an empty menu did not come back")
	}
}

// TestAnIncludeThatMatchesSomethingUnreadableDoesNotWait holds the menu open.
//
// An Include is a glob, and a glob matches whatever is there. Reading a pipe
// with nobody writing to it, or a terminal with nobody typing, does not return
// -- and this file is read to draw the menu, so that is a keypress that never
// comes back with nothing on screen to say why.
//
// Found by fuzzing, from "Include /*/*/0", which matches /dev/pts/0 on an
// ordinary Linux machine. A named pipe here instead, so the test does not
// depend on what a machine happens to have under /dev.
func TestAnIncludeThatMatchesSomethingUnreadableDoesNotWait(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	pipe := filepath.Join(home, "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("cannot make a named pipe here: %v", err)
	}
	// Nothing ever writes to it, so a read waits for as long as it is allowed.
	path := filepath.Join(home, "config")
	if err := os.WriteFile(path, []byte("Host before\nInclude "+pipe+"\nHost after\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan []string, 1)
	go func() { done <- hostsFrom(path, 0) }()

	select {
	case hosts := <-done:
		// The machines around the include are still offered: one unreadable
		// include is not a reason to lose the rest of somebody's config.
		if len(hosts) != 2 || hosts[0] != "before" || hosts[1] != "after" {
			t.Errorf("got %v, want both machines named either side of the include", hosts)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reading a config with a pipe in it did not come back, so the " +
			"menu would not open")
	}
}
