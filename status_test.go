package main

import (
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
)

// This is what somebody reads when they want to know whether the thing is
// working, and it had no tests at all.

func TestStatusColumnsFitWhatIsInThem(t *testing.T) {
	// The name field was fixed at twenty-two and the kind at nine, so the usual
	// case -- machines called bot, prod, ci -- put every state some twenty
	// columns from the machine it belongs to.
	hosts := []syncd.HostInfo{
		{Label: "bot", Connected: true, SSHOnly: true, Terminals: 3},
		{Label: "prod", Connected: true, Mirrors: 2},
		{Label: "ci", Connected: true, SSHOnly: true, Terminals: 12},
	}
	lines := statusLines(hosts)
	if len(lines) != len(hosts) {
		t.Fatalf("got %d lines for %d machines", len(lines), len(hosts))
	}
	for _, line := range lines {
		if width := text.Width(line); width > 40 {
			t.Errorf("a line for three short names is %d columns wide: %q", width, line)
		}
	}

	// The states line up under one another, or they are not columns.
	var at []int
	for _, line := range lines {
		i := strings.LastIndex(line, "ok")
		if i < 0 {
			t.Fatalf("no state in %q", line)
		}
		at = append(at, text.Width(line[:i]))
	}
	for _, position := range at[1:] {
		if position != at[0] {
			t.Errorf("states start at %v, want them all equal", at)
			break
		}
	}
}

func TestStatusSaysHowAMachineIsReachedEvenWhenItIsNot(t *testing.T) {
	// The kind is how you know which way the m key would toggle, so it is worth
	// saying for a machine that is not answering. The count is not: "0
	// mirrored" reads as a tally, and a machine that cannot be reached has
	// nothing to tally.
	hosts := []syncd.HostInfo{
		{Label: "staging", GaveUp: true, LastError: "host key changed"},
	}
	line := statusLines(hosts)[0]

	if !strings.Contains(line, "mirrored") {
		t.Errorf("%q does not say how the machine is reached", line)
	}
	if strings.Contains(line, "0 mirrored") {
		t.Errorf("%q counts terminals on a machine that is not answering", line)
	}
	if !strings.Contains(line, "unreachable") || !strings.Contains(line, "host key changed") {
		t.Errorf("%q does not say what is wrong", line)
	}
}

func TestStatusCountsAreRightAligned(t *testing.T) {
	// A two-digit count must not shift the column after it, or the kinds stop
	// lining up exactly when there is most to read.
	hosts := []syncd.HostInfo{
		{Label: "aaa", Connected: true, SSHOnly: true, Terminals: 9},
		{Label: "bbb", Connected: true, SSHOnly: true, Terminals: 100},
	}
	var at []int
	for _, line := range statusLines(hosts) {
		i := strings.Index(line, "ssh")
		if i < 0 {
			t.Fatalf("no kind in %q", line)
		}
		at = append(at, text.Width(line[:i]))
	}
	if at[0] != at[1] {
		t.Errorf("a longer count moved the kind column: %v", at)
	}
}

func TestStatusMakesALabelSafeToPrint(t *testing.T) {
	// Labels are whatever the user wrote in the config, and this goes straight
	// to a terminal.
	hosts := []syncd.HostInfo{
		{Label: "bot\x1b[31m\nrest", Connected: true, SSHOnly: true, Terminals: 1},
	}
	line := statusLines(hosts)[0]
	if strings.ContainsAny(line, "\n\r") || strings.ContainsRune(line, 0x1b) {
		t.Errorf("%q carries something that steers the terminal", line)
	}
}

func TestStatusOfNothingIsNoLines(t *testing.T) {
	if got := statusLines(nil); len(got) != 0 {
		t.Errorf("statusLines(nil) = %q, want nothing", got)
	}
}

func TestStatusSaysWhenATerminalCouldNotBeMirrored(t *testing.T) {
	// A terminal that fails to mirror often enough is given up on. It is still
	// open on the machine, and without saying so the listing simply reads
	// lower than what is over there -- somebody counting tabs finds one
	// missing and nothing anywhere explaining it.
	line := statusLines([]syncd.HostInfo{
		{Label: "bot", Connected: true, Mirrors: 2, Unmirrored: 1},
	})[0]

	if !strings.Contains(line, "could not be mirrored") {
		t.Errorf("%q does not say a terminal was given up on", line)
	}
	if !strings.Contains(line, "connect again") {
		t.Errorf("%q does not say how to try again", line)
	}
	// The count of what is mirrored is still what is mirrored.
	if !strings.Contains(line, "2 mirrored") {
		t.Errorf("%q lost the count of what is working", line)
	}
}

func TestAMachineThatCannotBeReachedSaysThatFirst(t *testing.T) {
	// A machine with terminals it could not mirror and no connection at all
	// has a better answer than the first of those.
	line := statusLines([]syncd.HostInfo{{
		Label: "bot", Connected: false, GaveUp: true, Unmirrored: 2,
		LastError: "host key changed — verify it, then update ~/.ssh/known_hosts",
	}})[0]

	if !strings.Contains(line, "host key changed") {
		t.Errorf("%q does not say what is actually wrong", line)
	}
	if strings.Contains(line, "could not be mirrored") {
		t.Errorf("%q leads with a detail instead of the machine being unreachable", line)
	}
}
