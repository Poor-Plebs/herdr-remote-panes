package cli

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/project"
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
	lines := statusLines(hosts, 0)
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
	//
	// Mirroring is set here rather than left off, which is what this asked for
	// all along: it comes from the config rather than from the connection, so
	// it is the machine's setting and reads the same whether or not the
	// machine ever answered. Left off, the fixture was a machine set to plain
	// ssh, and the assertion below held only because the column defaulted to
	// "mirrored" for everything that was down. Which kind is shown for which
	// setting is TestAMachineThatIsDownIsStillReportedAsWhatItIsSetTo.
	hosts := []syncd.HostInfo{
		{Label: "staging", GaveUp: true, Mirroring: true, LastError: "host key changed"},
	}
	line := statusLines(hosts, 0)[0]

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
	for _, line := range statusLines(hosts, 0) {
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
	line := statusLines(hosts, 0)[0]
	if strings.ContainsAny(line, "\n\r") || strings.ContainsRune(line, 0x1b) {
		t.Errorf("%q carries something that steers the terminal", line)
	}
}

func TestStatusOfNothingIsNoLines(t *testing.T) {
	if got := statusLines(nil, 0); len(got) != 0 {
		t.Errorf("statusLines(nil, 0) = %q, want nothing", got)
	}
}

func TestStatusSaysWhenATerminalCouldNotBeMirrored(t *testing.T) {
	// A terminal that fails to mirror often enough is given up on. It is still
	// open on the machine, and without saying so the listing simply reads
	// lower than what is over there -- somebody counting tabs finds one
	// missing and nothing anywhere explaining it.
	line := statusLines([]syncd.HostInfo{
		{Label: "bot", Connected: true, Mirrors: 2, Unmirrored: 1},
	}, 0)[0]

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
	}}, 0)[0]

	if !strings.Contains(line, "host key changed") {
		t.Errorf("%q does not say what is actually wrong", line)
	}
	if strings.Contains(line, "could not be mirrored") {
		t.Errorf("%q leads with a detail instead of the machine being unreachable", line)
	}
}

func TestAMachinesFailureCannotRepaintTheStatusLine(t *testing.T) {
	// This text is whatever the far side said. ssh passes a remote banner
	// through untouched, so a machine can put an escape sequence in it, and
	// status wrote it to the terminal as it arrived -- the name beside it had
	// been made safe to draw for years while this had not.
	lines := statusLines([]syncd.HostInfo{{
		Label:     "bot",
		LastError: "banner\x1b[2K\rrefused\nand a second line",
	}}, 0)
	if len(lines) != 1 {
		t.Fatalf("want one line, got %d: %v", len(lines), lines)
	}
	for _, forbidden := range []string{"\x1b", "\r", "\n"} {
		if strings.Contains(lines[0], forbidden) {
			t.Errorf("the line still carries %q: %q", forbidden, lines[0])
		}
	}
	// Still says what happened, so the sanitising has not eaten the answer.
	if !strings.Contains(lines[0], "refused") {
		t.Errorf("the reason is gone: %q", lines[0])
	}
}

func TestAMachineStillBeingRetriedHasNothingToCountEither(t *testing.T) {
	// Between "connected" and "given up" there is a machine whose last attempt
	// failed and which is still being retried. It has no terminals here, and
	// writing "0" beside it reads as a tally of what is open rather than as the
	// mode -- the same reason a machine that has been given up on shows none.
	//
	// The machine that has been given up on cannot tell the two apart: it has
	// neither flag set the way this one does.
	line := statusLines([]syncd.HostInfo{
		{Label: "staging", SSHOnly: true, LastError: "connection refused"},
	}, 0)[0]

	if strings.Contains(line, "0") {
		t.Errorf("%q counts terminals on a machine that is not connected", line)
	}
	// The kind still, because that is how you know which way m would toggle.
	if !strings.Contains(line, "ssh") {
		t.Errorf("%q does not say how the machine is reached", line)
	}
	if !strings.Contains(line, "connection refused") {
		t.Errorf("%q does not say what is wrong", line)
	}
}

func TestALongFailureCarriesOnUnderItsColumn(t *testing.T) {
	// A failure can run past a hundred characters. Left to the terminal it
	// breaks mid-word at whatever column the window happens to end at, and the
	// rest starts hard against the left margin — where a machine's name goes,
	// so the tail of one machine's trouble reads as another machine's row.
	hosts := []syncd.HostInfo{
		{Label: "bot", Connected: true, SSHOnly: true, Terminals: 1},
		{Label: "prod", GaveUp: true, SSHOnly: true,
			LastError: "host key changed — verify it, then update ~/.ssh/known_hosts"},
	}
	const width = 80
	lines := statusLines(hosts, width)

	for _, line := range lines {
		if got := text.Width(line); got > width {
			t.Errorf("a line is %d wide against a terminal of %d: %q", got, width, line)
		}
	}
	if len(lines) < 3 {
		t.Fatalf("the long failure was not carried on: %q", lines)
	}

	// The carried-on part starts under the state, not under the name.
	first := lines[1]
	stateAt := strings.Index(first, "unreachable")
	if stateAt < 0 {
		t.Fatalf("no state in %q", first)
	}
	for _, line := range lines[2:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if indent := len(line) - len(strings.TrimLeft(line, " ")); indent != stateAt {
			t.Errorf("a carried-on line starts at column %d, not under the state at %d: %q",
				indent, stateAt, line)
		}
	}

	// And nothing is lost in the carrying.
	joined := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	if !strings.Contains(joined, "update ~/.ssh/known_hosts") {
		t.Errorf("the end of the message went missing: %q", joined)
	}
}

func TestWithNoTerminalTheLineIsLeftWhole(t *testing.T) {
	// Run as a plugin action the output goes to the log, and piped it goes to
	// whatever is reading. Both want the line as it is: breaking it into
	// columns nobody is looking at helps no one and makes it harder to grep.
	hosts := []syncd.HostInfo{
		{Label: "prod", GaveUp: true, SSHOnly: true,
			LastError: "host key changed — verify it, then update ~/.ssh/known_hosts"},
	}
	if lines := statusLines(hosts, 0); len(lines) != 1 {
		t.Errorf("with no width the line was broken into %d: %q", len(lines), lines)
	}
}

func TestAnAbsurdlyNarrowTerminalIsLeftAlone(t *testing.T) {
	// Wrapping into a column a few characters wide produces a paragraph one
	// word per line, which is worse than a line that runs off the edge.
	hosts := []syncd.HostInfo{
		{Label: "prod", GaveUp: true, SSHOnly: true,
			LastError: "host key changed — verify it, then update ~/.ssh/known_hosts"},
	}
	for _, width := range []int{1, 10, 25, 30} {
		if lines := statusLines(hosts, width); len(lines) != 1 {
			t.Errorf("at %d columns the line was broken into %d: %q", width, len(lines), lines)
		}
	}
}

func TestALineThatExactlyFitsIsLeftAlone(t *testing.T) {
	// The bound is what a line may reach, not what it must stay under. Wrapping
	// one character early costs a second line for nothing, and does it at
	// whatever width somebody's terminal happens to be.
	hosts := []syncd.HostInfo{
		{Label: "prod", GaveUp: true, SSHOnly: true,
			LastError: "host key changed — verify it, then update ~/.ssh/known_hosts"},
	}
	whole := statusLines(hosts, 0)
	if len(whole) != 1 {
		t.Fatalf("with no bound the line was split: %q", whole)
	}
	exact := text.Width(whole[0])

	if got := statusLines(hosts, exact); len(got) != 1 {
		t.Errorf("a line of %d in a terminal of %d was split into %d: %q",
			exact, exact, len(got), got)
	}
	// One column narrower and it has to give.
	if got := statusLines(hosts, exact-1); len(got) < 2 {
		t.Errorf("a line of %d in a terminal of %d was left to run off the edge: %q",
			exact, exact-1, got)
	}
}

func TestAMachineThatCouldNotMirrorSaysSo(t *testing.T) {
	// Mirroring falls back to plain SSH when the machine turns out not to have
	// Herdr, which is right — the machine still works. What was missing is
	// anybody being told: the menu reads the setting and says mirroring is on,
	// status reads the machine and says ssh, and the reason was in the daemon's
	// log where nobody was looking.
	line := statusLines([]syncd.HostInfo{
		{Label: "bot", Connected: true, SSHOnly: true, NoHerdr: true, Terminals: 2},
	}, 0)[0]

	if !strings.Contains(line, "no herdr") {
		t.Errorf("%q does not say why the machine is not mirroring", line)
	}
	// And what to do about it, because there usually is something: herdr is
	// often installed off the PATH an SSH session gets.
	if !strings.Contains(line, "herdr_bin") {
		t.Errorf("%q does not say what would fix it", line)
	}

	// A machine that was never asked to mirror says nothing of the kind.
	plain := statusLines([]syncd.HostInfo{
		{Label: "ci", Connected: true, SSHOnly: true, Terminals: 1},
	}, 0)[0]
	if strings.Contains(plain, "no herdr") {
		t.Errorf("a plain SSH machine was told it could not mirror: %q", plain)
	}

	// And a machine that cannot be reached at all has something worse to say.
	worse := statusLines([]syncd.HostInfo{
		{Label: "prod", SSHOnly: true, NoHerdr: true, GaveUp: true, LastError: "connection refused"},
	}, 0)[0]
	if !strings.Contains(worse, "connection refused") {
		t.Errorf("%q lost the failure that matters more", worse)
	}
}

func TestAMachineAtTheMirrorLimitSaysSo(t *testing.T) {
	// A machine with more terminals than max_mirrors allows simply does not
	// mirror the rest. From here that looks like a machine with fewer terminals
	// than it has, and the only word about it was in the daemon's log.
	//
	// It is a different thing from the count beside it, and says so: those were
	// tried and failed, and connecting again may work, while these were never
	// tried and will not be until the number changes.
	line := statusLines([]syncd.HostInfo{
		{Label: "bot", Connected: true, Mirrors: 32, AtCapacity: true},
	}, 0)[0]

	if !strings.Contains(line, "limit") {
		t.Errorf("%q does not say the machine is at its limit", line)
	}
	if !strings.Contains(line, "max_mirrors") {
		t.Errorf("%q does not say which setting decides it", line)
	}
	if strings.Contains(line, "retry") {
		t.Errorf("%q offers a retry, which cannot help against a limit: %s", line, line)
	}

	// A machine under the limit says nothing of the kind.
	under := statusLines([]syncd.HostInfo{
		{Label: "ci", Connected: true, Mirrors: 3},
	}, 0)[0]
	if strings.Contains(under, "limit") {
		t.Errorf("a machine under the limit was told it had reached one: %q", under)
	}

	// And a machine that cannot be reached still leads with that.
	worse := statusLines([]syncd.HostInfo{
		{Label: "prod", AtCapacity: true, GaveUp: true, LastError: "connection refused"},
	}, 0)[0]
	if !strings.Contains(worse, "connection refused") {
		t.Errorf("%q lost the failure that matters more", worse)
	}
}

func TestAMachineWithTwoSpacesOfOneNameSaysSo(t *testing.T) {
	// The state is the most confusing one there is: two people both "in
	// pairing" on the same machine, in different spaces, seeing none of each
	// other's terminals and neither of them wrong.
	line := statusLines([]syncd.HostInfo{
		{Label: "bot", Connected: true, Mirrors: 2, SharedName: true},
	}, 0)[0]

	if !strings.Contains(line, "more than one space") {
		t.Errorf("%q does not say what is ambiguous", line)
	}
	if !strings.Contains(line, "remote_workspace_format") {
		t.Errorf("%q does not say what would settle it", line)
	}

	// And an ordinary machine says nothing of the kind.
	plain := statusLines([]syncd.HostInfo{{Label: "ci", Connected: true, Mirrors: 1}}, 0)[0]
	if strings.Contains(plain, "more than one space") {
		t.Errorf("an ordinary machine was told its name is ambiguous: %q", plain)
	}
}

func TestTheWorstQuietFailureIsTheOneReported(t *testing.T) {
	// Four different ways a machine can be quietly doing less than it was told
	// to, and one column to say it in. They can hold at once -- a machine over
	// its mirror limit can also have terminals that failed to mirror, and a
	// machine whose name is ambiguous can be either -- so which one wins is a
	// real decision, and it was made only by the order the code happens to be
	// written in.
	//
	// The order is most-wrong first: the ones that mean no mirroring at all
	// before the ones that mean some of it. Nothing is lost by picking one, as
	// the daemon's log still holds them all; but the line is what people read.
	says := map[string]string{
		"no herdr":     "no herdr found",
		"shared name":  "more than one space",
		"at the limit": "at the mirror limit",
		"unmirrored":   "could not be mirrored",
	}

	for _, c := range []struct {
		name   string
		host   syncd.HostInfo
		expect string
	}{
		{
			"all four at once",
			syncd.HostInfo{NoHerdr: true, SharedName: true, AtCapacity: true, Unmirrored: 3},
			"no herdr",
		},
		{
			"an ambiguous name outranks a limit",
			syncd.HostInfo{SharedName: true, AtCapacity: true, Unmirrored: 3},
			"shared name",
		},
		{
			"a limit outranks what failed under it",
			syncd.HostInfo{AtCapacity: true, Unmirrored: 3},
			"at the limit",
		},
		{
			"and on its own, what failed",
			syncd.HostInfo{Unmirrored: 3},
			"unmirrored",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.host.Label = "bot"
			c.host.Connected = true
			line := statusLines([]syncd.HostInfo{c.host}, 0)[0]

			if !strings.Contains(line, says[c.expect]) {
				t.Errorf("%q does not report %s, which is the worst of what is wrong", line, c.expect)
			}
			// One state, not four run together. The column is a phrase wide,
			// and a line carrying every complaint at once says none of them.
			for name, phrase := range says {
				if name != c.expect && strings.Contains(line, phrase) {
					t.Errorf("%q reports %s as well as %s", line, name, c.expect)
				}
			}
		})
	}
}

func TestWrappingStartsAsSoonAsThereIsRoomToWrapInto(t *testing.T) {
	// The narrow case is held from one side by the test above: too narrow, and
	// the line is left whole. Held only from that side, the threshold could be
	// raised to any number at all and every test would still pass -- and a
	// threshold nobody can raise too far is the whole point of having one.
	//
	// So: walk the widths from one up to the whole line, find where wrapping
	// begins, and check it begins as soon as the column it wraps into is worth
	// having rather than somewhere well past that.
	hosts := []syncd.HostInfo{
		{Label: "prod", GaveUp: true, SSHOnly: true,
			LastError: "host key changed — verify it, then update ~/.ssh/known_hosts"},
	}
	whole := statusLines(hosts, 0)[0]
	full := text.Width(whole)

	// The columns before the state, which is what the state has to wrap under.
	// Everything up to and including the run of spaces after the kind -- so the
	// start of the state itself, which for this host is what it says about
	// being unreachable, not the ssh message carried inside it.
	indent := strings.Index(whole, "unreachable")
	if indent <= 0 {
		t.Fatalf("could not find where the state begins in %q", whole)
	}

	first := 0
	for w := 1; w < full; w++ {
		if len(statusLines(hosts, w)) > 1 {
			first = w
			break
		}
	}
	if first == 0 {
		t.Fatalf("no width below %d wrapped at all", full)
	}

	// Deliberately not written against minWrapColumn. A test that measures the
	// threshold against the threshold cannot fail: raise the constant and the
	// expectation rises with it, which is how the first draft of this passed
	// happily at twice the value. The numbers here say what the wrapping is
	// for, in columns, and the constant has to keep up with them.
	room := first - indent
	if room < 15 {
		t.Errorf("wrapping began with only %d columns to wrap into, which is a word per line", room)
	}
	if room > 25 {
		t.Errorf("wrapping did not begin until there were %d columns to wrap into, so terminals wide enough to wrap were left running off the edge", room)
	}
}

func TestEveryStateAMachineCanBeInIsInTheREADME(t *testing.T) {
	// The status line is where somebody meets these, and the README is where
	// they look them up. Nothing connects the two, so a state added to one is
	// not added to the other -- and three of these went in without an entry
	// between them.
	//
	// Matched on a phrase rather than the whole line: the line carries a
	// machine's name and a count, and the README quotes the part that is the
	// same for everyone.
	prose := strings.Join(strings.Fields(docsText(t)), " ")

	for _, tt := range []struct {
		host   syncd.HostInfo
		phrase string
	}{
		{syncd.HostInfo{NoHerdr: true}, "no herdr found on the machine"},
		{syncd.HostInfo{SharedName: true}, "more than one space"},
		{syncd.HostInfo{AtCapacity: true}, "at the mirror limit"},
		{syncd.HostInfo{OutsideShared: 3}, "other spaces on the machine"},
		{syncd.HostInfo{Unmirrored: 2}, "could not be mirrored"},
		{syncd.HostInfo{GaveUp: true, LastError: "connection refused"}, "unreachable, not retrying"},
	} {
		tt.host.Label = "bot"
		tt.host.Connected = !tt.host.GaveUp
		line := statusLines([]syncd.HostInfo{tt.host}, 0)[0]

		// The phrase is what the code actually says, not what this hopes it
		// says: a test naming a phrase neither of them uses would pass the
		// README check by never looking.
		if !strings.Contains(line, tt.phrase) {
			t.Errorf("the status line %q does not contain %q, so this test is "+
				"holding the README to something nothing says", line, tt.phrase)
			continue
		}
		if !strings.Contains(prose, tt.phrase) {
			t.Errorf("a machine can say %q and the README never mentions it, "+
				"which is where somebody goes to find out what it means", tt.phrase)
		}
	}
}

// docsText is every page of documentation joined together: the README and the
// pages under docs/. These tests are about something the documentation shows
// agreeing with what the code does, and which page shows it is a decision that
// has already changed twice -- the troubleshooting and contributor sections
// both moved out of the README. Reading the set rather than one file keeps the
// check about the agreement rather than about where the prose currently sits.
func docsText(t *testing.T) string {
	t.Helper()
	text, err := project.DocsText()
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestTheREADMEQuotesAStatusLineTheCodeWouldPrint(t *testing.T) {
	// The README shows a status line for a machine that was asked to mirror and
	// could not. Shown rather than described, so somebody can match what is on
	// their screen against it -- which only works if it is what would be on
	// their screen, down to the spaces that put the columns where they are.
	//
	// The line drifts if the message is reworded or the columns move, and
	// neither of those is a change anybody would think to check a README for.
	want := "  bot  2 ssh  mirroring off: no herdr found on the machine — " +
		"set herdr_bin if it is installed elsewhere there"

	if !strings.Contains(docsText(t), want) {
		t.Errorf("the README does not show this line, which is what status prints "+
			"for a machine that could not mirror:\n%s", want)
	}

	// The machine the README's text is about: two terminals over plain SSH,
	// because the Herdr it needed was not found there.
	got := statusLines([]syncd.HostInfo{{
		Label: "bot", Connected: true, SSHOnly: true, Terminals: 2, NoHerdr: true,
	}}, 0)
	if len(got) != 1 || got[0] != want {
		t.Errorf("status prints\n%q\nand the README shows\n%q", got, want)
	}
}

func TestWithNoMachinesTheAnswerSaysWhatToDo(t *testing.T) {
	// The state somebody meets first: the plugin is installed, nothing is
	// connected yet, and they have asked it what it is doing. An answer that
	// only restates the question leaves them looking for the next step.
	summary := statusSummary(nil)

	if !strings.Contains(summary, "menu") {
		t.Errorf("with nothing connected the answer is %q, which does not say "+
			"where to go next", summary)
	}
	// "hosts" is what the config file calls them. Everything somebody reads
	// calls them machines, and this said the other thing on its own.
	if strings.Contains(summary, "host") {
		t.Errorf("the answer is %q; user-facing text says machines, not hosts", summary)
	}

	// And with machines, it is about them rather than about the menu.
	busy := statusSummary([]syncd.HostInfo{
		{Label: "bot", Connected: true, Mirrors: 2},
		{Label: "prod", GaveUp: true},
	})
	for _, want := range []string{"bot", "prod"} {
		if !strings.Contains(busy, want) {
			t.Errorf("the summary %q does not mention %s", busy, want)
		}
	}
	if strings.Contains(busy, "open the menu") {
		t.Errorf("the summary %q tells somebody to go and connect something "+
			"while listing what is already connected", busy)
	}
}

func TestStatusSaysWhatTheScopeIsLeavingOut(t *testing.T) {
	// The default scope mirrors the space this plugin made on the machine and
	// nothing else. Somebody who already had terminals open there turns
	// mirroring on and gets one — the setting doing exactly what it says, and
	// from here indistinguishable from terminals that failed to arrive.
	lines := statusLines([]syncd.HostInfo{
		{Target: "bot", Label: "bot", Connected: true, Mirrors: 1, OutsideShared: 3},
	}, 0)
	if len(lines) != 1 {
		t.Fatalf("want one line, got %d: %v", len(lines), lines)
	}
	for _, want := range []string{"3 more", "other spaces", "scope"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("the line does not mention %q: %q", want, lines[0])
		}
	}
	// It is not a failure and must not read as one: the machine is working,
	// and connecting again would change nothing.
	for _, wrong := range []string{"error", "unreachable", "could not"} {
		if strings.Contains(lines[0], wrong) {
			t.Errorf("a machine doing what it was told reads as broken: %q", lines[0])
		}
	}
}

func TestAMachineWithNothingLeftOutSaysNothing(t *testing.T) {
	// Scope "all", or a machine whose terminals are all in the shared space.
	// A line about none of them would appear on every machine forever.
	lines := statusLines([]syncd.HostInfo{
		{Target: "bot", Label: "bot", Connected: true, Mirrors: 4},
	}, 0)
	if len(lines) != 1 {
		t.Fatalf("want one line, got %d: %v", len(lines), lines)
	}
	if strings.Contains(lines[0], "scope") {
		t.Errorf("a machine with nothing left out mentions scope: %q", lines[0])
	}
}

func TestEveryThingAMachineCanReportIsAccountedFor(t *testing.T) {
	// The test above holds every state a machine can be in to a README entry,
	// and names them by hand. I added one today and had to remember to put it
	// in that list; the next person will not, and a state with no entry is a
	// line somebody meets with nowhere to look it up.
	//
	// So this asks the type. Every field a machine reports is either something
	// that changes what the status line says -- in which case it belongs in
	// that list -- or part of naming and counting, which the line is made of
	// rather than about.
	saysSomething := map[string]bool{
		"NoHerdr": true, "AtCapacity": true, "SharedName": true,
		"OutsideShared": true, "Unmirrored": true, "GaveUp": true,
	}
	partOfTheLine := map[string]bool{
		"Target": true, "Label": true, "Connected": true, "Mirrors": true,
		"SSHOnly": true, "Terminals": true, "Mirroring": true, "LastError": true,
	}

	shape := reflect.TypeOf(syncd.HostInfo{})
	if shape.NumField() < 14 {
		t.Fatalf("HostInfo has %d fields, which is fewer than it had -- this test "+
			"has stopped looking at the type", shape.NumField())
	}
	for i := 0; i < shape.NumField(); i++ {
		name := shape.Field(i).Name
		if !saysSomething[name] && !partOfTheLine[name] {
			t.Errorf("HostInfo.%s is something a machine reports and nothing here says "+
				"whether it changes the status line. If it does, it needs a phrase in "+
				"TestEveryStateAMachineCanBeInIsInTheREADME and an entry in the README "+
				"to go with it", name)
		}
	}

	// And everything called a state really does change the line, so the list
	// above cannot become a place to put a name and forget it.
	plain := statusLines([]syncd.HostInfo{{Label: "bot", Connected: true}}, 0)[0]
	for name := range saysSomething {
		host := syncd.HostInfo{Label: "bot", Connected: true}
		field := reflect.ValueOf(&host).Elem().FieldByName(name)
		switch field.Kind() {
		case reflect.Bool:
			field.SetBool(true)
		case reflect.Int:
			field.SetInt(2)
		default:
			t.Fatalf("HostInfo.%s is a %s, which this does not know how to set", name, field.Kind())
		}
		if got := statusLines([]syncd.HostInfo{host}, 0)[0]; got == plain {
			t.Errorf("HostInfo.%s is listed as changing the status line and does not: %q",
				name, got)
		}
	}
}

func TestAMachineThatIsDownIsStillReportedAsWhatItIsSetTo(t *testing.T) {
	// The kind column said "mirrored" for every machine that was not
	// connected, because the field it read -- SSHOnly -- records what happened
	// when the connection was made, and on a machine that never connected
	// nothing has happened. So a machine deliberately left on plain ssh was
	// reported as mirroring the moment it went down, which is the state
	// somebody runs `status` to look at.
	hosts := []syncd.HostInfo{
		{Label: "plain", GaveUp: true, LastError: "connection refused"},
		{Label: "mirror", GaveUp: true, Mirroring: true, LastError: "connection refused"},
	}
	lines := statusLines(hosts, 100)
	if len(lines) < 2 {
		t.Fatalf("two machines came out as %d lines: %q", len(lines), lines)
	}
	if strings.Contains(lines[0], "mirrored") {
		t.Errorf("a machine set to ssh reads as mirroring while it is down: %q", lines[0])
	}
	// The other half, and why this falls back to the setting rather than to a
	// blank: what a machine is set to is the only thing worth saying about one
	// that is not doing anything.
	if !strings.Contains(lines[1], "mirrored") {
		t.Errorf("a machine set to mirror stopped saying so while it is down: %q", lines[1])
	}
}

func TestTheListingSaysWhatBringsBackAMachineItGaveUpOn(t *testing.T) {
	// The table says "not retrying" and the failure says what to fix. Neither
	// says that fixing it is not enough: a machine given up on stays that way
	// until something asks again, so somebody who corrects a host key and
	// watches the machine stay down has every reason to think the correction
	// did not work.
	gaveUp := []syncd.HostInfo{
		{Target: "bot", Label: "bot", Connected: true, Mirrors: 1},
		{Target: "prod", Label: "prod", GaveUp: true, LastError: "host key changed"},
	}

	line := howToRetry(gaveUp)
	if line == "" {
		t.Fatal("a machine was given up on and nothing says what brings it back")
	}
	for _, want := range []string{"prod", "enter", syncd.PluginID + ".connect"} {
		if !strings.Contains(line, want) {
			t.Errorf("the advice reads %q, without %q", line, want)
		}
	}
	// Not the machine that is fine.
	if strings.Contains(line, "bot") {
		t.Errorf("a machine that is connected is named in the advice: %q", line)
	}

	// Nothing at all when every machine is being tried, rather than a line
	// under every listing anybody ever runs.
	if line := howToRetry(gaveUp[:1]); line != "" {
		t.Errorf("nothing was given up on and the listing still advised: %q", line)
	}
	if line := howToRetry(nil); line != "" {
		t.Errorf("no machines at all and the listing still advised: %q", line)
	}

	// It reads as English for one machine and for several.
	one := howToRetry([]syncd.HostInfo{{Label: "prod", GaveUp: true}})
	if !strings.Contains(one, "It is") || !strings.Contains(one, "its own") {
		t.Errorf("with one machine the advice reads %q", one)
	}
	many := howToRetry([]syncd.HostInfo{
		{Label: "prod", GaveUp: true}, {Label: "staging", GaveUp: true},
	})
	if !strings.Contains(many, "They are") || !strings.Contains(many, "their own") {
		t.Errorf("with two machines the advice reads %q", many)
	}
	if !strings.Contains(many, "prod or staging") {
		t.Errorf("the advice does not name both machines: %q", many)
	}
}

func TestStatusPrintsTheAdviceUnderTheTable(t *testing.T) {
	// The pieces were tested and the join was not: statusLines and howToRetry
	// each had their own test, and whether status ever printed the second one
	// was nobody's business. A sweep found it -- the condition could be
	// inverted, so the advice appeared only when there was none of it, and
	// every test still passed.
	answerWith(t, syncd.Reply{OK: true, Hosts: []syncd.HostInfo{
		{Target: "bot", Label: "bot", Connected: true, Mirrors: 1},
		{Target: "prod", Label: "prod", GaveUp: true, LastError: "host key changed"},
	}})

	out := whatStatusPrinted(t)
	if !strings.Contains(out, "bot") || !strings.Contains(out, "prod") {
		t.Fatalf("the table is missing machines:\n%s", out)
	}
	if !strings.Contains(out, "not tried again") {
		t.Errorf("a machine was given up on and the listing does not say what "+
			"brings it back:\n%s", out)
	}
	if !strings.Contains(out, syncd.PluginID+".connect") {
		t.Errorf("the advice does not name the action that retries everything:\n%s", out)
	}
}

func TestStatusSaysNothingExtraWhenEveryMachineIsFine(t *testing.T) {
	// The other half, and the reason the condition is there: a line under
	// every listing anybody ever runs is a line people stop reading.
	answerWith(t, syncd.Reply{OK: true, Hosts: []syncd.HostInfo{
		{Target: "bot", Label: "bot", Connected: true, Mirrors: 1},
	}})

	out := whatStatusPrinted(t)
	if !strings.Contains(out, "bot") {
		t.Fatalf("the table is missing the machine:\n%s", out)
	}
	if strings.Contains(out, "not tried again") {
		t.Errorf("every machine is fine and the listing still advised:\n%s", out)
	}
}

func TestTheAdviceKeepsTheCommandHoweverManyMachinesAreDown(t *testing.T) {
	// The command retries every machine at once, which is precisely what a
	// fleet of them being down calls for. It used to sit at the end of the
	// sentence after a list of every machine by name, so twenty down at eighty
	// columns ran past the lines the advice is allowed and the command was the
	// part cut off -- the one thing worth saying, gone exactly when it was
	// worth saying it.
	build := func(n int) []syncd.HostInfo {
		var hosts []syncd.HostInfo
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("build-runner-%02d", i)
			hosts = append(hosts, syncd.HostInfo{Target: name, Label: name, GaveUp: true})
		}
		return hosts
	}

	for _, n := range []int{1, 2, 4, 20, 200} {
		line := howToRetry(build(n))
		for _, width := range []int{60, 80, 120} {
			shown := strings.Join(text.Wrap(line, width, maxRetryLines), " ")
			if !strings.Contains(shown, syncd.PluginID+".connect") {
				t.Errorf("with %d machines down at %d columns the advice loses the "+
					"command that retries them all:\n%s", n, width, shown)
			}
		}
	}

	// A few are named and the rest counted, so the sentence stays a sentence.
	if got := howToRetry(build(200)); !strings.Contains(got, "any of the other 197") {
		t.Errorf("two hundred machines are not summarised: %q", got)
	}
	// And counting only when it saves more than one name: "the other 1" is a
	// worse line than the name it replaces.
	if got := howToRetry(build(4)); strings.Contains(got, "other 1 ") {
		t.Errorf("four machines were summarised rather than named: %q", got)
	}
}

// theRetryLines is what maxRetryLines is expected to be, written out rather
// than read from it. The advice sits under the table and names machines and a
// command, so it is longer than a terminal on any day the machines are not
// called a and b; how many lines it may take before the table is pushed off
// the screen is the decision.
const theRetryLines = 4

func TestTheAdviceUnderTheTableStopsAtFourLines(t *testing.T) {
	// The test above wraps to maxRetryLines and asks whether the command
	// survived, which it does at any number of lines at all -- one, or forty.
	// How many lines is the part that decides whether the table above the
	// advice is still on the screen, and nothing asked it.
	//
	// Wrap returns the moment it has filled the last line it is allowed, so a
	// message with more to say comes back as exactly that many lines.
	tooMuch := strings.TrimSpace(strings.Repeat("machine ", 200))

	got := text.Wrap(tooMuch, 60, maxRetryLines)
	if len(got) != theRetryLines {
		t.Errorf("advice that does not fit wrapped to %d lines, want %d:\n%s",
			len(got), theRetryLines, strings.Join(got, "\n"))
	}
	// The last of them says there was more, rather than stopping mid-sentence
	// as though that were all the advice there was.
	if last := got[len(got)-1]; !strings.HasSuffix(last, "\u2026") {
		t.Errorf("the last line does not say the advice was cut: %q", last)
	}

	// A message that fits is not padded out to the bound.
	if short := text.Wrap("connect again to try them", 60, maxRetryLines); len(short) != 1 {
		t.Errorf("advice that fits wrapped to %d lines, want 1: %v", len(short), short)
	}
}

func TestOneMachineCannotFillTheListingWithWhatItSays(t *testing.T) {
	// The state column carries what ssh printed, and a machine chooses those
	// bytes. Wrapped without a bound, a banner of a couple of thousand
	// characters becomes thirty-odd lines and pushes every other machine off
	// the screen -- so a listing of five machines shows one, and the one it
	// shows is the one misbehaving.
	//
	// Twelve written out rather than maxStateLines: measuring against the
	// bound means raising the bound raises what this expects, and the bound
	// could then grow to anything with this still passing.
	long := strings.Repeat("the machine said something about why it would not let us in. ", 40)
	hosts := []syncd.HostInfo{
		{Target: "bot", Label: "bot", Connected: true, Mirrors: 1},
		{Target: "prod", Label: "prod", GaveUp: true, LastError: long},
		{Target: "ci", Label: "ci", Connected: true, Mirrors: 2},
	}

	lines := statusLines(hosts, 80)
	if len(lines) > 12 {
		t.Errorf("one machine's failure took the listing to %d lines", len(lines))
	}
	// And the machines after it are still there, which is the point of
	// bounding it rather than the line count for its own sake.
	shown := strings.Join(lines, "\n")
	for _, name := range []string{"bot", "prod", "ci"} {
		if !strings.Contains(shown, name) {
			t.Errorf("%s is missing from the listing:\n%s", name, shown)
		}
	}
}
