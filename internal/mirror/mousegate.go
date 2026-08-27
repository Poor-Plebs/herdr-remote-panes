package mirror

import (
	"bytes"
	"io"
)

// mouseModes are the private modes that make a terminal report the mouse to
// whatever is running instead of selecting text with it.
//
// 1003 is the one that hurts most -- it reports every movement, so a terminal
// with it on hands over even a drag that was only ever going to be a
// selection.
var mouseModes = [][]byte{
	[]byte("1000"), // clicks
	[]byte("1002"), // clicks and drags
	[]byte("1003"), // every movement
	[]byte("1005"), // utf-8 coordinates
	[]byte("1006"), // SGR coordinates
	[]byte("1015"), // urxvt coordinates
	[]byte("1016"), // SGR pixel coordinates
}

// preambleLimit bounds how long a stream can go without anything that is not
// an escape sequence before this gives up and passes everything through. A
// preamble is a few hundred bytes; anything larger is not one, and holding a
// stream open looking for the end of it would be worse than letting it by.
const preambleLimit = 8 << 10

// mouseGate drops the mouse reporting an attach client turns on for itself.
//
// `herdr terminal attach` enables the lot -- 1000, 1002, 1003, 1015 and 1006 --
// in its opening handshake, before anything on the far side has asked for it,
// and leaves them on for the session. The terminal then gives every drag to
// the far side, so text cannot be selected out of a mirrored pane without
// holding whatever key the terminal uses to take the mouse back. Attaching
// over plain ssh does not do this, which is why the same machine behaves one
// way through this plugin and another way through a terminal.
//
// Only the handshake. Those enables arrive before any output does, so
// everything up to the first byte that is not part of an escape sequence is
// the client talking about itself, and everything after it is the far side.
// A program over there that wants the mouse -- vim, htop -- turns it on in
// its own output, which lands after that point and passes straight through.
// So the mouse still works where something is using it, and belongs to the
// terminal where nothing is.
//
// Attach only, and measured: `terminal session observe` sends no mouse
// sequences whatever. It streams the screen as rendered rather than the bytes
// that drew it, so mode changes are applied on the machine and never travel.
// Nothing to gate there, and gating it would be a guess about a stream that
// does not carry the thing being removed.
type mouseGate struct {
	w    io.Writer
	open bool // once the preamble has ended, everything passes untouched
	// held is an escape sequence split across writes. Passing on half of one
	// would leave the rest to be read as text.
	held []byte
	seen int
}

func newMouseGate(w io.Writer) *mouseGate { return &mouseGate{w: w} }

func (g *mouseGate) Write(p []byte) (int, error) {
	if g.open {
		return g.w.Write(p)
	}

	buf := p
	if len(g.held) > 0 {
		buf = make([]byte, 0, len(g.held)+len(p))
		buf = append(buf, g.held...)
		buf = append(buf, p...)
		g.held = nil
	}
	g.seen += len(p)

	out := make([]byte, 0, len(buf))
	for i := 0; i < len(buf); {
		if buf[i] != escape {
			// Output. The client has finished introducing itself.
			g.open = true
			out = append(out, buf[i:]...)
			break
		}
		size := escapeLen(buf[i:])
		if size < 0 {
			g.held = append([]byte(nil), buf[i:]...)
			break
		}
		if seq := buf[i : i+size]; !isMouseEnable(seq) {
			out = append(out, seq...)
		}
		i += size
	}

	// A preamble is a few hundred bytes. Something still introducing itself
	// after eight kilobytes is not one, and neither holding its tail nor
	// dropping what it sends is a thing to keep doing indefinitely.
	if !g.open && g.seen > preambleLimit {
		g.open = true
		out = append(out, g.held...)
		g.held = nil
	}

	if len(out) > 0 {
		if _, err := g.w.Write(out); err != nil {
			return 0, err
		}
	}
	// The caller's bytes were all dealt with, whether they were passed on or
	// dropped. Reporting fewer would be reporting a short write, which is an
	// error to whoever is copying into this.
	return len(p), nil
}

// flush writes back a sequence that was being held when the stream ended.
//
// Without it a stream that stops mid-escape loses its last few bytes -- which
// only happens on a preamble that never finished, but losing them silently is
// how a pane ends up missing something nobody can account for.
func (g *mouseGate) flush() {
	if len(g.held) > 0 {
		_, _ = g.w.Write(g.held)
		g.held = nil
	}
}

const escape = 0x1b

// escapeLen is the length of the escape sequence at the start of b, or -1 if b
// holds only part of one.
func escapeLen(b []byte) int {
	if len(b) < 2 {
		return -1
	}
	switch b[1] {
	case '[':
		// CSI: parameters, then a byte from @ to ~ that ends it.
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return i + 1
			}
		}
		return -1
	case ']':
		// OSC: ends at BEL, or at ST, which is itself an escape sequence.
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				return i + 1
			}
			if b[i] == escape {
				if i+1 >= len(b) {
					return -1
				}
				if b[i+1] == '\\' {
					return i + 2
				}
			}
		}
		return -1
	default:
		// Two bytes: ESC and one more.
		return 2
	}
}

// isMouseEnable reports whether seq turns mouse reporting on and nothing else.
//
// Nothing else matters: a terminal may be told to set several modes at once,
// and dropping "?1000;25h" to be rid of the mouse would take the cursor with
// it. Only a sequence whose every mode is a mouse mode can go.
func isMouseEnable(seq []byte) bool {
	if len(seq) < 5 || seq[0] != escape || seq[1] != '[' || seq[2] != '?' {
		return false
	}
	if seq[len(seq)-1] != 'h' {
		return false
	}
	params := seq[3 : len(seq)-1]
	if len(params) == 0 {
		return false
	}
	for _, mode := range bytes.Split(params, []byte(";")) {
		if !isMouseMode(mode) {
			return false
		}
	}
	return true
}

func isMouseMode(mode []byte) bool {
	for _, known := range mouseModes {
		if bytes.Equal(mode, known) {
			return true
		}
	}
	return false
}
