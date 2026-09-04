package picker

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
)

// The menu reads raw bytes from a terminal, and not all of them were typed by
// the person at it. Two of this plugin's real bugs were here: ctrl+up arrives
// as ESC [ 1 ; 5 A, and the "5" left behind after a half-read sequence was
// taken as picking the fifth machine, which connects to it; and a paste
// arrived as its own characters, so text holding a "d" disconnected whatever
// the cursor was on. Both were quiet -- something happened, and nothing said
// what or why.

// acts reports whether a key does something a person would have to undo.
func acts(k key) bool {
	switch k {
	case keyEnter, keyDisconnect, keyToggle:
		return true
	}
	// A digit picks a machine and connects to it.
	return k >= '1' && k <= '9'
}

func FuzzAPasteNeverPressesAnything(f *testing.F) {
	for _, seed := range []string{
		"", "hello", "d", "5", "\r", "ssh bot\nd\n5\n",
		"\x1b[A\x1b[B", "m", "q\rd", strings.Repeat("d5\r", 40),
		"\x1b", "\x1b[", "\x1b[2", "\x1bOA",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, pasted string) {
		// Content holding the end marker ends the paste early, and what follows
		// is typing as far as any terminal is concerned. That is bracketed
		// paste's own limit, not this menu's, so it is not claimed here.
		if strings.Contains(pasted, "\x1b[201~") {
			t.Skip("the content ends its own paste")
		}

		// A paste as a terminal delivers one, then a key that is unmistakably
		// typed, so the drain has somewhere to stop.
		stream := "\x1b[200~" + pasted + "\x1b[201~" + "q"
		r := bytes.NewReader([]byte(stream))

		for i := 0; i < len(stream)+8; i++ {
			k := parseKey(r)
			if k == keyQuit {
				return
			}
			if acts(k) {
				t.Fatalf("pasting %q pressed %v", pasted, k)
			}
		}
		t.Fatalf("draining %q never reached the keypress after it", pasted)
	})
}

func FuzzParseKeyDrainsWhatItReads(f *testing.F) {
	for _, seed := range []string{
		"", "q", "\x1b", "\x1b[", "\x1b[A", "\x1bOA", "\x1b[1;5A", "\x1b[5~",
		"\x1b[200~abc\x1b[201~", "\x1b[" + strings.Repeat("1;", 200) + "A",
		"\x00\x01\x02", "日本語", "\x1b[999999999~",
		// What a terminal sends that is not typing. The first is a click in
		// the older encoding, whose three raw bytes were read as three
		// keystrokes until this week.
		"\x1b[M \x21\x21", "\x1b[M \x30\x21", "\x1b[<0;21;5M", "\x1b[I",
		"\x1b]11;rgb:1c/1c/1c\x07", "\x1bP1$r0m\x1b\\", "\x1b[12;40R",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		r := bytes.NewReader([]byte(in))

		// Every call consumes at least one byte, or reports the stream ended.
		// Without that this loop is the menu's own loop, and it would spin.
		for i := 0; ; i++ {
			before := r.Len()
			k := parseKey(r)
			if k == keyQuit && before == 0 {
				return
			}
			if r.Len() == before {
				t.Fatalf("parseKey read nothing from %q at byte %d and returned %v",
					in, len(in)-before, k)
			}

			// And a call that began at an escape came back as a key rather
			// than as a character.
			//
			// Weaker than it looks, and worth saying so. It does not catch the
			// click that was read as three keystrokes this week: that call
			// began at the escape and returned nothing, correctly, and it was
			// the three bytes left behind that were read as typing -- by later
			// calls beginning at a space, where this says nothing. Removing
			// the fix leaves this passing, which I checked rather than
			// assumed.
			//
			// What holds the click is the table beside it, which drives each
			// kind of thing a terminal sends and asserts nothing is left over.
			// This holds the narrower thing: bytes inside a sequence never
			// come back as a character from the call that read it.
			if in[len(in)-before] == 0x1b {
				switch k {
				case keyUp, keyDown, keyEnter, keyQuit, keyToggle, keyDisconnect,
					keyTop, keyBottom, keyPageUp, keyPageDown, keyNone:
				default:
					t.Fatalf("parseKey(%q) gave %q for the sequence at byte %d: "+
						"a sequence came back as a character, which the menu acts "+
						"on as typing", in, rune(k), len(in)-before)
				}
			}
			if i > len(in)+8 {
				t.Fatalf("draining %q took more calls than it has bytes", in)
			}
		}
	})
}

// ownEscapes takes out every escape sequence the menu emits for itself, so that
// anything left came from a machine's name.
//
// It cannot work the other way round, and that is the whole of its limit: it
// removes those sequences BY VALUE, so one that arrived in a name is taken out
// exactly as if the menu had written it. Measured, with displayName's Sanitize
// removed: the frame grows from 18 escape bytes to 19, the machine's own
// "\x1b[31m" is in it, and after this nothing is left to find. The seed with
// "\x1b[31mprod" in it is the one this is blindest to. What covers that is the
// comparison below, which needs no such list.
func ownEscapes(s string) string {
	for _, own := range []string{esc + "[2J", esc + "[H", reset, dim, bold, green, yellow, red, reverse} {
		s = strings.ReplaceAll(s, own, "")
	}
	return s
}

// FuzzTheMenuFitsThePopupAndCarriesNothingFromAName draws the menu from names
// nobody would write and sizes nobody would use.
//
// Two properties, both about the terminal rather than the menu. A line wider
// than the popup wraps, which makes the menu a row taller than the layout
// planned for and scrolls the bottom of it away -- that is how the "showing
// 1-3 of 6" counter running off the side was found, by sweeping sizes with
// fixed names. This varies the names instead: they come out of ~/.ssh/config,
// which is a file, and a name of the wrong width is how the arithmetic that
// pads a column gets it wrong.
//
// And nothing from a name may reach the terminal as an escape. Sanitize is
// what stops it; this is the check that the menu actually calls it, on every
// piece of a machine it draws, and not merely on the one that had a test.
func FuzzTheMenuFitsThePopupAndCarriesNothingFromAName(f *testing.F) {
	f.Add("bot", "the label", "connection refused", 80, 24, 0)
	f.Add("\x1b[31mprod", "a\nb", "\x1b]0;title\x07", 40, 6, 1)
	f.Add("日本語のマシン", "🚀", "ﬀ", 16, 1, 2)
	// The menu's own sequences, in every field, which is the case ownEscapes
	// is blind to and the comparison below is not.
	f.Add(esc+"[2Jbot", esc+"[Hlabel", esc+"[31mrefused", 80, 24, 0)
	f.Add("", "", "", 200, 60, 0)

	f.Fuzz(func(t *testing.T, target, label, reason string, cols, rows, selected int) {
		// The range the layout says it serves: below this the machine line is
		// documented to wrap rather than shrink further. See nameWidth.
		cols = chromeWidth + 8 + mod(cols, 200)
		rows = 1 + mod(rows, 60)

		entries := []Entry{
			{Target: target, Label: label, Configured: true, Connected: true, Mirroring: true, Mirrors: 2},
			{Target: target + label, Configured: true, GaveUp: true, Reason: reason},
			{Target: "plain", Configured: true},
		}
		drawn := render(entries, mod(selected, len(entries)), cols, rows, "")

		for _, line := range strings.Split(drawn, "\r\n") {
			if got := text.Width(visible(line)); got > cols {
				t.Fatalf("at %d columns a line is %d wide: %q", cols, got, visible(line))
			}
		}

		// Pre-sanitising what goes IN must not change what comes out, and that
		// is the check with no list of exceptions in it. Every piece the menu
		// draws from a machine goes through text.Sanitize, so handing it the
		// sanitised form already can only give the same frame -- unless some
		// piece is drawn raw, and then the two differ wherever it is, whatever
		// the bytes happen to be. It uses Sanitize itself rather than a second
		// copy of its rule, so it cannot drift from it either.
		clean := []Entry{
			{Target: text.Sanitize(target), Label: text.Sanitize(label),
				Configured: true, Connected: true, Mirroring: true, Mirrors: 2},
			{Target: text.Sanitize(target + label), Configured: true, GaveUp: true,
				Reason: text.Sanitize(reason)},
			{Target: "plain", Configured: true},
		}
		// Self-verifying: the comparison means nothing unless sanitising twice
		// is the same as sanitising once, so say so here rather than assume it.
		for _, s := range []string{target, label, reason} {
			if once := text.Sanitize(s); text.Sanitize(once) != once {
				t.Fatalf("Sanitize(%q) is not settled after one pass: %q then %q",
					s, once, text.Sanitize(once))
			}
		}
		if other := render(clean, mod(selected, len(clean)), cols, rows, ""); other != drawn {
			t.Fatalf("the frame changes when the names are sanitised before they go "+
				"in, so some piece of a machine is drawn raw:\nraw:   %q\nclean: %q",
				drawn, other)
		}

		left := ownEscapes(drawn)
		if i := strings.IndexByte(left, 0x1b); i >= 0 {
			t.Fatalf("an escape from a machine reached the screen at %d: %q", i, left)
		}
		for _, line := range strings.Split(left, "\r\n") {
			for _, r := range line {
				if r < 0x20 || r == 0x7f {
					t.Fatalf("a control character %q from a machine reached the screen: %q", r, line)
				}
			}
		}
	})
}

// mod keeps a fuzzed int inside a range, negatives included.
func mod(n, size int) int {
	if n < 0 {
		n = -n
	}
	if n < 0 || size <= 0 { // math.MinInt negated is itself
		return 0
	}
	return n % size
}
