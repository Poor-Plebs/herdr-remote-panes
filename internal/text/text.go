// Package text draws names this plugin did not write.
//
// Machine names come from ~/.ssh/config, and terminal names come from whatever
// is running on the far machine — a shell sets its own title as a matter of
// course. Both end up in a terminal here. A newline drew a row nothing knew
// about, an escape sequence changed the colour of everything after it, and a
// long name ran past the edge of what it was drawn into.
package text

import (
	"strings"
	"unicode"
)

// Sanitize makes a name safe to draw: one line, no control characters, no
// escape sequences.
func Sanitize(name string) string {
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
			// Which is what actually catches the two cases above: every one of
			// the sixty-five characters they name is non-graphic as well, so
			// removing either changes nothing. They stay because they say what
			// is being kept out and why, and because this line is one
			// unicode.IsGraphic away from being wrong about all of them.
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// Width is how many terminal cells a string occupies.
//
// Padding by rune count misaligns anything that is not narrow: East Asian
// characters and most emoji take two cells, and combining marks take none.
func Width(s string) int {
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

// Truncate shortens a string to at most width cells, marking that it was cut.
// Cutting by bytes or runes would overshoot on wide characters.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if Width(s) <= width {
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

// Pad pads a string on the right so columns line up, measuring cells rather
// than characters.
func Pad(s string, width int) string {
	if pad := width - Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// Wrap breaks a message into at most maxLines lines of the given width, on
// spaces where it can. The last line is cut with an ellipsis when there is more
// than will fit.
//
// Cutting a message to one line loses whatever is at the end, which for a
// message explaining why something failed is the half worth reading.
func Wrap(s string, width, maxLines int) []string {
	if width <= 0 || maxLines <= 0 {
		return nil
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}

	var lines []string
	line := ""
	for i := 0; i < len(words); i++ {
		word := words[i]
		switch {
		case line == "":
			line = word
		case Width(line)+1+Width(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = ""
			if len(lines) == maxLines {
				// Out of room, and there are words left to place.
				return cut(lines, width, true)
			}
			i-- // Place this word on the next line.
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return cut(lines, width, false)
}

// cut holds the wrapped lines to the width, marking the last one when the
// message did not fit.
func cut(lines []string, width int, more bool) []string {
	for i, l := range lines {
		lines[i] = Truncate(l, width)
	}
	if more && len(lines) > 0 {
		last := len(lines) - 1
		if !strings.HasSuffix(lines[last], "…") {
			lines[last] = Truncate(lines[last]+" …", width)
		}
	}
	return lines
}
