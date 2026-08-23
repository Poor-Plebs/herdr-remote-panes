package remote

import (
	"os"
	"os/exec"
	"strings"
	"testing"
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
	// would open its own connection to the same machine.
	if NewWithBin("bot", "default", "").controlPath != NewWithBin("bot", "default", "").controlPath {
		t.Error("identical clients disagree on the control path")
	}
	// Different sessions must not, since the session is part of what the
	// connection is for.
	if NewWithBin("bot", "a", "").controlPath == NewWithBin("bot", "b", "").controlPath {
		t.Error("clients for different sessions share a control path")
	}
}
