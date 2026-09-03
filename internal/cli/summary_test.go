package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
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

func TestASummaryTooLongToReadNamesWhatIsWrong(t *testing.T) {
	// This becomes a desktop notification, drawn by something else and cut
	// wherever that decides. The order is the config file's, which puts the
	// machines that are fine in front of the ones that are not -- so with
	// enough machines what survived the cut was "a 1 mirrored · b 1 mirrored"
	// and the part that told somebody something was the part that went.
	many := func(n, bad int) []syncd.HostInfo {
		out := make([]syncd.HostInfo, 0, n)
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("machine%02d", i)
			if i >= n-bad {
				out = append(out, syncd.HostInfo{Label: name, GaveUp: true})
				continue
			}
			out = append(out, syncd.HostInfo{Label: name, Connected: true, Mirrors: 1})
		}
		return out
	}

	// Short enough to read: every machine is named, which is worth more than a
	// count when there is room for it.
	few := statusSummary(many(2, 1))
	if !strings.Contains(few, "machine00") || !strings.Contains(few, "machine01") {
		t.Errorf("a summary with room to name two machines does not: %q", few)
	}

	long := statusSummary(many(20, 3))
	if w := text.Width(long); w > maxSummary {
		t.Errorf("a summary of twenty machines is %d columns: %q", w, long)
	}
	// What is wrong is named; what is working is counted. A machine doing its
	// job needs no name in a line this size.
	for _, want := range []string{"machine17", "machine18", "machine19", "17 connected"} {
		if !strings.Contains(long, want) {
			t.Errorf("the summary does not say %q: %q", want, long)
		}
	}
	if strings.Contains(long, "machine00") {
		t.Errorf("a working machine is named at the cost of a failing one: %q", long)
	}

	// Enough of them unreachable at once and naming them all runs past the
	// bound too, which has to be cut rather than sent whole.
	if w := text.Width(statusSummary(many(40, 40))); w > maxSummary {
		t.Errorf("a summary of forty unreachable machines is %d columns", w)
	}

	// A label is whatever somebody wrote in their config, and this one goes
	// out as a notification rather than to a terminal -- but it is the same
	// text, and the named half is the half that was not being sanitized.
	nasty := statusSummary(append(many(20, 0),
		syncd.HostInfo{Label: "bot\x1b[2J", GaveUp: true}))
	if strings.ContainsAny(nasty, "\x1b") {
		t.Errorf("a label carries an escape into the notification: %q", nasty)
	}
}

func TestOneMachineIsNotCountedInThePlural(t *testing.T) {
	// The counted line is what is left when the named one is too wide, and one
	// machine gets there on its own: a label is whatever somebody wrote in
	// their config, and nothing bounds its length, so a single machine with a
	// long enough name overruns the line by itself.
	//
	// The fixture says so rather than assuming it. A label that happened to
	// fit would take the named branch and this would pass while checking
	// nothing, which is how the boundary above went untested until a sweep
	// found it.
	long := strings.Repeat("workbox-", 16)
	if w := text.Width(long); w <= maxSummary {
		t.Fatalf("the fixture label is %d columns and fits inside %d, so it never "+
			"reaches the counted line this is about", w, maxSummary)
	}

	got := statusSummary([]syncd.HostInfo{{Label: long, Connected: true, Mirrors: 3}})
	if strings.Contains(got, "1 machines") {
		t.Errorf("one machine is reported in the plural, in a desktop notification: %q", got)
	}

	// And the plural is still the plural: a fix that reads the count wrongly
	// in the other direction is the same defect facing the other way.
	two := statusSummary([]syncd.HostInfo{
		{Label: long, Connected: true, Mirrors: 3},
		{Label: long, Connected: true, Mirrors: 2},
	})
	if !strings.Contains(two, "2 machines") {
		t.Errorf("two machines are not reported in the plural: %q", two)
	}
}

func TestTheSummaryIsDecidedAtTheBoundNotPastIt(t *testing.T) {
	// Three things in this function survived a mutation sweep, all of them
	// boundaries the tests approached and never landed on.

	// A line exactly as wide as it is allowed to be is a line that fits. Built
	// to the column rather than guessed at: anything shorter would pass
	// against "less than" as well, which is the mutation this is here for.
	const tail = " 1 mirrored"
	label := strings.Repeat("a", maxSummary-len(tail))
	exact := statusSummary([]syncd.HostInfo{{Label: label, Connected: true, Mirrors: 1}})
	if w := text.Width(exact); w != maxSummary {
		t.Fatalf("the fixture is %d columns, not the %d this is about", w, maxSummary)
	}
	if !strings.Contains(exact, label) {
		t.Errorf("a summary that fits exactly was shortened anyway: %q", exact)
	}

	// A machine that has failed without being given up on yet is not working.
	// Counting it as such is what "and" instead of "or" does here, and every
	// fixture that had one also had it given up on, so the two agreed.
	failing := make([]syncd.HostInfo, 0, 20)
	for i := 0; i < 19; i++ {
		failing = append(failing, syncd.HostInfo{
			Label: fmt.Sprintf("machine%02d", i), Connected: true, Mirrors: 1})
	}
	failing = append(failing, syncd.HostInfo{Label: "trying", LastError: "connection refused"})
	got := statusSummary(failing)
	if !strings.Contains(got, "trying") {
		t.Errorf("a machine that is failing but not given up on is counted as working: %q", got)
	}
	if !strings.Contains(got, "19 connected") {
		t.Errorf("the count of working machines includes one that is not: %q", got)
	}

	// And with nothing working there is no count to give. "0 connected" is a
	// clause that costs room and says only that the rest of the line is all of
	// it.
	none := make([]syncd.HostInfo, 0, 20)
	for i := 0; i < 20; i++ {
		none = append(none, syncd.HostInfo{Label: fmt.Sprintf("machine%02d", i), GaveUp: true})
	}
	if all := statusSummary(none); strings.Contains(all, "0 connected") {
		t.Errorf("a summary with nothing working counts the nothing: %q", all)
	}
}
