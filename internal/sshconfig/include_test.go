package sshconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestAFileTooLargeToBeAConfigIsNotRead bounds what is read, after bounding
// how many and how long they take to find.
//
// A config fragment is kilobytes of host blocks somebody typed. What lies past
// a megabyte is not configuration: "Include /l*/d*/*" matches a hundred and
// eighty library files, and reading those a line at a time is sixteen seconds
// while somebody waits for a menu. It is now a fifth of a second.
//
// The size comes from the stat that already asks whether it is a regular file,
// so knowing costs nothing.
func TestAFileTooLargeToBeAConfigIsNotRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "conf.d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// A machine named at the top, then padding past the bound. Reading any of
	// it at all would offer that machine.
	big := append([]byte("Host buried\n"), make([]byte, maxConfigBytes+1)...)
	if err := os.WriteFile(filepath.Join(dir, "big"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	// And an ordinary fragment beside it, which must still be read.
	if err := os.WriteFile(filepath.Join(dir, "small"), []byte("Host kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(home, "config")
	if err := os.WriteFile(path, []byte("Include "+dir+"/*\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Exactly the bound is read, because the name says maximum.
	exact := append([]byte("Host exact\n"), make([]byte, maxConfigBytes-len("Host exact\n"))...)
	if int64(len(exact)) != int64(maxConfigBytes) {
		t.Fatalf("the file for the boundary is %d bytes, not %d", len(exact), maxConfigBytes)
	}
	if err := os.WriteFile(filepath.Join(dir, "atbound"), exact, 0o600); err != nil {
		t.Fatal(err)
	}

	hosts := hostsFrom(path, 0)
	for _, host := range hosts {
		if host == "buried" {
			t.Error("a file past the bound was read anyway")
		}
	}
	found := false
	for _, host := range hosts {
		if host == "exact" {
			found = true
		}
	}
	if !found {
		t.Errorf("a file of exactly %d bytes was not read; the limit is a "+
			"maximum, so that size is allowed: got %v", maxConfigBytes, hosts)
	}
	// One large file beside real ones is not a reason to lose them.
	kept := false
	for _, host := range hosts {
		if host == "kept" {
			kept = true
		}
	}
	if !kept {
		t.Errorf("got %v, want the machine from the fragment that is a config", hosts)
	}
}

// TestAnIncludeThatTakesTooLongToExpandIsAbandoned bounds the finding, not
// just what is found.
//
// The cap on matches bounds what is done with them and cannot bound looking
// for them, because that is one call into the filesystem.
// "Include /*/**/**/**/*///" walks five levels of wildcards and takes fourteen
// seconds to come back with nothing -- while somebody waits for a menu.
//
// A short budget here rather than a real pathological pattern, so the test
// costs milliseconds and does not depend on what this machine has mounted.
func TestAnIncludeThatTakesTooLongToExpandIsAbandoned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "conf.d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("Host included\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	was := includeGlobBudget
	includeGlobBudget = time.Nanosecond
	t.Cleanup(func() {
		includeGlobBudget = was
		slowGlobs.Delete(filepath.Join(dir, "*"))
	})

	path := filepath.Join(home, "config")
	if err := os.WriteFile(path, []byte("Host before\nInclude "+dir+"/*\nHost after\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	hosts := hostsFrom(path, 0)
	for _, host := range hosts {
		if host == "included" {
			t.Fatal("an expansion past its budget was waited for after all")
		}
	}
	// And the config around it still reads: one slow include is not a reason
	// to lose the machines somebody wrote down beside it.
	if len(hosts) != 2 || hosts[0] != "before" || hosts[1] != "after" {
		t.Errorf("got %v, want the machines either side of the abandoned include", hosts)
	}
}

// TestFilesThatIncludeEachOtherAreReadOnce bounds the shape the depth limit
// does not.
//
// The limit stops a chain at sixteen. It does not stop a chain that branches:
// three files that each include the directory they are in multiply at every
// level, which is three to the sixteenth reads and a menu that never opens.
// The shape is a copy-paste -- the same Include header in each fragment --
// rather than anything anybody set out to write.
//
// Reading each file once cannot change what comes back. Host aliases are
// deduplicated already; a second reading adds only the reading.
func TestFilesThatIncludeEachOtherAreReadOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "conf.d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		body := fmt.Sprintf("Host h%d\nInclude %s/*\n", i, dir)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("c%d", i)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(home, "config")
	if err := os.WriteFile(path, []byte("Host mine\nInclude "+dir+"/*\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan []string, 1)
	go func() { done <- hostsFrom(path, 0) }()

	select {
	case hosts := <-done:
		// All four machines, each once: the answer is the same as it would be
		// without the cycle, which is the point of the fix.
		if len(hosts) != 4 {
			t.Errorf("got %v, want the four machines named across the files", hosts)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("files that include each other did not come back; the depth " +
			"limit bounds the chain and not the branching")
	}
}

// TestAnIncludeThatMatchesEverythingIsRefused bounds the work one line can ask
// for.
//
// An Include names configuration somebody wrote. A pattern with a wildcard
// high in an absolute path names whatever is there: "Include /*/*" matches
// seventy-five thousand things on an ordinary machine, and each one is then
// asked about and possibly read. That is not slow, it is a menu that does not
// arrive.
//
// Refused whole rather than read in part: a config that meant to include
// something specific is better served by nothing arriving, which shows in the
// menu, than by whichever subset happens to sort first.
func TestAnIncludeThatMatchesEverythingIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// One more than the bound, each naming a machine, so reading any of them
	// would show.
	many := filepath.Join(home, "many")
	if err := os.MkdirAll(many, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxIncludeMatches; i++ {
		name := filepath.Join(many, fmt.Sprintf("c%04d", i))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("Host m%04d\n", i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(home, "config")
	body := "Host before\nInclude " + many + "/*\nHost after\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	hosts := hostsFrom(path, 0)
	for _, host := range hosts {
		if strings.HasPrefix(host, "m0") {
			t.Fatalf("an include matching %d files was read anyway: %q came back",
				maxIncludeMatches+1, host)
		}
	}

	// And exactly the bound is allowed, because the name says maximum. One
	// file fewer is the same directory with the last one taken out, so this
	// is the boundary itself rather than a number near it.
	if err := os.Remove(filepath.Join(many, fmt.Sprintf("c%04d", maxIncludeMatches))); err != nil {
		t.Fatal(err)
	}
	atTheBound := hostsFrom(path, 0)
	if len(atTheBound) != maxIncludeMatches+2 {
		t.Errorf("an include matching exactly %d files gave %d machines; the "+
			"limit is a maximum, so that many is allowed",
			maxIncludeMatches, len(atTheBound)-2)
	}
	// And the machines around it are still offered, as with any other include
	// that could not be used.
	if len(hosts) != 2 || hosts[0] != "before" || hosts[1] != "after" {
		t.Errorf("got %v, want the machines either side of the refused include", hosts)
	}
}

// TestAnIncludeInsideTheBoundIsStillRead is the other half: the bound has to
// sit past anything a person would write, or it is a feature quietly removed.
func TestAnIncludeInsideTheBoundIsStillRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, "conf.d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		name := filepath.Join(dir, fmt.Sprintf("c%02d", i))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("Host k%02d\n", i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(home, "config")
	if err := os.WriteFile(path, []byte("Include "+dir+"/*\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := len(hostsFrom(path, 0)); got != 12 {
		t.Errorf("an ordinary directory of host blocks gave %d machines, want 12", got)
	}
}

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
