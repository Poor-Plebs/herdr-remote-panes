package cli

import (
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
)

func TestStatusSummarySaysWhatEachMachineIsDoing(t *testing.T) {
	// The notification used to begin with "mirroring" and list every machine
	// with a count, which described a machine on a plain SSH terminal -- the
	// default, and most of them -- as doing something it was not.
	got := statusSummary([]syncd.HostInfo{
		{Label: "bot", Connected: true, SSHOnly: true, Terminals: 2},
		{Label: "ci", Connected: true, Mirrors: 3},
		{Label: "prod", GaveUp: true},
		{Label: "staging", Connected: false},
	})

	for _, want := range []string{"bot 2 open", "ci 3 mirrored", "prod unreachable", "staging unreachable"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q should contain %q", got, want)
		}
	}
	// A machine on plain SSH is never described as mirroring.
	if strings.Contains(got, "bot 2 mirrored") {
		t.Errorf("summary %q calls a plain SSH machine mirrored", got)
	}
	// Short enough for a notification.
	if len(got) > 200 {
		t.Errorf("summary is %d characters: %q", len(got), got)
	}
}

func TestStatusSummaryWithNothingConnected(t *testing.T) {
	if got := statusSummary(nil); !strings.Contains(got, "no machines") {
		t.Errorf("summary = %q, want it to say nothing is connected", got)
	}
}
