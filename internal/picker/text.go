package picker

import (
	"strings"
	"unicode"
)

// Machine names come from ~/.ssh/config and the plugin config, which this code
// does not control, and they are printed straight into a terminal. A name with
// a newline in it drew a second row that the menu did not know about, throwing
// off which entry the arrow keys were on; an escape sequence changed the colour
// of everything after it; and a long name ran past the edge of the popup.

// sanitizeName makes a name safe to draw: one line, no control characters, no
// escape sequences.
func sanitizeName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// C0 controls and DEL, which includes the escape that starts an
			// ANSI sequence and the newline that would draw another row.
			continue
		case r >= 0x80 && r <= 0x9f:
			// C1 controls, which some terminals also act on.
			continue
		case !unicode.IsGraphic(r) && r != ' ':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// displayWidth is how many terminal cells a string occupies.
//
// Padding by rune count misaligns anything that is not narrow: East Asian
// characters and most emoji take two cells, and combining marks take none.
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		width += runeWidth(r)
	}
	return width
}

func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r), unicode.Is(unicode.Cf, r):
		// Combining marks and format characters sit on the previous cell.
		return 0
	case isWide(r):
		return 2
	default:
		return 1
	}
}

// isWide reports the ranges a terminal draws in two cells. This covers the
// characters a machine name realistically contains rather than every case in
// the Unicode width tables.
func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF, // CJK radicals through Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // Fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1FAFF, // Emoji
		r >= 0x20000 && r <= 0x3FFFD: // CJK extensions
		return true
	}
	return false
}

// truncateToWidth shortens a string to at most width cells, marking that it was
// cut. Cutting by bytes or runes would overshoot on wide characters.
func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(s) <= width {
		return s
	}

	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runeWidth(r)
		if used+w > width-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

// padToWidth pads a string on the right so columns line up, measuring cells
// rather than characters.
func padToWidth(s string, width int) string {
	if pad := width - displayWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}
