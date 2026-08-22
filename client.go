package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
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
		kind := "mirrored"
		if h.SSHOnly {
			kind = "ssh panes (no herdr on host)"
		}
		fmt.Printf("%-24s %2d %s  %s\n", h.Label, h.Mirrors, kind, state)
		summary = append(summary, fmt.Sprintf("%s (%d)", h.Label, h.Mirrors))
	}
	if os.Getenv("HERDR_PLUGIN_ACTION_ID") != "" {
		herdrcli.Notify("mirroring " + strings.Join(summary, ", "))
	}
	return nil
}
