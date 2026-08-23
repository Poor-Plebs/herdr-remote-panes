package text

import (
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// A newline drew a second row the menu did not know about, so the
			// arrow keys and the entry numbers no longer matched what was on
			// screen, and it let one name impersonate another entry.
			name: "a newline cannot draw another row",
			in:   "bot\nfake-entry",
			want: "botfake-entry",
		},
		{
			// An escape sequence changed the colour of everything drawn after
			// it, or could hide text entirely.
			name: "an escape sequence is stripped",
			in:   "bot\x1b[31mRED\x1b[0m",
			want: "bot[31mRED[0m",
		},
		{"a carriage return is stripped", "bot\rmore", "botmore"},
		{"a tab becomes a space", "bot\tcol", "bot col"},
		{"a bell is stripped", "bot\a", "bot"},
		// Written as an escape: a literal one is invisible in the source, which
		// is the very thing being stripped.
		{"C1 controls are stripped", "bot\u0085more", "botmore"},
		{"an ordinary name is untouched", "build-server-01", "build-server-01"},
		{"non-ASCII names are kept", "构建服务器", "构建服务器"},
		{"surrounding space is trimmed", "  bot  ", "bot"},
		{"an empty name stays empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sanitize(tt.in); got != tt.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	t.Run("nothing printable survives a control-only name", func(t *testing.T) {
		if got := Sanitize("\x00\x01\x02"); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestDisplayWidth(t *testing.T) {
	// Padding by rune count misaligns every column after a wide character.
	for in, want := range map[string]int{
		"":           0,
		"workbox":    7,
		"构建":         4, // two cells each
		"构建server":   10,
		"\U0001F680": 2, // an emoji takes two cells
		"é":         1, // a combining accent sits on the previous cell
	} {
		if got := Width(in); got != want {
			t.Errorf("Width(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTruncateToWidth(t *testing.T) {
	t.Run("a short name is untouched", func(t *testing.T) {
		if got := Truncate("workbox", 20); got != "workbox" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("a long name is cut and marked", func(t *testing.T) {
		got := Truncate("build-server-eu-west-1a-production", 20)
		if Width(got) > 20 {
			t.Errorf("%q is %d cells, want at most 20", got, Width(got))
		}
		if !strings.HasSuffix(got, "…") {
			t.Errorf("%q should show that it was cut", got)
		}
	})

	t.Run("wide characters do not overshoot", func(t *testing.T) {
		// Cutting by runes would leave twice the intended width on screen and
		// push the column after it past the edge of the popup.
		got := Truncate(strings.Repeat("构", 20), 10)
		if Width(got) > 10 {
			t.Errorf("%q is %d cells, want at most 10", got, Width(got))
		}
	})

	t.Run("no room means nothing", func(t *testing.T) {
		if got := Truncate("workbox", 0); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestPadToWidth(t *testing.T) {
	if got := Pad("bot", 6); Width(got) != 6 {
		t.Errorf("Pad(bot, 6) is %d cells, want 6", Width(got))
	}
	// A wide name must be padded by cells, or the column after it shifts.
	if got := Pad("构建", 6); Width(got) != 6 {
		t.Errorf("padding a wide name gave %d cells, want 6", Width(got))
	}
	// Something already too wide is left alone rather than padded negatively.
	if got := Pad("a-very-long-name", 4); got != "a-very-long-name" {
		t.Errorf("got %q, want it untouched", got)
	}
}

func TestWrap(t *testing.T) {
	// Cutting a message to one line loses whatever is at the end, which for a
	// message explaining why something failed is the half worth reading.
	msg := "Could not read the plugin config, so only ~/.ssh/config machines are listed: unexpected end of JSON input"

	one := Wrap(msg, 72, 1)
	if len(one) != 1 {
		t.Fatalf("Wrap(...,1) = %d lines, want 1", len(one))
	}
	if !strings.HasSuffix(one[0], "…") {
		t.Errorf("a message that did not fit should say so: %q", one[0])
	}

	two := Wrap(msg, 72, 2)
	if len(two) != 2 {
		t.Fatalf("Wrap(...,2) = %d lines, want 2", len(two))
	}
	if !strings.Contains(strings.Join(two, " "), "unexpected end of JSON input") {
		t.Errorf("the reason was lost: %q", two)
	}
	for _, line := range two {
		if Width(line) > 72 {
			t.Errorf("line is %d columns wide: %q", Width(line), line)
		}
	}

	// A message that fits is left whole, with nothing added to it.
	short := `mode "shh" is not one of ssh, attach or observe`
	if got := Wrap(short, 72, 2); len(got) != 1 || got[0] != short {
		t.Errorf("Wrap(short) = %q, want it unchanged", got)
	}

	// Nothing to say, nothing drawn.
	for _, empty := range []string{"", "   ", "\t"} {
		if got := Wrap(empty, 72, 2); got != nil {
			t.Errorf("Wrap(%q) = %q, want nothing", empty, got)
		}
	}

	// Degenerate sizes must not loop or panic.
	for _, width := range []int{-1, 0, 1, 2} {
		for _, max := range []int{0, 1, 2} {
			for _, line := range Wrap(msg, width, max) {
				if Width(line) > width {
					t.Errorf("width=%d produced %q", width, line)
				}
			}
		}
	}
}

func TestWrapKeepsAWordTogetherWhereItCan(t *testing.T) {
	got := Wrap("alpha beta gamma delta", 11, 3)
	want := []string{"alpha beta", "gamma delta"}
	if len(got) != len(want) {
		t.Fatalf("Wrap = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Wrap = %q, want %q", got, want)
			break
		}
	}
}

func TestTruncateKeepsAsMuchAsFits(t *testing.T) {
	// The width contract alone does not pin this: cutting one character too
	// early still fits, so a mutation that dropped an extra character from
	// every shortened name passed every test. What it costs is a letter of
	// every machine name in a narrow popup, which is exactly where the letters
	// matter most.
	cases := []struct {
		in    string
		width int
		want  string
	}{
		// The ellipsis takes one cell, so a limit of n keeps n-1 cells of text.
		{"abcdef", 6, "abcdef"},
		{"abcdef", 5, "abcd…"},
		{"abcdef", 3, "ab…"},
		{"abcdef", 2, "a…"},
		{"abcdef", 1, "…"},
		{"abcdef", 0, ""},

		// A wide character takes two cells, so it is kept only if both fit.
		{"日本語", 6, "日本語"},
		{"日本語", 5, "日本…"},
		{"日本語", 4, "日…"},
		{"日本語", 3, "日…"},
		{"日本語", 2, "…"},

		// A zero-width mark rides along with the character it belongs to.
		{"éxyz", 3, "éx…"},
	}
	for _, tt := range cases {
		if got := Truncate(tt.in, tt.width); got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
	}
}

func TestWrapAtNoRoomGivesNothing(t *testing.T) {
	// A popup can be reported as having no room -- windowSize falls back when
	// stty says nothing useful -- and the callers draw whatever comes back.
	// Returning a line for a zero-width popup puts characters where there are
	// no columns to hold them.
	for _, tt := range []struct{ width, maxLines int }{
		{0, 4}, {-1, 4}, {10, 0}, {10, -1}, {0, 0},
	} {
		if got := Wrap("a message that would otherwise wrap", tt.width, tt.maxLines); len(got) != 0 {
			t.Errorf("Wrap(..., %d, %d) = %q, want nothing", tt.width, tt.maxLines, got)
		}
	}
}

func TestTheWidthTableCoversItsRangesExactly(t *testing.T) {
	// Padding by rune count misaligns anything that is not narrow, so these
	// ranges are what keeps a column of machine names lined up. Every boundary
	// here could be moved by one without a single test noticing.
	wide := []rune{
		0x1100, 0x115F, // Hangul Jamo
		0x2E80, 0xA4CF, // CJK radicals through Yi
		0xAC00, 0xD7A3, // Hangul syllables
		0xF900, 0xFAFF, // CJK compatibility ideographs
		0xFE30, 0xFE6F, // CJK compatibility forms
		0xFF00, 0xFF60, // Fullwidth forms
		0xFFE0, 0xFFE6,
		0x1F300, 0x1FAFF, // Emoji
		0x20000, 0x3FFFD, // CJK extensions
	}
	for _, r := range wide {
		if got := Width(string(r)); got != 2 {
			t.Errorf("Width(%U) = %d, want 2: it is inside a wide range", r, got)
		}
	}

	// One step outside each range, which is what an off-by-one would move.
	narrow := []rune{
		0x10FF, 0x1160, 0x2E7F, 0xA4D0, 0xABFF, 0xD7A4, 0xF8FF, 0xFB00,
		0xFE2F, 0xFE70, 0xFEFF, 0xFF61, 0xFFDF, 0xFFE7, 0x1F2FF, 0x1FB00,
		0x1FFFF, 0x3FFFE,
	}
	for _, r := range narrow {
		if got := Width(string(r)); got == 2 {
			t.Errorf("Width(%U) = 2, but it is outside every wide range", r)
		}
	}
}
