package cli

import (
	"encoding/json"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	raw, err := os.ReadFile("cli.go")
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
	raw, err := os.ReadFile("cli.go")
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
	raw, err := os.ReadFile("cli.go")
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
	raw, err := os.ReadFile("cli.go")
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
	body := docsText(t)
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

func TestWhichMachineAnActionMeans(t *testing.T) {
	// Herdr runs an action with no argv, so which machine it means comes from
	// the environment: what was typed, else HRP_HOST, else whatever text was
	// selected when the action was triggered. disconnect closes a machine's
	// terminals, so reading this wrong closes the wrong machine's.
	context := func(selected string) string {
		return `{"selected_text":` + strconv.Quote(selected) + `}`
	}
	for _, tt := range []struct {
		what     string
		args     []string
		hrpHost  string
		ctxJSON  string
		want     string
		wantsErr bool
	}{
		{what: "what was typed wins", args: []string{"bot"}, hrpHost: "ci", want: "bot"},
		{what: "then the variable Herdr sets", hrpHost: "ci", ctxJSON: context("prod"), want: "ci"},
		{what: "then the selection", ctxJSON: context("prod"), want: "prod"},
		{what: "an empty argument is not an answer", args: []string{""}, hrpHost: "ci", want: "ci"},
		{what: "nor is a blank variable", hrpHost: "   ", ctxJSON: context("prod"), want: "prod"},
		{what: "the selection is trimmed", ctxJSON: context("  bot\n"), want: "bot"},
		{what: "nothing anywhere is an error", wantsErr: true},
		{what: "a blank selection is nothing", ctxJSON: context("   "), wantsErr: true},
		// Herdr's context is JSON this did not write. Unreadable is the same as
		// absent: the action says what to do about it rather than failing on a
		// parse error nobody can act on.
		{what: "and so is a context that will not parse", ctxJSON: "{not json", wantsErr: true},
	} {
		t.Run(tt.what, func(t *testing.T) {
			t.Setenv("HRP_HOST", tt.hrpHost)
			t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", tt.ctxJSON)

			got, err := hostArg("disconnect", tt.args)
			if tt.wantsErr {
				if err == nil {
					t.Fatalf("want an error, got %q", got)
				}
				// The message has to say how to answer, since the usual way of
				// running this passes no argument at all.
				if !strings.Contains(err.Error(), "disconnect") || !strings.Contains(err.Error(), "select") {
					t.Errorf("the error does not say what to do: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWhichSpaceAnActionWasTriggeredIn(t *testing.T) {
	// This decides whether "new terminal" opens on a machine or locally, so a
	// wrong answer opens a local shell in a machine's space -- which the daemon
	// then moves onto the machine and closes here.
	for _, tt := range []struct {
		what, env, ctxJSON, want string
	}{
		{what: "the variable when it is set", env: "w4A", ctxJSON: `{"workspace_id":"w9"}`, want: "w4A"},
		{what: "the context when it is not", ctxJSON: `{"workspace_id":"w9"}`, want: "w9"},
		{what: "nothing when there is neither", want: ""},
		{what: "nothing when the context will not parse", ctxJSON: "{not json", want: ""},
		{what: "nothing when the context has no space in it", ctxJSON: `{"selected_text":"bot"}`, want: ""},
	} {
		t.Run(tt.what, func(t *testing.T) {
			t.Setenv("HERDR_WORKSPACE_ID", tt.env)
			t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", tt.ctxJSON)
			if got := contextWorkspace(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVersionDoesNotWarnAboutADaemonThatIsNotRunning(t *testing.T) {
	// "not running" and "the running daemon is an older build" are both about
	// the same daemon, and only one of them can be true. The second is silenced
	// by asking whether anything answered -- and that guard could be removed
	// without any test minding, because a test binary has no build of its own
	// to compare against and the warning stays quiet either way.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "no-daemon-here")

	var out, warn strings.Builder
	if err := reportVersion(&out, &warn, "9fcc667"); err != nil {
		t.Fatalf("version failed with no daemon: %v", err)
	}

	if !strings.Contains(out.String(), "not running") {
		t.Errorf("the answer does not say the daemon is not running: %q", out.String())
	}
	if !strings.Contains(out.String(), "9fcc667") {
		t.Errorf("the answer does not name the installed build: %q", out.String())
	}
	if warn.String() != "" {
		t.Errorf("warned about a daemon that is not running: %q", warn.String())
	}
}

func TestWhichMachineACommandIsAimedAt(t *testing.T) {
	// Three places a machine's name can come from and an order between them:
	// what was typed, then HRP_HOST -- which Herdr sets for an action bound to
	// a pane -- and then, for connect alone, whatever the cursor is over.
	//
	// Getting the order wrong connects to the wrong machine, which is the one
	// mistake here nobody would call a small one.
	for _, tt := range []struct {
		what      string
		args      []string
		env       string
		want      string
		wantNamed bool
	}{
		{"an argument", []string{"bot"}, "", "bot", true},
		{"an argument beats the environment", []string{"bot"}, "ci", "bot", true},
		{"the environment when nothing was typed", nil, "ci", "ci", true},
		// Herdr passes these through as it finds them.
		{"the environment, trimmed", nil, "  ci\n", "ci", true},
		{"an environment of only spaces is no machine", nil, "   ", "", false},
		{"nothing at all", nil, "", "", false},
		// An empty argument is still an argument: a keybinding passing nothing
		// through aimed this command somewhere, and answering "none given"
		// sends it off to pick a machine of its own instead.
		{"an empty argument is still an argument", []string{""}, "ci", "", true},
	} {
		t.Run(tt.what, func(t *testing.T) {
			t.Setenv("HRP_HOST", tt.env)
			host, named := hostFor(tt.args)
			if host != tt.want || named != tt.wantNamed {
				t.Errorf("hostFor(%q) with HRP_HOST=%q = (%q, %v), want (%q, %v)",
					tt.args, tt.env, host, named, tt.want, tt.wantNamed)
			}
		})
	}
}

func TestWhatASelectionIsReadAs(t *testing.T) {
	// connect with no machine named falls back to whatever is selected in the
	// terminal, so this turns a highlighted word into an argument for ssh. The
	// text is not necessarily something you wrote -- a line of someone else's
	// output can be selected as easily as a hostname.
	for _, tt := range []struct{ what, ctxJSON, want string }{
		{"a selected name", `{"selected_text":"bot"}`, "bot"},
		// Herdr hands back what was highlighted, and a double-click takes the
		// spaces around a word with it.
		{"spaces around it", `{"selected_text":"  bot\n"}`, "bot"},
		{"nothing selected", `{"selected_text":""}`, ""},
		{"only spaces selected", `{"selected_text":"   \t\n"}`, ""},
		{"no context at all", "", ""},
		{"a context that will not parse", "{not json", ""},
		{"a context with no selection in it", `{"workspace_id":"w1"}`, ""},
	} {
		t.Run(tt.what, func(t *testing.T) {
			t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", tt.ctxJSON)
			if got := selectedText(); got != tt.want {
				t.Errorf("selectedText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestASelectionThatIsNotAMachineIsRefused(t *testing.T) {
	// The README says so, in the section about what this trusts: a selection is
	// checked before it is used, and anything ssh would read as an option
	// rather than a machine is refused.
	//
	// The two halves of that are tested apart -- what a selection is read as,
	// above, and what counts as a plausible machine, in the config package.
	// This is the join: that what comes out of one is what goes into the other,
	// and that a line of output selected by accident does not become an
	// argument to ssh.
	for _, tt := range []struct{ what, selected string }{
		{"a line of prose", "the build failed on bot"},
		{"something ssh reads as an option", "-oProxyCommand=touch /tmp/pwned"},
		{"a leading dash", "-bot"},
		{"a line with a newline inside it", "bot\nrm -rf /"},
		{"an escape sequence", "bot\x1b[31m"},
		{"a tab", "bot\tprod"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{"selected_text": tt.selected})
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", string(raw))

			name := selectedText()
			if name == "" {
				return // Nothing to hand on, which is refusal enough.
			}
			if err := config.PlausibleTarget(name); err == nil {
				t.Errorf("a selection of %q comes out as %q and is accepted as a machine",
					tt.selected, name)
			}
		})
	}

	// And an ordinary name still works, so this is not passing by refusing
	// everything.
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"selected_text":"workbox"}`)
	if name := selectedText(); config.PlausibleTarget(name) != nil {
		t.Errorf("a plain machine name %q was refused", name)
	}
}

func TestTheREADMEShowsTheVersionOutputThatIsPrinted(t *testing.T) {
	// The README shows both lines so somebody can compare them against their
	// own: the point of the command is that the two revisions differ, and that
	// only reads if the second column lines up under the first. Padding is the
	// easiest thing here to change without noticing, and the least likely to
	// be checked against a README afterwards.
	// The revisions the README happens to use, taken from what it shows rather
	// than repeated here, so rewriting the example does not need this changed.
	shown := regexp.MustCompile(`herdr-remote-panes ([0-9a-f]{7})\ndaemon +([0-9a-f]{7})`).
		FindStringSubmatch(docsText(t))
	if shown == nil {
		t.Fatal("the README no longer shows the two lines `version` prints")
	}

	got := versionLines(shown[1], shown[2])
	if len(got) != 2 {
		t.Fatalf("version printed %d lines, want 2: %q", len(got), got)
	}
	if want := shown[0]; strings.Join(got, "\n") != want {
		t.Errorf("version prints\n%q\nand the README shows\n%q", strings.Join(got, "\n"), want)
	}
}

// answerAs stands a daemon up on the control socket and has it answer every
// command with the reply given, until the test ends. Every test here used to run with
// nothing listening, which exercises exactly one of the three answers
// reportVersion can give -- and the switch it picks with was invisible to the
// mutation sweep until case expressions were included in it. `status` had no
// test at all for the same reason.
func answerAs(t *testing.T, reply syncd.Reply) {
	t.Helper()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	socket, err := syncd.ControlSocket()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}

	served := make(chan struct{})
	go func() {
		defer close(served)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var cmd syncd.Command
			if err := json.NewDecoder(conn).Decode(&cmd); err == nil {
				_ = json.NewEncoder(conn).Encode(reply)
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-served
	})
}

func TestVersionNamesTheBuildTheDaemonAnswersWith(t *testing.T) {
	answerAs(t, syncd.Reply{OK: true, Revision: "9fcc667"})

	var out, warn strings.Builder
	if err := reportVersion(&out, &warn, "9fcc667"); err != nil {
		t.Fatalf("version failed: %v", err)
	}

	daemon := daemonLine(t, out.String())
	if !strings.Contains(daemon, "9fcc667") {
		t.Errorf("the daemon line does not name the build it answered with: %q", daemon)
	}
	if strings.Contains(daemon, "unknown") || strings.Contains(daemon, "not running") {
		t.Errorf("a daemon that answered is reported as if it had not: %q", daemon)
	}
	if warn.String() != "" {
		t.Errorf("warned about a daemon running the installed build: %q", warn.String())
	}
}

func TestVersionSaysUnknownWhenTheDaemonNamesNoBuild(t *testing.T) {
	// What `go run` and a test binary look like: something answers, but it has
	// no commit of its own to report. A blank column reads as a bug.
	answerAs(t, syncd.Reply{OK: true})

	var out, warn strings.Builder
	if err := reportVersion(&out, &warn, "9fcc667"); err != nil {
		t.Fatalf("version failed: %v", err)
	}

	daemon := daemonLine(t, out.String())
	if !strings.Contains(daemon, "unknown") {
		t.Errorf("a daemon that named no build leaves the column blank: %q", daemon)
	}
	// The warning appears directly under that column, and used to call the
	// same daemon "an older build" -- a guess, stated as fact, about the one
	// thing the line above had just said was unknown. It might equally be
	// newer: a checkout run with `go run` reports no build either.
	if strings.Contains(warn.String(), "an older build") {
		t.Errorf("the column says unknown and the warning says it knows:\n%s%s", out.String(), warn.String())
	}
	if !strings.Contains(warn.String(), "restart Herdr") {
		t.Errorf("a daemon that might not be the installed build goes unmentioned: %q", warn.String())
	}
}

// daemonLine picks the daemon's row out of the two columns version prints, so
// that a test about the daemon's build cannot be satisfied by the binary's.
func daemonLine(t *testing.T, printed string) string {
	t.Helper()
	for _, line := range strings.Split(printed, "\n") {
		if strings.HasPrefix(line, daemonLabel) {
			return line
		}
	}
	t.Fatalf("no %q line in the version output:\n%s", daemonLabel, printed)
	return ""
}

// captureOutput runs something that prints to the process's own streams and
// hands back what it wrote.
//
// status writes to os.Stdout directly, as a command with nothing else to do
// should. Rather than reshape it for the benefit of a test, the test takes the
// streams: what it checks is then what a user actually sees, rather than what
// an injected writer was handed.
func captureOutput(t *testing.T, run func() error) (string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	realOut, realErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW

	runErr := run()

	os.Stdout, os.Stderr = realOut, realErr
	outW.Close()
	errW.Close()
	// Small enough for the pipe buffer, so nothing has to drain concurrently.
	printed, _ := io.ReadAll(outR)
	warned, _ := io.ReadAll(errR)
	if runErr != nil {
		t.Fatalf("status failed: %v", runErr)
	}
	return string(printed), string(warned)
}

func TestStatusPrintsTheMachinesTheDaemonReports(t *testing.T) {
	answerAs(t, syncd.Reply{OK: true, Hosts: []syncd.HostInfo{
		{Target: "deploy@vm", Label: "vm", Connected: true, Mirrors: 3},
		{Target: "ci@build", Label: "build", Connected: true, SSHOnly: true, Terminals: 1},
	}})

	printed, warned := captureOutput(t, status)

	for _, want := range []string{"vm", "build"} {
		if !strings.Contains(printed, want) {
			t.Errorf("the table does not name %q:\n%s", want, printed)
		}
	}
	if warned != "" {
		t.Errorf("warned with nothing to warn about: %q", warned)
	}
}

func TestStatusSaysSoWhenThereIsNothingToShow(t *testing.T) {
	// A blank answer to `status` reads as a broken command. It has to say that
	// there is nothing connected, in the words everything else here uses.
	answerAs(t, syncd.Reply{OK: true})

	printed, _ := captureOutput(t, status)

	if strings.TrimSpace(printed) == "" {
		t.Error("status printed nothing at all for a daemon with no machines")
	}
	if want := statusSummary(nil); !strings.Contains(printed, want) {
		t.Errorf("status printed %q, and the notification says %q", printed, want)
	}
}

func TestStatusPassesOnWhatTheDaemonIsWarningAbout(t *testing.T) {
	// The daemon is the only one that can see a config it could not read. If
	// status drops that, the machines simply are not listed and nothing says
	// why.
	answerAs(t, syncd.Reply{OK: true, Warning: "machine \"vm\" has no target"})

	printed, warned := captureOutput(t, status)

	if !strings.Contains(warned, "has no target") {
		t.Errorf("the daemon's warning was dropped; stderr was %q, stdout %q", warned, printed)
	}
	if strings.Contains(printed, "has no target") {
		t.Errorf("the warning went to stdout, where it would be parsed as a machine:\n%s", printed)
	}
}

func TestADaemonThatRefusesIsAnError(t *testing.T) {
	// Both ways of sending a command turn a refusal into an error, so that
	// whatever is calling has one thing to check rather than two. Neither had
	// a test: a reply carrying OK false and a reason could be read as success,
	// and the reason -- the only thing saying what went wrong -- dropped.
	answerAs(t, syncd.Reply{Message: `no machine called "vm" is configured`})

	if _, err := ask(syncd.Command{Cmd: "connect", Host: "vm"}); err == nil {
		t.Error("ask read a refusal as success")
	} else if !strings.Contains(err.Error(), "no machine called") {
		t.Errorf("ask lost the reason it was refused: %v", err)
	}

	if err := call(syncd.Command{Cmd: "connect", Host: "vm"}); err == nil {
		t.Error("call read a refusal as success")
	} else if !strings.Contains(err.Error(), "no machine called") {
		t.Errorf("call lost the reason it was refused: %v", err)
	}
}

func TestACommandThatWorkedSaysSo(t *testing.T) {
	// Action stdout only reaches the plugin log, so a result nobody printed is
	// a command that looks like it did nothing.
	answerAs(t, syncd.Reply{OK: true, Message: "connected to vm"})

	printed, warned := captureOutput(t, func() error {
		return call(syncd.Command{Cmd: "connect", Host: "vm"})
	})

	if !strings.Contains(printed, "connected to vm") {
		t.Errorf("the daemon said what happened and nothing printed it: %q", printed)
	}
	if warned != "" {
		t.Errorf("a command that worked warned about something: %q", warned)
	}

	if got, err := ask(syncd.Command{Cmd: "connect", Host: "vm"}); err != nil {
		t.Errorf("ask failed on a reply that said OK: %v", err)
	} else if got != "connected to vm" {
		t.Errorf("ask handed back %q, not what the daemon said", got)
	}
}

func TestHelpGoesWhereItCanBeRead(t *testing.T) {
	// `herdr-remote-panes help | less` showed nothing: the help was written to
	// stderr whether it had been asked for or not. Asked for, it is the answer
	// to the command rather than a complaint about it.
	printed, warned := captureOutput(t, func() error { return run("help", nil) })

	if !strings.Contains(printed, "herdr-remote-panes") {
		t.Errorf("help printed nothing to stdout; stderr had %q", warned)
	}
	if warned != "" {
		t.Errorf("help that was asked for went to stderr as well: %q", warned)
	}
}

func TestAMistypedCommandSaysSoAndDoesNotPollute(t *testing.T) {
	// The other half: unasked-for help must not land in whatever was reading
	// this command's output.
	var err error
	printed, warned := captureOutput(t, func() error {
		// Kept out of captureOutput's own return, which fails the test on an
		// error -- here the error is the thing being checked.
		err = run("stauts", nil)
		return nil
	})

	if err == nil {
		t.Fatal("a command that does not exist reported success")
	}
	if !strings.Contains(err.Error(), "stauts") {
		t.Errorf("the error does not name what was typed: %v", err)
	}
	if !strings.Contains(warned, "herdr-remote-panes") {
		t.Errorf("nothing showed what the commands are: %q", warned)
	}
	if printed != "" {
		t.Errorf("help for a mistyped command went to stdout: %q", printed)
	}
}

func TestDisconnectWithNothingToDisconnectSaysHow(t *testing.T) {
	// No argument, no HRP_HOST, no selection: an action triggered with nothing
	// to act on. The message has to say both ways of giving it one, since the
	// action is usually reached from a menu rather than a command line.
	t.Setenv("HRP_HOST", "")
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "")

	err := run("disconnect", nil)
	if err == nil {
		t.Fatal("disconnect with no machine named reported success")
	}
	for _, want := range []string{"disconnect", "ssh-target", "select"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q: %v", want, err)
		}
	}
}

func TestHowWideStatusMayDraw(t *testing.T) {
	// `stty size` is the terminal's answer, and it is read as text: the width
	// is the second of two fields. Everything else it might say -- a machine
	// with no terminal, a truncated answer, a nonsense number -- means "no
	// limit", which is what the plugin-action and piped cases want anyway.
	//
	// Driven through a stand-in on PATH, so what is checked is the parsing
	// rather than a copy of it.
	for _, tt := range []struct {
		what, script string
		want         int
	}{
		{"an ordinary terminal", `echo "24 80"`, 80},
		{"a wide one", `echo "50 200"`, 200},
		{"no terminal at all", `exit 1`, 0},
		{"nothing said", `echo ""`, 0},
		{"only the rows", `echo "24"`, 0},
		{"more than it was asked", `echo "24 80 extra"`, 0},
		{"a width that is not a number", `echo "24 wide"`, 0},
		{"a width of zero", `echo "24 0"`, 0},
		{"a negative width", `echo "24 -1"`, 0},
	} {
		t.Run(tt.what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "stty"),
				[]byte("#!/bin/sh\n"+tt.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

			if got := outputWidth(); got != tt.want {
				t.Errorf("stty said %q, read as %d, want %d", tt.script, got, tt.want)
			}
		})
	}
}

func TestWhatTheCommandExitsWith(t *testing.T) {
	// Herdr reads the exit status of an action, and the two failures mean
	// different things to whoever is looking at a plugin log: 2 is being asked
	// for something that is not a command, which is a keybinding or a manifest
	// with a typo in it, and 1 is a command that was understood and did not
	// work, which is about the machine or the daemon.
	//
	// Nothing held these. They are the whole of what a caller outside this
	// process can see, and they were checked by hand twice while moving code
	// around -- which is exactly the check worth writing down.
	saved := os.Args
	defer func() { os.Args = saved }()

	// Somewhere quiet: Main writes usage and errors as it goes, and a test
	// that fills the output with them is a test people stop reading.
	quiet, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer quiet.Close()
	savedOut, savedErr := os.Stdout, os.Stderr
	savedLog := log.Writer()
	os.Stdout, os.Stderr = quiet, quiet
	log.SetOutput(quiet)
	defer func() {
		os.Stdout, os.Stderr = savedOut, savedErr
		log.SetOutput(savedLog)
	}()

	for _, tt := range []struct {
		what string
		args []string
		want int
	}{
		{"nothing to do", []string{"herdr-remote-panes"}, 2},
		{"a command that is not one", []string{"herdr-remote-panes", "notacommand"}, 1},
		{"one that is, and works", []string{"herdr-remote-panes", "version"}, 0},
	} {
		os.Args = tt.args
		if got := Main(); got != tt.want {
			t.Errorf("%s: exited %d, want %d", tt.what, got, tt.want)
		}
	}
}

func TestACommandThatIgnoresAnArgumentSaysSo(t *testing.T) {
	// "status bot" reads as asking about one machine and reports every one,
	// which is the shape of mistake this plugin keeps finding in itself:
	// something quietly not doing what it was asked. There is nothing wrong
	// with the answer; there is something wrong with giving it in silence.
	var said strings.Builder
	restore, prefix, out := log.Flags(), log.Prefix(), log.Writer()
	t.Cleanup(func() {
		log.SetFlags(restore)
		log.SetPrefix(prefix)
		log.SetOutput(out)
	})
	log.SetFlags(0)
	log.SetOutput(&said)

	// Nowhere to reach a daemon, so run gets no further than the warning --
	// which is the part under test.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "nobody")
	_ = run("status", []string{"bot"})

	if !strings.Contains(said.String(), "status takes no arguments") {
		t.Errorf("an ignored argument was ignored in silence: %q", said.String())
	}
	// Which argument, since a command can be given several and a message that
	// does not name them leaves somebody counting.
	if !strings.Contains(said.String(), `"bot"`) {
		t.Errorf("the warning does not name what was ignored: %q", said.String())
	}

	// A command that reads one does not complain about being given one.
	said.Reset()
	_ = run("disconnect", []string{"bot"})
	if strings.Contains(said.String(), "takes no arguments") {
		t.Errorf("a command that uses its argument called it ignored: %q", said.String())
	}

	// And no argument at all is the ordinary case, which is how every action
	// in the manifest invokes these.
	said.Reset()
	_ = run("status", nil)
	if strings.Contains(said.String(), "takes no arguments") {
		t.Errorf("a command given nothing was warned about arguments: %q", said.String())
	}
}
