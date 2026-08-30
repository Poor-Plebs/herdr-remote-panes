package text

import (
	"strings"
	"testing"
	"unicode"
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
		"":        0,
		"workbox": 7,

		// Characters that take no cell of their own. A width counted one too
		// high for any of these pads a column that is already full, so the
		// state beside it starts a column late on that row alone -- which is
		// how a table stops looking like one.
		"\x00":   0, // NUL, which a terminal draws nothing for
		"a\x00b": 2,
		"\u200b": 0, // a zero-width space
		"\ufeff": 0, // a byte-order mark, which arrives in pasted text

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

func TestNoRuneAtAllComesThroughAsSomethingTheTerminalActsOn(t *testing.T) {
	// The table above holds the cases somebody thought of. This holds the rest.
	//
	// What goes through here is a machine's idea of a terminal title and
	// whatever is written in ~/.ssh/config, so the input is not a list of
	// likely characters, it is every character there is. One that came through
	// as an escape or a newline would move the cursor in a menu that redraws
	// on every keypress -- which is how one name came to impersonate another.
	//
	// Wrapped in ordinary letters because the result is trimmed: a character
	// that survives at the end of a name would be trimmed away here and the
	// test would pass for the wrong reason.
	for r := rune(0); r <= 0x10FFFF; r++ {
		// Surrogates are not characters; a Go string cannot hold one, and
		// ranging over one yields the replacement character instead.
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		for _, out := range Sanitize("a" + string(r) + "b") {
			// Graphic is the whole contract, and it is wider than "not a
			// control character". U+202E turns the text after it round, U+200B
			// and U+FEFF take no space at all, and a name using either can be
			// made to read as another machine's while being a different string
			// -- which is the same impersonation a newline used to manage, by
			// quieter means.
			if !unicode.IsGraphic(out) {
				t.Fatalf("a name holding %U comes out holding %U, which is not "+
					"something a terminal draws", r, out)
			}
		}
	}
}

func TestAnEmojiVariationIsTwoCells(t *testing.T) {
	// A character with both a symbol form and an emoji form is drawn as emoji
	// when U+FE0F follows it, and a terminal gives emoji two cells. Counted
	// apart -- the character one, the selector nothing -- a label measures a
	// cell short of what is on the screen, and every column after it is out by
	// that much for as long as the label is there.
	//
	// This plugin's own workspace names use two of them: ☁ for a machine and
	// ⚠ for one that is down.
	for _, tt := range []struct {
		what  string
		in    string
		width int
	}{
		{"a symbol on its own", "☁", 1},
		{"the same asked for as emoji", "☁️", 2},
		{"a warning sign", "⚠", 1},
		{"the same as emoji", "⚠️", 2},
		{"emoji that needs no asking", "\U0001F680", 2},
		{"a selector after something already wide", "\U0001F680️", 2},
		{"a selector with nothing before it", "️", 0},
		{"in a label", "☁️ bot", 6},
		{"plain text is unchanged", "bot", 3},
	} {
		if got := Width(tt.in); got != tt.width {
			t.Errorf("%s: Width(%q) = %d, want %d", tt.what, tt.in, got, tt.width)
		}
	}
}

func TestCuttingAnEmojiLabelMeasuresItTheSameWay(t *testing.T) {
	// Truncate cuts to a number of cells, so it has to count them the way
	// Width does or it cuts at a column that is not where it looked.
	label := "☁️ production"
	for width := 2; width <= Width(label); width++ {
		if got := Width(Truncate(label, width)); got > width {
			t.Errorf("cut to %d cells and got %d: %q", width, got, Truncate(label, width))
		}
	}
}

func TestASkinToneIsPartOfTheEmojiItRecolours(t *testing.T) {
	// A modifier never stands alone: it recolours the emoji in front of it,
	// and the pair is drawn in the two cells that emoji already had. Counted
	// as a character of its own, a label measures two cells wider than the
	// screen shows -- and in a menu that pads every name to a column, every
	// column after it is out by that much.
	//
	// The same fault the emoji presentation selector is handled for, and
	// unlike a zero-width joiner there is nothing to weigh up: no terminal has
	// anything to draw for a modifier by itself.
	plain := "\U0001F44D"           // thumbs up
	toned := "\U0001F44D\U0001F3FD" // the same, recoloured
	if Width(plain) != 2 {
		t.Fatalf("the fixture is wrong: an emoji is %d cells", Width(plain))
	}
	if got := Width(toned); got != Width(plain) {
		t.Errorf("recolouring an emoji changed its width from %d to %d",
			Width(plain), got)
	}

	// And the column it is padded into is the width asked for, which is the
	// thing that goes wrong downstream.
	if got := Width(Pad(toned, 6)); got != 6 {
		t.Errorf("padding a recoloured emoji to six cells gave %d", got)
	}

	// Every tone, since they are a contiguous block and picking one would test
	// the boundary of nothing.
	for tone := rune(0x1F3FB); tone <= 0x1F3FF; tone++ {
		if got := Width(plain + string(tone)); got != 2 {
			t.Errorf("an emoji with tone %U is %d cells", tone, got)
		}
	}
	// The characters either side of the block are not tones and keep their own
	// width: a bound that swallows its neighbours is a different bug.
	for _, notATone := range []rune{0x1F3FA, 0x1F400} {
		if Width(string(notATone)) == 0 {
			t.Errorf("%U is not a skin tone and was given no width", notATone)
		}
	}
}
