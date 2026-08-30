package cli

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
)

// FuzzTheStatusTableSurvivesWhatAMachineSays holds the `status` listing to its
// claims against text it did not write.
//
// Two of the three columns hold text from outside: a label is whatever somebody
// put in their config, and a failure carries whatever the machine printed on
// its way out -- a login banner, a message in another language, a partial rune
// from a connection cut mid-character. The table then measures those in
// terminal cells to line the columns up, which is where a byte count and a cell
// count disagreeing stops being academic.
func FuzzTheStatusTableSurvivesWhatAMachineSays(f *testing.F) {
	for _, label := range []string{"bot", "", "日本語", "caf\xe9", "\x1b[2Jbot", "a\nb"} {
		for _, failure := range []string{"", "connection refused", "\x1b]0;x\x07", strings.Repeat("x", 500)} {
			for _, width := range []int{0, 1, 40, 100} {
				f.Add(label, failure, width, 3, true, false)
			}
		}
	}
	f.Fuzz(func(t *testing.T, label, failure string, width, mirrors int, connected, gaveUp bool) {
		if width < 0 || width > 4096 {
			t.Skip("not a terminal width")
		}
		if mirrors < 0 || mirrors > 1<<20 {
			t.Skip("not a number of terminals")
		}
		hosts := []syncd.HostInfo{{
			Label:     label,
			LastError: failure,
			Mirrors:   mirrors,
			Connected: connected,
			GaveUp:    gaveUp,
		}}

		lines := statusLines(hosts, width)
		if len(lines) == 0 {
			t.Fatalf("statusLines(%q, %q, %d) reported no line at all for a machine",
				label, failure, width)
		}
		for _, line := range lines {
			if !utf8.ValidString(line) {
				t.Fatalf("statusLines(%q, %q, %d) gave invalid utf-8: %q", label, failure, width, line)
			}
			// The whole point of the column widths: a line that steers the
			// terminal, or wraps it, is a line that has left the table.
			if strings.ContainsAny(line, "\n\r\x1b\x07") {
				t.Fatalf("statusLines(%q, %q, %d) gave %q, which steers the terminal",
					label, failure, width, line)
			}
			// Deliberately not held to the width. The table stops wrapping
			// once what is left after the columns is too narrow to wrap into,
			// and lets the line run off the edge instead -- a state coming out
			// one word per line is worse to read, and the edge at least uses
			// the whole terminal. A long enough label puts any width in that
			// case, so "fits the terminal" is not true here and the unit tests
			// hold the columns where it is.
		}

		// The summary goes to a notification rather than a terminal, and is
		// bounded so that whatever draws it is unlikely to have to choose.
		summary := statusSummary(hosts)
		if !utf8.ValidString(summary) {
			t.Fatalf("statusSummary gave invalid utf-8: %q", summary)
		}
		if strings.ContainsAny(summary, "\n\r\x1b\x07") {
			t.Fatalf("statusSummary gave %q, which steers whatever draws it", summary)
		}
	})
}
