package text

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// These functions decide what the menu looks like, and every one of them takes
// text this plugin did not write: a machine's label from ~/.ssh/config, a
// terminal's own title, an ssh error. Their contracts are what stop a name from
// running off the edge of a popup or wrapping mid-character, so the contracts
// are worth holding against input nobody thought of rather than against the
// handful of cases somebody did.

func FuzzSanitize(f *testing.F) {
	for _, seed := range []string{
		"", "bot", "my work", "a\tb", "a\nb", "\x1b[31mred\x1b[0m", "\x00\x01",
		"日本語", "é", "🇬🇧", "\xff\xfe", strings.Repeat("x", 500),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := Sanitize(in)

		// It is drawn into a line of a popup, so it must be one line.
		if strings.ContainsAny(got, "\n\r") {
			t.Fatalf("Sanitize(%q) = %q, which spans lines", in, got)
		}
		// Nothing that steers the terminal, and nothing a terminal will not
		// draw: these end up in a pane's name and in a workspace's, both drawn
		// straight into the sidebar. Wider than "not a control character" on
		// purpose -- U+202E turns what follows it round, and U+200B takes no
		// room at all, so a name using either can be made to read as another
		// machine's while being a different string.
		for _, r := range got {
			if !unicode.IsGraphic(r) {
				t.Fatalf("Sanitize(%q) = %q, which still holds %U", in, got, r)
			}
		}
		if !utf8.ValidString(got) {
			t.Fatalf("Sanitize(%q) = %q, which is not valid UTF-8", in, got)
		}
		// Cleaning something already clean must not change it, or a name would
		// drift every time it passed through.
		if again := Sanitize(got); again != got {
			t.Fatalf("Sanitize is not settled: %q -> %q -> %q", in, got, again)
		}
	})
}

func FuzzTruncate(f *testing.F) {
	for _, seed := range []string{"", "bot", "日本語です", "éx", "🇬🇧🇬🇧", strings.Repeat("ab", 80)} {
		for _, width := range []int{-1, 0, 1, 2, 3, 8, 200} {
			f.Add(seed, width)
		}
	}
	f.Fuzz(func(t *testing.T, in string, width int) {
		got := Truncate(in, width)

		// The whole point: it fits. A pane name one cell too wide pushes the
		// popup's border off the screen.
		if width >= 0 && Width(got) > width {
			t.Fatalf("Truncate(%q, %d) = %q, which is %d cells wide", in, width, got, Width(got))
		}
		// Cutting is by cells, and a cell can be several bytes, so the risk is
		// stopping halfway through one. Only claimed for text that was valid
		// going in: fixing an encoding is Sanitize's job, and every caller
		// carrying text from outside runs that first.
		if utf8.ValidString(in) && !utf8.ValidString(got) {
			t.Fatalf("Truncate(%q, %d) = %q, which was cut mid-character", in, width, got)
		}
		// Something that already fits is left alone -- a name that fits must
		// not pick up an ellipsis. Not claimed at width zero, where there is
		// nowhere to put anything and the answer is always empty.
		if width >= 1 && Width(in) <= width && got != in {
			t.Fatalf("Truncate(%q, %d) = %q, but it already fit", in, width, got)
		}
		// And something that was cut says so, so a shortened name is never read
		// as the whole name.
		if width >= 1 && Width(in) > width && !strings.HasSuffix(got, "…") {
			t.Fatalf("Truncate(%q, %d) = %q, with nothing to show it was cut", in, width, got)
		}
	})
}

func FuzzPad(f *testing.F) {
	for _, seed := range []string{"", "bot", "日本語", "é"} {
		for _, width := range []int{-1, 0, 1, 10} {
			f.Add(seed, width)
		}
	}
	f.Fuzz(func(t *testing.T, in string, width int) {
		got := Pad(in, width)

		// Columns line up only if padding lands on the cell, not the rune.
		if want := Width(in); want < width && Width(got) != width {
			t.Fatalf("Pad(%q, %d) = %q, %d cells wide", in, width, got, Width(got))
		}
		// Padding adds; it never cuts.
		if !strings.HasPrefix(got, in) {
			t.Fatalf("Pad(%q, %d) = %q, which does not start with the input", in, width, got)
		}
	})
}

func FuzzWrap(f *testing.F) {
	for _, seed := range []string{
		"", "one two three", strings.Repeat("word ", 40),
		"a-very-long-unbreakable-token-that-cannot-be-split-anywhere",
		"日本語 の テキスト", "\x1b[31m", strings.Repeat("🇬🇧", 40),
	} {
		for _, width := range []int{-1, 0, 1, 2, 10, 80} {
			for _, maxLines := range []int{0, 1, 2, 5} {
				f.Add(seed, width, maxLines)
			}
		}
	}
	f.Fuzz(func(t *testing.T, in string, width, maxLines int) {
		if width > 4096 || maxLines > 4096 {
			t.Skip("beyond any terminal, and only slows the fuzzer down")
		}
		got := Wrap(in, width, maxLines)

		if maxLines >= 0 && len(got) > maxLines {
			t.Fatalf("Wrap(%q, %d, %d) gave %d lines", in, width, maxLines, len(got))
		}
		for _, line := range got {
			// Every line has to fit the popup, including one holding a single
			// word longer than the popup is wide.
			if width >= 1 && Width(line) > width {
				t.Fatalf("Wrap(%q, %d, %d) produced %q, %d cells wide",
					in, width, maxLines, line, Width(line))
			}
			if strings.ContainsAny(line, "\n\r") {
				t.Fatalf("Wrap produced a line that spans lines: %q", line)
			}
			// As with Truncate: valid in, valid out. Sanitize is what makes
			// text valid, and every caller with text from outside runs it.
			if utf8.ValidString(in) && !utf8.ValidString(line) {
				t.Fatalf("Wrap(%q, %d, %d) broke a character: %q", in, width, maxLines, line)
			}
		}
	})
}

func FuzzTheDrawingPipeline(f *testing.F) {
	// Sanitize is what stands between a name from outside and the terminal, and
	// the two that follow it are what keep it inside the popup. Held together
	// rather than one at a time, because that is the order they run in and the
	// only order in which the whole claim -- one line, no escapes, valid, and
	// no wider than asked -- is true.
	for _, seed := range []string{
		"bot", "\xff\xfe", "\x1b[31mred", "日本語", "caf\xe9", "a\nb\tc",
	} {
		for _, width := range []int{1, 4, 20} {
			f.Add(seed, width)
		}
	}
	f.Fuzz(func(t *testing.T, in string, width int) {
		if width < 1 || width > 4096 {
			t.Skip("not a popup width")
		}
		clean := Sanitize(in)

		cut := Truncate(clean, width)
		if !utf8.ValidString(cut) || Width(cut) > width {
			t.Fatalf("Sanitize+Truncate(%q, %d) = %q (%d cells)", in, width, cut, Width(cut))
		}
		if Width(Pad(cut, width)) != width && Width(cut) < width {
			t.Fatalf("Sanitize+Truncate+Pad(%q, %d) does not fill the column", in, width)
		}

		for _, line := range Wrap(clean, width, 4) {
			if !utf8.ValidString(line) || Width(line) > width {
				t.Fatalf("Sanitize+Wrap(%q, %d) gave %q (%d cells)", in, width, line, Width(line))
			}
			if strings.ContainsAny(line, "\n\r") || strings.ContainsRune(line, 0x1b) {
				t.Fatalf("Sanitize+Wrap(%q, %d) gave %q, which steers the terminal", in, width, line)
			}
		}
	})
}
