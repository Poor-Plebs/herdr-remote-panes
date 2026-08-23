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
	if running, installed := reply.Revision, version.Short(); staleDaemon(running, installed) {
		if running == "" {
			// A daemon old enough not to report its build at all.
			running = "an older build"
		}
		fmt.Fprintf(os.Stderr,
			"warning: the running daemon is %s but %s is installed; restart Herdr to pick up the update\n",
			running, installed)
	}
	if len(reply.Hosts) == 0 {
		report("no hosts connected")
		return nil
	}
	summary := make([]string, 0, len(reply.Hosts))
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
		summary = append(summary, fmt.Sprintf("%s (%d)", h.Label, count))
	}
	if os.Getenv("HERDR_PLUGIN_ACTION_ID") != "" {
		herdrcli.Notify("mirroring " + strings.Join(summary, ", "))
	}
	return nil
}

// staleDaemon reports whether the daemon answering is a different build from
// the one installed, which happens after an update: the files are replaced but
// the running daemon is left alone, so its fixes do nothing until Herdr
// restarts.
//
// A build made outside a checkout has no revision to compare, so it is left
// alone rather than warned about on every status.
func staleDaemon(running, installed string) bool {
	if installed == "" || installed == "unknown" {
		return false
	}
	return running != installed
}
