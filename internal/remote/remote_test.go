package remote

import (
	"errors"
	"fmt"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteCommandClearsSocketOverrides(t *testing.T) {
	// HERDR_SOCKET_PATH outranks HERDR_SESSION when Herdr resolves which
	// server to talk to, so it must be cleared before the remote invocation.
	// An explicit binary skips the probe, keeping this test offline.
	argv, err := NewWithBin("workbox", "agents", "herdr").Argv(false, "pane", "list")
	if err != nil {
		t.Fatalf("Argv: %v", err)
	}
	got := strings.Join(argv, " ")
	for _, want := range []string{
		"-u HERDR_SOCKET_PATH",
		"-u HERDR_CLIENT_SOCKET_PATH",
		"HERDR_SESSION=agents herdr pane list",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("argv %q missing %q", got, want)
		}
	}
}

func TestSSHArgsTTY(t *testing.T) {
	interactive := strings.Join(New("workbox", "").SSHArgs(true), " ")
	if !strings.Contains(interactive, "-tt") {
		t.Errorf("interactive attach needs a remote pty: %q", interactive)
	}
	if strings.Contains(interactive, "BatchMode=yes") {
		t.Errorf("interactive attach must allow auth prompts: %q", interactive)
	}

	polling := strings.Join(New("workbox", "").SSHArgs(false), " ")
	if !strings.Contains(polling, "BatchMode=yes") {
		t.Errorf("polling must not block on prompts: %q", polling)
	}
	if strings.Contains(polling, "-tt") {
		t.Errorf("polling must not allocate a pty: %q", polling)
	}
}

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"simple":      "simple",
		"term_abc123": "term_abc123",
		"":            "''",
		"two words":   "'two words'",
		"it's":        `'it'\''s'`,
		"a;rm -rf /":  "'a;rm -rf /'",
		"$(whoami)":   "'$(whoami)'",

		// The ends of every range the safe set is written as, so that what
		// counts as safe is pinned rather than sampled from the middle. Every
		// one of these left out is a character that would be quoted when it
		// need not be -- harmless on its own, and the same slip made the other
		// way is how a set stops being safe.
		"azAZ09":      "azAZ09",
		"-_./:=":      "-_./:=",
		"a`b":         "'a`b'",
		"a\\b":        `'a\b'`,
		"{brace}":     "'{brace}'",
		"back\\slash": `'back\slash'`,
	}
	for in, want := range tests {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfiguredBinIsUsedVerbatim(t *testing.T) {
	// A remote install under ~/.local/bin is invisible to `ssh host <cmd>`,
	// which runs no login shell, so the path must survive into the command.
	argv, err := NewWithBin("workbox", "", "~/.local/bin/herdr").Argv(false, "pane", "list")
	if err != nil {
		t.Fatalf("Argv: %v", err)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "'~/.local/bin/herdr' pane list") {
		t.Errorf("argv %q does not invoke the configured binary", got)
	}
}

func TestShellQuoteSurvivesARealShell(t *testing.T) {
	// The table test above asserts what the quoting should look like, which is
	// only as good as the belief behind it. `ssh host <cmd>` runs the command
	// through a shell on the far machine, so the real question is whether a
	// string comes back out of a shell exactly as it went in. Anything that
	// does not is an injection on someone else's machine.
	//
	// The payloads below announce themselves rather than doing damage: if the
	// quoting leaks, the output contains INJECTED instead of the literal text.
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh to check against")
	}

	inputs := []string{
		"simple",
		"two words",
		"it's",
		`"double"`,
		`back\slash`,
		"$(echo INJECTED)",
		"`echo INJECTED`",
		"${IFS}INJECTED",
		"a;echo INJECTED",
		"a|echo INJECTED",
		"a&&echo INJECTED",
		"a>/dev/null",
		"*",
		"~root",
		"--flag",
		"-",
		"line\nbreak",
		"tab\there",
		"emoji 🌩 name",
		"héllo",
		"'",
		`'\''`,
		"$",
		"!history",
		"a$'\\n'b",
	}

	for _, in := range inputs {
		// printf %s prints its argument with no interpretation of its own, so
		// whatever comes back is exactly what the shell handed it.
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellQuote(in)).Output()
		if err != nil {
			t.Errorf("shellQuote(%q) produced something the shell rejected: %v", in, err)
			continue
		}
		if string(out) != in {
			t.Errorf("shellQuote(%q) came back as %q", in, string(out))
		}
	}
}

func TestRemoteCommandQuotesEveryPart(t *testing.T) {
	// A session name comes from a config file edited by hand, and a mistake in
	// it should stay a mistake rather than becoming a command on the machine at
	// the far end.
	c := &Client{Target: "bot", Session: "a;echo INJECTED"}
	cmd := c.remoteCommand("/usr/bin/herdr", []string{"pane", "list", "--filter", "$(echo INJECTED)"})

	out, err := exec.Command("/bin/sh", "-c", "set -- "+cmd+`; printf '%s\n' "$@"`).Output()
	if err != nil {
		t.Fatalf("the rendered command is not valid shell: %v (%s)", err, cmd)
	}
	if strings.Contains(string(out), "INJECTED\n") && !strings.Contains(string(out), "$(echo INJECTED)") {
		t.Errorf("a value escaped its quoting: %q", string(out))
	}
	if !strings.Contains(string(out), "$(echo INJECTED)") {
		t.Errorf("the literal argument did not survive: %q", string(out))
	}
}

func TestSameSettings(t *testing.T) {
	// The session is part of the multiplexed connection's identity, so a client
	// built for one session cannot stand in for another.
	c := NewWithBin("bot", "default", "/usr/bin/herdr")

	if !c.SameSettings("bot", "default", "/usr/bin/herdr") {
		t.Error("a client did not recognise its own settings")
	}
	for _, other := range [][3]string{
		{"other", "default", "/usr/bin/herdr"},
		{"bot", "remote", "/usr/bin/herdr"},
		{"bot", "default", "/opt/herdr"},
		{"bot", "default", ""},
	} {
		if c.SameSettings(other[0], other[1], other[2]) {
			t.Errorf("settings %v were accepted as unchanged", other)
		}
	}

	// Two clients for the same settings must share a control path, or each
	// would open its own connection to the same machine. Held in variables so
	// it reads as "call it twice and compare" rather than as a comparison of
	// something with itself, which is also how it reads to a linter.
	first := NewWithBin("bot", "default", "").controlPath
	second := NewWithBin("bot", "default", "").controlPath
	if first != second {
		t.Errorf("identical clients disagree on the control path: %q and %q", first, second)
	}
	// Different sessions must not, since the session is part of what the
	// connection is for.
	if NewWithBin("bot", "a", "").controlPath == NewWithBin("bot", "b", "").controlPath {
		t.Error("clients for different sessions share a control path")
	}
}

func TestSSHArgsEndOptionsBeforeTheTarget(t *testing.T) {
	// ssh takes options on the command line, so without "--" a destination
	// beginning with a dash is read as one. -oProxyCommand=... runs a command,
	// and a target need not have been typed by the person running this: connect
	// falls back to whatever text is selected in the terminal.
	c := New("bot", "default")

	for _, tty := range []bool{true, false} {
		args := c.SSHArgs(tty)
		if len(args) < 2 {
			t.Fatalf("SSHArgs(%v) = %v", tty, args)
		}
		if args[len(args)-1] != "bot" {
			t.Errorf("SSHArgs(%v) does not end with the target: %v", tty, args)
		}
		if args[len(args)-2] != "--" {
			t.Errorf("SSHArgs(%v) has no -- before the target: %v", tty, args)
		}
	}
}

func TestProbeScriptFindsHerdrWherePathDoesNot(t *testing.T) {
	// `ssh host <command>` runs no login shell, so an install under
	// ~/.local/bin -- where Herdr's own installer puts it for a non-root user
	// -- is invisible to PATH even though an interactive login finds it. This
	// script is what runs on someone else's machine to sort that out, so it is
	// checked by running it rather than by reading it.
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh to run the probe in")
	}

	run := func(home, path string) (string, error) {
		cmd := exec.Command("/bin/sh", "-c", probeScript)
		cmd.Env = []string{"HOME=" + home, "PATH=" + path}
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	install := func(dir, name string) string {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("on PATH", func(t *testing.T) {
		dir := t.TempDir()
		install(dir, "herdr")
		got, err := run(t.TempDir(), dir)
		if err != nil {
			t.Fatalf("the probe failed: %v", err)
		}
		if !strings.HasSuffix(got, "/herdr") {
			t.Errorf("probe said %q, want a path to herdr", got)
		}
	})

	t.Run("only under ~/.local/bin", func(t *testing.T) {
		home := t.TempDir()
		want := install(filepath.Join(home, ".local", "bin"), "herdr")
		got, err := run(home, t.TempDir()) // an empty PATH directory
		if err != nil {
			t.Fatalf("the probe failed where herdr is installed: %v", err)
		}
		if got != want {
			t.Errorf("probe said %q, want %q", got, want)
		}
	})

	t.Run("a home directory with a space in it", func(t *testing.T) {
		// Quoted in the script; unquoted it would split and find nothing.
		home := filepath.Join(t.TempDir(), "My Home")
		want := install(filepath.Join(home, ".local", "bin"), "herdr")
		got, err := run(home, t.TempDir())
		if err != nil {
			t.Fatalf("the probe failed: %v", err)
		}
		if got != want {
			t.Errorf("probe said %q, want %q", got, want)
		}
	})

	t.Run("nowhere at all", func(t *testing.T) {
		got, err := run(t.TempDir(), t.TempDir())
		if err == nil {
			t.Errorf("the probe reported success with no herdr installed: %q", got)
		}
		if got != "" {
			t.Errorf("the probe printed %q when it found nothing", got)
		}
	})

	t.Run("a directory named herdr is not a herdr", func(t *testing.T) {
		// -x is true for a directory, so a bare existence check would offer one
		// as the binary and every later call would fail confusingly.
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".local", "bin", "herdr"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, _ := run(home, t.TempDir())
		if got != "" {
			t.Errorf("the probe offered a directory as the binary: %q", got)
		}
	})
}

func TestShellArgvOpensAnInteractiveLogin(t *testing.T) {
	// This is what every plain SSH pane runs, which is the default and so the
	// path most people are on.
	argv := New("bot", "default").ShellArgv()

	if len(argv) == 0 || argv[0] != "ssh" {
		t.Fatalf("ShellArgv = %v, want it to start with ssh", argv)
	}
	joined := strings.Join(argv, " ")

	// A pty on the far side, or the shell there runs without a terminal and
	// behaves like a pipe: no prompt, no job control, no editor.
	if !strings.Contains(joined, "-tt") {
		t.Errorf("ShellArgv does not ask for a terminal: %v", argv)
	}
	// BatchMode=yes would refuse to ask for a passphrase, and the pane would
	// close instead of prompting. The polling calls set it; this must not.
	if strings.Contains(joined, "BatchMode=yes") {
		t.Errorf("an interactive shell cannot be in batch mode: %v", argv)
	}
	if !strings.Contains(joined, "BatchMode=no") {
		t.Errorf("ShellArgv should ask for interaction explicitly: %v", argv)
	}
	// The destination last, after "--", so it is never read as an option.
	if argv[len(argv)-1] != "bot" || argv[len(argv)-2] != "--" {
		t.Errorf("ShellArgv should end with -- bot: %v", argv)
	}
	// Nothing to run: the point is the login shell itself.
	if strings.Contains(joined, "herdr") {
		t.Errorf("a plain SSH pane should not need Herdr on the machine: %v", argv)
	}
}

func TestStartCommandDetachesAndCleansItsEnvironment(t *testing.T) {
	// This runs on the far machine to bring a Herdr session up. It is checked
	// by running it, because what matters about it is what a shell does with
	// it rather than what it says.
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh to run it in")
	}

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	fake := filepath.Join(dir, "herdr")
	// Stands in for Herdr: writes down how it was called, then lingers, as a
	// server would.
	script := "#!/bin/sh\n{ echo \"args=$*\"; echo \"session=${HERDR_SESSION-unset}\"; " +
		"echo \"socket=${HERDR_SOCKET_PATH-unset}\"; echo \"client=${HERDR_CLIENT_SOCKET_PATH-unset}\"; } > " +
		record + "\nsleep 5\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	command := New("bot", "work").startCommand(fake)

	cmd := exec.Command("/bin/sh", "-c", command)
	// Set the very variables the command is supposed to clear.
	cmd.Env = append(os.Environ(),
		"HERDR_SOCKET_PATH=/hub/herdr.sock",
		"HERDR_CLIENT_SOCKET_PATH=/hub/client.sock")

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the command failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		// ssh waits for the streams to close, so a background process holding
		// one open leaves the connection hanging until it exits, which for a
		// server is never.
		t.Fatal("the command did not return; something is still holding a stream")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the command took %s to return; it should not wait for the server", elapsed)
	}

	// It really did start, and detached rather than dying with its parent.
	var raw []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if raw, _ = os.ReadFile(record); len(raw) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := string(raw)
	if got == "" {
		t.Fatal("the server was never started")
	}

	for _, want := range []string{
		"args=server",
		"session=work",
		// SSH forwards this machine's sockets, and a Herdr started with those
		// set would talk back to the session here instead of its own.
		"socket=unset",
		"client=unset",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the server saw %q, want %q", strings.TrimSpace(got), want)
		}
	}
}

func TestStartCommandWithNoSessionNamed(t *testing.T) {
	// An unnamed session means Herdr's own default, so nothing should be set.
	command := New("bot", "").startCommand("/usr/bin/herdr")
	if strings.Contains(command, "HERDR_SESSION") {
		t.Errorf("command sets a session when none was named: %q", command)
	}
	// The clearing still happens: that is not about the session.
	for _, want := range []string{"-u HERDR_SOCKET_PATH", "-u HERDR_CLIENT_SOCKET_PATH", "nohup"} {
		if !strings.Contains(command, want) {
			t.Errorf("command %q should contain %q", command, want)
		}
	}
}

func TestARemoteRefusalKeepsItsCode(t *testing.T) {
	// Herdr on the machine signals a refusal the same way it does here: exiting
	// non-zero with the error envelope printed. Returning the exit status alone
	// threw away the code, so nothing could tell "that pane is already gone"
	// from "that went wrong" at the far end.
	envelope := []byte(`{"error":{"code":"pane_not_found","message":"pane w1:p2 not found"}}`)
	err := herdrcli.RunError(errors.New("exit status 1"), []string{"pane", "close", "w1:p2"}, envelope, nil)

	if !herdrcli.IsNotFound(err) {
		t.Errorf("%v does not carry the code", err)
	}
	// Wrapped with the machine's name, as Run does, and still recognisable.
	wrapped := fmt.Errorf("%s: %w", "bot", err)
	if !herdrcli.IsNotFound(wrapped) {
		t.Errorf("%v stopped being recognisable once named", wrapped)
	}
	if !strings.Contains(wrapped.Error(), "bot") {
		t.Errorf("error %q should name the machine", wrapped)
	}
}

func TestEverySSHCommandPutsTheDestinationAfterADash(t *testing.T) {
	// The README says a machine's name cannot be read as an option, because the
	// destination is passed after "--". That is true of SSHArgs, which nearly
	// every call goes through, and the tests around it hold that one.
	//
	// What nothing held is that every call goes through it. ssh takes -o on the
	// command line and -oProxyCommand=... runs a command, so a destination read
	// as an option is a command somebody else chose -- and a name does not have
	// to be typed to get here: connect falls back to whatever is selected in
	// the terminal.
	//
	// Read from the source because that is where a new call would appear. A
	// seventh one built by hand, without the separator, is not a test failing
	// anywhere else: it works perfectly until the day a name starts with a dash.
	raw, err := os.ReadFile("remote.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")

	found := 0
	for i, line := range lines {
		if !strings.Contains(line, `[]string{"ssh"`) {
			continue
		}
		found++
		// Either it hands off to SSHArgs, which ends with the separator, or it
		// carries one itself. Looked for close by: these are short functions,
		// and an argv assembled ten lines later is one worth reading anyway.
		safe := strings.Contains(line, `"--"`)
		for j := i + 1; j < len(lines) && j <= i+6 && !safe; j++ {
			if strings.Contains(lines[j], "SSHArgs(") || strings.Contains(lines[j], `"--"`) {
				safe = true
			}
		}
		if !safe {
			t.Errorf("remote.go:%d builds an ssh command with no \"--\" before the "+
				"destination and no SSHArgs to add one:\n\t%s", i+1, strings.TrimSpace(line))
		}
	}
	if found < 5 {
		t.Fatalf("found %d ssh commands in remote.go, which is fewer than there are; "+
			"this test is looking for the wrong shape", found)
	}

	// And the thing they all lean on actually does it, in both modes.
	for _, tty := range []bool{true, false} {
		args := New("bot", "").SSHArgs(tty)
		if len(args) < 2 || args[len(args)-2] != "--" || args[len(args)-1] != "bot" {
			t.Errorf("SSHArgs(%v) ends %v, want the destination after a \"--\"", tty, args[len(args)-2:])
		}
	}
}

func TestClosingUsesTheConnectionItIsClosing(t *testing.T) {
	// Closing a shared connection is `ssh -O exit` against the control socket
	// it was made on. Point it at a different path and ssh has nothing to
	// close: it reports no such socket, the error is discarded -- there is
	// nothing useful to do with it -- and the connection stays up until
	// ControlPersist runs out, holding a session open on a machine somebody
	// has disconnected from.
	//
	// So what matters is not that Close runs, but that it names the same
	// socket every other call to that machine uses.
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	client := NewWithBin("deploy@vm", "agents", "herdr")
	client.Close()

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("Close ran no ssh at all: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")

	// The control socket every other call to this machine uses.
	want := ""
	for i, arg := range client.SSHArgs(false) {
		if arg == "-o" && i+1 < len(client.SSHArgs(false)) {
			if path, found := strings.CutPrefix(client.SSHArgs(false)[i+1], "ControlPath="); found {
				want = path
			}
		}
	}
	if want == "" {
		t.Fatal("SSHArgs no longer names a ControlPath; this test is checking nothing")
	}

	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "ControlPath="+want) {
		t.Errorf("Close is closing %q and the connection is on %q", joined, want)
	}
	if !strings.Contains(joined, "-O exit") {
		t.Errorf("Close does not ask ssh to close anything: %v", got)
	}
	// And the destination is still an argument rather than an option.
	if at := strings.Index(joined, "--"); at < 0 || !strings.Contains(joined[at:], "deploy@vm") {
		t.Errorf("the machine is not passed after the separator: %v", got)
	}
}
