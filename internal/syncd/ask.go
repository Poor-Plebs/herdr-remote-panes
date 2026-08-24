package syncd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// sendTimeout bounds getting the command onto the socket, which is a few
// hundred bytes to a listener that has already accepted.
var sendTimeout = 10 * time.Second

// answerTimeout bounds waiting for the daemon to answer.
//
// Longer than sending, because it covers the work: connecting to a machine can
// take several ssh calls, each with its own timeout, and the last of them
// waits on a Herdr session starting at the far end. One deadline for the whole
// exchange had to cover that too, and when the work outlasted it the failure
// arrived as a broken connection rather than as anything about a machine being
// slow.
var answerTimeout = 2 * time.Minute

// Ask sends one command to the running daemon and returns its reply.
func Ask(cmd Command) (Reply, error) {
	socket, err := ControlSocket()
	if err != nil {
		return Reply{}, err
	}
	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return Reply{}, fmt.Errorf(
			"no running daemon (is the plugin enabled? check `herdr plugin log list --plugin %s`): %w",
			PluginID, err)
	}
	defer conn.Close()

	_ = conn.SetWriteDeadline(time.Now().Add(sendTimeout))
	if err := json.NewEncoder(conn).Encode(cmd); err != nil {
		return Reply{}, fmt.Errorf("could not send %s to the daemon: %w", cmd.Cmd, err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(answerTimeout))
	var reply Reply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		// Said in terms of what was being waited for. A bare "EOF" or "i/o
		// timeout" from a decoder says nothing about a command having been
		// sent to a daemon that has not answered yet.
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return Reply{}, fmt.Errorf(
				"the daemon did not answer %s within %s; it may still be working through a slow machine",
				cmd.Cmd, answerTimeout)
		}
		return Reply{}, fmt.Errorf("the daemon stopped answering %s: %w", cmd.Cmd, err)
	}
	return reply, nil
}
