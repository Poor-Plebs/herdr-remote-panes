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
	"os"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
