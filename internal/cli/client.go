package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/version"
	"strconv"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
)

// call runs a command that only reports success or failure.
func call(cmd syncd.Command) error {
	reply, err := syncd.Ask(cmd)
	if err != nil {
		return err
	}
	if !reply.OK {
		return fmt.Errorf("%s", reply.Message)
	}
	report(reply.Message)
	return nil
}

// report writes a result where the user will actually see it. Action stdout
// only reaches the plugin log, so surface it as a Herdr notification too.
func report(message string) {
	fmt.Fprintln(os.Stdout, message)
	notifyIfAction(message)
}

// notifyIfAction shows a Herdr notification when this run is an action.
//
// Herdr sets HERDR_PLUGIN_ACTION_ID for a command it invoked as an action, and
// an action's stdout goes to the plugin log rather than to anybody. So the
// notification is the whole of what the person who pressed the key sees, and
// the same run from a terminal must not raise one -- they are reading the
// output.
//
// One function because it was written twice, and a gate nothing holds is a
// gate that can be got backwards in one place and not the other.
func notifyIfAction(message string) {
	if os.Getenv("HERDR_PLUGIN_ACTION_ID") != "" {
		herdrcli.Notify(message)
	}
}

// status prints one line per connected host.
func status() error {
	reply, err := syncd.Ask(syncd.Command{Cmd: "status"})
	if err != nil {
		return err
	}
	if reply.Warning != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", reply.Warning)
	}
	// Installing an update replaces the files but leaves the running daemon
	// alone, so its fixes do nothing until Herdr restarts. That is invisible
	// otherwise: the new build sits on disk while the old one keeps answering.
	if stale := version.StaleMessage(reply.Revision); stale != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", stale)
	}
	if len(reply.Hosts) == 0 {
		// The same words the notification uses, from the same place. Said
		// twice, the two drifted: this printed "hosts", which is what the
		// config file calls them and not what anything else here calls them.
		report(statusSummary(reply.Hosts))
		return nil
	}
	for _, line := range statusLines(reply.Hosts, outputWidth()) {
		fmt.Println(line)
	}
	notifyIfAction(statusSummary(reply.Hosts))
	return nil
}

// statusLines is one line per connected machine, as columns.
//
// The columns are sized to what is in them. They were fixed at twenty-two for
// the name and nine for the kind, so the usual case -- machines called bot,
// prod, ci, all on plain SSH -- put every state some twenty columns from the
// machine it belongs to. Names are measured in terminal cells rather than
// characters, since a label can hold anything the user wrote in the config.
func statusLines(hosts []syncd.HostInfo, width int) []string {
	type row struct{ name, count, kind, state string }

	rows := make([]row, 0, len(hosts))
	for _, h := range hosts {
		r := row{name: text.Sanitize(h.Label), state: "ok"}
		// The failure as well as the name. A name holds whatever somebody wrote
		// in their config, but this holds whatever the far side said: ssh
		// passes a remote banner through untouched, and a banner can carry an
		// escape sequence that moves the cursor or repaints the line. The menu
		// already makes this safe to draw; here it went to the terminal as it
		// arrived.
		failure := text.Sanitize(h.LastError)
		switch {
		case h.GaveUp:
			r.state = "unreachable, not retrying: " + failure
		case !h.Connected:
			r.state = "error: " + failure
		}
		open := h.Mirrors
		r.kind = "mirrored"
		// SSHOnly records what happened when the connection was made, so on a
		// machine that has not connected there is nothing in it to read and it
		// is false -- which left this column saying "mirrored" for every
		// machine that was down, including the ones set to plain ssh that
		// would never have mirrored anything. The setting is what is left to
		// go on, and it is what the menu falls back on for the same reason.
		if h.SSHOnly || (!h.Connected && !h.Mirroring) {
			open, r.kind = h.Terminals, "ssh"
		}
		// What follows is everything the machine is quietly not doing. Each is
		// reported only when there is nothing worse to say, because a machine
		// that cannot be reached at all has a better answer than any of them,
		// and they run most-wrong first: the ones that mean no mirroring at all
		// before the ones that mean some of it.

		// Asked to mirror and could not. The machine works, so this is not a
		// failure — but the settings say one thing and the machine is doing
		// another, and without this only the daemon's log knew.
		if h.NoHerdr && r.state == "ok" {
			r.state = "mirroring off: no herdr found on the machine — set herdr_bin if it is installed elsewhere there"
		}
		// Two spaces on the machine answer to its name, so which of them you
		// are looking at comes down to which was found first. Nothing fails and
		// nobody is wrong; you simply cannot see what is in the other one, and
		// the only hint without this is a count that reads too low.
		if h.SharedName && r.state == "ok" {
			r.state = "more than one space on the machine has this machine's name — rename the others, or set remote_workspace_format"
		}
		// More terminals than the limit allows. Different from the count below:
		// those were tried and failed, and trying again may work; these were
		// never tried, and will not be until the number is changed.
		if h.AtCapacity && r.state == "ok" {
			r.state = "at the mirror limit — raise max_mirrors to mirror the rest"
		}
		// Terminals in the machine's own spaces, which the default scope does
		// not mirror. Not a failure — it is the setting doing what it says —
		// but from here it looks the same as one: you turn mirroring on, you
		// had four terminals there, and one arrives.
		if h.OutsideShared > 0 && r.state == "ok" {
			r.state = fmt.Sprintf(
				"%d more in other spaces on the machine — set scope to \"all\" to mirror those too",
				h.OutsideShared)
		}
		// Terminals the machine has that this could not mirror. Left out, the
		// count simply reads lower than what is on the machine, with nothing
		// at all to say why.
		if h.Unmirrored > 0 && r.state == "ok" {
			r.state = fmt.Sprintf("%d could not be mirrored — connect again to retry", h.Unmirrored)
		}
		// The kind is worth saying even for a machine that is not answering:
		// it is how you know which way the m key would toggle. The count is
		// not -- "0 mirrored" reads as a tally rather than as the mode, and a
		// machine that cannot be reached has nothing to tally.
		if h.Connected && !h.GaveUp {
			r.count = strconv.Itoa(open)
		}
		rows = append(rows, r)
	}

	var nameCol, countCol, kindCol int
	for _, r := range rows {
		nameCol = max(nameCol, text.Width(r.name))
		countCol = max(countCol, text.Width(r.count))
		kindCol = max(kindCol, text.Width(r.kind))
	}

	// Where the state starts, which is where a state too long for the terminal
	// carries on: "  name  count kind  ".
	indent := 2 + nameCol + 2 + countCol + 1 + kindCol + 2

	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		prefix := fmt.Sprintf("  %s  %s %s  ",
			text.Pad(r.name, nameCol),
			// Counts right-aligned, so a two-digit one does not shift the
			// column that follows it.
			strings.Repeat(" ", countCol-text.Width(r.count))+r.count,
			text.Pad(r.kind, kindCol))

		// A failure can run to a hundred characters and more, and left to the
		// terminal it breaks mid-word at whatever column the window happens to
		// end at, with the rest starting hard against the left margin where a
		// machine's name goes. Carried on under the state instead, the columns
		// survive and the second line reads as more of the same thing.
		// Whether to wrap at all: not when there is no width to respect, not
		// when it already fits, and not when what is left of the terminal after
		// the columns is too narrow to wrap into. That last one is a terminal
		// so narrow the state would come out one word per line, which is worse
		// to read than a line running off the edge -- and the edge at least
		// uses the whole width, where a column of syllables uses a fifth of it.
		//
		// The middle one decides nothing: a state that already fits comes back
		// from the wrapping whole, so taking it out changes no line at any
		// width -- checked against every width from none to two hundred. It
		// earns its place by saying "it already fits" out loud, and by not
		// walking the string in the case that is nearly all of them.
		room := width - indent
		if width <= 0 || indent+text.Width(r.state) <= width || room < minWrapColumn {
			lines = append(lines, strings.TrimRight(prefix+r.state, " "))
			continue
		}
		for i, part := range text.Wrap(r.state, room, maxStateLines) {
			if i == 0 {
				lines = append(lines, prefix+part)
				continue
			}
			lines = append(lines, strings.Repeat(" ", indent)+part)
		}
	}
	return lines
}

// minWrapColumn is the narrowest column worth wrapping a state into. Roughly a
// long word and a short one: below it the wrapping is doing more harm than the
// overrun it prevents.
const minWrapColumn = 20

// maxStateLines bounds how far one machine's state may run. Generous -- the
// longest thing here is an ssh failure, and those are a sentence -- but not
// unbounded, since what a machine says about itself is not this side's to
// trust.
const maxStateLines = 8

// outputWidth is how wide status may draw, or 0 for no limit.
//
// Asked of the terminal the same way the menu asks. When there is no terminal
// -- run as a plugin action, whose output goes to the log, or piped into
// something -- there is no width to respect and nothing is wrapped: both want
// the line whole.
func outputWidth() int {
	cmd := exec.Command("stty", "size")
	cmd.Stdin = os.Stdin
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return 0
	}
	cols, err := strconv.Atoi(fields[1])
	if err != nil || cols <= 0 {
		return 0
	}
	return cols
}

// statusSummary is the one line Herdr shows as a notification.
//
// It used to begin with "mirroring" and list every machine with a count, which
// described a machine on a plain SSH terminal -- the default, and most of them
// -- as doing something it was not. The count was right; the word was left over
// from when mirroring was the only thing this did.
func statusSummary(hosts []syncd.HostInfo) string {
	if len(hosts) == 0 {
		// Where somebody is most likely to be reading this: they have just
		// installed the thing and asked it what it is doing.
		return "no machines connected — open the menu to pick one"
	}
	parts := make([]string, 0, len(hosts))
	for _, h := range hosts {
		// As in the lines above: a label is whatever somebody wrote in their
		// config, and this one goes out as a notification.
		name := text.Sanitize(h.Label)
		switch {
		case h.GaveUp || !h.Connected:
			parts = append(parts, name+" unreachable")
		case h.SSHOnly:
			parts = append(parts, fmt.Sprintf("%s %d open", name, h.Terminals))
		default:
			parts = append(parts, fmt.Sprintf("%s %d mirrored", name, h.Mirrors))
		}
	}
	full := strings.Join(parts, " · ")
	if text.Width(full) <= maxSummary {
		return full
	}

	// Too long to be read whole. This goes to a desktop notification, which is
	// drawn by something else and cut wherever that decides -- and the order
	// here is the order of the config file, which puts the machines that are
	// fine in front of the ones that are not. So with enough machines the part
	// that survives is "a 1 mirrored · b 1 mirrored", and the one that told
	// somebody something is the part that went.
	//
	// What is wrong is named and what is working is counted, because a machine
	// doing its job needs no name in a line this size.
	var wrong []string
	working := 0
	for _, h := range hosts {
		if h.GaveUp || !h.Connected {
			wrong = append(wrong, text.Sanitize(h.Label))
			continue
		}
		working++
	}
	if len(wrong) == 0 {
		return fmt.Sprintf("%d machines connected", working)
	}
	short := fmt.Sprintf("%d unreachable: %s", len(wrong), strings.Join(wrong, ", "))
	if working > 0 {
		short = fmt.Sprintf("%d connected · %s", working, short)
	}
	// Enough machines can be unreachable at once that naming them all runs
	// past the bound as well, and then the cut is back where it started --
	// except that everything before it is now worth reading.
	return text.Truncate(short, maxSummary)
}

// maxSummary bounds the line a notification is made of. It is not what the
// notification will show, which nothing here can know: it is short enough that
// whatever does the showing is unlikely to have to choose.
const maxSummary = 120
