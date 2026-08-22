package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
)

// send delivers one command to the running daemon and returns its reply.
func send(cmd syncd.Command) (syncd.Reply, error) {
	socket, err := syncd.ControlSocket()
	if err != nil {
		return syncd.Reply{}, err
	}
	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return syncd.Reply{}, fmt.Errorf(
			"no running daemon (is the plugin enabled? check `herdr plugin log list --plugin %s`): %w",
			syncd.PluginID, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	if err := json.NewEncoder(conn).Encode(cmd); err != nil {
		return syncd.Reply{}, err
	}
	var reply syncd.Reply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return syncd.Reply{}, err
	}
	return reply, nil
}

// call runs a command that only reports success or failure.
func call(cmd syncd.Command) error {
	reply, err := send(cmd)
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
	reply, err := send(syncd.Command{Cmd: "status"})
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
		if !h.Connected {
			state = "error: " + h.LastError
		}
		fmt.Printf("%-24s %2d mirrored  %s\n", h.Label, h.Mirrors, state)
		summary = append(summary, fmt.Sprintf("%s (%d)", h.Label, h.Mirrors))
	}
	if os.Getenv("HERDR_PLUGIN_ACTION_ID") != "" {
		herdrcli.Notify("mirroring " + strings.Join(summary, ", "))
	}
	return nil
}
