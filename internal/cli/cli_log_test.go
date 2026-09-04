package cli

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/version"
)

// TestTheDaemonsLogReachesTheFileItNames holds the join rather than the parts.
//
// logfile is tested thoroughly on its own -- rotating, reopening after a
// failed rotate, coming back when the disk does. What none of that says is
// whether the daemon's log is connected to it, and that is the half somebody
// depends on: every instruction for finding out why something went wrong ends
// with reading this file.
//
// It would fail silently. The daemon carries on either way, nothing checks
// that a line arrived, and the file simply stays empty -- which reads exactly
// like a daemon with nothing to say.
func TestTheDaemonsLogReachesTheFileItNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	// Whatever else happens, the standard logger goes back where it was: it is
	// process-wide, and a test that leaves it pointing into its own temporary
	// directory takes every later test's output with it.
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// Standing in for the terminal, so the other half of the join can be read:
	// the daemon writes to both, and running it by hand and seeing nothing is
	// the same silence as a daemon that has stopped.
	//
	// Swapped before the call, because the writer is built from whatever
	// os.Stderr is at that moment.
	readable, terminal, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	wasStderr := os.Stderr
	os.Stderr = terminal
	t.Cleanup(func() { os.Stderr = wasStderr })

	closeLog := daemonLog()
	if closeLog == nil {
		t.Fatal("no log was opened, with a state directory that exists")
	}

	path := filepath.Join(dir, "daemon.log")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("daemon.log was not created: %v", err)
	}

	log.Printf("a line the daemon would write")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	said := string(raw)

	if !strings.Contains(said, "a line the daemon would write") {
		t.Errorf("what the daemon logged is not in daemon.log:\n%s", said)
	}
	// The starting line is what says which build is running, and is the first
	// thing worth knowing when a log is being read at all.
	if !strings.Contains(said, "starting") || !strings.Contains(said, version.Short()) {
		t.Errorf("daemon.log does not open by naming the build that started:\n%s", said)
	}

	// Read before closing anything, and bounded: the pipe holds what has been
	// written so far and reading past it would wait for a writer that is still
	// open.
	_ = terminal.Close()
	os.Stderr = wasStderr
	shown := make([]byte, 4096)
	n, _ := readable.Read(shown)
	if !strings.Contains(string(shown[:n]), "a line the daemon would write") {
		t.Errorf("the daemon's log did not reach the terminal as well as the "+
			"file, so running it by hand shows nothing:\n%s", shown[:n])
	}

	closeLog()
	log.Printf("after the daemon has stopped")
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "after the daemon has stopped") {
		t.Error("the log is still being written to after it was closed")
	}
}

// TestNoStateDirectoryMeansNoLogRatherThanACrash holds the other half of what
// daemonLog returns. Its caller writes `if closeLog := daemonLog(); closeLog
// != nil` -- calling a nil func is a panic, and this is the case that returns
// one.
func TestNoStateDirectoryMeansNoLogRatherThanACrash(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	if closeLog := daemonLog(); closeLog != nil {
		closeLog()
		t.Error("a log was opened with nowhere to put it")
	}
}

func TestTheDaemonLogSaysWhichDaySomethingHappened(t *testing.T) {
	// daemon.log is kept until it rolls at a quarter of a megabyte, which for a
	// healthy daemon is days. With the time alone, the page runs backwards
	// every time Herdr is restarted on a later day -- a real one here has
	// "stopping on terminated" at 21:29 directly above "starting" at 12:24 --
	// and placing an entry means counting restarts.
	//
	// mirror.log, written beside it, has carried a full timestamp all along.
	var written strings.Builder
	restore := log.Flags()
	prefix := log.Prefix()
	out := log.Writer()
	t.Cleanup(func() {
		log.SetFlags(restore)
		log.SetPrefix(prefix)
		log.SetOutput(out)
	})

	// Set the way Main sets them, so this is about what a daemon writes rather
	// than about the defaults.
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(&written)
	log.Print("listening on /somewhere/control-hub.sock")

	line := written.String()
	if !regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}`).MatchString(line) {
		t.Errorf("a log line reads %q, without a date on it", strings.TrimSpace(line))
	}
}

// logLine is what one line written through the logger Main sets up looks like:
// a prefix naming the program, then the date and time.
var logLine = regexp.MustCompile(`(?m)^(\S+: )(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}) `)

// TestEveryLineNamesTheProgramAndTheDay holds what Main's logger renders.
//
// This replaces a test that grepped cli.go for `log.SetFlags(log.Ldate |
// log.Ltime)`. That was the only thing holding any of the three settings, and it
// held the SOURCE TEXT: the same line moved into a function nothing calls would
// still have passed it, and the prefix and the sanitiser beside it were held by
// nothing whatever. The reason given was that Main takes over the process's
// logger and arguments, which was true of Main and is not true of logTo.
func TestEveryLineNamesTheProgramAndTheDay(t *testing.T) {
	flags, prefix, out := log.Flags(), log.Prefix(), log.Writer()
	t.Cleanup(func() {
		log.SetFlags(flags)
		log.SetPrefix(prefix)
		log.SetOutput(out)
	})

	// Nothing inherited: the standard logger ALREADY dates every line by
	// default, so with the flags left alone the date says nothing about whether
	// logTo asked for it -- deleting that line changed no rendering at all until
	// this was here. The prefix is cleared for the same reason.
	log.SetFlags(0)
	log.SetPrefix("")

	var written strings.Builder
	logTo(&written)
	// With an escape in it, because the third thing logTo sets is the sanitiser
	// and a message of ordinary text cannot tell whether it is there.
	log.Print("listening on \x1b[31m/somewhere/control-hub.sock")

	line := strings.TrimSpace(written.String())
	if strings.ContainsRune(line, 0x1b) {
		t.Errorf("an escape from the far side reached the terminal reading the "+
			"report: %q", line)
	}
	match := logLine.FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("a log line reads %q, which names neither the program nor the day: "+
			"daemon.log covers days of restarts, and Herdr collects this into the "+
			"plugin log beside every other plugin's", line)
	}

	// And the page that teaches somebody to read these lines shows them opening
	// the same way. Taken from what the logger RENDERS rather than from the
	// literal in cli.go, so the two cannot drift apart while both look right.
	shown := logLine.FindAllStringSubmatch(docsText(t), -1)
	if len(shown) == 0 {
		t.Fatal("no example log line in the documentation, so this compares nothing")
	}
	for _, example := range shown {
		if example[1] != match[1] {
			t.Errorf("the pages show a log line opening %q and this writes %q",
				example[1], match[1])
		}
	}
}

func TestVersionDoesNotClaimTheDaemonIsDownWhenItCannotAsk(t *testing.T) {
	// This command exists to compare the build installed against the one
	// running, which is the question after an upgrade. Run from an ordinary
	// shell rather than through Herdr there is no state directory, so no
	// socket to knock on -- and every failure read as "not running", which is
	// a definite answer to a question that was never put. The daemon may be up
	// and mirroring; this process cannot see it either way.
	//
	// Believing it costs a restart of Herdr to no purpose, or the opposite
	// conclusion: that the new build is the one running.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")
	t.Setenv("HERDR_SESSION", "")

	var out, warn strings.Builder
	if err := reportVersion(&out, &warn, "abc1234"); err != nil {
		t.Fatal(err)
	}
	said := out.String()
	if strings.Contains(said, "not running") {
		t.Errorf("with no way to ask, the report says the daemon is not running:\n%s", said)
	}
	if !strings.Contains(said, "cannot tell") {
		t.Errorf("the report does not say it could not ask:\n%s", said)
	}
	// And no stale-build warning either: comparing against a build nothing
	// reported is comparing against nothing.
	if warn.Len() != 0 {
		t.Errorf("a warning was given about a daemon that was never reached: %q", warn.String())
	}

	// With somewhere to look and nothing there, "not running" is the right
	// answer and still gets given.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "nobody")
	out.Reset()
	warn.Reset()
	if err := reportVersion(&out, &warn, "abc1234"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Errorf("with a socket to knock on and no answer, the report says:\n%s", out.String())
	}
}

// theDaemonLogLimit is what maxDaemonLog is expected to be, written out rather
// than read from it. The size is the whole policy: the daemon is a long-lived
// process on somebody's laptop, one generation of this file is kept, and the
// space it may take is twice this number and nothing else.
//
// mirror's maxLogBytes is the same 256K for the failure log, and stays a
// separate number. What the two share is the logfile package that does the
// rolling; the sizes are two judgements about two different logs.
const theDaemonLogLimit = 256 * 1024

func TestTheDaemonLogRollsOverAtTwoHundredAndFiftySixKilobytes(t *testing.T) {
	// Written out rather than read from maxDaemonLog, and checked on both
	// sides so the number cannot move either way without this saying so. The
	// log tests above prove the daemon's lines reach the file; where the file
	// stops growing is a different question and nothing asked it.
	//
	// Registered before the stderr swap so it runs after it: cleanups run in
	// reverse, and the logger must be pointed back at the real stderr rather
	// than at the one this test is about to take away.
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// The daemon writes its starting line to stderr as well as to the file,
	// and that line is not what this test is reading.
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	wasStderr := os.Stderr
	os.Stderr = devnull
	t.Cleanup(func() { os.Stderr = wasStderr; _ = devnull.Close() })

	for _, tt := range []struct {
		what   string
		fill   int
		rolled bool
	}{
		{"exactly the limit", theDaemonLogLimit, true},
		{"a kilobyte under it", theDaemonLogLimit - 1024, false},
	} {
		dir := t.TempDir()
		t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
		path := filepath.Join(dir, "daemon.log")
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), tt.fill), 0o600); err != nil {
			t.Fatal(err)
		}

		// Opening the log writes the starting line, which is the write that
		// decides whether the file had room for it.
		closeLog := daemonLog()
		if closeLog == nil {
			t.Fatal("no log was opened, with a state directory that exists")
		}
		closeLog()

		_, err := os.Stat(path + ".1")
		rolled := err == nil
		if rolled != tt.rolled {
			info, _ := os.Stat(path)
			t.Errorf("%s: filled to %d bytes and started the daemon: rolled over = %v, "+
				"want %v (the log is now %d bytes)",
				tt.what, tt.fill, rolled, tt.rolled, info.Size())
		}
	}
}

func TestTheDaemonRunsOnDefaultsWhenTheConfigCannotBeRead(t *testing.T) {
	// The daemon says "continuing with defaults" and then has to be running on
	// them. Without the line that puts them there it carries on with the zero
	// value instead: no session name, no poll interval, no label format -- and
	// the message it just printed is untrue.
	//
	// This was unreachable from a test until the load was lifted out of run,
	// which ends in the daemon's own Run and does not return. That is why
	// nothing held it.
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	var said strings.Builder
	log.SetOutput(&said)

	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := daemonConfig()
	if err == nil {
		t.Fatal("a config that could not be read was reported as fine")
	}

	// The three that decide whether a daemon can do anything at all. A zero
	// config has none of them.
	want := config.Defaults()
	if cfg.Session != want.Session {
		t.Errorf("session = %q, want %q", cfg.Session, want.Session)
	}
	if cfg.PollInterval != want.PollInterval {
		t.Errorf("poll interval = %q, want %q", cfg.PollInterval, want.PollInterval)
	}
	if cfg.LabelFormat != want.LabelFormat {
		t.Errorf("label format = %q, want %q", cfg.LabelFormat, want.LabelFormat)
	}

	// And it said so, since the message and the fallback are one promise.
	if !strings.Contains(said.String(), "continuing with defaults") {
		t.Errorf("nothing said the daemon was carrying on with defaults:\n%s", said.String())
	}
}

func TestAConfigThatReadsIsUsedAsWritten(t *testing.T) {
	// The other half: a readable config is handed over unchanged and reports
	// no error, so the daemon does not announce a fault it did not have.
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	log.SetOutput(io.Discard)

	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"session":"chosen"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := daemonConfig()
	if err != nil {
		t.Fatalf("a config that reads was reported as broken: %v", err)
	}
	if cfg.Session != "chosen" {
		t.Errorf("session = %q, want the one the config names", cfg.Session)
	}
}
