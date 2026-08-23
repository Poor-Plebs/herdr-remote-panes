package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/version"
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
	for _, h := range reply.Hosts {
		state := "ok"
		switch {
		case h.GaveUp:
			state = "unreachable, not retrying: " + h.LastError
		case !h.Connected:
			state = "error: " + h.LastError
		}
		count, kind := h.Mirrors, "mirrored"
		if h.SSHOnly {
			count, kind = h.Terminals, "ssh"
		}
		fmt.Printf("  %-22s %2d %-9s %s\n", h.Label, count, kind, state)
	}
	if os.Getenv("HERDR_PLUGIN_ACTION_ID") != "" {
		herdrcli.Notify(statusSummary(reply.Hosts))
	}
	return nil
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
