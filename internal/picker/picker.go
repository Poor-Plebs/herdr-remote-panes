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

	"errors"
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
	// OutsideShared is how many terminals the machine has that the scope does
	// not mirror. Not a failure, and the one thing that explains a machine
	// showing one mirror when there are four terminals on it.
	OutsideShared int
	// Terminals is how many plain SSH terminals the machine has open, which is
	// what a machine in SSH mode has instead of mirrors.
	Terminals int
	SSHOnly   bool
	// Mirroring reports whether this machine's terminals are kept in step,
	// rather than being a plain SSH session.
	Mirroring bool
	// ReadOnly is a machine in observe mode: its terminals are mirrored and
	// cannot be typed into. It is a config setting rather than something the
	// menu offers, so m does not change it -- and the line has to say so,
	// because otherwise it is indistinguishable from an attach machine right
	// up until somebody tries to type.
	ReadOnly bool
	// GaveUp marks a machine that could not be reached and is no longer being
	// retried until it is connected to again.
	GaveUp bool
	// Reason is why, in a few words. A machine that says only "unreachable"
	// leaves somebody with nothing to do about it, and this is the screen they
	// are looking at when they want to know.
	Reason string
	// NoHerdr is a machine asked to mirror that fell back to plain SSH,
	// because the machine has no herdr on the PATH an SSH session gets. The
	// fallback is deliberate and the row was right to say ssh; what it could
	// not say is that ssh was not what was asked for.
	NoHerdr bool
	// AtCapacity is a machine holding max_mirrors terminals with more waiting.
	// Unlike a mirror that failed, these were never attempted and will not be
	// until the setting changes, so nothing later makes them appear.
	AtCapacity bool
	// SharedName is more than one space on the machine answering to the name
	// this machine's terminals live under. Nothing fails; the terminals in the
	// others cannot be seen, and the only sign is a count that reads low.
	SharedName bool
	// Unmirrored is how many of the machine's terminals were given up on after
	// failing to mirror. They are still open over there, and this is the only
	// one of these that is a failure rather than a setting doing what it says.
	Unmirrored int
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
		heading, body := noMachinesNotice(warning)
		notice(heading, body...)
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
			if !worthDisconnecting(entry) {
				break
			}
			if _, err := disconnect(entry.Target); err != nil {
				notice("Could not disconnect "+text.Sanitize(entry.Target),
					err.Error(), "Press any key.")
				readKey()
			}
			entries, warning = collect()
			selected = planSelectionAfterChange(selected, len(entries))

		case keyToggle:
			// Toggling in place, rather than closing the menu, so the change
			// and its effect are visible together.
			entry := entries[selected]
			if entry.ReadOnly {
				// m only ever wrote "attach" or "ssh", so on an observe machine
				// it read as a toggle and was a one-way door: observe went to
				// ssh, ssh went to attach, and nothing in the menu went back.
				// Two presses turned a machine chosen to be read-only into one
				// that can be typed into, silently.
				notice(text.Sanitize(entry.Target)+" is read-only",
					"Its mode is set to observe in your config, and m does not change that.",
					"Edit the config to change it. Press any key.")
				readKey()
				break
			}
			mode := "attach"
			if entry.Mirroring {
				mode = "ssh"
			}
			if !confirmToggle(entry, mode) {
				break
			}
			if _, err := setMode(entry.Target, mode); err != nil {
				notice("Could not change "+entry.Target,
					err.Error(), "Press any key.")
				readKey()
			}
			entries, warning = collect()
			selected = planSelectionAfterChange(selected, len(entries))
		default:
			// Digits jump straight to an entry.
			if index, ok := planDigitChoice(key, len(entries)); ok {
				return choose(entries[index], connect)
			}
		}
	}
}

// planDigitChoice says which machine a digit picks, if any.
//
// The menu offers "1-9 pick", and those are the only nine it can offer: there
// is no key for the tenth. A digit past the end of the list picks nothing
// rather than the wrong machine, which for a key that connects somewhere
// matters more than it looks -- the machines move about as they connect and
// disconnect, so the number under a digit is not the same one it was.
func planDigitChoice(pressed key, count int) (int, bool) {
	index := int(pressed - '1')
	if index < 0 || index >= count || index >= 9 {
		return 0, false
	}
	return index, true
}

// bothWarnings puts two warnings on the one line the menu has for them.
//
// Either can be absent, and the separator belongs between them rather than
// after whichever came first: a line ending in a dangling " · " reads as a
// message that was cut off, which is worse than the message that is missing.
//
// Its own function so that it can be read. It was four lines inside collect,
// reachable only with a daemon answering and an installed copy newer than the
// running one, so nothing about it was ever exercised.
func bothWarnings(first, second string) string {
	switch {
	case first == "":
		return second
	case second == "":
		return first
	}
	return first + warningSeparator + second
}

// warningSeparator joins two warnings, and is how the wrap tells that there are
// two of them.
const warningSeparator = " · "

// worthDisconnecting reports whether d has anything to do to a machine.
//
// A machine that has never been connected to has no panes here to close, so
// pressing d on one is a no-op rather than an error. One that was given up on
// does: giving up leaves its terminals on screen wearing the failure, and d is
// how they are cleared.
//
// Its own function so that it can be tested. Inline it was a condition in a key
// handler, reachable only by driving the whole menu against a running daemon,
// which is why inverting it -- so that d did nothing to a connected machine --
// broke no test at all.
func worthDisconnecting(entry Entry) bool {
	return entry.Connected || entry.GaveUp
}

// widestWindow picks whichever of these shapes shows the most machines, and
// reports whether any of them showed one at all.
//
// Ties go to the earlier shape, which is the one keeping more of the hints and
// the warning: giving either up buys nothing when the same number of machines
// is drawn either way.
func widestWindow(options []struct {
	hints   bool
	warning int
}, count, selected, rows, heading int) (layout, bool) {
	best := 0
	var chosen layout

	for _, opt := range options {
		chrome := heading
		if opt.hints {
			chrome += 3 // a blank separator and two lines of hints
		}
		if opt.warning > 0 {
			chrome += opt.warning + 1 // the warning and the blank line under it
		}

		if visible := rows - chrome; visible >= 1 && visible >= count {
			if count > best {
				best = count
				chosen = layout{first: 0, last: count, hints: opt.hints, warning: opt.warning}
			}
			continue
		}
		visible := rows - chrome - 1 // the range counter needs a row too
		// The second half of that condition changes nothing today: the shapes
		// are offered widest-chrome first, so each one after it has room for at
		// least as many machines. It is there for when that ordering is not
		// true any more -- checked by taking it out, which leaves every layout
		// at every size identical.
		if visible < 1 || visible <= best {
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
		best = visible
		chosen = layout{
			first: first, last: first + visible,
			counter: true, hints: opt.hints, warning: opt.warning,
		}
	}
	return chosen, best > 0
}

// planSelectionAfterChange keeps the cursor on the list after it changes.
//
// Disconnecting a machine can take it out of the list, and a cursor left past
// the end draws nothing and moves nowhere. Back to the top is the one position
// that is always there.
func planSelectionAfterChange(selected, count int) int {
	if selected >= count {
		return 0
	}
	return selected
}

// worthAskingBeforeToggle reports whether this toggle costs anything.
//
// Apart from the asking so that it can be asked without a terminal to answer
// on. Only turning mirroring on costs: it closes plain SSH terminals, whose
// shells go when their panes do.
func worthAskingBeforeToggle(entry Entry, mode string) bool {
	return mode == "attach" && entry.Terminals > 0
}

// confirmToggle asks before a toggle that costs somebody their work.
//
// The two directions are not alike. Turning mirroring off drops the panes here
// and leaves everything on the machine, so there is nothing to ask about.
// Turning it on closes plain SSH terminals -- and a plain SSH terminal's shell
// goes when its pane does, with whatever was running in it.
//
// So it asks only when there is something to lose, and with the same key, so
// that meaning it costs one more press and not meaning it costs nothing. "m"
// sits beside "d", which closes a machine's panes and leaves the work running:
// the two keys are one apart and their consequences are not.
func confirmToggle(entry Entry, mode string) bool {
	if !worthAskingBeforeToggle(entry, mode) {
		return true
	}
	terminals := fmt.Sprintf("%d terminals", entry.Terminals)
	if entry.Terminals == 1 {
		terminals = "1 terminal"
	}
	notice("Turn mirroring on for "+text.Sanitize(displayName(entry))+"?",
		"Mirroring works differently, so its "+terminals+" here are closed and the "+
			"machine is connected again. They are plain SSH sessions, so whatever is "+
			"running in them goes with them.",
		"m to go ahead, any other key to leave it alone.")
	return readKey() == keyToggle
}

// noMachinesNotice is what a fresh installation draws: nothing in the plugin's
// config and nothing in ~/.ssh/config either, so there is no menu to put a
// machine in.
//
// Apart from Run so that what it says can be read without a terminal to draw
// it on. It is the first screen this plugin ever shows somebody, and the only
// one of the four that wait for a key which did not say that it was waiting --
// a popup with nothing in it and no way out written down.
func noMachinesNotice(warning string) (heading string, body []string) {
	body = []string{"Add hosts to ~/.ssh/config or to the plugin's config.json."}
	// With no menu to put it in, a warning still has to be said somewhere.
	if warning != "" {
		body = append(body, warning)
	}
	return "No machines found.", append(body, "Press any key.")
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

// configFault is the part of a config error that says what to fix, without the
// path of the one file it can be about.
func configFault(err error) string {
	var parseErr *config.ParseError
	if errors.As(err, &parseErr) {
		return parseErr.Detail
	}
	return err.Error()
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
		// What to fix goes first: the warning gets two lines in the popup, and
		// a leading file path spends both of them.
		warning = "Could not read the plugin config: " + configFault(err) +
			". Only ~/.ssh/config machines are listed."
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
		entry.ReadOnly = cfg.EffectiveMode(host) == config.ModeObserve
	}
	for _, host := range sshconfig.Hosts() {
		// An alias ssh would read as an option never arrives here: the
		// reading leaves it out, and since 95081bc says on the warning line
		// that it did. Checking again would take nothing out that is still
		// here -- and if the reading ever handed them over instead, this would
		// drop them again in silence, which is the thing that was fixed.
		//
		// Machines switched off in the config are a different matter: somebody
		// asked for those to be absent, and the daemon's startup report says
		// which they are.
		if disabled[host] {
			continue
		}
		entry := add(host)
		// Only for the ones this loop is actually introducing. add hands back
		// the entry that is already there for a machine the config named too,
		// and a configured machine's mode was settled above from its own host
		// entry -- working it out again from a bare target loses the mode it
		// was set to and reads the top-level default instead.
		//
		// That default is why this is here at all: a config that sets observe
		// for everything means a machine picked out of ~/.ssh/config is
		// observe as well, and m must refuse it for the same reason.
		if !entry.Configured {
			entry.ReadOnly = cfg.EffectiveMode(config.Host{Target: host}) == config.ModeObserve
		}
	}

	// A machine missing from this list because a file could not be read looks
	// exactly like a machine somebody deleted. Said here rather than left to be
	// worked out from an emptier menu than yesterday's.
	if why := sshconfig.Unreadable(); why != "" {
		warning = bothWarnings(warning, "could not read "+sshconfig.Path()+": "+why)
	}

	hosts, stale := status()
	// Worth saying where machines are picked: an update that has not taken
	// effect looks exactly like a fix that did not work.
	warning = bothWarnings(warning, stale)
	for _, info := range hosts {
		if entry, ok := byTarget[info.Target]; ok {
			entry.Connected = info.Connected
			entry.Mirrors = info.Mirrors
			entry.OutsideShared = info.OutsideShared
			entry.Terminals = info.Terminals
			entry.SSHOnly = info.SSHOnly
			entry.NoHerdr = info.NoHerdr
			entry.AtCapacity = info.AtCapacity
			entry.SharedName = info.SharedName
			entry.Unmirrored = info.Unmirrored
			entry.Mirroring = info.Mirroring
			entry.GaveUp = info.GaveUp
			entry.Reason = shortReason(info.LastError)
		}
	}

	entries := make([]Entry, 0, len(order))
	for _, target := range order {
		entries = append(entries, *byTarget[target])
	}
	// Configured machines first; they are the ones being worked on.
	//
	// They are already in that order, because the config is walked before
	// ~/.ssh/config is. This says so anyway, so that the order survives the two
	// being walked the other way round.
	sort.SliceStable(entries, func(i, j int) bool {
		return configuredFirst(entries[i], entries[j])
	})
	return entries, warning
}

// configuredFirst puts machines named in the config ahead of ones only
// ~/.ssh/config knows about.
//
// Named, so that it can be tested on an order the menu cannot currently
// produce. Inline, nothing reached it out of order and any change to it was
// invisible -- a safety net that no longer catches anything fails quietly.
func configuredFirst(a, b Entry) bool {
	return a.Configured && !b.Configured
}

// status asks the daemon what it is currently mirroring. A daemon that is not
// running is not an error here: every machine simply shows as unconnected.
// status reports the machines the daemon is tracking, and anything about the
// daemon itself worth putting in front of someone opening the menu.
func status() ([]syncd.HostInfo, string) {
	reply, err := syncd.Ask(syncd.Command{Cmd: "status"})
	if err != nil {
		// Said here rather than left to be discovered by pressing enter. With
		// nothing answering, every machine reads "not connected", which is
		// exactly what a working daemon shows before anything is connected --
		// so the menu looked ready and nothing in it would work.
		return nil, "The daemon is not running, so nothing here can be connected to. " +
			"Check `herdr plugin log list --plugin " + syncd.PluginID + "`."
	}
	// reply.Warning is deliberately dropped. It says a config that could not be
	// read and settings that read fine and mean nothing, and collect works
	// both of those out for itself a few lines up, from its own read of the
	// same file -- so taking this one as well printed the sentence twice, on a
	// line that gets two lines in the popup and an ellipsis after that.
	//
	// The local one is the better of the two where they differ. The daemon
	// reports the config it is running, which is the file as of its last
	// reload; collect reads the file as it is now, which is what somebody who
	// has just edited it is asking about. A daemon holding a stale config
	// would otherwise report a problem in a file that no longer has one.
	//
	// It is not dead weight in the reply: `status` runs as its own process and
	// prints it there, which is where the full list of problems goes.
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
	return pageStepIn(len(entries), selected, cols, rows, warning)
}

// pageStepIn is pageStep at a given popup size, which is what makes it
// checkable: the whole point of this is that it agrees with what was drawn, and
// the only way to test agreement is to ask both at the same size.
func pageStepIn(count, selected, cols, rows int, warning string) int {
	frame := planLayout(count, selected, rows, len(warningLines(cols, warning)))
	if step := frame.last - frame.first; step > 0 {
		return step
	}
	return 1
}

// warningLines wraps a warning to the popup, or returns nothing when there is
// none to draw.
func warningLines(cols int, warning string) []string {
	return text.Wrap(text.Sanitize(warning), cols-4, warningBudget(warning))
}

// warningBudget is how many lines the warning may take, which is twice as many
// when there are two warnings.
//
// Two do not fit in the room for one. The config here could not be read and the
// daemon is not running is a pair that happens together -- a config bad enough
// to stop the daemon starting is a config that cannot be read -- and in two
// lines the second arrived as "The daemon is...", a subject with its predicate
// cut off, where "is not running" and "is running an older build" ask for
// opposite things. bothWarnings already refuses to leave a dangling separator
// for the same reason. Which of the two to keep whole is not this function's to
// decide, so it keeps both, and planLayout still trims a line at a time when
// the popup is too short to hold them.
func warningBudget(warning string) int {
	if strings.Contains(warning, warningSeparator) {
		return 2 * maxWarningLines
	}
	return maxWarningLines
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

// whyTheCountIsLow explains a machine mirroring fewer terminals than it has.
//
// Three things cause it and the menu has room for one. They are not competing
// pieces of information: they are three answers to the single question this
// line gets asked, which is why four terminals on a machine show up as one.
// Any of them answers it well enough to stop the count reading as three
// mirrors that failed, and `status` prints all of them for somebody who wants
// the rest.
//
// The order is `status`'s, which had these first and gives the reasoning: most
// wrong first, the ones that mean no mirroring at all before the ones that mean
// some of it. Two lists in one order rather than two orders, because a machine
// answering one way here and another there is worse than either order being
// wrong -- and the menu's own first ordering differed from it by a place, which
// nothing noticed until the two were compared.
//
// Keeping it to one slot is also what keeps the state column inside its width.
// Every column this takes comes off the machine names, for every machine, on
// every row -- including the machines with nothing wrong with them.
func whyTheCountIsLow(entry Entry) []span {
	switch {
	case entry.SharedName:
		return []span{{" · shared name", yellow}}
	case entry.AtCapacity:
		return []span{{" · at limit", yellow}}
	case entry.OutsideShared > 0:
		return []span{{fmt.Sprintf(" · %d elsewhere", entry.OutsideShared), dim}}
	case entry.Unmirrored > 0:
		return []span{{fmt.Sprintf(" · %d unmirrored", entry.Unmirrored), yellow}}
	}
	return nil
}

// whyNotMirrored says when a machine is on plain SSH that was not asked for.
//
// The rows for a connected machine read the same whether SSH is what the
// config says or what was left after mirroring could not start, and those are
// opposite situations: one is working, the other is the machine somebody
// pressed m on and is now looking at to find out why nothing happened. The
// daemon knew, logged it, and sent it here in every status reply.
//
// Not dim, unlike the other trailing detail. Dim is for what can be dropped
// for want of room, and this is the only thing on the line that is not simply
// a report of a machine working.
func whyNotMirrored(entry Entry) []span {
	if !entry.NoHerdr {
		return nil
	}
	// The cause, not the remedy -- the remedy is a sentence about herdr_bin
	// and where a login's PATH reaches, which is what `status` and the log are
	// for. This is a few words on a line that already has some.
	return []span{{" · herdr not found", yellow}}
}

// statusSpans is what a machine's line says after its name.
func statusSpans(entry Entry) []span {
	mode := "ssh"
	if isMirroring(entry) {
		mode = "mirrored"
		if entry.ReadOnly {
			mode = "read-only"
		}
	}
	switch {
	case entry.GaveUp:
		// The mode is worth saying even here: this is the line someone reads
		// before pressing m, and without it there is no telling which way the
		// toggle would go.
		out := []span{{"unreachable", red}}
		if entry.Reason != "" {
			// Before the reminder, not after. When there is not room for
			// everything the reminder is what goes: enter is guessable and the
			// reason is not, and "unreachable" on its own leaves somebody with
			// nothing they can do next.
			// Sanitised here, where it is drawn, and not only where the
			// entry is filled in. The reason is the one thing on this line
			// that a remote machine wrote -- it is ssh's complaint, or the
			// machine's -- and the name beside it is made safe by displayName
			// on the way to the screen rather than on the way into the entry.
			// A reason that is not is a field whose safety depends on which
			// function built the entry.
			out = append(out, span{" · " + text.Sanitize(entry.Reason), dim})
		}
		if isMirroring(entry) {
			out = append(out, span{" · " + mode, dim})
		}
		return append(out, span{" · enter to retry", dim})
	case entry.Connected && isMirroring(entry):
		// The mode word rather than "mirrored" always: this is the line
		// somebody reads before pressing m, and a read-only machine that says
		// "mirrored" is indistinguishable from one they can type into.
		out := []span{{fmt.Sprintf("connected · %d %s", entry.Mirrors, mode), green}}
		// What the scope is leaving alone. This is the line somebody reads
		// straight after pressing m, and without it a machine with four
		// terminals on it showing one mirror looks like three that failed.
		// Dim and last, so it is the first thing dropped for want of room.
		return append(out, whyTheCountIsLow(entry)...)
	case entry.Connected && entry.Terminals > 0:
		return append([]span{{fmt.Sprintf("connected · %d open", entry.Terminals), green}}, whyNotMirrored(entry)...)
	case entry.Connected:
		return append([]span{{"connected", green}, {" · ssh", dim}}, whyNotMirrored(entry)...)
	case entry.Configured:
		return []span{{"not connected", yellow}, {" · " + mode, dim}}
	default:
		return []span{{"from ~/.ssh/config · " + mode, dim}}
	}
}

// shortReason is the part of a failure worth putting in a menu.
//
// The summaries are written as a cause and then what to do about it, joined by
// a dash: "host key changed — verify it, then update ~/.ssh/known_hosts". The
// second half is a sentence and belongs where there is room for one, which is
// the listing and the log. The first half is a few words and is the part that
// stops "unreachable" being a dead end.
func shortReason(summary string) string {
	if cause, _, found := strings.Cut(summary, " — "); found {
		summary = cause
	}
	return text.Truncate(text.Sanitize(summary), maxReasonWidth)
}

// maxReasonWidth keeps a long cause from crowding out everything beside it.
const maxReasonWidth = 28

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
	kept := spans
	for len(kept) > 1 && text.Width(plainOf(kept)) > room {
		kept = kept[:len(kept)-1]
	}
	if len(kept) == 1 && text.Width(kept[0].text) > room {
		kept[0].text = text.Truncate(kept[0].text, room)
		return kept
	}

	// Whatever room is left over goes to the best of what was dropped, cut to
	// fit rather than left out.
	//
	// Dropping is by whole pieces, so one that overruns by a column takes all
	// of itself with it: at forty-three columns -- a popup of the usual sixty
	// per cent on a seventy-two column terminal, which is a window somebody
	// really has -- "unreachable · host key changed" is one column too wide,
	// and what was drawn was "unreachable" with nineteen columns spare. The
	// reason is the half that stops "unreachable" being a dead end, and half a
	// reason is a great deal more than none of one.
	//
	// Only when more than the last piece went. The pieces are in order of what
	// they are worth, so the last of them is the least: if it alone did not
	// fit, the line is whole as far as it goes, and cutting that piece down
	// adds an ellipsis to say nothing the reader could not have guessed. Two
	// pieces gone is information actually lost, and then half of the better
	// one is worth the ellipsis it costs.
	//
	// Measured over every width from the narrowest served to a hundred and
	// twenty: an unreachable machine gains a cause it did not show at eleven
	// of them, and a connected or a plain one is not changed at all. Putting
	// back the last piece too gained nothing more and cut fourteen further
	// lines short, most of them "· enter to ret…".
	if len(kept) < len(spans)-1 {
		if left := room - text.Width(plainOf(kept)); left >= minPartialStatus {
			next := spans[len(kept)]
			// Copied rather than appended in place: spans is the caller's, and
			// its next element is the one being rewritten.
			with := make([]span, len(kept), len(kept)+1)
			copy(with, kept)
			kept = append(with, span{text.Truncate(next.text, left), next.colour})
		}
	}
	return kept
}

// minPartialStatus is the least room worth putting a cut-down piece of the
// state into. Each begins with " · " and Truncate keeps a column for its
// ellipsis, so below this there is room for three characters of the thing
// itself -- which is a line noisier than the one it replaces and no more use.
const minPartialStatus = 8

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
		{Connected: true, Mirroring: true, Mirrors: 99, OutsideShared: 99},
		{GaveUp: true, Mirroring: true, ReadOnly: true},
		{Connected: true, Mirroring: true, ReadOnly: true, Mirrors: 99, OutsideShared: 99},
		{Connected: true, Mirroring: true, ReadOnly: true, Mirrors: 99, AtCapacity: true},
		{Connected: true, Mirroring: true, ReadOnly: true, Mirrors: 99, SharedName: true},
		{Connected: true, Mirroring: true, ReadOnly: true, Mirrors: 99, Unmirrored: 99},
		{Connected: true, Terminals: 99, NoHerdr: true},
		{Connected: true, Terminals: 99},
		{Connected: true, NoHerdr: true},
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
	// A name shorter than this is not a name, it is a first letter and an
	// ellipsis, so the column stops shrinking here and the state column gives
	// up its room instead. Below chromeWidth+8 columns there is no room left to
	// give and the line wraps: a popup that narrow is narrower than any
	// terminal anyone runs, and buying it back would cost every ordinary width
	// a worse layout. TestNothingInTheMenuRunsOffTheSide holds from there up.
	if width < 8 {
		width = 8
	}
	if width > 40 {
		width = 40
	}
	return width
}

// displayName is how a machine is written in the menu: its name, and its label
// after it when the two differ.
//
// Names come from ~/.ssh/config, so they are made safe to draw rather than
// trusted to be short and printable.
func displayName(entry Entry) string {
	name := text.Sanitize(entry.Target)
	if entry.Label != "" && entry.Label != entry.Target {
		return fmt.Sprintf("%s (%s)", name, text.Sanitize(entry.Label))
	}
	return name
}

// nameWithin is the best name for a machine in the room available.
//
// Ordinarily that is the full "target (label)". When it does not fit, what got
// drawn was the front of the target and an ellipsis -- and a login is the front
// of the target: "deploy@prod" and "deploy@staging" both became "deploy@…",
// two rows of a menu naming two different machines identically, with each of
// their labels short enough to have been drawn whole.
//
// So when the pair will not fit, pick a name that survives instead of a prefix
// that does not identify anything. The label goes first, being the name
// somebody chose for the machine; the target if the label is the longer of the
// two. If neither fits, the label still loses less: labels differ from each
// other where several machines sharing a login do not.
func nameWithin(entry Entry, width int) string {
	full := displayName(entry)
	target, label := text.Sanitize(entry.Target), text.Sanitize(entry.Label)
	if text.Width(full) <= width || label == "" || label == target {
		// Nothing to choose between, so the caller truncates as before.
		return full
	}
	if text.Width(label) <= width {
		return label
	}
	if text.Width(target) <= width {
		return target
	}
	return label
}

// namesWithin picks each machine's name for a column this wide, and makes sure
// no two of them come out the same.
//
// A label can collide with another machine's target -- a "staging" in
// ~/.ssh/config beside a configured machine labelled "staging" -- and two rows
// naming different machines identically is the thing this is here to prevent,
// so a name that collides goes back to the full form and is cut instead.
//
// Computed across every machine, not the visible ones, so that a name does not
// change as the list scrolls under the cursor.
func namesWithin(entries []Entry, width int) []string {
	names := make([]string, len(entries))
	used := make(map[string]int, len(entries))
	for i, entry := range entries {
		names[i] = nameWithin(entry, width)
		used[names[i]]++
	}
	for i, entry := range entries {
		if used[names[i]] > 1 {
			names[i] = displayName(entry)
		}
	}
	return names
}

// nameColumn is how wide the column of machine names should be.
//
// It used to be whatever the popup could afford, which for the usual case --
// machines called bot, prod, web1 -- left the status of each stranded some
// thirty columns from the name it belongs to, with nothing in between. The eye
// has to cross that gap to pair them up, and there was no reason for it: the
// space was reserved for names nobody had.
//
// Measured across every machine rather than only the ones on screen. Sizing to
// the visible ones is tighter still, but the column would then change width as
// the list scrolls, and names sliding about under the cursor is worse than a
// column wider than one screenful strictly needs.
func nameColumn(entries []Entry, cols int) int {
	limit := nameWidth(cols)
	widest := 0
	for _, entry := range entries {
		if w := text.Width(displayName(entry)); w > widest {
			widest = w
		}
	}
	// A gutter, so the two columns read as two columns. Without it a list whose
	// names are all the same length has every status hard against its name.
	const gutter = 2
	if widest+gutter < limit {
		return widest + gutter
	}
	return limit
}

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

	// Whichever of these shows the most machines, rather than the first that can
	// show any.
	//
	// Taking the first left a taller popup showing fewer machines than a
	// shorter one: at six rows the hints did not fit at all, so they were given
	// up and three machines drawn; at seven they just fitted, so they were kept
	// and one machine drawn beside them. Growing the window took machines off
	// the screen, at two different heights, which is not something anybody
	// would think to report.
	//
	// The two passes are what stops that fix going too far. Machines beat the
	// key hints -- a reminder of which key moves the cursor is worth less than
	// the machines it is covering. They do not beat the warning: that is the
	// line saying the daemon is not answering or the config cannot be read, and
	// a menu that hides it to fit two more machines in is a menu that looks
	// fine while nothing in it works. So the whole warning is offered first,
	// and the trimmed-down ones only when keeping it would leave no room for a
	// single machine.
	for _, opts := range [][]struct {
		hints   bool
		warning int
	}{options[:2], options[2:]} {
		if frame, ok := widestWindow(opts, count, selected, rows, heading); ok {
			return frame
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

	column := nameColumn(entries, cols)
	names := namesWithin(entries, column)

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

		name := text.Pad(text.Truncate(names[i], column), column)

		var line string
		state := fitStatus(statusSpans(entry), cols-chromeWidth-text.Width(name))
		line = colourOf(state)

		b.WriteString(marker + " " + number + " " + name + " " + line + "\r\n")
	}

	if frame.counter {
		// Truncated like the heading above it. It is the one line of the menu
		// written without asking how wide the popup is, and "showing 1-3 of 6"
		// is eighteen columns whatever the terminal says.
		counter := fmt.Sprintf("showing %d-%d of %d", first+1, last, len(entries))
		b.WriteString("  " + dim + text.Truncate(counter, cols-4) + reset + "\r\n")
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

// String names a key, so that a failure about one reads as the key rather than
// as the number in the private-use block it happens to be.
func (k key) String() string {
	switch k {
	case keyUp:
		return "up"
	case keyDown:
		return "down"
	case keyEnter:
		return "enter"
	case keyQuit:
		return "quit"
	case keyNone:
		return "nothing"
	case keyToggle:
		return "toggle mirroring"
	case keyPageUp:
		return "page up"
	case keyPageDown:
		return "page down"
	case keyTop:
		return "top"
	case keyBottom:
		return "bottom"
	case keyDisconnect:
		return "disconnect"
	}
	// An ordinary keystroke is itself: a digit picks a machine.
	return strconv.QuoteRune(rune(k))
}

// readKey reads one keypress from the popup.
func readKey() key {
	return parseKey(os.Stdin)
}

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

// mouseReportBytes is how much a click in the old encoding carries after its
// sequence: the button, the column and the row, each offset by 32.
const mouseReportBytes = 3

// maxEscapeParams bounds how much of an escape sequence is read before giving
// up on it, so a stream that never ends one cannot be read forever.
const maxEscapeParams = 16

// parseKey reads one keypress, translating the escape sequences a terminal
// sends for arrows and paging.
//
// Both cursor-key encodings are handled: a terminal in application mode sends
// ESC O A for Up rather than ESC [ A, and reading only the second form leaves
// the arrow keys dead with no clue why.
func parseKey(r io.Reader) key {
	var buf [1]byte
	read := func() (byte, bool) {
		// The byte, not the error. io.Reader is allowed to hand back what it
		// read together with the error that ended the stream, and says to use
		// it: a terminal closing right after a keypress delivers both at once.
		// Reading that as nothing turned the keypress into a quit, so the menu
		// shut instead of moving -- and the next read says 0 and the error
		// again, which is where quitting belongs.
		n, _ := r.Read(buf[:])
		return buf[0], n == 1
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

		// A mouse click in the old encoding, which is ESC [ M and then three
		// raw bytes saying which button and where. Those bytes are not a
		// sequence and nothing above stops at them, so they are read as three
		// keystrokes of their own -- and the column byte is the column plus 32,
		// which for columns 16 to 25 is a digit. A digit picks a machine and
		// connects to it. So clicking in the menu connected to whatever was
		// under a number nobody typed.
		//
		// The newer encoding puts the numbers in the parameters, where they are
		// consumed above, and needs nothing here.
		if final == 'M' && len(params) == 0 {
			for i := 0; i < mouseReportBytes; i++ {
				if _, ok := read(); !ok {
					return keyQuit
				}
			}
			return keyNone
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
