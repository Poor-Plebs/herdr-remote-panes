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

// theConfigLimit is what maxConfigBytes is expected to be, written out rather
// than read from it.
//
// The files these tests build are the size of the bound, and taking that size
// from the constant made them grow with it: raising maxConfigBytes a
// thousandfold asked for a gigabyte of padding several times over, and the run
// was killed rather than failing. A killed run is not a verdict, and this bound
// came back held on one sweep and killed on the next because of it.
//
// The value itself is held next door, in the test that keeps it in step with
// the documentation page that quotes it as 1 MB. Writing it out here is what
// lets that test be reached at all.
const theConfigLimit = 1 << 20

// TestTheTwoHalvesAgreeAboutAConfigTooLargeToRead holds them to each other.
//
// One half decides what is read and the other explains an empty menu. A bound
// added to the first and not the second means a config that is skipped is also
// pronounced fine: no machines, and nothing anywhere saying why. That is the
// failure this package exists to prevent, and adding the size bound created a
// new instance of it -- caught by asking what the two halves would each do
// with the same file rather than by anything failing.
func TestTheTwoHalvesAgreeAboutAConfigTooLargeToRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Ordinary in every way except its size: short lines, real host blocks.
	var body strings.Builder
	for i := 0; body.Len() <= theConfigLimit+1024; i++ {
		fmt.Fprintf(&body, "Host machine%06d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	hosts := Hosts()
	why := Unreadable()
	if len(hosts) == 0 && why == "" {
		t.Fatal("no machines and no reason: the menu would be empty with " +
			"nothing anywhere to explain it")
	}
	if len(hosts) == 0 && !strings.Contains(why, "larger than") {
		t.Errorf("the reason given is %q, which does not say what is wrong "+
			"with the file", why)
	}
}

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
	big := append([]byte("Host buried\n"), make([]byte, theConfigLimit+1)...)
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
	exact := append([]byte("Host exact\n"), make([]byte, theConfigLimit-len("Host exact\n"))...)
	if int64(len(exact)) != int64(theConfigLimit) {
		t.Fatalf("the file for the boundary is %d bytes, not %d", len(exact), theConfigLimit)
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
			"maximum, so that size is allowed: got %v", theConfigLimit, hosts)
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
	// Nought rather than a nanosecond. With a budget that small the timer has
	// fired and the glob may also have finished, and a select between two ready
	// cases picks at random -- so this asked a question with two right answers
	// and failed about one run in twenty. Nought asks it exactly.
	includeGlobBudget = 0
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

func TestAnIncludeThatIsSkippedSaysWhy(t *testing.T) {
	// The reading gives up on an Include for four reasons, and every one of
	// them takes a group of machines out of the menu. They were all silent:
	// the file itself was fine, so the check that explains an empty menu found
	// nothing to say and the machines were simply gone.
	//
	// That is the failure d3fa765 and f69a26b were both about, one level
	// further in -- there it was the file that could not be read, here it is
	// the pattern that names the files.
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	inc := filepath.Join(ssh, "conf.d")
	if err := os.MkdirAll(inc, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	top := filepath.Join(ssh, "config")

	t.Run("more files than an Include may pull in", func(t *testing.T) {
		for i := 0; i <= maxIncludeMatches; i++ {
			if err := os.WriteFile(filepath.Join(inc, fmt.Sprintf("h%04d", i)),
				[]byte(fmt.Sprintf("Host machine%04d\n", i)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(top, []byte("Host toplevel\nInclude "+inc+"/*\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		hosts := Hosts()
		if len(hosts) != 1 {
			t.Fatalf("the fixture no longer drops the include: %d machines", len(hosts))
		}
		why := Unreadable()
		if why == "" {
			t.Fatalf("%d machines are missing and nothing says why", maxIncludeMatches+1)
		}
		for _, want := range []string{"conf.d", "matched 257", "256"} {
			if !strings.Contains(why, want) {
				t.Errorf("the reason is %q, which does not say %q", why, want)
			}
		}
	})

	t.Run("no time to expand", func(t *testing.T) {
		// The budget, asked for exactly. The pattern is recorded so the next
		// menu does not wait again -- and that skip has to say so too, or the
		// second read of a config is quieter than the first about the same
		// machines.
		was := includeGlobBudget
		includeGlobBudget = 0
		pattern := filepath.Join(inc, "*")
		t.Cleanup(func() {
			includeGlobBudget = was
			slowGlobs.Delete(pattern)
		})

		if why := Unreadable(); !strings.Contains(why, "expand") {
			t.Errorf("an include given no time to expand is reported as %q", why)
		}
		// Again, now that it is remembered as slow.
		if why := Unreadable(); !strings.Contains(why, "expand") {
			t.Errorf("an include skipped for being slow before is reported as %q", why)
		}
	})
}

func TestAMachineInTwoIncludedFilesIsOfferedOnce(t *testing.T) {
	// A host read through an Include is added only if it has not been seen,
	// and the marking is what makes the second sighting a repeat. Deleting
	// that marking left every test here green.
	//
	// The fuzz target does assert that a host is never offered twice, and ran
	// a hundred thousand times without finding this: it is handed one file's
	// contents, and what is missing here is a host arriving from two files.
	// A fuzzer over a string cannot build a directory of them.
	//
	// What it costs is the same machine listed twice in the menu, which reads
	// as two machines to connect to and picks a different one each time.
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "conf.d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"10-first":  "Host bot\n  HostName bot.example\n",
		"20-second": "Host bot\n  HostName bot.example\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(home, "config")
	if err := os.WriteFile(path, []byte("Include "+dir+"/*\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	hosts := hostsFrom(path, 0)
	offered := 0
	for _, host := range hosts {
		if host == "bot" {
			offered++
		}
	}
	if offered != 1 {
		t.Errorf("bot is offered %d times from two included files, want once: %v",
			offered, hosts)
	}
}
