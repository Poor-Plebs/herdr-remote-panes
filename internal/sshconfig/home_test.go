package sshconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWithNoHomeNothingIsReadFromTheWorkingDirectory holds the two lines that
// give up when there is no home directory to look in.
//
// os.UserHomeDir fails when HOME is unset, and Path() answers with nothing
// rather than with what filepath.Join makes of an empty home -- which is
// ".ssh/config", a RELATIVE path. The daemon is started by Herdr from a
// directory this does not choose and cannot know. So without those two lines, a
// session with no HOME reads whatever .ssh/config happens to sit in that
// directory and offers the machines in it under the same names as the real
// ones: a repository with a checked-in .ssh/config would put its hosts in
// somebody's menu.
//
// HOME is unset in more places than it looks: a systemd unit without it, a
// container, a cron job, an su without a login shell.
func TestWithNoHomeNothingIsReadFromTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(dir, ".ssh", "config")
	if err := os.WriteFile(planted, []byte("Host notyours\n  HostName 10.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("HOME", "")

	// The fixture says it is readable rather than being taken on trust: if this
	// config could not be read at all, finding nothing below would prove
	// nothing about the guard.
	if hosts := hostsFrom(planted, 0); len(hosts) == 0 {
		t.Fatal("the planted config was not readable, so reading nothing below says nothing")
	}

	if got := Path(); got != "" {
		t.Errorf("with no home the config path is %q, want nothing at all", got)
	}
	if hosts := Hosts(); len(hosts) > 0 {
		t.Errorf("with no home the menu offers %v, read out of the directory this happens "+
			"to be running in", hosts)
	}
	// And nothing is reported as unreadable: there is no config here that
	// somebody could go and fix, so a warning would send them looking for a
	// file that is not the one they have.
	if why := Unreadable(); why != "" {
		t.Errorf("with no home there is no config to call unreadable, and it says %q", why)
	}
}

// TestAConfigThatCannotEvenBeLookedAtSaysSo holds the note for a stat that
// fails with something other than "not there".
//
// A config that is absent is the ordinary case and stays quiet, so the branch
// that speaks is the one where the file is there in some sense and cannot be
// reached. The plainest way to be in that state is for ~/.ssh to be a file
// rather than a directory -- a broken restore, or a hand-made one-line
// "config" written to the wrong path -- and then looking at ~/.ssh/config
// fails with "not a directory" rather than "not there".
//
// Without the note the menu is simply short: every machine in the SSH config
// is missing and nothing says why, which is the failure this whole file is
// arranged around.
func TestAConfigThatCannotEvenBeLookedAtSaysSo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// ~/.ssh is a file, so ~/.ssh/config is a path through it.
	if err := os.WriteFile(filepath.Join(home, ".ssh"), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The fixture is in the state this is about: there, and not statable.
	if _, err := os.Stat(Path()); err == nil {
		t.Fatal("the fixture path can be looked at, so there is nothing here to report")
	} else if os.IsNotExist(err) {
		t.Fatalf("the fixture path reads as simply absent (%v), which is the quiet case", err)
	}

	why := Unreadable()
	if why == "" {
		t.Fatal("a config that cannot be looked at was reported as fine, so every machine " +
			"in it is missing from the menu with nothing said about it")
	}
	if hosts := Hosts(); len(hosts) > 0 {
		t.Errorf("machines were read out of a path that cannot be looked at: %v", hosts)
	}
}
