package cli

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
)

// answerWith puts a daemon on the control socket that gives one reply to
// anything asked of it.
//
// What `status` prints is assembled from that reply and then written out, and
// nothing here could reach the writing: every test of it called the pieces.
// A mutation sweep found the join -- the condition deciding whether the advice
// under the table is printed at all could be inverted and no test noticed.
// The commands the daemon was sent are returned as well, because half of what
// this package does is turning a word somebody typed into one of those, and
// nothing could see the result of that: the daemon's side is tested with a
// command built by hand, and the words are tested against the usage text.
func answerWith(t *testing.T, reply syncd.Reply) func() []syncd.Command {
	t.Helper()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	socket, err := syncd.ControlSocket()
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

	var mu sync.Mutex
	var asked []syncd.Command

	served := make(chan struct{})
	go func() {
		defer close(served)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var cmd syncd.Command
			if err := json.NewDecoder(conn).Decode(&cmd); err == nil {
				mu.Lock()
				asked = append(asked, cmd)
				mu.Unlock()
				_ = json.NewEncoder(conn).Encode(reply)
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-served
	})
	return func() []syncd.Command {
		mu.Lock()
		defer mu.Unlock()
		return append([]syncd.Command(nil), asked...)
	}
}

// whatStatusPrinted runs status with standard output captured.
func whatStatusPrinted(t *testing.T) string {
	t.Helper()
	saved := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	done := make(chan string)
	go func() {
		var b []byte
		buf := make([]byte, 4096)
		for {
			n, err := read.Read(buf)
			b = append(b, buf[:n]...)
			if err != nil {
				break
			}
		}
		done <- string(b)
	}()
	err = status()
	write.Close()
	os.Stdout = saved
	out := <-done
	read.Close()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return out
}
