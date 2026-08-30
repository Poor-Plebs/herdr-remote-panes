// Package cli is the plugin's command line: the argument it was given decides
// which of its roles it runs in, and everything a role needs is here.
//
// Apart from [Main] this is unexported. It is one binary's insides, split out
// of the repository root so that the root holds the command and nothing else
// -- a Go package's tests live in its own directory, so a root full of
// implementation is a root full of tests.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/logfile"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/mirror"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/picker"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/version"
)

// Main runs the command and reports what to exit with.
//
// The exit codes are the ones a shell expects and the manifest relies on: 2
// for being asked for something that is not a command, 1 for a command that
// was understood and did not work.
func Main() int {
	// The date as well as the time. daemon.log is kept until it rolls at a
	// quarter of a megabyte, which is days of a healthy daemon, and the times
	// alone then run backwards down the page every time Herdr is restarted on
	// a later day -- "stopping" at 21:29 above "starting" at 12:24. Anyone
	// working out when something happened has to count restarts to place it.
	//
	// mirror.log, written beside it, has carried a full timestamp all along.
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetPrefix("herdr-remote-panes: ")
	// Every command's failure is reported through this logger, and a failure
	// that came from a machine carries whatever that machine said. Herdr shows
	// a command's standard error once it has finished, so an escape in it acts
	// on the terminal reading the report.
	log.SetOutput(logfile.Sanitized(os.Stderr))

	args := os.Args[1:]
	if len(args) == 0 {
		// Run with nothing to do: stderr and a non-zero exit, same as a
		// command that was not understood.
		usage(os.Stderr)
		return 2
	}

	if err := run(args[0], args[1:]); err != nil {
		log.Print(err)
		return 1
	}
	return 0
}

// hostFor picks which machine a command names: the argument if one was given,
// otherwise HRP_HOST, which Herdr sets for an action invoked on a pane.
//
// Whether a machine was named at all is the second half of the answer, and it
// is not the same question as whether the name is empty. An empty argument was
// still an argument -- somebody's keybinding passing nothing through -- and
// treating it as "none given" sends the command somewhere of its own choosing
// instead of failing where it was aimed.
func hostFor(args []string) (host string, named bool) {
	if len(args) > 0 {
		return args[0], true
	}
	if env := strings.TrimSpace(os.Getenv("HRP_HOST")); env != "" {
		return env, true
	}
	return "", false
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
		host, named := hostFor(args)
		if !named {
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
		// invoked from decides which machine the terminal opens on. No falling
		// back to what is selected, unlike connect above: this opens something
		// new, and opening it on whatever the cursor happens to be over is a
		// surprise where reconnecting to it is what was meant.
		host, _ := hostFor(args)
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

	case "version", "--version":
		return printVersion()

	case "help", "-h", "--help":
		// Asked for, so it is the answer rather than a complaint about the
		// question: stdout, where `| less` and `| grep` can reach it.
		usage(os.Stdout)
		return nil

	default:
		// Not asked for. It goes to stderr so that a mistyped command cannot
		// put a page of help into whatever was reading this command's output.
		usage(os.Stderr)
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

func usage(w io.Writer) {
	fmt.Fprint(w, `herdr-remote-panes — work on other machines from this Herdr

  daemon                     run the reconciler (Herdr [[startup]] hook)
  mirror                     run a machine's terminal in a pane (pane entrypoint)
  menu                       open the machine menu (Herdr popup)
  picker                     draw the machine menu (popup entrypoint)

  connect [ssh-target]       connect to a machine and go to its space;
                             with no machine, reconnects every configured one
  disconnect <ssh-target>    close a machine's terminals here
  open [ssh-target]          new terminal on the machine you are looking at
  open-tab [ssh-target]      the same, placed as a tab
  refresh                    reconcile every connected machine now
  status                     list the machines connected and what each has open
  version                    which build this is, and which one the daemon runs

Machines get a plain SSH terminal by default, which needs nothing installed on
them. Mirroring, which keeps both ends showing the same terminals, is
experimental and turned on per machine.
`)
}

// printVersion reports this build and, when one is answering, the daemon's.
//
// Both, because they disagree: installing an update replaces the files on disk
// and leaves the running daemon alone, so its fixes do nothing until Herdr is
// restarted. The daemon is the half that matters -- it is the one reconciling
// panes -- and until now the only way to see which build it was came from
// reading its log.
func printVersion() error {
	return reportVersion(os.Stdout, os.Stderr, version.Short())
}

// reportVersion is printVersion with the installed build handed to it and
// somewhere to write, since version.Short cannot be anything but "unknown"
// inside a test binary -- and that one answer is what silences the warning
// below, so with it the whole of this reads as correct whether it is or not.
func reportVersion(out, warn io.Writer, installed string) error {
	running := ""
	reply, err := syncd.Ask(syncd.Command{Cmd: "status"})
	// Whether the question could be put at all. Run from an ordinary shell
	// rather than through Herdr, there is no state directory and so no socket
	// to knock on -- and every failure here read as "not running", which is a
	// definite answer to a question that was never asked. The daemon may be up
	// and mirroring; this process cannot see it either way.
	//
	// Worth telling apart because of what this command is for: comparing the
	// build installed against the one running, after an upgrade. Told the
	// daemon is not running, somebody restarts Herdr to no purpose -- or
	// concludes the new build is in use, which is the opposite of what this is
	// asked to establish.
	_, reachable := syncd.ControlSocket()
	switch {
	case reachable != nil:
		running = "cannot tell from here — run this through Herdr"
	case err != nil:
		// Not an error. Asking which build you have installed is a reasonable
		// thing to do when the daemon is not running -- it is a reasonable
		// thing to do *because* the daemon is not running.
		running = "not running"
	case reply.Revision == "":
		// Built outside a checkout, which is what `go run` and a test binary
		// look like. Saying so beats a blank column.
		running = "unknown"
	default:
		running = reply.Revision
	}

	for _, line := range versionLines(installed, running) {
		fmt.Fprintln(out, line)
	}
	// Only when something answered. A daemon that is not running is not a
	// daemon running an older build, and saying both at once is a
	// contradiction in four lines of output.
	// Only err, and not reachable as well: Ask asks ControlSocket first and
	// hands back its error, so anything that answered was reached. The pair
	// read as belt and braces and the braces were sewn to the belt.
	if err == nil {
		if stale := version.StaleMessageFor(reply.Revision, installed); stale != "" {
			fmt.Fprintf(warn, "warning: %s\n", stale)
		}
	}
	return nil
}

// The two things a version answer names. Their widths set the column, rather
// than a number written down beside them that goes stale the moment either is
// renamed.
const (
	binaryLabel = "herdr-remote-panes"
	daemonLabel = "daemon"
)

// versionLines is the answer as columns: this build, and the daemon's.
func versionLines(binary, daemon string) []string {
	width := max(len(binaryLabel), len(daemonLabel))
	return []string{
		fmt.Sprintf("%-*s %s", width, binaryLabel, binary),
		fmt.Sprintf("%-*s %s", width, daemonLabel, daemon),
	}
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
	log.SetOutput(io.MultiWriter(logfile.Sanitized(os.Stderr), f))
	log.Printf("herdr-remote-panes %s starting", version.Short())
	return func() {
		log.SetOutput(logfile.Sanitized(os.Stderr))
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
