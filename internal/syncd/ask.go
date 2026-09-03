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

// ErrNoDaemon is returned when nothing is listening on the control socket, as
// opposed to a daemon that answered the connection and then did not finish.
//
// Told apart because the two want opposite things from whoever is reading. No
// daemon means start one; a daemon that has not answered yet may be part way
// through connecting a slow machine, and the useful thing is to wait. Callers
// had only the message to go on, and the menu turned every failure into "the
// daemon is not running" -- which is the opposite of true for a daemon that is
// two ssh calls into a connect.
var ErrNoDaemon = errors.New("no running daemon")

// Ask sends one command to the running daemon and returns its reply.
func Ask(cmd Command) (Reply, error) {
	socket, err := ControlSocket()
	if err != nil {
		return Reply{}, err
	}
	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		// Both wrapped: the sentinel for callers that need to tell this apart,
		// and the dial error because it says which socket and why. The text is
		// what it always was.
		return Reply{}, fmt.Errorf(
			"%w (is the plugin enabled? check `herdr plugin log list --plugin %s`): %w",
			ErrNoDaemon, PluginID, err)
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
