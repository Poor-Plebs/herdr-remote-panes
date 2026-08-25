package main

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
		report("no hosts connected")
		return nil
	}
	for _, line := range statusLines(reply.Hosts, outputWidth()) {
		fmt.Println(line)
	}
	if os.Getenv("HERDR_PLUGIN_ACTION_ID") != "" {
		herdrcli.Notify(statusSummary(reply.Hosts))
	}
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
		if h.SSHOnly {
			open, r.kind = h.Terminals, "ssh"
		}
		// Terminals the machine has that this could not mirror. Left out, the
		// count simply reads lower than what is on the machine, with nothing
		// to say why. Only when there is nothing worse to report: a machine
		// that cannot be reached at all has a better answer than this one.
		// Asked to mirror and could not. The machine works, so this is not a
		// failure — but the settings say one thing and the machine is doing
		// another, and without this only the daemon's log knew.
		if h.NoHerdr && r.state == "ok" {
			r.state = "mirroring off: no herdr found on the machine — set herdr_bin if it is installed elsewhere there"
		}
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
		room := width - indent
		if width <= 0 || indent+text.Width(r.state) <= width || room < 20 {
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
		return "no machines connected"
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
	return strings.Join(parts, " · ")
}
