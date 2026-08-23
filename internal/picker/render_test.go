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

func lines(entries []Entry, selected, cols, rows int) []string {
	out := render(entries, selected, cols, rows)
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
			_ = render(machines(10), 5, size[0], size[1])
		}()
	}
}
