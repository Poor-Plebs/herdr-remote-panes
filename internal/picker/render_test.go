package picker

import (
	"strings"

	"fmt"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// visible strips the escape sequences so a line can be measured as drawn.
func visible(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' && s[i] != 'H' && s[i] != 'J' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func lines(entries []Entry, selected, cols, rows int, warning ...string) []string {
	warn := ""
	if len(warning) > 0 {
		warn = warning[0]
	}
	out := render(entries, selected, cols, rows, warn)
	out = strings.ReplaceAll(out, esc+"[2J"+esc+"[H", "")
	return strings.Split(out, "\r\n")
}

func machines(n int) []Entry {
	out := make([]Entry, n)
	for i := range out {
		out[i] = Entry{Target: "machine", Configured: true}
	}
	return out
}

func TestMenuNeverDrawsMoreThanItHasRoomFor(t *testing.T) {
	// The row budget did not account for the "showing x-y of z" line, which is
	// only drawn once the list scrolls. The menu was one line too tall in
	// exactly the case where space was already short, and the heading scrolled
	// off the top of the popup.
	for _, rows := range []int{4, 5, 6, 7, 8, 12, 24, 40, 60} {
		for _, count := range []int{0, 1, 2, 7, 20, 100} {
			got := len(lines(machines(count), count/2, 72, rows))
			if got > rows {
				t.Errorf("rows=%d count=%d drew %d lines, one popup holds %d",
					rows, count, got, rows)
			}
		}
	}
}

func TestMenuKeepsItsColumnsAligned(t *testing.T) {
	// A name that is wider than one column per character, or longer than the
	// space for it, must not push the status column out of line.
	entries := []Entry{
		{Target: "bot", Configured: true, Connected: true, Terminals: 1},
		{Target: "日本語のホスト", Configured: true},
		{Target: "web-frontend-eu-west-1a", Label: "a very long label indeed", Configured: true},
		{Target: "ci", Configured: true},
	}

	var starts []int
	for _, line := range lines(entries, 0, 72, 24) {
		plain := visible(line)
		// The status column is the last run of two spaces before the text.
		// "not connected" contains "connected", so the longer status has to be
		// looked for first or the column is measured four places too far along.
		for _, status := range []string{"not connected", "connected", "from ~/.ssh"} {
			if i := strings.Index(plain, status); i >= 0 {
				starts = append(starts, text.Width(plain[:i]))
				break
			}
		}
	}
	if len(starts) < 3 {
		t.Fatalf("expected several status columns, found %d", len(starts))
	}
	for _, s := range starts[1:] {
		if s != starts[0] {
			t.Errorf("status columns start at %v, want them all equal", starts)
			break
		}
	}
}

func TestMenuShowsTheCounterOnlyWhenItScrolls(t *testing.T) {
	short := strings.Join(lines(machines(3), 0, 72, 24), "\n")
	if strings.Contains(short, "showing") {
		t.Error("a list that fits should not be labelled with a range")
	}

	long := strings.Join(lines(machines(100), 50, 72, 24), "\n")
	if !strings.Contains(long, "showing") {
		t.Error("a list that scrolls should say where it is")
	}
}

func TestMenuMarksTheSelectedMachine(t *testing.T) {
	drawn := lines(machines(5), 2, 72, 24)
	found := 0
	for _, line := range drawn {
		if strings.Contains(line, reverse) {
			found++
		}
	}
	if found != 1 {
		t.Errorf("%d machines look selected, want exactly 1", found)
	}
}

func TestMenuSurvivesAnAbsurdlySmallPopup(t *testing.T) {
	// stty can report nonsense, and a popup can be dragged very small.
	for _, size := range [][2]int{{1, 1}, {0, 0}, {3, 2}, {200, 3}} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("cols=%d rows=%d panicked: %v", size[0], size[1], r)
				}
			}()
			_ = render(machines(10), 5, size[0], size[1], "")
		}()
	}
}

func TestMenuShowsAWarningWithoutStealingTheScreen(t *testing.T) {
	// A config problem used to be shown on its own screen that had to be
	// dismissed before the menu appeared. A problem worth mentioning every time
	// the menu opens is not worth interrupting every time the menu opens.
	warning := "Check the plugin config: mode \"shh\" is not one of ssh, attach or observe"
	drawn := lines(machines(4), 0, 72, 24, warning)

	joined := strings.Join(drawn, "\n")
	if !strings.Contains(joined, "shh") {
		t.Error("the warning is not in the menu")
	}
	if !strings.Contains(joined, "Connect to a machine") {
		t.Error("the menu itself is missing")
	}
	if !strings.Contains(joined, "machine") {
		t.Error("the machines are missing")
	}
}

func TestAWarningStillFitsThePopup(t *testing.T) {
	// The warning costs rows like anything else, and a long one must not push
	// the frame past the bottom of the popup.
	long := strings.Repeat("a very long configuration problem; ", 20)
	for _, rows := range []int{4, 6, 8, 12, 24} {
		for _, count := range []int{0, 1, 20} {
			got := len(lines(machines(count), 0, 72, rows, long))
			if got > rows {
				t.Errorf("rows=%d count=%d drew %d lines with a warning, one popup holds %d",
					rows, count, got, rows)
			}
		}
	}
}

func TestAWarningIsCutToTheWidth(t *testing.T) {
	long := strings.Repeat("problem ", 40)
	for _, line := range lines(machines(3), 0, 60, 24, long) {
		if w := text.Width(visible(line)); w > 60 {
			t.Errorf("a line is %d columns wide in a %d column popup: %q", w, 60, visible(line))
		}
	}
}

func TestAWarningIsMadeSafeToDraw(t *testing.T) {
	// The warning can quote a value straight from the config file, which is
	// edited by hand and can hold anything at all.
	drawn := strings.Join(lines(machines(2), 0, 72, 24, "mode \x1b[31m\"bad\"\x1b[0m is\nunknown"), "\n")
	if strings.Contains(drawn, "\x1b[31m") {
		t.Error("an escape from the config reached the screen")
	}
}

func TestAWarningKeepsItsReason(t *testing.T) {
	// The reason a config could not be read sits at the end of the message,
	// where cutting it to one line threw it away.
	warning := "Could not read the plugin config, so only ~/.ssh/config machines are listed: unexpected end of JSON input"
	joined := strings.Join(lines(machines(4), 0, 76, 24, warning), "\n")

	if !strings.Contains(joined, "unexpected end of JSON input") {
		t.Errorf("the reason was cut off:\n%s", joined)
	}
}

func TestAShortPopupKeepsSomeOfTheWarning(t *testing.T) {
	// Half a warning is worth more than none, so the second line goes before
	// the first one does.
	warning := "Could not read the plugin config, so only ~/.ssh/config machines are listed: unexpected end of JSON input"
	drawn := lines(machines(20), 0, 76, 9, warning)

	if len(drawn) > 9 {
		t.Errorf("drew %d lines in a 9 row popup", len(drawn))
	}
	if !strings.Contains(strings.Join(drawn, "\n"), "Could not read") {
		t.Errorf("the warning went entirely:\n%s", strings.Join(drawn, "\n"))
	}
}

func noticeLines(cols int, heading string, body ...string) []string {
	out := renderNotice(cols, heading, body...)
	out = strings.ReplaceAll(out, esc+"[2J"+esc+"[H", "")
	return strings.Split(out, "\r\n")
}

func TestNoticeStaysInsideThePopup(t *testing.T) {
	// These screens used to be printed at whatever length they happened to be,
	// so an error carrying a socket path ran off the edge and wrapped wherever
	// the terminal chose, mid-word and mid-path.
	long := "no running daemon (is the plugin enabled? check `herdr plugin log " +
		"list --plugin poorplebs.remote-panes`): dial unix " +
		"/tmp/hrp-9777840ec7cbab2b.sock: connect: no such file or directory"

	for _, cols := range []int{40, 60, 76, 120} {
		for _, line := range noticeLines(cols, "Could not change prod", long, "Press any key.") {
			if w := text.Width(visible(line)); w > cols {
				t.Errorf("cols=%d: line is %d wide: %q", cols, w, visible(line))
			}
		}
	}
}

func TestNoticeKeepsTheWholeMessage(t *testing.T) {
	// The end of an error is usually the part that says what actually went
	// wrong, so it has to survive the wrapping.
	long := "no running daemon: dial unix /tmp/hrp-9777840ec7cbab2b.sock: " +
		"connect: no such file or directory"
	// The lines carry their indent, so collapse the whitespace before looking
	// for a phrase that the wrapping may have split across two of them.
	joined := strings.Join(strings.Fields(
		visible(strings.Join(noticeLines(76, "Could not change prod", long), " "))), " ")

	if !strings.Contains(joined, "no such file or directory") {
		t.Errorf("the end of the message was lost: %q", joined)
	}
	if !strings.Contains(joined, "prod") {
		t.Errorf("the machine was not named: %q", joined)
	}
}

func TestNoticeIsMadeSafeToDraw(t *testing.T) {
	// A machine name comes from ~/.ssh/config and an error can quote a remote
	// message, so neither is trusted to be printable.
	joined := strings.Join(noticeLines(76, "Connecting to \x1b[31mbot\x1b[0m ..."), "\n")
	if strings.Contains(joined, "\x1b[31m") {
		t.Error("an escape reached the screen")
	}
}

func TestNoticeSurvivesAnAbsurdlyNarrowPopup(t *testing.T) {
	for _, cols := range []int{0, 1, 4, 5} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("cols=%d panicked: %v", cols, r)
				}
			}()
			_ = renderNotice(cols, "Could not connect", "something went wrong")
		}()
	}
}

func TestAnUnreachableMachineStillSaysHowItIsReached(t *testing.T) {
	// This is the line someone reads before pressing m, and without the mode
	// there is no telling which way the toggle would go.
	drawn := strings.Join(lines([]Entry{
		{Target: "staging", Configured: true, GaveUp: true, Mirroring: true},
		{Target: "prod", Configured: true, GaveUp: true},
	}, 0, 76, 24), "\n")

	if !strings.Contains(drawn, "mirrored") {
		t.Errorf("a mirroring machine does not say so:\n%s", visible(drawn))
	}
	// Both still say what to do about it.
	if strings.Count(drawn, "enter to retry") != 2 {
		t.Errorf("both should offer a retry:\n%s", visible(drawn))
	}
	// The plain one is not labelled as mirroring.
	for _, line := range lines([]Entry{{Target: "prod", Configured: true, GaveUp: true}}, 0, 76, 24) {
		if strings.Contains(line, "prod") && strings.Contains(line, "mirrored") {
			t.Errorf("a plain SSH machine was labelled as mirroring: %q", visible(line))
		}
	}
}

func TestAPageMovesExactlyOneScreenful(t *testing.T) {
	// The step used to be the popup height less a constant, which stopped
	// matching when the frame learned to give up its parts as room ran short.
	// It was two rows out at every size, so paging through a long list stepped
	// over two machines each time without ever showing them.
	for _, rows := range []int{8, 10, 16, 24, 40} {
		for _, warning := range []string{"", "check the plugin config: mode \"shh\" is unknown"} {
			entries := machines(60)
			frame := planLayout(len(entries), 0, rows, len(warningLines(76, warning)))
			onScreen := frame.last - frame.first

			// Everything the layout shows, and nothing it does not.
			drawn := 0
			for _, line := range lines(entries, 0, 76, rows, warning) {
				// The heading says "Connect to a machine" as well, and only the
				// first nine entries carry a number, so neither is a way to
				// pick them out.
				plain := visible(line)
				if strings.Contains(plain, "machine") && !strings.Contains(plain, "Connect to a") {
					drawn++
				}
			}
			if drawn != onScreen {
				t.Errorf("rows=%d: layout says %d on screen, %d were drawn", rows, onScreen, drawn)
			}
			if onScreen < 1 {
				t.Errorf("rows=%d: nothing is visible", rows)
			}
		}
	}
}

func TestNoLineEverRunsPastThePopup(t *testing.T) {
	// The room kept for the state column was a number written down beside the
	// code that drew it, and it went stale the moment a state line grew: the
	// reservation stayed at what "connected · NN mirrored" needed while the
	// longest had become half as long again, and the line ran off the popup by
	// a dozen columns at ordinary widths.
	entries := []Entry{
		{Target: "bot", Configured: true, Connected: true, Terminals: 3},
		{Target: "build-server-eu-west-1a", Configured: true, GaveUp: true, Mirroring: true},
		{Target: "prod", Configured: true, GaveUp: true},
		{Target: "staging", Configured: true, Connected: true, Mirroring: true, Mirrors: 99},
		{Target: "日本語のホスト", Configured: true, Mirroring: true},
		{Target: strings.Repeat("very-long-machine-name-", 6), Configured: true},
		{Target: "laptop"},
	}

	for _, cols := range []int{20, 30, 40, 48, 60, 76, 80, 100, 200} {
		for _, rows := range []int{8, 12, 24, 40} {
			for _, warning := range []string{"", "check the plugin config: mode \"shh\" is unknown"} {
				for _, line := range lines(entries, 1, cols, rows, warning) {
					if w := text.Width(visible(line)); w > cols {
						t.Errorf("cols=%d rows=%d: a line is %d wide: %q",
							cols, rows, w, visible(line))
					}
				}
			}
		}
	}
}

func TestTheStateSurvivesEvenWhenTheHintCannot(t *testing.T) {
	// Room is given up from the end: a hint about which key to press is worth
	// less than knowing the machine cannot be reached.
	narrow := strings.Join(lines([]Entry{
		{Target: "prod", Configured: true, GaveUp: true, Mirroring: true},
	}, 0, 34, 24), "\n")

	if !strings.Contains(narrow, "unreachable") {
		t.Errorf("the state itself was given up:\n%s", visible(narrow))
	}

	// With room, the whole of it is there.
	wide := strings.Join(lines([]Entry{
		{Target: "prod", Configured: true, GaveUp: true, Mirroring: true},
	}, 0, 100, 24), "\n")
	for _, want := range []string{"unreachable", "mirrored", "enter to retry"} {
		if !strings.Contains(wide, want) {
			t.Errorf("a wide popup should say %q:\n%s", want, visible(wide))
		}
	}
}

func TestWidestStatusMatchesWhatIsDrawn(t *testing.T) {
	// The reservation and the drawing must agree, which they can only do by
	// one of them asking the other.
	reserved := widestStatus()
	for _, entry := range []Entry{
		{GaveUp: true, Mirroring: true},
		{Connected: true, Mirroring: true, Mirrors: 99},
		{Connected: true, Terminals: 99},
		{Configured: true, Mirroring: true},
		{},
	} {
		if w := text.Width(plainOf(statusSpans(entry))); w > reserved {
			t.Errorf("%+v needs %d columns, only %d are kept for it", entry, w, reserved)
		}
	}
}

func TestTheMenuOffersDisconnectAtEveryWidth(t *testing.T) {
	// The menu could connect a machine and never let go of one, so the only way
	// to close a machine's panes was to bind the action separately or invoke it
	// by hand. A key that is not mentioned is a key nobody presses.
	for _, cols := range []int{40, 56, 80, 120} {
		drawn := strings.Join(lines(machines(3), 0, cols, 24), "\n")
		if !strings.Contains(visible(drawn), "d ") {
			t.Errorf("cols=%d: the hints do not mention d:\n%s", cols, visible(drawn))
		}
	}
}

func TestAMachineThatFellBackReadsAsWhatItIs(t *testing.T) {
	// A machine without Herdr falls back to a plain SSH terminal rather than
	// refusing to connect. The menu read the setting rather than what happened,
	// so such a machine sat there saying "connected · 0 mirrored" while running
	// a terminal it declined to count.
	fellBack := Entry{
		Target: "workbox", Configured: true, Connected: true,
		Mirroring: true, // asked for
		SSHOnly:   true, // what happened
		Terminals: 2,
	}
	drawn := visible(strings.Join(lines([]Entry{fellBack}, 0, 76, 24), "\n"))

	if strings.Contains(drawn, "mirrored") {
		t.Errorf("a machine on plain SSH is described as mirrored:\n%s", drawn)
	}
	if !strings.Contains(drawn, "2 open") {
		t.Errorf("the terminals it actually has are not counted:\n%s", drawn)
	}

	// One that really is mirroring still says so.
	really := Entry{
		Target: "ci", Configured: true, Connected: true,
		Mirroring: true, Mirrors: 3,
	}
	drawn = visible(strings.Join(lines([]Entry{really}, 0, 76, 24), "\n"))
	if !strings.Contains(drawn, "3 mirrored") {
		t.Errorf("a machine that is mirroring does not say so:\n%s", drawn)
	}
}

func TestBeforeConnectingTheSettingIsAllThereIs(t *testing.T) {
	// SSHOnly means nothing until a machine has been reached, so what was asked
	// for is what to show.
	asked := Entry{Target: "workbox", Configured: true, Mirroring: true}
	drawn := visible(strings.Join(lines([]Entry{asked}, 0, 76, 24), "\n"))
	if !strings.Contains(drawn, "mirrored") {
		t.Errorf("a machine set to mirror should say so before it is reached:\n%s", drawn)
	}

	// And one given up on says how it was meant to be reached, which is the
	// line somebody reads before pressing m.
	gaveUp := Entry{Target: "prod", Configured: true, GaveUp: true, Mirroring: true}
	drawn = visible(strings.Join(lines([]Entry{gaveUp}, 0, 76, 24), "\n"))
	if !strings.Contains(drawn, "mirrored") {
		t.Errorf("an unreachable machine should still say how it is reached:\n%s", drawn)
	}
}

func TestTheMenuSaysWhenNothingIsAnswering(t *testing.T) {
	// With no daemon, every machine reads "not connected" -- which is exactly
	// what a working daemon shows before anything has been connected. The menu
	// looked ready, and nothing in it would work until enter was pressed and
	// the failure arrived on a screen of its own.
	warning := "The daemon is not running, so nothing here can be connected to. " +
		"Check `herdr plugin log list --plugin poorplebs.remote-panes`."

	drawn := visible(strings.Join(lines(machines(3), 0, 76, 24, warning), "\n"))

	if !strings.Contains(drawn, "daemon is not running") {
		t.Errorf("the menu does not say the daemon is down:\n%s", drawn)
	}
	// The machines are still listed: knowing what is configured is useful even
	// when none of it can be reached.
	if !strings.Contains(drawn, "machine") {
		t.Errorf("the machines were dropped along with the daemon:\n%s", drawn)
	}
	// And it still fits.
	for _, line := range lines(machines(3), 0, 76, 24, warning) {
		if w := text.Width(visible(line)); w > 76 {
			t.Errorf("a line is %d columns wide: %q", w, visible(line))
		}
	}
}

func TestTheNameColumnFitsTheNamesThatAreThere(t *testing.T) {
	// The column used to be whatever the popup could afford, which for the
	// usual case -- machines called bot, prod, web1 -- left each status some
	// thirty columns from the name it belongs to, with nothing in between. The
	// eye has to cross that to pair them up, and the space was reserved for
	// names nobody had.
	short := []Entry{
		{Target: "bot", Configured: true, Connected: true, Terminals: 3},
		{Target: "prod", Configured: true},
		{Target: "ci", Configured: true},
	}

	t.Run("short names do not reserve room for long ones", func(t *testing.T) {
		got := nameColumn(short, 80)
		if got >= nameWidth(80) {
			t.Errorf("column is %d wide for names of at most 4, the same as the old fixed %d",
				got, nameWidth(80))
		}
		// Wide enough for the longest name, or it would be cut for no reason.
		if got < 4 {
			t.Errorf("column is %d wide, too narrow for %q", got, "prod")
		}
	})

	t.Run("the status still starts clear of the name", func(t *testing.T) {
		for _, line := range lines(short, 0, 80, 24) {
			plain := visible(line)
			// The longer status first: "not connected" contains "connected",
			// so searching for the short one finds the column four places in.
			i := -1
			for _, status := range []string{"not connected", "connected"} {
				if at := strings.Index(plain, status); at >= 0 {
					i = at
					break
				}
			}
			if i < 0 {
				continue
			}
			// At least one blank column between the longest name and the
			// status, so the two read as two columns rather than one.
			if !strings.HasSuffix(plain[:i], "  ") {
				t.Errorf("status runs straight into the name: %q", plain)
			}
		}
	})

	t.Run("a long name is still capped to what the popup can afford", func(t *testing.T) {
		long := append([]Entry{}, short...)
		long = append(long, Entry{Target: strings.Repeat("x", 200), Configured: true})
		if got := nameColumn(long, 80); got > nameWidth(80) {
			t.Errorf("column is %d wide, past the %d the popup can afford", got, nameWidth(80))
		}
	})

	t.Run("the column does not change as the list scrolls", func(t *testing.T) {
		// Sizing to the visible machines is tighter, but then names slide
		// sideways under the cursor while paging, which is worse than a column
		// wider than one screenful strictly needs.
		many := make([]Entry, 40)
		for i := range many {
			many[i] = Entry{Target: fmt.Sprintf("machine-%d", i), Configured: true}
		}
		many[39] = Entry{Target: "a-considerably-longer-machine-name", Configured: true}

		want := nameColumn(many, 80)
		for _, selected := range []int{0, 10, 25, 39} {
			if got := nameColumn(many, 80); got != want {
				t.Errorf("at selection %d the column is %d, was %d", selected, got, want)
			}
		}
	})
}

// rowFor is the drawn line for a machine, found by its name rather than by
// counting: the frame's leading lines come and go with the popup's size.
func rowFor(t *testing.T, drawn []string, target string) string {
	t.Helper()
	for _, line := range drawn {
		plain := visible(line)
		if strings.Contains(plain, target) && !strings.Contains(plain, "Connect to a machine") {
			return plain
		}
	}
	t.Fatalf("no row for %q in:\n%s", target, strings.Join(drawn, "\n"))
	return ""
}

func TestAnUnreachableMachineSaysWhyInTheMenu(t *testing.T) {
	// "unreachable" on its own is a dead end: it is the screen somebody is
	// looking at when they want to know what to do, and it told them only that
	// there was nothing to be done from here. The reason has been in the
	// listing and the log all along.
	entries := []Entry{{
		Target: "prod", Configured: true, GaveUp: true,
		Reason: shortReason("host key changed — verify it, then update ~/.ssh/known_hosts"),
	}}

	line := rowFor(t, lines(entries, 0, 80, 20), "prod")
	if !strings.Contains(line, "unreachable") {
		t.Fatalf("the machine does not read as unreachable: %q", line)
	}
	if !strings.Contains(line, "host key changed") {
		t.Errorf("%q does not say why the machine cannot be reached", line)
	}
	// The cause, not the sentence about what to do with it: that belongs where
	// there is room for a sentence.
	if strings.Contains(line, "known_hosts") {
		t.Errorf("%q carries the whole summary into a menu row", line)
	}
	// And cut at the join rather than wherever the width ran out. Cutting by
	// width alone also hides the second half, but leaves half a sentence
	// trailing off -- "host key changed — verify i…" -- which reads as
	// something going wrong rather than as an answer.
	if strings.Contains(line, "—") {
		t.Errorf("%q keeps the join, so what follows is a sentence cut short", line)
	}
	if strings.Contains(line, "…") {
		t.Errorf("%q trails off; the cause on its own fits", line)
	}
	if !strings.Contains(line, "enter to retry") {
		t.Errorf("%q lost the way back", line)
	}
}

func TestWhenRoomIsShortTheReasonOutlastsTheReminder(t *testing.T) {
	// Enter is guessable. Why a machine cannot be reached is not, so when only
	// one of them fits it is the reminder that goes.
	entries := []Entry{{
		Target: "prod", Configured: true, GaveUp: true,
		Reason: shortReason("host key changed — verify it"),
	}}

	tight := ""
	for cols := 80; cols >= 40; cols -= 2 {
		line := rowFor(t, lines(entries, 0, cols, 20), "prod")
		if !strings.Contains(line, "enter to retry") {
			tight = line
			break
		}
	}
	if tight == "" {
		t.Skip("the reminder still fits at every width tried")
	}
	if !strings.Contains(tight, "host key changed") {
		t.Errorf("at the width where the reminder went, the reason went first: %q", tight)
	}
}

func TestAReasonFromAMachineIsMadeSafeToDraw(t *testing.T) {
	// An unrecognised failure keeps the first line of whatever ssh printed,
	// which is text from somewhere else on its way to a terminal.
	entry := Entry{
		Target: "prod", Configured: true, GaveUp: true,
		Reason: shortReason("\x1b[31mred\x1b[0m\nand a second line"),
	}
	if strings.ContainsAny(entry.Reason, "\n\r") || strings.ContainsRune(entry.Reason, 0x1b) {
		t.Errorf("the reason is %q, which steers the terminal", entry.Reason)
	}

	line := rowFor(t, lines([]Entry{entry}, 0, 80, 20), "prod")
	if text.Width(line) > 80 {
		t.Errorf("the row is %d columns wide: %q", text.Width(line), line)
	}
}

func TestAMachineWithNothingToSayStillReadsProperly(t *testing.T) {
	// Not every failure has a summary -- a machine given up on for dropping
	// terminals has no ssh error behind it -- and the row must not end up with
	// a dangling separator.
	line := rowFor(t, lines([]Entry{{Target: "prod", Configured: true, GaveUp: true}}, 0, 80, 20), "prod")
	if strings.Contains(line, "·  ·") || strings.Contains(line, "· ·") {
		t.Errorf("%q has an empty reason between the separators", line)
	}
	if !strings.Contains(line, "unreachable") || !strings.Contains(line, "enter to retry") {
		t.Errorf("%q is missing part of what it should say", line)
	}
}

// fakeStty puts an stty on PATH that answers "size" with the given output.
func fakeStty(t *testing.T, output string, status int) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\nexit %d\n", output, status)
	if err := os.WriteFile(filepath.Join(dir, "stty"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestThePopupIsDrawnAtTheSizeItIs(t *testing.T) {
	// Everything about the frame is decided from this: how many machines fit,
	// how wide a name may be, how far a page key moves. Reading it wrong is not
	// a crash but a menu drawn for a terminal somebody does not have.
	t.Run("the size the terminal reports", func(t *testing.T) {
		// stty prints rows first, and this returns columns first.
		fakeStty(t, "24 100", 0)
		cols, rows := windowSize()
		if cols != 100 || rows != 24 {
			t.Errorf("got %dx%d, want 100x24 — rows and columns are the wrong way round", cols, rows)
		}
	})

	for _, tt := range []struct {
		name   string
		output string
		status int
	}{
		{"a terminal that cannot be asked", "", 1},
		{"an answer that is not two numbers", "24", 0},
		{"an answer that is not numbers at all", "rows columns", 0},
		{"an answer of nothing", "", 0},
		{"a size of zero", "0 0", 0},
	} {
		t.Run(tt.name+" falls back to something usable", func(t *testing.T) {
			// A popup drawn at zero columns shows nothing at all, so anything
			// that cannot be believed has to give way to a size that works.
			fakeStty(t, tt.output, tt.status)
			cols, rows := windowSize()
			if cols < 20 || rows < 5 {
				t.Errorf("got %dx%d, which is too small to draw a menu in", cols, rows)
			}
		})
	}

	t.Run("one number readable and the other not", func(t *testing.T) {
		// Half an answer is still half an answer: keep the half that parsed.
		fakeStty(t, "40 wide", 0)
		cols, rows := windowSize()
		if rows != 40 {
			t.Errorf("rows = %d, want the 40 the terminal reported", rows)
		}
		if cols < 20 {
			t.Errorf("cols = %d, want the fallback rather than nothing", cols)
		}
	})
}

func TestOnlyTheMachinesADigitCanReachAreNumbered(t *testing.T) {
	// The menu offers "1-9 pick", so the tenth machine down has no digit to be
	// picked by. Numbering it anyway offers a key that does nothing, and a key
	// that does nothing in a menu is the quietest kind of broken.
	entries := make([]Entry, 12)
	for i := range entries {
		entries[i] = Entry{Target: fmt.Sprintf("machine-%d", i), Configured: true}
	}

	drawn := lines(entries, 0, 80, 30)
	numbered := 0
	for _, line := range drawn {
		plain := visible(line)
		for digit := 1; digit <= 9; digit++ {
			if strings.Contains(plain, fmt.Sprintf(" %d. machine-", digit)) {
				numbered++
			}
		}
	}
	if numbered != 9 {
		t.Errorf("%d machines are numbered, want the nine a digit can reach:\n%s",
			numbered, strings.Join(drawn, "\n"))
	}
	// And the tenth is drawn, just without one.
	found := false
	for _, line := range drawn {
		if strings.Contains(visible(line), "machine-9") {
			found = true
			if strings.Contains(visible(line), "10.") {
				t.Errorf("the tenth machine is numbered %q, and no key presses that", visible(line))
			}
		}
	}
	if !found {
		t.Error("the tenth machine is not drawn at all")
	}
}

func TestEveryStateAMachineCanBeInReadsAsItself(t *testing.T) {
	// This line is the whole of what the menu tells you about a machine, and
	// every one of these says something different about what pressing enter or
	// m will do. Reading the wrong one is not a crash: it is a machine that
	// looks connected and is not, or looks like plain SSH and is mirrored, and
	// you find out by pressing something.
	for _, tt := range []struct {
		what  string
		entry Entry
		want  []string
		avoid []string
	}{
		{
			what:  "a machine with terminals open on it",
			entry: Entry{Configured: true, Connected: true, Terminals: 3},
			want:  []string{"connected", "3 open"},
		},
		{
			what:  "a machine connected with nothing open",
			entry: Entry{Configured: true, Connected: true},
			want:  []string{"connected", "ssh"},
			avoid: []string{"0 open"},
		},
		{
			what:  "a mirrored machine, which counts mirrors and not terminals",
			entry: Entry{Configured: true, Connected: true, Mirroring: true, Mirrors: 2},
			want:  []string{"connected", "2 mirrored"},
			avoid: []string{"open"},
		},
		{
			what:  "a configured machine nobody has connected to",
			entry: Entry{Configured: true},
			want:  []string{"not connected", "ssh"},
		},
		{
			what:  "a configured machine set to mirror, before connecting",
			entry: Entry{Configured: true, Mirroring: true},
			want:  []string{"not connected", "mirrored"},
		},
		{
			what:  "a machine only ~/.ssh/config knows about",
			entry: Entry{},
			want:  []string{"~/.ssh/config", "ssh"},
			avoid: []string{"not connected"},
		},
		{
			what:  "one that could not be reached",
			entry: Entry{Configured: true, GaveUp: true, Reason: "host key changed"},
			want:  []string{"unreachable", "host key changed", "enter to retry"},
		},
		{
			what:  "one that could not be reached, and is set to mirror",
			entry: Entry{Configured: true, GaveUp: true, Mirroring: true},
			want:  []string{"unreachable", "mirrored", "enter to retry"},
		},
	} {
		t.Run(tt.what, func(t *testing.T) {
			got := plainOf(statusSpans(tt.entry))
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("the line reads %q, without %q", got, want)
				}
			}
			for _, avoid := range tt.avoid {
				if strings.Contains(got, avoid) {
					t.Errorf("the line reads %q, which should not mention %q", got, avoid)
				}
			}
		})
	}
}

func TestNothingInTheMenuRunsOffTheSide(t *testing.T) {
	// A popup is whatever size the window is, and every part of the menu has
	// its own idea of how much room it has. When one of them is wrong the line
	// wraps, the menu is a column taller than it thought, and the bottom of it
	// scrolls away.
	//
	// Checked through render rather than against each part's own arithmetic:
	// the hints fit themselves to cols-4 but are drawn at an indent, and a test
	// that repeated that sum would agree with the code instead of the popup.
	entries := []Entry{
		{Target: "bot", Configured: true, Connected: true, Terminals: 3},
		{Target: "a-machine-with-a-very-long-name-indeed", Configured: true},
		{Target: "gone", Configured: true, GaveUp: true, Reason: "connection refused"},
		{Target: "mirrored-one", Configured: true, Connected: true, Mirroring: true, Mirrors: 4},
	}
	// Heights as well as widths. At one height nothing scrolls, and the
	// "showing 1-3 of 6" line is only drawn when something does -- so a sweep
	// of every width at one tall popup never once drew it, and it was the one
	// line written without asking how wide the popup is.
	counted := false
	// From the narrowest popup the layout claims to serve: see nameWidth.
	for cols := chromeWidth + 8; cols <= 200; cols++ {
		for rows := 1; rows <= 24; rows++ {
			for _, warning := range []string{"", "could not read ~/.ssh/config"} {
				for _, selected := range []int{0, len(entries) - 1} {
					drawn := render(entries, selected, cols, rows, warning)
					if strings.Contains(drawn, "showing ") {
						counted = true
					}
					for _, line := range strings.Split(drawn, "\r\n") {
						if got := text.Width(visible(line)); got > cols {
							t.Fatalf("at %d columns and %d rows a line is %d wide: %q",
								cols, rows, got, visible(line))
						}
					}
				}
			}
		}
	}
	// Otherwise this stops covering the counter the day the layout stops
	// scrolling at these sizes, and goes on passing.
	if !counted {
		t.Error("no size in the sweep scrolled, so the counter line was never drawn")
	}
}

func TestTheKeysStaySpelledOutHoweverNarrowItGets(t *testing.T) {
	// Narrower means shorter hints, never no hints: the menu is the only place
	// these keys are written down, and one that says nothing is one you have to
	// already know.
	for cols := chromeWidth + 8; cols <= 200; cols++ {
		hints := hintLines(cols)
		if len(hints) != 2 {
			t.Fatalf("at %d columns there are %d hint lines, want 2", cols, len(hints))
		}
		for i, hint := range hints {
			if strings.TrimSpace(hint) == "" {
				t.Errorf("at %d columns hint %d is empty", cols, i)
			}
		}
		// Whatever else goes, the way out stays.
		if !strings.Contains(hints[1], "q") {
			t.Errorf("at %d columns nothing says how to leave: %q", cols, hints[1])
		}
		// And they fit, which is the whole of what choosing between them is
		// for. Both lines of a pair, not one: the two are different lengths,
		// so there is a range of widths where the first fits and the second
		// does not, and taking the pair on the strength of either one leaves
		// the other running off the side.
		for i, hint := range hints {
			if w := text.Width(hint); w > cols-4 {
				t.Errorf("at %d columns hint %d is %d wide and runs off the side: %q",
					cols, i, w, hint)
			}
		}
	}
}

func TestWhatGoesFirstWhenTheStateWillNotFit(t *testing.T) {
	// A machine that could not be reached has three things to say and rarely
	// room for all of them. Which one goes when is a decision, not an accident:
	// what happened is worth more than why, and why is worth more than a
	// reminder that enter retries — enter is guessable and a bare "unreachable"
	// leaves somebody with nothing to do next.
	//
	// So these may drop off the end, but never out of order.
	entry := Entry{Target: "gone", Configured: true, GaveUp: true, Reason: "connection refused"}
	for cols := chromeWidth + 8; cols <= 200; cols++ {
		out := visible(render([]Entry{entry}, 0, cols, 24, ""))
		var (
			what   = strings.Contains(out, "unreachable")
			why    = strings.Contains(out, "connection refused")
			remind = strings.Contains(out, "enter to retry")
		)
		if why && !what {
			t.Fatalf("at %d columns the reason is shown without what happened: %q", cols, out)
		}
		if remind && !why {
			t.Fatalf("at %d columns the reminder is shown without the reason: %q", cols, out)
		}
	}
}

func TestOneEnormousNameDoesNotShoveTheStateColumnAway(t *testing.T) {
	// Names come from ~/.ssh/config and some of them are a fully qualified
	// domain and a jump host. Left alone, one of those sets the width of the
	// name column for every machine in the list, and the states end up so far
	// to the right that they no longer read as belonging to anything.
	long := Entry{Target: strings.Repeat("x", 60), Configured: true}
	longer := Entry{Target: strings.Repeat("x", 160), Configured: true}
	short := Entry{Target: "bot", Configured: true, Connected: true, Terminals: 1}

	where := func(entries []Entry) int {
		for _, line := range strings.Split(visible(render(entries, 0, 200, 24, "")), "\r\n") {
			if i := strings.Index(line, "connected · 1 open"); i >= 0 {
				return i
			}
		}
		t.Fatalf("no line for %q", short.Target)
		return 0
	}
	if a, b := where([]Entry{long, short}), where([]Entry{longer, short}); a != b {
		t.Errorf("the state column moves from %d to %d when a name gets longer", a, b)
	}
}

func TestTheWindowAlwaysHoldsTheCursorAndNothingElseIsOutOfBounds(t *testing.T) {
	// The list is windowed when it is longer than the popup, and the whole of
	// what that has to get right is: show a run of machines that exists, and
	// have the selected one inside it. A window that leaves the cursor outside
	// means moving the selection changes nothing on screen, which reads as the
	// arrow keys not working.
	//
	// Written as properties over every shape rather than as a handful of cases,
	// because the mistakes here are all at boundaries — an empty list, a single
	// machine, a popup one row shorter than it needs — and those are exactly
	// the shapes nobody thinks to write down.
	// Every small shape exhaustively, then a few large ones. The small ones are
	// where the boundaries are; the large ones are where a window that is a
	// fraction of the list gets used in earnest, and nothing else here has more
	// machines than fit on a screen.
	counts := []int{}
	for n := 0; n <= 12; n++ {
		counts = append(counts, n)
	}
	counts = append(counts, 25, 100, 500)

	for _, count := range counts {
		for selected := 0; selected < count || (count == 0 && selected == 0); selected++ {
			// A long list is checked at its ends and middle rather than at
			// every position, which would be half a million layouts.
			if count > 12 && selected != 0 && selected != count/2 && selected != count-1 {
				continue
			}
			for rows := 1; rows <= 16; rows++ {
				for warnLines := 0; warnLines <= 2; warnLines++ {
					got := planLayout(count, selected, rows, warnLines)

					if got.first < 0 || got.last < got.first || got.last > count {
						t.Fatalf("count=%d selected=%d rows=%d warn=%d gives [%d,%d), which is not a run of %d machines",
							count, selected, rows, warnLines, got.first, got.last, count)
					}
					// A count line saying "showing 1-8 of 8" is noise: it is
					// there to say that something is hidden.
					if got.counter && got.last-got.first == count {
						t.Fatalf("count=%d rows=%d warn=%d shows every machine and still counts them",
							count, rows, warnLines)
					}
					// And the other way round, which is the half that matters:
					// the count is the only thing on screen saying the list
					// goes on. Without it a popup showing one machine of forty
					// looks like a machine list with one machine in it.
					//
					// Below four rows there is no row to spare for it -- what
					// is drawn there is the heading and the machine the cursor
					// is on, and nothing else fits.
					if rows >= 4 && !got.counter && got.last-got.first < count {
						t.Fatalf("count=%d rows=%d warn=%d shows %d machines and does not say the rest are there",
							count, rows, warnLines, got.last-got.first)
					}
					if count == 0 {
						continue
					}
					if selected < got.first || selected >= got.last {
						t.Fatalf("count=%d selected=%d rows=%d warn=%d shows [%d,%d), leaving the cursor off screen",
							count, selected, rows, warnLines, got.first, got.last)
					}
				}
			}
		}
	}
}

func TestAWindowGivesUpTheRightThingsFirst(t *testing.T) {
	// What goes when there is not room is a decision: the machines are what the
	// menu is for, a warning is worth more than a reminder of which keys move
	// the cursor, and half a warning is worth more than none.
	const count, selected, warn = 8, 0, 2

	// With plenty of room, everything is shown.
	roomy := planLayout(count, selected, 20, warn)
	if !roomy.hints || roomy.warning != warn || roomy.last != count {
		t.Fatalf("with twenty rows the menu dropped something: %+v", roomy)
	}

	// Taking rows away, the hints go before any of the warning does, and the
	// warning is given up a line at a time rather than all at once.
	var lostHints, lostWarningLine bool
	for rows := 19; rows >= 4; rows-- {
		got := planLayout(count, selected, rows, warn)
		if !got.hints && !lostHints {
			lostHints = true
		}
		if got.warning < warn && !lostWarningLine {
			lostWarningLine = true
			if !lostHints {
				t.Errorf("at %d rows the warning was cut while the key hints were still shown", rows)
			}
		}
		if got.warning > 0 && got.warning < warn {
			// A warning cut down rather than dropped: this is the case worth
			// having, and it must actually happen at some width.
			lostWarningLine = true
		}
	}
	if !lostHints {
		t.Error("the hints were never given up, however short the popup got")
	}
}

func TestAListThatJustFitsIsNotScrolled(t *testing.T) {
	// The properties beside this one hold for a window that scrolls when it did
	// not need to: the cursor is still inside it, the range still exists, and
	// the counter still only appears when something is hidden. What none of
	// them say is that a list which fits should be shown whole.
	//
	// Room for the heading and its blank line, two lines of hints and the blank
	// line above them, and then a machine per row. Give the popup exactly that
	// and every machine belongs on screen; one row less and it has to start
	// hiding some. The difference between those two is one character in a
	// comparison, and nothing held it.
	const chrome = 5 // heading, its blank line, a separator, two lines of hints

	for count := 1; count <= 12; count++ {
		rows := count + chrome

		got := planLayout(count, 0, rows, 0)
		if got.last-got.first != count {
			t.Errorf("%d machines in a popup of %d rows shows %d of them, though "+
				"they all fit: [%d,%d)", count, rows, got.last-got.first, got.first, got.last)
		}
		if got.counter {
			t.Errorf("%d machines in a popup of %d rows counts what it is hiding, "+
				"and it is hiding nothing", count, rows)
		}
		if !got.hints {
			t.Errorf("%d machines in a popup of %d rows dropped the key hints, "+
				"which there was room for", count, rows)
		}

		// One row less has to give something up, so the check above is not
		// passing for a popup that was roomy all along.
		if tight := planLayout(count, 0, rows-1, 0); tight.hints && tight.last-tight.first == count {
			t.Errorf("%d machines, the hints, and the heading all fit in %d rows, "+
				"which is one row less than they take", count, rows-1)
		}
	}
}

func TestHowAMachineIsNamedInTheMenu(t *testing.T) {
	// The name in the menu is what somebody picks by, and there was nothing
	// holding any of it. A label is worth showing exactly when it says
	// something the target does not: a machine labelled the same as itself
	// reading "bot (bot)" is noise, and one whose label is the useful half
	// losing it is worse.
	for _, tt := range []struct{ target, label, want string }{
		{"bot", "", "bot"},
		{"bot", "build", "bot (build)"},
		// The label repeating the target says nothing twice.
		{"bot", "bot", "bot"},
		// A label that differs only in case is still something somebody wrote.
		{"bot", "Bot", "bot (Bot)"},
		{"deploy@vm", "", "deploy@vm"},
	} {
		if got := displayName(Entry{Target: tt.target, Label: tt.label}); got != tt.want {
			t.Errorf("a machine %q labelled %q reads as %q, want %q",
				tt.target, tt.label, got, tt.want)
		}
	}

	// Both halves come from files somebody else can write -- the target from
	// ~/.ssh/config, the label from the plugin's config -- and both are drawn
	// into a menu that is repainted on every keypress. An escape sequence in
	// either would steer the terminal rather than name a machine.
	steered := displayName(Entry{
		Target: "bot\x1b[31m\n",
		Label:  "build\x1b[2J",
	})
	if strings.ContainsRune(steered, 0x1b) || strings.ContainsAny(steered, "\n\r") {
		t.Errorf("the menu would draw %q, which moves the cursor rather than naming a machine", steered)
	}
}

func TestFittingAStatusIntoItsColumn(t *testing.T) {
	// The state column gives up its tail until what is left fits: the state
	// itself is kept whatever happens, and what follows it elaborates. None of
	// that was held, and it is all boundaries -- a line that exactly fits, a
	// column with room for one character, a state longer than the whole column.
	//
	// Built fresh each time, because fitting truncates the first piece in
	// place: one caller, one slice per call, so that is safe there and would
	// quietly corrupt a test that reused one.
	full := func() []span {
		return []span{
			{text: "unreachable"},
			{text: " · connection refused"},
			{text: " · press c to retry"},
		}
	}
	width := text.Width(plainOf(full()))

	// Room to spare, and room for exactly what is there: neither gives
	// anything up. Trimming one character early costs a whole piece of the
	// line, at whatever width somebody's terminal happens to be.
	for _, room := range []int{width + 20, width} {
		got := fitStatus(full(), room)
		if plainOf(got) != plainOf(full()) {
			t.Errorf("with %d columns for a line of %d, the line came back as %q",
				room, width, plainOf(got))
		}
	}

	// Nothing to fit, which the one caller does not produce -- every branch of
	// statusSpans says at least one thing -- and which this must not fall over
	// on regardless. The check for a single piece left is what keeps it safe:
	// reach for that piece before checking there is one, and a machine with
	// nothing to say takes the menu down with it.
	for _, room := range []int{-1, 0, 1, 40} {
		if got := fitStatus(nil, room); len(got) != 0 {
			t.Errorf("fitting nothing into %d columns produced %d pieces", room, len(got))
		}
	}

	// One column short: the tail goes rather than the state.
	short := fitStatus(full(), width-1)
	if !strings.HasPrefix(plainOf(short), "unreachable") {
		t.Errorf("one column short, the line reads %q; the state is the part to keep", plainOf(short))
	}

	// Every width from nothing at all to more than enough: what comes back
	// fits, is never empty, and still starts with the state.
	for room := -3; room <= width+3; room++ {
		got := fitStatus(full(), room)
		if len(got) == 0 {
			t.Fatalf("with %d columns the whole state was given up, leaving the "+
				"machine with nothing said about it", room)
		}
		if w := text.Width(plainOf(got)); room >= 1 && w > room {
			t.Fatalf("with %d columns the line came back %d wide, which runs past "+
				"the column and into the next machine's", room, w)
		}
		// Once there is room for the state itself, the state is what is
		// there. Below that all anyone can be given is a piece of it and an
		// ellipsis, which is still better than a blank.
		if room >= text.Width("unreachable") && !strings.HasPrefix(plainOf(got), "unreachable") {
			t.Fatalf("with %d columns, room enough for the state, the line reads %q",
				room, plainOf(got))
		}
	}
}

func TestTheFullerHintsAppearAsSoonAsTheyFit(t *testing.T) {
	// Three pairs of hints, longest first, and the widest that fits is the one
	// drawn. "Fits" has to mean fits: giving up a pair that would have sat
	// exactly inside the popup costs the longer spelling of every key on it,
	// and does it at whatever width somebody's terminal happens to be.
	//
	// Written as the width where each pair first appears rather than against
	// the strings themselves, so that rewording a hint does not fail this.
	widest := func(lines []string) int {
		w := 0
		for _, l := range lines {
			if n := text.Width(l); n > w {
				w = n
			}
		}
		return w
	}

	seen := map[string]int{}
	for cols := chromeWidth + 8; cols <= 200; cols++ {
		hints := hintLines(cols)
		key := strings.Join(hints, "\n")
		if _, already := seen[key]; !already {
			seen[key] = cols
		}
	}

	for key, cols := range seen {
		lines := strings.Split(key, "\n")
		// The narrowest width at which this pair is drawn. One column less
		// and something shorter was drawn instead, so this pair must not fit
		// there -- which means it fits here with nothing to spare.
		if w := widest(lines); w != cols-4 && cols > chromeWidth+8 {
			t.Errorf("this pair is %d wide and first appears at %d columns, "+
				"which leaves %d columns spare -- so at %d it was passed over "+
				"for something shorter that it did not need to be: %q",
				w, cols, (cols-4)-w, cols-1, lines[0])
		}
	}
}

func TestATallerPopupNeverShowsFewerMachines(t *testing.T) {
	// The menu used to show fewer machines as the popup grew. At six rows the
	// key hints did not fit at all, so they were given up and three machines
	// drawn; at seven they just fitted, so they were kept and one machine drawn
	// beside them. Growing the window took two machines off the screen, and did
	// it again a few rows later.
	//
	// Nobody reports that. It reads as the menu being arbitrary, and the only
	// way to see it is to resize the terminal slowly while watching a list that
	// is longer than the popup.
	for _, count := range []int{1, 3, 5, 12, 40} {
		for _, selected := range []int{0, count / 2, count - 1} {
			previous := 0
			for rows := 1; rows <= 40; rows++ {
				frame := planLayout(count, selected, rows, 0)
				shown := frame.last - frame.first
				if shown < previous {
					t.Errorf("with %d machines and the cursor on %d, a popup of %d rows "+
						"shows %d of them where %d rows showed %d",
						count, selected, rows, shown, rows-1, previous)
				}
				previous = shown
			}
		}
	}
}

func TestTheWarningSurvivesAPopupWorthReadingItIn(t *testing.T) {
	// Machines beat the key hints. They do not beat the warning: that line is
	// what says the daemon is not answering or the config cannot be read, and a
	// menu that hides it to fit two more machines in is one that looks fine
	// while nothing in it works.
	//
	// The first version of this fix had exactly that. With twenty machines the
	// warning was gone at nine rows, at fourteen and at eighteen -- every
	// ordinary popup size -- because dropping it always bought more machines.
	for _, rows := range []int{9, 14, 18, 24, 30} {
		frame := planLayout(20, 0, rows, 2)
		if frame.warning == 0 {
			t.Errorf("a popup of %d rows drew no warning at all, and there was one to draw", rows)
		}
		if shown := frame.last - frame.first; shown == 0 {
			t.Errorf("a popup of %d rows drew the warning and no machines", rows)
		}
	}
}

func TestWhichNameFitsTheRoomThereIs(t *testing.T) {
	// The full "target (label)" whenever it fits. When it does not, what used
	// to be drawn was the front of the target -- and the front of a target is
	// a login, so every machine reached as the same user came out identical.
	for _, tt := range []struct {
		what  string
		entry Entry
		width int
		want  string
	}{
		{"both, when there is room", Entry{Target: "deploy@prod", Label: "prod"}, 30, "deploy@prod (prod)"},
		{"the label, when the pair will not fit", Entry{Target: "deploy@prod", Label: "prod"}, 8, "prod"},
		{"the target, when it is the shorter name", Entry{Target: "prod", Label: "production web server"}, 8, "prod"},
		{"the label, when neither fits whole", Entry{Target: "deploy@prod", Label: "production-web"}, 6, "production-web"},
		{"the target, when there is no label", Entry{Target: "raspberrypi.local"}, 8, "raspberrypi.local"},
		{"the target, when the label repeats it", Entry{Target: "workbox", Label: "workbox"}, 4, "workbox"},

		// Exactly as wide as the column, which is the ordinary case rather
		// than an edge one: the column is sized to the widest name there is,
		// so the widest name fits it exactly. A boundary read one column tight
		// drops the full name for the label at precisely the width the column
		// was made for.
		{"the pair, exactly filling the column", Entry{Target: "bot", Label: "b"}, 7, "bot (b)"},
		// Both fit and the label is chosen, which is the rule: the label is the
		// name somebody picked for the machine. It takes both fitting to see
		// it -- with only the label fitting, the fallback at the end returns
		// the label too, so the two paths agree by accident.
		{"the label, when both fit exactly", Entry{Target: "bot", Label: "prod"}, 4, "prod"},
		{"the target, exactly filling it", Entry{Target: "prod", Label: "production web"}, 4, "prod"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			if got := nameWithin(tt.entry, tt.width); got != tt.want {
				t.Errorf("in %d columns the name is %q, want %q", tt.width, got, tt.want)
			}
		})
	}
}

func TestANarrowMenuStillTellsMachinesApart(t *testing.T) {
	// Three machines reached as the same user. Their targets share a prefix
	// and their labels do not, so a narrow menu that draws targets draws the
	// same eight characters three times.
	entries := []Entry{
		{Target: "deploy@prod", Label: "prod", Configured: true},
		{Target: "deploy@staging", Label: "staging", Configured: true},
		{Target: "deploy@canary", Label: "canary", Configured: true},
	}

	drawn := visible(strings.Join(lines(entries, 0, 54, 24), "\n"))

	for _, want := range []string{"prod", "staging", "canary"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the menu does not name %q:\n%s", want, drawn)
		}
	}
	if strings.Count(drawn, "deploy@") > 0 {
		t.Errorf("a login is still standing in for a machine name:\n%s", drawn)
	}
}

func TestTwoMachinesAreNeverDrawnWithTheSameName(t *testing.T) {
	// A label can collide with another machine's target: a "staging" in
	// ~/.ssh/config beside a configured machine labelled "staging". Two rows
	// naming different machines identically is the thing this is here to
	// prevent, so the one that would collide goes back to the full form.
	entries := []Entry{
		{Target: "deploy@staging", Label: "staging", Configured: true},
		{Target: "staging"},
	}

	names := namesWithin(entries, 8)

	if names[0] == names[1] {
		t.Errorf("both machines are drawn as %q", names[0])
	}
	if names[1] != "staging" {
		t.Errorf("the machine that owns the name lost it: %q", names[1])
	}
}

func TestTheFirstScreenAFreshInstallationShows(t *testing.T) {
	// Nothing configured and nothing in ~/.ssh/config either. There is no menu
	// to draw, so this notice is the whole of what somebody sees the first
	// time they press the key they just bound.
	heading, body := noMachinesNotice("")
	drawn := visible(renderNotice(76, heading, body...))

	// What to do about it.
	for _, want := range []string{"No machines found", "~/.ssh/config", "config.json"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the first screen does not mention %q:\n%s", want, drawn)
		}
	}
	// And how to leave it. The three other screens that wait for a key say so;
	// this one did not, which left a popup with nothing in it and no way out
	// written down.
	if !strings.Contains(drawn, "any key") {
		t.Errorf("the first screen does not say how to close it:\n%s", drawn)
	}
	// It still fits.
	for _, line := range strings.Split(drawn, "\r\n") {
		if w := text.Width(line); w > 76 {
			t.Errorf("a line is %d columns wide: %q", w, line)
		}
	}
}

func TestAConfigProblemIsSaidEvenWithNoMachinesToSayItAbout(t *testing.T) {
	// A config that cannot be read is why there are no machines, and with no
	// menu to carry the warning it would otherwise go nowhere a person looks.
	_, body := noMachinesNotice("check the plugin config: mode \"shh\" is unknown")

	joined := strings.Join(body, "\n")
	if !strings.Contains(joined, "shh") {
		t.Errorf("the reason there are no machines was dropped: %q", joined)
	}
	if !strings.Contains(joined, "any key") {
		t.Errorf("the way out went missing once there was a warning: %q", joined)
	}
}

// withStty puts an stty on PATH that records how it was called and succeeds or
// fails as asked. It hands back what has been asked of it so far.
func withStty(t *testing.T, works bool) func() []string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "asked")
	status := 0
	if !works {
		status = 1
	}
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\nexit %d\n", log, status)
	if err := os.WriteFile(filepath.Join(dir, "stty"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() []string {
		raw, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
}

// captureStdout runs something that prints to the process's own stdout and
// hands back what it wrote. The menu's terminal control is written there
// directly, which is the only place it could be written.
func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	real := os.Stdout
	os.Stdout = w
	run()
	os.Stdout = real
	w.Close()
	printed, _ := io.ReadAll(r)
	return string(printed)
}

func TestTheTerminalIsPutBackWhenTheMenuCloses(t *testing.T) {
	// The menu puts the terminal into raw mode to read single keys, and has to
	// put it back: left raw, the shell underneath echoes nothing and reads
	// nothing line by line, so what somebody is left with after picking a
	// machine is a terminal that appears to have stopped working.
	asked := withStty(t, true)

	var restore func()
	printed := captureStdout(t, func() { restore = rawMode() })

	if got := asked(); len(got) != 1 || !strings.Contains(got[0], "raw") {
		t.Errorf("stty was asked %v, want raw mode once", got)
	}
	// Pastes arrive wrapped in markers so they can be told from typing --
	// without which pasting "prod" presses p, r, o and d, and d disconnects
	// the machine under the cursor.
	if !strings.Contains(printed, "[?2004h") {
		t.Errorf("bracketed paste was not turned on: %q", printed)
	}

	printed = captureStdout(t, restore)

	if got := asked(); len(got) != 2 || !strings.Contains(got[1], "sane") {
		t.Errorf("stty was asked %v, want the terminal put back", got)
	}
	if !strings.Contains(printed, "[?2004l") {
		t.Errorf("bracketed paste was left on: %q", printed)
	}
}

func TestATerminalThatWillNotGoRawIsLeftAlone(t *testing.T) {
	// stty failing means this is not a terminal that can be driven -- output
	// redirected, or no tty at all. Turning bracketed paste on anyway would
	// leave whatever is reading this with paste markers in its input and
	// nothing to turn them off, since there is no working restore to do it.
	asked := withStty(t, false)

	var restore func()
	printed := captureStdout(t, func() { restore = rawMode() })

	if strings.Contains(printed, "[?2004") {
		t.Errorf("a terminal that would not go raw was sent paste markers: %q", printed)
	}
	printed = captureStdout(t, restore)
	if printed != "" {
		t.Errorf("restoring a terminal that was never changed wrote %q", printed)
	}
	for _, line := range asked() {
		if strings.Contains(line, "sane") {
			t.Errorf("stty was asked to put back a terminal it never changed: %v", asked())
		}
	}
}

func TestTheMenuSaysWhatTheScopeIsLeavingAlone(t *testing.T) {
	// The line somebody reads straight after pressing m. Without this, a
	// machine with four terminals on it showing one mirror looks like three
	// that failed rather than like the scope doing what it says.
	entry := Entry{Target: "workbox", Configured: true, Connected: true,
		Mirroring: true, Mirrors: 1, OutsideShared: 3}

	drawn := visible(strings.Join(lines([]Entry{entry}, 0, 80, 24), "\n"))
	if !strings.Contains(drawn, "1 mirrored") {
		t.Errorf("the menu does not say what is mirrored:\n%s", drawn)
	}
	if !strings.Contains(drawn, "3 elsewhere") {
		t.Errorf("the menu does not say what is not:\n%s", drawn)
	}

	// A machine with nothing left out says nothing, or every machine carries
	// the phrase for ever.
	entry.OutsideShared = 0
	drawn = visible(strings.Join(lines([]Entry{entry}, 0, 80, 24), "\n"))
	if strings.Contains(drawn, "elsewhere") {
		t.Errorf("a machine with nothing left out still mentions it:\n%s", drawn)
	}
}

func TestSayingWhatIsLeftOutCostsNoNameColumn(t *testing.T) {
	// The name column is what the status column does not take, so a longer
	// worst-case status is paid for by every machine's name at every width —
	// which is the thing the column was fitted to the names to stop.
	//
	// Written out rather than compared against widestStatus(), which would
	// agree with itself. Changing these numbers is allowed; changing them
	// without having looked at what it costs is what this is here to stop.
	//
	// 39 → 40 was read-only. The word replaces "mirrored" in the widest status
	// there is, "unreachable · mirrored · enter to retry", and is one longer.
	// Paid deliberately: nameWidth caps at 40, which it reaches by 88 columns,
	// so above that the extra column costs nothing at all. Between 72 and 88 it
	// costs one character of a name, and only of a name already too long for
	// the column. Against that, a machine somebody cannot type into looked
	// exactly like one they can, which is found out by typing into it.
	if got := widestStatus(); got != 40 {
		t.Errorf("the widest status is %d columns, and adding to it takes the "+
			"same from every machine's name; check what grew", got)
	}
	if got := nameWidth(80); got != 32 {
		t.Errorf("the name column at 80 columns is %d, want 32", got)
	}
	// The cap is the point of the paragraph above, so it is checked rather
	// than asserted: an ordinary terminal pays nothing for this.
	if got := nameWidth(100); got != 40 {
		t.Errorf("the name column at 100 columns is %d, want the 40 cap: the "+
			"status column is now taking room at widths that used to be free", got)
	}
}

func TestWhenTurningMirroringOnAsksFirst(t *testing.T) {
	// The two directions are not alike. Turning it off drops the panes here
	// and leaves the work on the machine; turning it on closes plain SSH
	// terminals, and a plain SSH terminal's shell goes when its pane does,
	// with whatever was running in it.
	//
	// So it asks only where there is something to lose. Asking every time
	// would put a keypress in front of the harmless direction as well, and a
	// question people answer without reading is not a safeguard.
	for _, tt := range []struct {
		what  string
		entry Entry
		mode  string
		asks  bool
	}{
		{"turning it on with terminals open", Entry{Target: "bot", Connected: true, Terminals: 3}, "attach", true},
		{"and with one", Entry{Target: "bot", Connected: true, Terminals: 1}, "attach", true},

		// Nothing to lose in any of these.
		{"turning it on with nothing open", Entry{Target: "bot", Connected: true}, "attach", false},
		{"turning it off", Entry{Target: "bot", Connected: true, Mirroring: true, Mirrors: 3}, "ssh", false},
		{"a machine not connected", Entry{Target: "bot", Configured: true}, "attach", false},
	} {
		t.Run(tt.what, func(t *testing.T) {
			if got := worthAskingBeforeToggle(tt.entry, tt.mode); got != tt.asks {
				t.Errorf("asks = %v, want %v", got, tt.asks)
			}
			// And the ones that cost nothing go straight through, rather than
			// waiting on a key there is no terminal to press.
			if !tt.asks && !confirmToggle(tt.entry, tt.mode) {
				t.Error("refused a toggle that costs nothing")
			}
		})
	}
}

func TestWhatTheToggleQuestionSays(t *testing.T) {
	// It has to say what will happen and what it will cost, or it is a
	// keypress people learn to dismiss.
	drawn := visible(renderNotice(76, "Turn mirroring on for bot?",
		"Mirroring works differently, so its 3 terminals here are closed and the "+
			"machine is connected again. They are plain SSH sessions, so whatever is "+
			"running in them goes with them.",
		"m to go ahead, any other key to leave it alone."))

	for _, want := range []string{"3 terminals", "closed", "running in them", "m to go ahead"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the question does not say %q:\n%s", want, drawn)
		}
	}

	// And it fits whatever the popup is. A question about losing work is a bad
	// one to have wrapped mid-word by the terminal, and this is the longest
	// thing the menu draws outside the machine list.
	for _, cols := range []int{40, 54, 60, 76, 120} {
		for _, line := range noticeLines(cols, "Turn mirroring on for bot?",
			"Mirroring works differently, so its 3 terminals here are closed and the "+
				"machine is connected again. They are plain SSH sessions, so whatever "+
				"is running in them goes with them.",
			"m to go ahead, any other key to leave it alone.") {
			if w := text.Width(visible(line)); w > cols {
				t.Errorf("at %d columns a line is %d wide: %q", cols, w, visible(line))
			}
		}
	}
}

func TestNoStateIsWiderThanTheColumnReservedForIt(t *testing.T) {
	// widestStatus decides how much room the names get, by measuring the widest
	// state there is. It measures a hand-written list of the worst entries — so
	// a state added to statusSpans and not to that list is a state nobody
	// measured, and the names are handed room on the understanding that it does
	// not exist.
	//
	// Nothing runs off the edge when that happens: fitStatus trims the state to
	// whatever the name left it. That is the damage. The new state is the one
	// cut off, on exactly the machines whose names are long enough to want the
	// room — so it reads correctly everywhere it is tested by hand, and elides
	// on the machine someone named after its fully qualified domain.
	//
	// Every combination rather than one field at a time, which is what this
	// checked first and why it was worth changing: read-only was added, and it
	// only reaches the line on a machine that is also mirroring. One at a time
	// never sets two things, so it measured nothing and passed while the widest
	// state in the menu was a column past what the names had been told. The
	// combinations are the whole point — they are what the hand-written list is
	// made of, and the list is what this is checking.
	widest := widestStatus()

	shape := reflect.TypeOf(Entry{})
	var fields []string
	for i := 0; i < shape.NumField(); i++ {
		switch name := shape.Field(i).Name; name {
		case "Target", "Label", "Reason":
			// What somebody wrote or what a machine said, which is trimmed to
			// fit rather than measured: fitStatus does that, and holding a
			// reason to this width would hold the wrong thing.
		default:
			fields = append(fields, name)
		}
	}
	if len(fields) < 8 {
		t.Fatalf("found %d states to combine, which is fewer than there are", len(fields))
	}
	if len(fields) > 20 {
		// 2^20 is a minute of CPU to say what a smaller sweep says. If Entry
		// ever grows this far the sweep needs rethinking rather than running.
		t.Fatalf("%d states is too many to combine; this needs a different approach", len(fields))
	}

	for mask := 0; mask < 1<<uint(len(fields)); mask++ {
		var entry Entry
		holder := reflect.ValueOf(&entry).Elem()
		for i, name := range fields {
			if mask&(1<<uint(i)) == 0 {
				continue
			}
			value := holder.FieldByName(name)
			switch value.Kind() {
			case reflect.Bool:
				value.SetBool(true)
			case reflect.Int:
				value.SetInt(99)
			default:
				t.Fatalf("Entry.%s is a %s, which this does not know how to set",
					name, value.Kind())
			}
		}
		if got := text.Width(plainOf(statusSpans(entry))); got > widest {
			t.Fatalf("%+v makes a status %d columns wide and widestStatus measures "+
				"%d: add it to the list there, or it is the state that gets trimmed "+
				"away once a name is long enough to want the room", entry, got, widest)
		}
	}
}

func TestOnlyMGoesAheadWithClosingTerminals(t *testing.T) {
	// The question is the last thing between somebody and losing what is
	// running in three terminals, and the half of it that had never been run
	// was the answering: worthAskingBeforeToggle is held everywhere, and what
	// happens once it has asked was reached by no test, because reading a key
	// wants a terminal.
	//
	// Anything other than m has to leave it alone. A question that proceeds on
	// a stray keypress is not a safeguard, and the keys people press to escape
	// a prompt they did not expect -- escape, enter, q -- are exactly the ones
	// that must not.
	entry := Entry{Target: "bot", Connected: true, Terminals: 3}
	if !worthAskingBeforeToggle(entry, "attach") {
		t.Fatal("this entry does not ask, so the test below proves nothing")
	}

	quiet, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer quiet.Close()
	savedOut := os.Stdout
	os.Stdout = quiet
	defer func() { os.Stdout = savedOut }()

	for _, tt := range []struct {
		what  string
		keys  string
		ahead bool
	}{
		{"m", "m", true},
		{"M, which is the same key", "M", true},
		{"enter", "\r", false},
		{"escape", "\x1b", false},
		{"q", "q", false},
		{"a digit, which elsewhere picks a machine", "3", false},
		{"d, which elsewhere disconnects", "d", false},
		{"nothing, because the terminal went", "", false},
	} {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		go func(keys string) {
			if keys != "" {
				_, _ = write.WriteString(keys)
			}
			write.Close()
		}(tt.keys)

		savedIn := os.Stdin
		os.Stdin = read
		got := confirmToggle(entry, "attach")
		os.Stdin = savedIn
		read.Close()

		if got != tt.ahead {
			t.Errorf("%s: went ahead = %v, want %v", tt.what, got, tt.ahead)
		}
	}
}

func TestTheQuestionSaysHowMuchThereIsToLose(t *testing.T) {
	// The question exists to say what pressing m would cost, so the number in
	// it is the whole point. Saying "1 terminal" over three of them is not a
	// wording slip: it is the prompt understating the thing it was put there
	// to warn about.
	for _, tt := range []struct {
		terminals int
		want      string
	}{
		{1, "1 terminal"},
		{2, "2 terminals"},
		{7, "7 terminals"},
	} {
		entry := Entry{Target: "bot", Connected: true, Terminals: tt.terminals}
		if !worthAskingBeforeToggle(entry, "attach") {
			t.Fatalf("%d terminals does not ask, so this proves nothing", tt.terminals)
		}

		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		savedOut := os.Stdout
		os.Stdout = write
		// Answering no, since what is being read here is the question.
		keysRead, keysWrite, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		savedIn := os.Stdin
		os.Stdin = keysRead
		go func() { _, _ = keysWrite.WriteString("q"); keysWrite.Close() }()

		asked := make(chan string, 1)
		go func() {
			var b strings.Builder
			_, _ = io.Copy(&b, read)
			asked <- b.String()
		}()
		confirmToggle(entry, "attach")

		os.Stdout, os.Stdin = savedOut, savedIn
		write.Close()
		keysRead.Close()
		question := <-asked

		if !strings.Contains(question, tt.want) {
			t.Errorf("with %d open the question does not say %q:\n%s",
				tt.terminals, tt.want, visible(question))
		}
		// And not the other spelling, which is how "1 terminals" would pass a
		// check for "1 terminal".
		if tt.terminals == 1 && strings.Contains(question, "1 terminals") {
			t.Errorf("the question says %q for one terminal", "1 terminals")
		}
	}
}

func TestTheMenuStaysDrawableWithAVeryLongSSHConfig(t *testing.T) {
	// The menu is redrawn on every keypress, and two of the things it does
	// before drawing a row look at every machine rather than the screenful
	// being shown: the name column is measured across all of them so it does
	// not change width as the list scrolls, and the names are made all at once
	// so that two which shorten to the same text can be told apart. Both are
	// deliberate, and both are linear.
	//
	// What this holds is the shape rather than the speed. A time on its own
	// says as much about the machine running it as about the code: the first
	// try here bounded a redraw at half a second, and a quadratic version of
	// the duplicate check -- comparing every name with every other rather than
	// counting them in a map -- came in at a tenth of that and passed.
	//
	// So it measures the same work at two sizes and compares the two. Machine
	// speed divides out: whatever the hardware, quadrupling the list costs a
	// linear pass about two and a half times, and a quadratic one twelve to
	// sixteen depending on where the sizes fall. Both were measured, with and
	// without the race detector, before the eight between them was chosen --
	// and the sizes are the smallest that kept the two apart, since this runs
	// on every push.
	drawn := func(n int) time.Duration {
		entries := make([]Entry, n)
		for i := range entries {
			entries[i] = Entry{
				Target:     fmt.Sprintf("deploy@machine%05d", i),
				Configured: i%3 == 0,
				Connected:  i%2 == 0,
				Mirroring:  i%5 == 0,
				Mirrors:    i % 7,
			}
		}
		start := time.Now()
		const redraws = 5
		for i := 0; i < redraws; i++ {
			// A different position each time, as scrolling gives: ten redraws
			// of one screenful would not exercise a window computed once.
			_ = render(entries, i*n/redraws, 100, 40, "")
		}
		return time.Since(start) / redraws
	}

	// Neither is an absurd config. A host block per machine in a fleet, or a
	// generated one, reaches the first easily; sshconfig will read a megabyte
	// of such a file, which is some twenty thousand of them.
	small, large := drawn(2000), drawn(8000)
	if small <= 0 {
		t.Skip("the clock here cannot measure a redraw, so there is nothing to compare")
	}
	if grew := float64(large) / float64(small); grew > 8 {
		t.Errorf("four times the machines costs %.1f times the redraw (%s to %s), "+
			"which is not a slower machine but a different shape of work: "+
			"something now looks at every machine once per machine",
			grew, small, large)
	}
}
