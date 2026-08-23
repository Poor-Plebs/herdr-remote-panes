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
		{"C1 controls are stripped", "botmore", "botmore"},
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
