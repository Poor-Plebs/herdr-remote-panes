package sshconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNoSSHConfigIsNotAProblem(t *testing.T) {
	// Most people running this have no SSH config, and telling them so on
	// every menu would be noise about something that is not wrong.
	t.Setenv("HOME", t.TempDir())
	if why := Unreadable(); why != "" {
		t.Errorf("a machine with no SSH config complained: %q", why)
	}
	if got := Hosts(); len(got) != 0 {
		t.Errorf("found %v in a home with no SSH config", got)
	}
}

func TestAnIncludeNamingNoFileDoesNotComplainAboutTheDirectory(t *testing.T) {
	// `Include ""` is a line naming no file -- the same shape as the `Host ""`
	// the Host branch calls ordinary, and it arrives the same way, through the
	// rule that lets a name hold a space. An empty pattern is not absolute, so
	// it was joined onto the directory the config sits in, matched ~/.ssh
	// itself, and a directory is not a regular file. The menu has one line for
	// saying something is wrong and it read "~/.ssh: not a regular file, so it
	// is not read", about a path nobody had written, with every machine in the
	// config present and correct underneath it.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "Include \"\"\nHost bot\n"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if why := Unreadable(); why != "" {
		t.Errorf("an include naming no file complained: %q", why)
	}
	// And the line is skipped rather than the file abandoned at it.
	if got := Hosts(); len(got) != 1 || got[0] != "bot" {
		t.Errorf("aliases = %v, want [bot]", got)
	}
}

func TestAnSSHConfigThatCannotBeReadIsSaidOutLoud(t *testing.T) {
	// Hosts returns nothing when the file cannot be read, which is also what it
	// returns when there is none -- so a file that is there and unreadable
	// emptied the menu of every machine it knows about and said nothing. That
	// looks exactly like somebody having deleted them.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a file the running user cannot read")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".ssh", "config")
	if err := os.WriteFile(path, []byte("Host bot\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	// It is there, and it has a machine in it.
	if got := Hosts(); len(got) != 0 {
		t.Fatalf("the file was readable after all: %v — this proves nothing", got)
	}
	why := Unreadable()
	if why == "" {
		t.Fatal("a config that could not be read said nothing, so the machines in " +
			"it vanish from the menu with no explanation")
	}
	if !strings.Contains(why, "permission denied") {
		t.Errorf("the reason does not say what stopped it: %q", why)
	}
}
