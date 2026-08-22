// Command herdr-remote-panes mirrors panes from remote Herdr servers into the
// local session.
//
// It runs in three roles, selected by its first argument:
//
//	daemon   the [[startup]] hook; polls each host and reconciles mirror panes
//	mirror   the [[panes]] entrypoint; bridges one remote terminal into a pane
//	connect | disconnect | refresh | status
//	         [[actions]]; these talk to the running daemon
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/mirror"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
)

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("herdr-remote-panes: ")

	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	if err := run(args[0], args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(command string, args []string) error {
	switch command {
	case "daemon":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return syncd.New(cfg).Run()

	case "mirror":
		return mirror.Run()

	case "connect", "disconnect":
		host, err := hostArg(command, args)
		if err != nil {
			return err
		}
		return call(syncd.Command{Cmd: command, Host: host})

	case "refresh":
		return call(syncd.Command{Cmd: "refresh"})

	case "status":
		return status()

	case "help", "-h", "--help":
		usage()
		return nil

	default:
		usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

// hostArg resolves the host for connect/disconnect. When invoked as a plugin
// action there is no argv, so it falls back to HRP_HOST and then to the text
// the user had selected when they triggered the action.
func hostArg(command string, args []string) (string, error) {
	if len(args) > 0 && args[0] != "" {
		return args[0], nil
	}
	if host := strings.TrimSpace(os.Getenv("HRP_HOST")); host != "" {
		return host, nil
	}
	if host := selectedText(); host != "" {
		return host, nil
	}
	return "", fmt.Errorf(
		"usage: herdr-remote-panes %s <ssh-target> (or select the target and run the action)",
		command)
}

// selectedText pulls the selection out of Herdr's invocation context.
func selectedText() string {
	raw := os.Getenv("HERDR_PLUGIN_CONTEXT_JSON")
	if raw == "" {
		return ""
	}
	var ctx struct {
		SelectedText string `json:"selected_text"`
	}
	if err := json.Unmarshal([]byte(raw), &ctx); err != nil {
		return ""
	}
	return strings.TrimSpace(ctx.SelectedText)
}

func usage() {
	fmt.Fprint(os.Stderr, `herdr-remote-panes — mirror remote Herdr panes into this session

  daemon                     run the reconciler (Herdr [[startup]] hook)
  mirror                     bridge one remote terminal (Herdr pane entrypoint)
  connect <ssh-target>       start mirroring a host
  disconnect <ssh-target>    stop mirroring a host and close its panes
  refresh                    reconcile every connected host now
  status                     show connected hosts and mirror counts
`)
}
