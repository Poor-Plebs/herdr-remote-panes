package mirror

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShouldReportFailure(t *testing.T) {
	boom := errors.New("ssh: connection closed")

	tests := []struct {
		name        string
		err         error
		askedToStop bool
		want        bool
	}{
		{
			// The connection dropped on its own. Without recording it, the
			// machine's space empties and nothing brings it back.
			name: "a bridge that dies on its own is a failure",
			err:  boom, askedToStop: false, want: true,
		},
		{
			// Closing a pane signals this process, and the bridge then exits
			// with an error too. Recording that would reopen the terminal
			// someone just closed.
			name: "an exit that was asked for is not a failure",
			err:  boom, askedToStop: true, want: false,
		},
		{
			name: "a clean exit is never a failure",
			err:  nil, askedToStop: false, want: false,
		},
		{
			name: "a clean exit after being asked to stop is not a failure",
			err:  nil, askedToStop: true, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldReportFailure(tt.err, tt.askedToStop); got != tt.want {
				t.Errorf("shouldReportFailure(%v, %v) = %v, want %v",
					tt.err, tt.askedToStop, got, tt.want)
			}
		})
	}
}

func TestDescribeCommand(t *testing.T) {
	// The ssh options are the same on every call -- control socket, keepalive,
	// timeouts -- and printing them buried the one part that differs, the
	// machine, behind a hundred and fifty characters of noise in a pane that is
	// about to close.
	argv := []string{
		"ssh", "-o", "ControlMaster=auto", "-o", "ControlPath=/tmp/hrp-abc.sock",
		"-o", "ControlPersist=120", "-o", "ServerAliveInterval=15",
		"-o", "ConnectTimeout=10", "-tt", "-o", "BatchMode=no",
		"--", "bot",
	}
	if got := describeCommand(argv); got != "ssh bot" {
		t.Errorf("describeCommand = %q, want %q", got, "ssh bot")
	}

	// Whatever is being run on the machine is worth keeping.
	withCommand := append(append([]string{}, argv...), "herdr pane list")
	if got := describeCommand(withCommand); got != "ssh bot herdr pane list" {
		t.Errorf("describeCommand = %q, want the remote command kept", got)
	}

	// Anything not built that way is left as it is rather than mangled.
	plain := []string{"stty", "size"}
	if got := describeCommand(plain); got != "stty size" {
		t.Errorf("describeCommand = %q, want it unchanged", got)
	}
	if got := describeCommand(nil); got != "" {
		t.Errorf("describeCommand(nil) = %q", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	// A plain SSH pane has neither a name nor a remote terminal, and the
	// message read as "[herdr-remote-panes] : exit status 255" -- a colon
	// introducing nothing. The machine identifies the pane in that case.
	if got := firstNonEmpty("", "", "bot"); got != "bot" {
		t.Errorf("firstNonEmpty = %q, want the machine", got)
	}
	if got := firstNonEmpty("shell@bot", "term_1", "bot"); got != "shell@bot" {
		t.Errorf("firstNonEmpty = %q, want the name", got)
	}
	if got := firstNonEmpty("", "", ""); got != "" {
		t.Errorf("firstNonEmpty = %q, want nothing", got)
	}
}

func TestFailureLogIsRolledOverRatherThanGrowingForever(t *testing.T) {
	// Every failed pane appended to this and nothing ever shortened it, so it
	// grew for as long as the plugin was installed -- slowly, but with no end
	// to it, in a directory nobody thinks to look in.
	dir := t.TempDir()
	path := filepath.Join(dir, "mirror.log")

	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxLogBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	appendToLog(path, "the newest failure")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= maxLogBytes {
		t.Errorf("the log is still %d bytes; it was not rolled over", info.Size())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "the newest failure") {
		t.Error("the message that triggered the rollover was lost")
	}

	// One generation is kept: the failure that began a run of them is worth
	// not losing outright.
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("the previous log was not kept: %v", err)
	}

	// A second rollover replaces that generation rather than accumulating.
	if err := os.WriteFile(path, bytes.Repeat([]byte("y"), maxLogBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	appendToLog(path, "later still")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want the log and one generation", names)
	}
}

func TestFailureLogIsCreatedPrivate(t *testing.T) {
	// It records which machines were being reached and why it failed.
	dir := t.TempDir()
	path := filepath.Join(dir, "mirror.log")
	appendToLog(path, "could not reach bot")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions are %o, want 600", perm)
	}
}
