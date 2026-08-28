package picker

import (
	"bytes"
	"strings"
	"testing"
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
