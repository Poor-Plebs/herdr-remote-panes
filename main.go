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
	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/logfile"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/mirror"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/picker"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/version"
	"io"
	"path/filepath"
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
		// A configuration that cannot be read must not stop the daemon: the
		// menu and every action reach it over its socket, so exiting here
		// leaves them all failing with no visible reason.
		cfg, err := config.Load()
		if err != nil {
			log.Printf("%v", err)
			log.Print("continuing with defaults; fix the config and reconnect")
			cfg = config.Defaults()
		}
		// Settings that are readable but will not do what they look like they
		// say. Reporting them beats guessing silently, which is how a mode
		// spelled wrong quietly turned mirroring on.
		for _, problem := range cfg.Problems() {
			log.Printf("config: %s", problem)
		}
		// Herdr shows a plugin command's output once it has finished, and the
		// daemon does not finish, so everything it has to say would otherwise
		// go somewhere nobody can read it.
		if closeLog := daemonLog(); closeLog != nil {
			defer closeLog()
		}
		return syncd.NewWithConfigError(cfg, err).Run()

	case "mirror":
		return mirror.Run()

	case "menu":
		// Opens the picker as a Herdr popup: session-modal, receives every
		// key including Escape, and closes when the picker exits.
		_, err := herdrcli.Run("plugin", "pane", "open",
			"--plugin", syncd.PluginID, "--entrypoint", "picker",
			"--placement", "popup", "--focus")
		return err

	case "picker":
		return picker.Run(
			func(target string) (string, error) {
				return ask(syncd.Command{Cmd: "connect", Host: target})
			},
			func(target, mode string) (string, error) {
				return ask(syncd.Command{Cmd: "set-mode", Host: target, Mode: mode})
			},
			func(target string) (string, error) {
				return ask(syncd.Command{Cmd: "disconnect", Host: target})
			},
		)

	case "connect":
		// A host is optional: with none, every configured host reconnects.
		host := ""
		if len(args) > 0 {
			host = args[0]
		} else if env := strings.TrimSpace(os.Getenv("HRP_HOST")); env != "" {
			host = env
		} else {
			host = selectedText()
		}
		return call(syncd.Command{Cmd: "connect", Host: host})

	case "disconnect":
		host, err := hostArg(command, args)
		if err != nil {
			return err
		}
		return call(syncd.Command{Cmd: command, Host: host})

	case "open", "open-tab":
		// A host is optional here: with none, the workspace the action was
		// invoked from decides which machine the terminal opens on.
		host := ""
		if len(args) > 0 {
			host = args[0]
		} else if env := strings.TrimSpace(os.Getenv("HRP_HOST")); env != "" {
			host = env
		}
		placement := ""
		if command == "open-tab" {
			placement = "tab"
		}
		return call(syncd.Command{
			Cmd:       "open",
			Host:      host,
			Workspace: contextWorkspace(),
			Placement: placement,
		})

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

// contextWorkspace reports the workspace an action was invoked from.
func contextWorkspace() string {
	raw := os.Getenv("HERDR_WORKSPACE_ID")
	if raw != "" {
		return raw
	}
	var ctx struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal([]byte(os.Getenv("HERDR_PLUGIN_CONTEXT_JSON")), &ctx); err != nil {
		return ""
	}
	return ctx.WorkspaceID
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
  connect [ssh-target]       start mirroring a host, or all configured hosts
  open [ssh-target]          new terminal on the machine you are looking at
  open-tab [ssh-target]      the same, placed as a tab
  disconnect <ssh-target>    stop mirroring a host and close its panes
  menu                       open the machine menu (Herdr popup)
  picker                     draw the machine menu (popup entrypoint)
  refresh                    reconcile every connected host now
  status                     show connected hosts and mirror counts
`)
}

// maxDaemonLog bounds the daemon's log. It is quiet in ordinary use -- a line
// when a machine is connected or given up on -- so this is generous.
const maxDaemonLog = 256 * 1024

// daemonLog also writes the daemon's diagnostics to a file, and returns a
// function that closes it. Standard error is kept as well, so nothing is lost
// if the file cannot be opened.
func daemonLog() func() {
	dir, err := syncd.StateDir()
	if err != nil {
		return nil
	}
	f, err := logfile.Open(filepath.Join(dir, "daemon.log"), maxDaemonLog)
	if err != nil {
		log.Printf("could not open the daemon log: %v", err)
		return nil
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.Printf("herdr-remote-panes %s starting", version.Short())
	return func() {
		log.SetOutput(os.Stderr)
		_ = f.Close()
	}
}

// ask sends one command to the daemon and reports what it said, treating a
// refusal as an error so the menu has one thing to check rather than two.
func ask(cmd syncd.Command) (string, error) {
	reply, err := syncd.Ask(cmd)
	if err != nil {
		return "", err
	}
	if !reply.OK {
		return "", fmt.Errorf("%s", reply.Message)
	}
	return reply.Message, nil
}
