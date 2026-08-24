package main

import (
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The help text is a fourth place that describes this plugin, after the
// manifest, the package comments and the notification -- and like those, it had
// gone on describing it as it was when mirroring was the only thing it did. Two
// contracts are checkable and worth holding: every command the binary handles
// is listed, and every command listed is one it handles.

func commandsInSource(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	body = body[strings.Index(body, "func run("):]
	body = body[:strings.Index(body, "func usage(")]

	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`case ("[a-z-]+"(?:, "[a-z-]+")*):`).FindAllStringSubmatch(body, -1) {
		for _, name := range strings.Split(m[1], ", ") {
			found[strings.Trim(name, `"`)] = true
		}
	}
	// A test that finds no commands would pass every assertion below without
	// checking anything, which is the failure mode of reading source in a test.
	for _, want := range []string{"daemon", "connect", "status", "open-tab"} {
		if !found[want] {
			t.Fatalf("the command parser found %v, which does not include %q -- it has stopped working",
				keysOf(found), want)
		}
	}
	return found
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestUsageListsEveryCommand(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	help := string(raw)
	help = help[strings.Index(help, "func usage("):]

	handled := commandsInSource(t)
	for name := range handled {
		switch name {
		case "help", "-h", "--help":
			continue // listing the help in the help would be odd
		}
		// A dashed spelling of a command that is already listed: --version and
		// version are one thing, and giving each a line describes it as two.
		if plain := strings.TrimLeft(name, "-"); plain != name && handled[plain] {
			continue
		}
		if !strings.Contains(help, "\n  "+name+" ") && !strings.Contains(help, "\n  "+name+"\n") {
			t.Errorf("the help does not mention %q, so nobody will find it", name)
		}
	}
}

func TestUsageDoesNotOfferWhatIsNotThere(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	help := body[strings.Index(body, "func usage("):]
	handled := commandsInSource(t)

	// Every indented word at the start of a help line should be a command.
	for _, line := range strings.Split(help, "\n") {
		m := regexp.MustCompile(`^  ([a-z][a-z-]+) `).FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if !handled[m[1]] {
			t.Errorf("the help offers %q but nothing handles it: %q", m[1], strings.TrimSpace(line))
		}
	}
}

func TestUsageDoesNotCallMirroringTheUsualThing(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	help := string(raw)
	help = help[strings.Index(help, "func usage("):]

	// The first line is what somebody reads before anything else.
	first := strings.SplitN(help[strings.Index(help, "herdr-remote-panes"):], "\n", 2)[0]
	if strings.Contains(strings.ToLower(first), "mirror") {
		t.Errorf("the first line of the help is about mirroring, which is off by default: %q", first)
	}
}

// captureStdout runs f with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	runErr := f()
	os.Stdout = saved
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out, runErr
}

func TestVersionSaysWhichBuildAndWhichDaemon(t *testing.T) {
	// The two disagree, and that is the point: an update replaces the files on
	// disk and leaves the running daemon alone. Asking is how you find out
	// whether the fix you just installed is the one actually reconciling panes.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "no-daemon-here")

	out, err := captureStdout(t, func() error { return run("version", nil) })
	if err != nil {
		// Asking which build you have installed has to work when the daemon is
		// not running: that is when you most want to know.
		t.Fatalf("version failed with no daemon: %v", err)
	}
	if !strings.Contains(out, "herdr-remote-panes") {
		t.Errorf("no line for this build: %q", out)
	}
	if !strings.Contains(out, "daemon") || !strings.Contains(out, "not running") {
		t.Errorf("nothing says the daemon is not running: %q", out)
	}
	// Two lines, both saying which build: a single line would leave you unable
	// to tell which half of the answer you were reading.
	if n := len(strings.Split(strings.TrimSpace(out), "\n")); n != 2 {
		t.Errorf("want a line each for the binary and the daemon, got %d: %q", n, out)
	}
}

func TestVersionIsSpeltBothWays(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "no-daemon-here")
	for _, spelling := range []string{"version", "--version"} {
		out, err := captureStdout(t, func() error { return run(spelling, nil) })
		if err != nil {
			t.Errorf("%s failed: %v", spelling, err)
		}
		if !strings.Contains(out, "herdr-remote-panes") {
			t.Errorf("%s printed %q", spelling, out)
		}
	}
}

func TestTheREADMEShowsWhatVersionActuallyPrints(t *testing.T) {
	// The README shows the two-line answer with its columns lined up. Written
	// out by hand it agrees with the code until either label changes, and then
	// it quietly shows a layout the command has never produced.
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	body := string(readme)
	const marker = "$ herdr-remote-panes version\n"
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("the README no longer shows %q", strings.TrimSpace(marker))
	}
	block := body[i+len(marker):]
	block = block[:strings.Index(block, "```")]

	shown := strings.Split(strings.TrimRight(block, "\n"), "\n")
	if len(shown) != 2 {
		t.Fatalf("want two lines in the README's example, got %d: %q", len(shown), shown)
	}
	// Read the builds out of what the README shows, so the check is about the
	// layout and not about which commit happens to be in the example.
	var binary, daemon string
	if f := strings.Fields(shown[0]); len(f) == 2 {
		binary = f[1]
	}
	if f := strings.Fields(shown[1]); len(f) == 2 {
		daemon = f[1]
	}
	if binary == "" || daemon == "" {
		t.Fatalf("cannot read the builds out of the README's example: %q", shown)
	}

	want := versionLines(binary, daemon)
	for i := range want {
		if shown[i] != want[i] {
			t.Errorf("the README shows\n\t%q\nbut the command prints\n\t%q", shown[i], want[i])
		}
	}
}
