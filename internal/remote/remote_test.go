package remote

import (
	"strings"
	"testing"
)

func TestRemoteCommandClearsSocketOverrides(t *testing.T) {
	// HERDR_SOCKET_PATH outranks HERDR_SESSION when Herdr resolves which
	// server to talk to, so it must be cleared before the remote invocation.
	got := strings.Join(New("workbox", "agents").Argv(false, "pane", "list"), " ")
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
