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

// Disconnect closes a machine's panes here. The work on the machine is left
// running, so this undoes the view rather than the session.
type Disconnect func(target string) (string, error)

// Run draws the menu and connects to whatever the user picks. It returns when
// the user chooses or cancels; the pane closes as soon as it returns.
func Run(connect Connect, setMode SetMode, disconnect Disconnect) error {
	entries, warning := collect()
	if len(entries) == 0 {
		// With no menu to put it in, a warning still has to be said somewhere.
		body := []string{"Add hosts to ~/.ssh/config or to the plugin's config.json."}
		if warning != "" {
			body = append(body, warning)
		}
		notice("No machines found.", body...)
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
			selected = move(selected, -pageStep(entries, selected, warning), len(entries))
		case keyPageDown:
			selected = move(selected, pageStep(entries, selected, warning), len(entries))
		case keyTop:
			selected = 0
		case keyBottom:
			selected = len(entries) - 1
		case keyQuit:
			clear()
			return nil
		case keyEnter:
			return choose(entries[selected], connect)
		case keyDisconnect:
			// Closing the panes here, not the work there, so this is
			// recoverable: enter brings the machine back with its terminals.
			entry := entries[selected]
			if !entry.Connected && !entry.GaveUp {
				break
			}
			if _, err := disconnect(entry.Target); err != nil {
				notice("Could not disconnect "+text.Sanitize(entry.Target),
					err.Error(), "Press any key.")
				readKey()
			}
			entries, warning = collect()
			if selected >= len(entries) {
				selected = 0
			}

		case keyToggle:
			// Toggling in place, rather than closing the menu, so the change
			// and its effect are visible together.
			entry := entries[selected]
			mode := "attach"
			if entry.Mirroring {
				mode = "ssh"
			}
			if _, err := setMode(entry.Target, mode); err != nil {
				notice("Could not change "+entry.Target,
					err.Error(), "Press any key.")
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
	notice("Connecting to " + text.Sanitize(entry.Target) + " ...")

	message, err := connect(entry.Target)
	if err != nil {
		notice("Could not connect to "+text.Sanitize(entry.Target),
			err.Error(), "Press any key.")
		waitForKey()
		return nil
	}
	notice("", message)
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

	// Machines turned off in the config, so the sweep of ~/.ssh/config below
	// does not put them back. Almost every disabled machine is in that file --
	// it is where it came from -- so without this "disabled" only stripped a
	// machine of its settings and left it in the list looking unconfigured.
	disabled := map[string]bool{}
	for _, host := range cfg.Hosts {
		if host.Disabled {
			disabled[host.Target] = true
		}
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
		if config.ValidTarget(host) != nil || disabled[host] {
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

// pageStep is how far a page key moves: exactly what is on screen.
//
// It used to subtract a constant from the popup height, which stopped matching
// when the frame learned to give up its parts as room ran short. It was two
// rows out at every size, so paging through a long list stepped over two
// machines each time without showing them. Asking the layout is the only way
// these two stay in agreement.
func pageStep(entries []Entry, selected int, warning string) int {
	cols, rows := windowSize()
	frame := planLayout(len(entries), selected, rows, len(warningLines(cols, warning)))
	if step := frame.last - frame.first; step > 0 {
		return step
	}
	return 1
}

// warningLines wraps a warning to the popup, or returns nothing when there is
// none to draw.
func warningLines(cols int, warning string) []string {
	return text.Wrap(text.Sanitize(warning), cols-4, maxWarningLines)
}

// hintLines are the key reminders at the foot of the menu.
//
// They were two fixed strings, sixty and forty-six columns wide, printed
// whatever the popup could hold -- so any popup narrower than sixty, which a
// modest terminal gives, had them running off the side. A narrow one gets the
// short pair rather than the long pair cut off mid-word.
func hintLines(cols int) []string {
	room := cols - 4
	full := []string{
		"↑↓ jk move · pgup/pgdn g/G jump · 1-9 pick · enter connect",
		"d disconnect · m toggle mirroring (experimental) · q cancel",
	}
	short := []string{
		"↑↓ jk move · 1-9 pick · enter connect",
		"d disconnect · m mirroring · q cancel",
	}
	shortest := []string{"↑↓ enter", "d · m · q"}

	for _, pair := range [][]string{full, short, shortest} {
		if text.Width(pair[0]) <= room && text.Width(pair[1]) <= room {
			return pair
		}
	}
	return []string{
		text.Truncate(shortest[0], room),
		text.Truncate(shortest[1], room),
	}
}

// span is a piece of a machine's state line, with the colour it is drawn in.
// Kept as pieces so the words exist once: what is drawn, how wide it is, and
// how much room the name column may have are all worked out from these.
type span struct {
	text   string
	colour string
}

// isMirroring reports whether a machine is actually being mirrored, rather than
// whether it was asked to be.
//
// A machine without Herdr falls back to a plain SSH terminal rather than
// refusing to connect, which is the documented behaviour and the point of the
// default mode. The menu read the setting rather than what happened, so such a
// machine sat there saying "connected · 0 mirrored" while running a terminal it
// declined to count. The field recording what happened was carried all the way
// here and then not looked at.
//
// Only meaningful once connected: before that, what was asked for is all there
// is to go on.
func isMirroring(entry Entry) bool {
	if entry.Connected && entry.SSHOnly {
		return false
	}
	return entry.Mirroring
}

// statusSpans is what a machine's line says after its name.
func statusSpans(entry Entry) []span {
	mode := "ssh"
	if isMirroring(entry) {
		mode = "mirrored"
	}
	switch {
	case entry.GaveUp:
		// The mode is worth saying even here: this is the line someone reads
		// before pressing m, and without it there is no telling which way the
		// toggle would go.
		out := []span{{"unreachable", red}}
		if isMirroring(entry) {
			out = append(out, span{" · mirrored", dim})
		}
		return append(out, span{" · enter to retry", dim})
	case entry.Connected && isMirroring(entry):
		return []span{{fmt.Sprintf("connected · %d mirrored", entry.Mirrors), green}}
	case entry.Connected && entry.Terminals > 0:
		return []span{{fmt.Sprintf("connected · %d open", entry.Terminals), green}}
	case entry.Connected:
		return []span{{"connected", green}, {" · ssh", dim}}
	case entry.Configured:
		return []span{{"not connected", yellow}, {" · " + mode, dim}}
	default:
		return []span{{"from ~/.ssh/config · " + mode, dim}}
	}
}

func plainOf(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.text)
	}
	return b.String()
}

func colourOf(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.colour + s.text + reset)
	}
	return b.String()
}

// fitStatus gives up the tail of a state line until what is left fits.
//
// The first piece is the state itself and is kept whatever happens; what
// follows it elaborates, and a hint about which key to press is worth less than
// a line that stays inside the popup.
func fitStatus(spans []span, room int) []span {
	if room < 1 {
		room = 1
	}
	for len(spans) > 1 && text.Width(plainOf(spans)) > room {
		spans = spans[:len(spans)-1]
	}
	if len(spans) == 1 && text.Width(spans[0].text) > room {
		spans[0].text = text.Truncate(spans[0].text, room)
	}
	return spans
}

// chromeWidth is everything on a machine's line that is not its name or its
// state: the selection marker, the number, and the spaces between the columns.
const chromeWidth = 8

// widestStatus is how much room the state column needs, worked out by asking
// the code that draws it rather than by writing a number down beside it.
//
// It was a number written down beside it, and it went stale the moment a state
// line grew: the reservation stayed at what "connected · NN mirrored" needed
// while the longest had become half as long again, and the line ran off the
// popup by a dozen columns.
func widestStatus() int {
	worst := []Entry{
		{GaveUp: true, Mirroring: true},
		{Connected: true, Mirroring: true, SSHOnly: true, Terminals: 99},
		{GaveUp: true},
		{Connected: true, Mirroring: true, Mirrors: 99},
		{Connected: true, Terminals: 99},
		{Connected: true},
		{Configured: true, Mirroring: true},
		{Configured: true},
		{Mirroring: true},
		{},
	}
	widest := 0
	for _, entry := range worst {
		if w := text.Width(plainOf(statusSpans(entry))); w > widest {
			widest = w
		}
	}
	return widest
}

// nameWidth is how much room the machine column gets, leaving space for the
// marker, the number and the state that follows it.
func nameWidth(cols int) int {
	width := cols - chromeWidth - widestStatus()
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
// maxNoticeLines bounds one paragraph on a screen that is not the menu. An
// error can carry a socket path and a suggested command and still be one
// sentence, so there is more room here than a warning in the menu gets.
const maxNoticeLines = 8

// renderNotice draws a message on a screen of its own, wrapped to the popup.
//
// These used to be printed straight out at whatever length they happened to
// be, so an error carrying a socket path ran off the edge of the popup and
// wrapped wherever the terminal chose, mid-word and mid-path.
func renderNotice(cols int, heading string, body ...string) string {
	width := cols - 4
	var b strings.Builder
	b.WriteString(esc + "[2J" + esc + "[H\r\n")
	if heading != "" {
		for _, line := range text.Wrap(text.Sanitize(heading), width, maxNoticeLines) {
			b.WriteString("  " + bold + line + reset + "\r\n")
		}
	}
	for _, part := range body {
		b.WriteString("\r\n")
		for _, line := range text.Wrap(text.Sanitize(part), width, maxNoticeLines) {
			b.WriteString("  " + line + "\r\n")
		}
	}
	return b.String()
}

// notice draws renderNotice at the popup's current size.
func notice(heading string, body ...string) {
	cols, _ := windowSize()
	fmt.Print(renderNotice(cols, heading, body...))
}

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
	warned := warningLines(cols, warning)
	frame := planLayout(len(entries), selected, rows, len(warned))
	first, last := frame.first, frame.last

	var b strings.Builder
	b.WriteString(esc + "[2J" + esc + "[H")
	b.WriteString("  " + bold + text.Truncate("Connect to a machine", cols-4) + reset + "\r\n\r\n")
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

		var line string
		state := fitStatus(statusSpans(entry), cols-chromeWidth-text.Width(name))
		line = colourOf(state)

		b.WriteString(marker + " " + number + " " + name + " " + line + "\r\n")
	}

	if frame.counter {
		b.WriteString("  " + dim + fmt.Sprintf("showing %d-%d of %d", first+1, last, len(entries)) + reset + "\r\n")
	}
	if frame.hints {
		b.WriteString("\r\n")
		hints := hintLines(cols)
		b.WriteString("  " + dim + hints[0] + reset + "\r\n")
		b.WriteString("  " + dim + hints[1] + reset)
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
	keyUp         key = 0xE000
	keyDown       key = 0xE001
	keyEnter      key = 0xE002
	keyQuit       key = 0xE003
	keyNone       key = 0xE004
	keyToggle     key = 0xE005
	keyPageUp     key = 0xE006
	keyPageDown   key = 0xE007
	keyTop        key = 0xE008
	keyBottom     key = 0xE009
	keyDisconnect key = 0xE00A
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
// swallowPaste reads to the end of a bracketed paste and reports that nothing
// was pressed.
//
// The end marker is ESC [ 201 ~. Anything before it is pasted text, which is
// not a decision somebody made in this menu -- and left as keystrokes it is
// several: "d" disconnects the machine under the cursor and a digit connects to
// one.
func swallowPaste(read func() (byte, bool)) key {
	// Matched byte by byte rather than by reading a fixed tail, because pasted
	// text can contain an escape of its own: pasting an arrow key put ESC [ A
	// in the middle, and reading six bytes to test for the end marker consumed
	// the start of the real one.
	const end = "\x1b[201~"
	matched := 0

	// Bounded: something claiming to be a paste and never finishing is a stream
	// to stop reading, not one to keep waiting on.
	for i := 0; i < maxPasteBytes; i++ {
		b, ok := read()
		if !ok {
			return keyQuit
		}
		switch {
		case b == end[matched]:
			matched++
			if matched == len(end) {
				return keyNone
			}
		case b == end[0]:
			matched = 1
		default:
			matched = 0
		}
	}
	return keyNone
}

// maxPasteBytes bounds how much pasted text is read before giving up on finding
// the end of it.
const maxPasteBytes = 1 << 16

// maxEscapeParams bounds how much of an escape sequence is read before giving
// up on it, so a stream that never ends one cannot be read forever.
const maxEscapeParams = 16

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
	case 'd', 'D':
		return keyDisconnect
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

		// Read to the end of the sequence before deciding anything. Giving up
		// partway leaves the rest in the buffer, where the next read takes them
		// for keystrokes of their own: ctrl+up is ESC [ 1 ; 5 A, and the "5"
		// left behind was read as picking the fifth machine -- which connects
		// to it. A parameter byte is below 0x40 and the final byte is not, so
		// the end is unambiguous whatever is in between.
		var params []byte
		var final byte
		for i := 0; ; i++ {
			b, ok := read()
			if !ok {
				return keyQuit
			}
			if b >= 0x40 && b <= 0x7E {
				final = b
				break
			}
			// Nothing this reads has a long parameter list, and a stream that
			// never ends one is not something to keep reading.
			if i >= maxEscapeParams {
				return keyNone
			}
			params = append(params, b)
		}

		// A paste, which is not typing and should not press anything. Read to
		// the end of it and say nothing happened.
		if final == '~' && string(params) == "200" {
			return swallowPaste(read)
		}

		switch final {
		case 'A':
			return keyUp
		case 'B':
			return keyDown
		case 'H':
			return keyTop
		case 'F':
			return keyBottom
		case '~':
			// The number before the tilde says which key it was. Modifiers
			// arrive after a semicolon and do not change which key it is.
			number, _, _ := strings.Cut(string(params), ";")
			switch number {
			case "5":
				return keyPageUp
			case "6":
				return keyPageDown
			case "1", "7":
				return keyTop
			case "4", "8":
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
	// Ask for pastes to arrive wrapped in markers, so they can be told from
	// typing and ignored. Without that a paste is a run of keystrokes: pasting
	// the word "prod" presses p, r, o and then d, which disconnects the machine
	// under the cursor, and any digit in what follows picks a machine and
	// connects to it.
	fmt.Print(esc + "[?2004h")
	return func() {
		fmt.Print(esc + "[?2004l")
		_, _ = sttyOutput("sane")
	}
}

func sttyOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = nil
	return cmd.Output()
}
