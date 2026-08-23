package version

import (
	"strings"
	"testing"
)

func TestShortIsAlwaysSomethingPrintable(t *testing.T) {
	// Tests are built outside a checkout, so this exercises the fallback: it
	// must still return something rather than an empty column in the status.
	got := Short()
	if got == "" {
		t.Fatal("Short() is empty")
	}
	if strings.ContainsAny(got, " \n\t") {
		t.Errorf("Short() = %q, want a single printable token", got)
	}
	if len(got) > 20 {
		t.Errorf("Short() = %q, too long to sit in a status line", got)
	}
}

func TestShortIsStable(t *testing.T) {
	// It is read on every status, so it must not recompute or drift.
	if a, b := Short(), Short(); a != b {
		t.Errorf("Short() returned %q then %q", a, b)
	}
}

func TestStaleMessage(t *testing.T) {
	// Installing an update leaves the running daemon alone, so the new build
	// sits on disk while the old one keeps answering. Nothing said so, which
	// made it possible to watch an old build behave like an old build and
	// conclude the update had not worked.
	installed := Short()

	if got := StaleMessage(installed); got != "" {
		t.Errorf("a matching build should say nothing, got %q", got)
	}

	// Built outside a checkout there is nothing to compare, so it stays quiet
	// rather than warning on every status.
	if installed == "unknown" {
		for _, running := range []string{"", "427e2ad"} {
			if got := StaleMessage(running); got != "" {
				t.Errorf("StaleMessage(%q) = %q, want silence from an unknown build", running, got)
			}
		}
		return
	}

	got := StaleMessage("427e2ad")
	if !strings.Contains(got, "427e2ad") || !strings.Contains(got, installed) {
		t.Errorf("StaleMessage = %q, want it to name both builds", got)
	}
	if !strings.Contains(got, "restart") {
		t.Errorf("StaleMessage = %q, want it to say what to do", got)
	}

	if got := StaleMessage(""); !strings.Contains(got, "older build") {
		t.Errorf("a daemon too old to report a build should still be named: %q", got)
	}
}
