package main

import (
	"fmt"
	"os"
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
	for _, line := range statusLines(reply.Hosts) {
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
func statusLines(hosts []syncd.HostInfo) []string {
	type row struct{ name, count, kind, state string }

	rows := make([]row, 0, len(hosts))
	for _, h := range hosts {
		r := row{name: text.Sanitize(h.Label), state: "ok"}
		switch {
		case h.GaveUp:
			r.state = "unreachable, not retrying: " + h.LastError
		case !h.Connected:
			r.state = "error: " + h.LastError
		}
		open := h.Mirrors
		r.kind = "mirrored"
		if h.SSHOnly {
			open, r.kind = h.Terminals, "ssh"
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

	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, strings.TrimRight(fmt.Sprintf("  %s  %s %s  %s",
			text.Pad(r.name, nameCol),
			// Counts right-aligned, so a two-digit one does not shift the
			// column that follows it.
			strings.Repeat(" ", countCol-text.Width(r.count))+r.count,
			text.Pad(r.kind, kindCol),
			r.state), " "))
	}
	return lines
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
		switch {
		case h.GaveUp || !h.Connected:
			parts = append(parts, h.Label+" unreachable")
		case h.SSHOnly:
			parts = append(parts, fmt.Sprintf("%s %d open", h.Label, h.Terminals))
		default:
			parts = append(parts, fmt.Sprintf("%s %d mirrored", h.Label, h.Mirrors))
		}
	}
	return strings.Join(parts, " · ")
}
