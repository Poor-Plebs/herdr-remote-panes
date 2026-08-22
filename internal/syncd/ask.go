package syncd

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

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
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	if err := json.NewEncoder(conn).Encode(cmd); err != nil {
		return Reply{}, err
	}
	var reply Reply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return Reply{}, err
	}
	return reply, nil
}
