// Package picker draws the machine menu shown in a Herdr popup pane.
package picker

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/sshconfig"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/version"
)

// Entry is one machine offered in the menu.
type Entry struct {
	Target string
	// Label is how the machine is named in Herdr, when it is configured.
	Label string
	// Configured marks a machine listed in the plugin config rather than only
	// in the SSH config.
	Configured bool
	Connected  bool
	Mirrors    int
	// Terminals is how many plain SSH terminals the machine has open, which is
	// what a machine in SSH mode has instead of mirrors.
	Terminals int
	SSHOnly   bool
	// Mirroring reports whether this machine's terminals are kept in step,
	// rather than being a plain SSH session.
	Mirroring bool
	// GaveUp marks a machine that could not be reached and is no longer being
	// retried until it is connected to again.
	GaveUp bool
}

// Connect asks the daemon to connect to a machine.
type Connect func(target string) (string, error)

// SetMode asks the daemon to change how a machine is reached.
type SetMode func(target, mode string) (string, error)

// Run draws the menu and connects to whatever the user picks. It returns when
// the user chooses or cancels; the pane closes as soon as it returns.
func Run(connect Connect, setMode SetMode) error {
	entries, warning := collect()
	if len(entries) == 0 {
		// With no menu to put it in, a warning still has to be said somewhere.
		if warning != "" {
			fmt.Printf("\r\n  %s\r\n", warning)
		}
		fmt.Print("\r\nNo machines found.\r\n\r\nAdd hosts to ~/.ssh/config or to the plugin's config.json.\r\n")
		waitForKey()
		return nil
	}

	restore := rawMode()
	defer restore()

	selected := 0
	for {
		draw(entries, selected, warning)

		key := readKey()
		switch key {
		case keyUp:
			selected = (selected - 1 + len(entries)) % len(entries)
		case keyDown:
			selected = (selected + 1) % len(entries)
		case keyPageUp:
			selected = move(selected, -pageStep(), len(entries))
		case keyPageDown:
			selected = move(selected, pageStep(), len(entries))
		case keyTop:
			selected = 0
		case keyBottom:
			selected = len(entries) - 1
		case keyQuit:
			clear()
			return nil
		case keyEnter:
			return choose(entries[selected], connect)
		case keyToggle:
			// Toggling in place, rather than closing the menu, so the change
			// and its effect are visible together.
			entry := entries[selected]
			mode := "attach"
			if entry.Mirroring {
				mode = "ssh"
			}
			if _, err := setMode(entry.Target, mode); err != nil {
				clear()
				fmt.Printf("\r\n  Could not change %s: %v\r\n\r\n  Press any key.\r\n", entry.Target, err)
				readKey()
			}
			entries, warning = collect()
			if selected >= len(entries) {
				selected = 0
			}
		default:
			// Digits jump straight to an entry.
			if index := int(key - '1'); index >= 0 && index < len(entries) && index < 9 {
				return choose(entries[index], connect)
			}
		}
	}
}

func choose(entry Entry, connect Connect) error {
	clear()
	fmt.Printf("  Connecting to %s ...\r\n", entry.Target)

	message, err := connect(entry.Target)
	if err != nil {
		fmt.Printf("\r\n  Could not connect: %v\r\n\r\n  Press any key.\r\n", err)
		waitForKey()
		return nil
	}
	fmt.Printf("\r\n  %s\r\n", message)
	return nil
}

// collect merges the machines from the SSH config with those in the plugin
// config, and marks which are already connected.
func collect() ([]Entry, string) {
	// A config that cannot be read would otherwise drop every machine that is
	// only listed there, leaving the menu quietly incomplete.
	warning := ""
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Defaults()
		warning = fmt.Sprintf("Could not read the plugin config, so only ~/.ssh/config machines are listed: %v", err)
	} else if problems := cfg.Problems(); len(problems) > 0 {
		// A setting that reads fine but means something else is worth saying
		// once, where the machines are chosen.
		warning = "Check the plugin config: " + strings.Join(problems, "; ")
	}

	byTarget := map[string]*Entry{}
	var order []string
	add := func(target string) *Entry {
		if existing, ok := byTarget[target]; ok {
			return existing
		}
		entry := &Entry{Target: target}
		byTarget[target] = entry
		order = append(order, target)
		return entry
	}

	for _, host := range cfg.Hosts {
		if host.Disabled {
			continue
		}
		entry := add(host.Target)
		entry.Configured = true
		entry.Label = host.DisplayLabel()
		entry.Mirroring = cfg.Mirrors(host)
	}
	for _, host := range sshconfig.Hosts() {
		// An alias ssh would read as an option is not a machine anyone can
		// connect to, so offering it would only produce a refusal later.
		if config.ValidTarget(host) != nil {
			continue
		}
		add(host)
	}

	hosts, stale := status()
	if stale != "" {
		// Worth saying where machines are picked: an update that has not taken
		// effect looks exactly like a fix that did not work.
		if warning == "" {
			warning = stale
		} else {
			warning += " · " + stale
		}
	}
	for _, info := range hosts {
		if entry, ok := byTarget[info.Target]; ok {
			entry.Connected = info.Connected
			entry.Mirrors = info.Mirrors
			entry.Terminals = info.Terminals
			entry.SSHOnly = info.SSHOnly
			entry.Mirroring = info.Mirroring
			entry.GaveUp = info.GaveUp
		}
	}

	entries := make([]Entry, 0, len(order))
	for _, target := range order {
		entries = append(entries, *byTarget[target])
	}
	// Configured machines first; they are the ones being worked on.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Configured && !entries[j].Configured
	})
	return entries, warning
}

// status asks the daemon what it is currently mirroring. A daemon that is not
// running is not an error here: every machine simply shows as unconnected.
// status reports the machines the daemon is tracking, and anything about the
// daemon itself worth putting in front of someone opening the menu.
func status() ([]syncd.HostInfo, string) {
	reply, err := syncd.Ask(syncd.Command{Cmd: "status"})
	if err != nil {
		return nil, ""
	}
	return reply.Hosts, version.StaleMessage(reply.Revision)
}

const (
	esc     = "\x1b"
	reset   = esc + "[0m"
	dim     = esc + "[2m"
	bold    = esc + "[1m"
	green   = esc + "[32m"
	yellow  = esc + "[33m"
	red     = esc + "[31m"
	reverse = esc + "[7m"
)

func clear() {
	fmt.Print(esc + "[2J" + esc + "[H")
}

// move shifts the selection by n, stopping at either end rather than wrapping,
// which is what paging past the edge should do.
func move(selected, n, count int) int {
	next := selected + n
	if next < 0 {
		return 0
	}
	if next >= count {
		return count - 1
	}
	return next
}

// pageStep is how far a page key moves, derived from the popup height.
func pageStep() int {
	_, rows := windowSize()
	step := rows - 4
	if step < 1 {
		step = 1
	}
	return step
}

// nameWidth is how much room the machine column gets, leaving space for the
// marker, the number and the state that follows it.
func nameWidth(cols int) int {
	const chrome = 8 // marker, number and the spaces between columns
	const state = 26 // the widest state text, "connected · NN mirrored"
	width := cols - chrome - state
	if width < 8 {
		width = 8
	}
	if width > 40 {
		width = 40
	}
	return width
}

// visibleWindow picks the slice of entries to show, keeping the selected one
// on screen.
//
// The menu runs in a popup whose height it does not control, and writing more
// lines than fit scrolls the top away — taking the first machine, and the
// heading, with it. So the list is windowed rather than assumed to fit.
// maxWarningLines bounds how much of the popup a warning may take.
const maxWarningLines = 2

// layout is what fits in a popup of a given height: which slice of the machines
// to draw, and which of the surrounding lines there is room for.
type layout struct {
	first, last int
	counter     bool
	hints       bool
	// warning is how many lines of the warning there is room for.
	warning int
}

// planLayout decides the whole frame in one place. It used to be split between
// a row budget here and the drawing itself, and the two drifted: the budget did
// not know about the "showing x-y of z" line, so once the list scrolled the
// menu was a line taller than the popup and the heading scrolled away.
//
// When everything will not fit, the key hints go first and the warning second.
// The machines are what the menu is for, and a warning is worth more than a
// reminder of which keys move the selection.
func planLayout(count, selected, rows, warnLines int) layout {
	if rows < 1 {
		rows = 1
	}
	// The heading and the blank line under it are always drawn.
	const heading = 2

	// What to give up, in order. The key hints go first, then the warning a
	// line at a time: the machines are what the menu is for, a warning is worth
	// more than a reminder of which keys move the selection, and half a warning
	// is worth more than none.
	options := []struct {
		hints   bool
		warning int
	}{
		{true, warnLines}, {false, warnLines},
	}
	for lines := warnLines - 1; lines >= 0; lines-- {
		options = append(options, struct {
			hints   bool
			warning int
		}{false, lines})
	}

	for _, opt := range options {
		chrome := heading
		if opt.hints {
			chrome += 3 // a blank separator and two lines of hints
		}
		if opt.warning > 0 {
			chrome += opt.warning + 1 // the warning and the blank line under it
		}

		if visible := rows - chrome; visible >= 1 && visible >= count {
			return layout{first: 0, last: count, hints: opt.hints, warning: opt.warning}
		}
		visible := rows - chrome - 1 // the range counter needs a row too
		if visible < 1 {
			continue
		}

		first := selected - visible/2
		if first < 0 {
			first = 0
		}
		if first+visible > count {
			first = count - visible
		}
		if first < 0 {
			first = 0
		}
		return layout{
			first: first, last: first + visible,
			counter: true, hints: opt.hints, warning: opt.warning,
		}
	}

	// Nothing fits properly; show the selected machine and nothing else.
	if selected < 0 || selected >= count {
		return layout{first: 0, last: count}
	}
	return layout{first: selected, last: selected + 1}
}

func draw(entries []Entry, selected int, warning string) {
	cols, rows := windowSize()
	fmt.Print(render(entries, selected, cols, rows, warning))
}

// render draws the menu into a string. Keeping it separate from the terminal it
// is printed to is what makes the layout checkable: alignment, truncation and
// the wide characters in a host name are otherwise only ever seen by eye.
func render(entries []Entry, selected, cols, rows int, warning string) string {
	// Wrapped rather than cut to one line: a warning that explains why
	// something failed keeps the reason at the end, which is the half worth
	// reading.
	warned := text.Wrap(text.Sanitize(warning), cols-4, maxWarningLines)
	frame := planLayout(len(entries), selected, rows, len(warned))
	first, last := frame.first, frame.last

	var b strings.Builder
	b.WriteString(esc + "[2J" + esc + "[H")
	b.WriteString("  " + bold + "Connect to a machine" + reset + "\r\n\r\n")
	if frame.warning > 0 {
		// Shown in the menu rather than on a screen that has to be dismissed
		// first: a problem worth mentioning every time is not worth
		// interrupting every time.
		for _, line := range warned[:frame.warning] {
			b.WriteString("  " + yellow + line + reset + "\r\n")
		}
		b.WriteString("\r\n")
	}

	for i := first; i < last; i++ {
		entry := entries[i]
		marker := "  "
		line := ""
		if i == selected {
			marker = reverse + " >" + reset
		}
		number := "  "
		if i < 9 {
			number = fmt.Sprintf("%d.", i+1)
		}

		// Names come from ~/.ssh/config, so they are made safe to draw and cut
		// to fit rather than trusted to be short and printable.
		name := text.Sanitize(entry.Target)
		if entry.Label != "" && entry.Label != entry.Target {
			name = fmt.Sprintf("%s (%s)", name, text.Sanitize(entry.Label))
		}
		name = text.Pad(text.Truncate(name, nameWidth(cols)), nameWidth(cols))

		mode := "ssh"
		if entry.Mirroring {
			mode = "mirrored"
		}
		switch {
		case entry.GaveUp:
			line = red + "unreachable" + reset + dim + " · enter to retry" + reset
		case entry.Connected && entry.Mirroring:
			line = green + fmt.Sprintf("connected · %d mirrored", entry.Mirrors) + reset
		case entry.Connected && entry.Terminals > 0:
			line = green + fmt.Sprintf("connected · %d open", entry.Terminals) + reset
		case entry.Connected:
			line = green + "connected" + reset + dim + " · ssh" + reset
		case entry.Configured:
			line = yellow + "not connected" + reset + dim + " · " + mode + reset
		default:
			line = dim + "from ~/.ssh/config · " + mode + reset
		}

		b.WriteString(marker + " " + number + " " + name + " " + line + "\r\n")
	}

	if frame.counter {
		b.WriteString("  " + dim + fmt.Sprintf("showing %d-%d of %d", first+1, last, len(entries)) + reset + "\r\n")
	}
	if frame.hints {
		b.WriteString("\r\n")
		b.WriteString("  " + dim + "↑↓ jk move · pgup/pgdn g/G jump · 1-9 pick · enter connect" + reset + "\r\n")
		b.WriteString("  " + dim + "m toggle mirroring (experimental) · q cancel" + reset)
	}
	return strings.TrimSuffix(b.String(), "\r\n")
}

// windowSize reports the popup's size, falling back to something sensible when
// the terminal cannot be queried.
func windowSize() (cols, rows int) {
	cols, rows = 80, 20
	out, err := sttyOutput("size")
	if err != nil {
		return cols, rows
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return cols, rows
	}
	if r, err := strconv.Atoi(fields[0]); err == nil && r > 0 {
		rows = r
	}
	if c, err := strconv.Atoi(fields[1]); err == nil && c > 0 {
		cols = c
	}
	return cols, rows
}

type key rune

const (
	keyUp       key = 0xE000
	keyDown     key = 0xE001
	keyEnter    key = 0xE002
	keyQuit     key = 0xE003
	keyNone     key = 0xE004
	keyToggle   key = 0xE005
	keyPageUp   key = 0xE006
	keyPageDown key = 0xE007
	keyTop      key = 0xE008
	keyBottom   key = 0xE009
)

// readKey reads one keypress from the popup.
func readKey() key {
	return parseKey(os.Stdin)
}

// parseKey reads one keypress, translating the escape sequences a terminal
// sends for arrows and paging.
//
// Both cursor-key encodings are handled: a terminal in application mode sends
// ESC O A for Up rather than ESC [ A, and reading only the second form leaves
// the arrow keys dead with no clue why.
func parseKey(r io.Reader) key {
	var buf [1]byte
	read := func() (byte, bool) {
		n, err := r.Read(buf[:])
		return buf[0], err == nil && n == 1
	}

	first, ok := read()
	if !ok {
		return keyQuit
	}

	switch first {
	case '\r', '\n':
		return keyEnter
	case 'q', 'Q', 3: // 3 is ctrl+c
		return keyQuit
	case 'm', 'M':
		return keyToggle
	case 'k':
		return keyUp
	case 'j':
		return keyDown
	case 'g':
		return keyTop
	case 'G':
		return keyBottom
	case 0x1b:
		// Bare Escape, or the start of a sequence.
		intro, ok := read()
		if !ok {
			return keyQuit
		}
		if intro != '[' && intro != 'O' {
			return keyQuit
		}
		code, ok := read()
		if !ok {
			return keyQuit
		}
		switch code {
		case 'A':
			return keyUp
		case 'B':
			return keyDown
		case 'H':
			return keyTop
		case 'F':
			return keyBottom
		case '5', '6', '1', '4':
			// A numeric sequence, terminated by '~'.
			if tilde, ok := read(); !ok || tilde != '~' {
				return keyNone
			}
			switch code {
			case '5':
				return keyPageUp
			case '6':
				return keyPageDown
			case '1':
				return keyTop
			case '4':
				return keyBottom
			}
		}
		return keyNone
	}
	return key(first)
}

func waitForKey() {
	restore := rawMode()
	defer restore()
	readKey()
}

// rawMode puts the popup's terminal into raw mode so keys arrive unbuffered
// and are not echoed. stty keeps this dependency-free.
func rawMode() func() {
	if _, err := sttyOutput("raw", "-echo"); err != nil {
		return func() {}
	}
	return func() { _, _ = sttyOutput("sane") }
}

func sttyOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = nil
	return cmd.Output()
}
