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
