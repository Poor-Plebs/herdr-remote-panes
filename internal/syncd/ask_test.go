package syncd

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// listenForTest puts a control socket where Ask will find it and hands back a
// listener the test drives itself.
func listenForTest(t *testing.T) net.Listener {
	t.Helper()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	socket, err := ControlSocket()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func TestAskSaysWhatItWasWaitingFor(t *testing.T) {
	// The client used to carry one deadline for the whole exchange, so it also
	// had to cover the daemon's work -- and the daemon's work is connecting to
	// machines. When it ran long, the decoder failed and the failure surfaced
	// as "EOF" or "i/o timeout", which says nothing about a command having been
	// sent to a daemon that has not answered yet.
	restore := answerTimeout
	answerTimeout = 100 * time.Millisecond
	defer func() { answerTimeout = restore }()
	// Read once, here: the goroutine below must not read the package variable
	// that the deferred restore writes.
	slow := 10 * answerTimeout

	listener := listenForTest(t)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Reads the command and then takes its time, as a slow connect does.
		var cmd Command
		_ = json.NewDecoder(conn).Decode(&cmd)
		time.Sleep(slow)
	}()

	_, err := Ask(Command{Cmd: "connect"})
	if err == nil {
		t.Fatal("a daemon that never answered was treated as success")
	}
	for _, want := range []string{"daemon", "connect", "did not answer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestAskCarriesTheDaemonsAnswerBack(t *testing.T) {
	listener := listenForTest(t)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var cmd Command
		if err := json.NewDecoder(conn).Decode(&cmd); err != nil {
			return
		}
		_ = json.NewEncoder(conn).Encode(Reply{OK: true, Message: "connected to " + cmd.Host})
	}()

	reply, err := Ask(Command{Cmd: "connect", Host: "bot"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !reply.OK || reply.Message != "connected to bot" {
		t.Errorf("reply = %+v", reply)
	}
}

func TestAskSaysWhenNothingIsListening(t *testing.T) {
	// The daemon not running is the ordinary way this fails, and the message
	// has to say where to look rather than name a socket path.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	_, err := Ask(Command{Cmd: "status"})
	if err == nil {
		t.Fatal("Ask succeeded with no daemon running")
	}
	if !strings.Contains(err.Error(), "no running daemon") {
		t.Errorf("error %q does not say the daemon is not running", err)
	}
	if !strings.Contains(err.Error(), "herdr plugin log list") {
		t.Errorf("error %q does not say where to look", err)
	}
}

func TestAskGivesUpOnADaemonThatHangsUp(t *testing.T) {
	// Distinct from a timeout: the connection went, which means the daemon
	// died or refused rather than being slow.
	listener := listenForTest(t)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		// The command is read, so sending succeeds; the connection then goes
		// without an answer, which is what a daemon dying mid-command looks
		// like. Closing before the read would fail the send instead, which is
		// a different thing and says so.
		var cmd Command
		_ = json.NewDecoder(conn).Decode(&cmd)
		conn.Close()
	}()

	_, err := Ask(Command{Cmd: "status"})
	if err == nil {
		t.Fatal("a daemon that hung up was treated as success")
	}
	if !strings.Contains(err.Error(), "stopped answering") {
		t.Errorf("error %q should say the daemon stopped answering", err)
	}
}
