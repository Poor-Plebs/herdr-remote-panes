package picker

import (
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
	"testing"
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

func TestPagingReachesEveryMachine(t *testing.T) {
	// The point of the step matching the screenful: paging from the top must
	// eventually land on every machine rather than jumping past some.
	entries := machines(37)
	rows := 24
	step := planLayout(len(entries), 0, rows, 0)
	onScreen := step.last - step.first

	seen := map[int]bool{}
	selected := 0
	for i := 0; i < len(entries)*2; i++ {
		frame := planLayout(len(entries), selected, rows, 0)
		for j := frame.first; j < frame.last; j++ {
			seen[j] = true
		}
		next := move(selected, onScreen, len(entries))
		if next == selected {
			break
		}
		selected = next
	}

	for i := range entries {
		if !seen[i] {
			t.Errorf("machine %d was never shown while paging through", i)
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
