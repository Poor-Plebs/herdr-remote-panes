// Package mirror is the plugin pane entrypoint: it bridges one remote Herdr
// terminal into the local pane it is running in.
package mirror

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
)

// errResized ends a stream so the next one can negotiate the new pane size.
var errResized = errors.New("pane resized")

// Env var names the daemon sets on each mirror pane.
const (
	EnvTarget   = "HRP_TARGET"
	EnvSession  = "HRP_SESSION"
	EnvTerminal = "HRP_TERMINAL"
	EnvMode     = "HRP_MODE"
	EnvName     = "HRP_NAME"
)

// Run bridges the remote terminal named by the environment into this pane.
//
// Herdr does not capture a pane process's stderr, and a pane closes the moment
// its command exits, so a failure is reported three ways: into the pane itself,
// into the plugin state directory, and through the exit status.
func Run() error {
	err := bridge()
	if err != nil {
		reportFailure(err)
	}
	return err
}

// reportFailure leaves a trace a user can actually find.
func reportFailure(err error) {
	name := os.Getenv(EnvName)
	if name == "" {
		name = os.Getenv(EnvTerminal)
	}
	message := fmt.Sprintf("[herdr-remote-panes] %s: %v", name, err)

	if dir := os.Getenv("HERDR_PLUGIN_STATE_DIR"); dir != "" {
		if f, ferr := os.OpenFile(filepath.Join(dir, "mirror.log"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); ferr == nil {
			fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), message)
			f.Close()
		}
	}

	// Hold the pane open long enough for the message to be read.
	fmt.Fprintf(os.Stdout, "\r\n%s\r\n", message)
	time.Sleep(5 * time.Second)
}

func bridge() error {
	target := os.Getenv(EnvTarget)
	terminal := os.Getenv(EnvTerminal)
	if target == "" || terminal == "" {
		return fmt.Errorf("%s and %s must be set", EnvTarget, EnvTerminal)
	}
	client := remote.New(target, os.Getenv(EnvSession))

	switch config.Mode(os.Getenv(EnvMode)) {
	case config.ModeObserve:
		return observe(client, terminal)
	default:
		return attach(client, terminal)
	}
}

// attach hands the pane straight to `herdr terminal attach` on the far side.
// Herdr's own direct-attach client then owns input, resize and scrollback.
func attach(client *remote.Client, terminal string) error {
	argv := client.Argv(true, "terminal", "attach", terminal)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w running: %s", err, strings.Join(argv, " "))
	}
	return nil
}

// observe renders a read-only copy of the remote terminal. Unlike attach it
// takes no ownership of the remote terminal and never locks its size, so any
// number of machines can watch the same pane at once.
func observe(client *remote.Client, terminal string) error {
	restore := rawMode()
	defer restore()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	attempt := 0
	for {
		cols, rows := windowSize()
		err := streamOnce(client, terminal, cols, rows, winch)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, errResized):
			// Not a failure: reconnect straight away at the new size.
			attempt = 0
			continue
		case attempt >= 4:
			return err
		}
		select {
		case <-time.After(time.Duration(attempt+1) * time.Second):
		case <-winch:
		}
		attempt++
	}
}

// streamOnce runs one observe stream, returning nil when the remote terminal
// is gone and an error when the stream should be retried. A resize ends the
// stream so the next one can negotiate the new size.
func streamOnce(client *remote.Client, terminal string, cols, rows int, winch <-chan os.Signal) error {
	argv := client.Argv(false, "terminal", "session", "observe", terminal,
		"--cols", strconv.Itoa(cols), "--rows", strconv.Itoa(rows))

	cmd := exec.Command(argv[0], argv[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{})
	var wasResized atomic.Bool
	go func() {
		select {
		case <-winch:
			wasResized.Store(true)
			_ = cmd.Process.Signal(syscall.SIGTERM)
		case <-done:
		}
	}()

	frames := bufio.NewScanner(stdout)
	frames.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	sawFrame := false
	for frames.Scan() {
		line := strings.TrimSpace(frames.Text())
		if line == "" {
			continue
		}
		var frame struct {
			Bytes string `json:"bytes"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil || frame.Bytes == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(frame.Bytes)
		if err != nil {
			continue
		}
		sawFrame = true
		if _, err := os.Stdout.Write(raw); err != nil {
			break
		}
	}
	close(done)

	waitErr := cmd.Wait()
	if wasResized.Load() {
		return errResized
	}
	if waitErr != nil && !sawFrame {
		return waitErr
	}
	// A stream that delivered frames and then ended cleanly means the remote
	// terminal went away; let the pane close.
	return nil
}

// windowSize reports this pane's size, falling back to a sane default when the
// pty cannot be queried.
func windowSize() (cols, rows int) {
	cols, rows = 120, 40
	out, err := sttyOutput("size")
	if err != nil {
		return cols, rows
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return cols, rows
	}
	if r, err := strconv.Atoi(fields[0]); err == nil && r > 0 {
		rows = r
	}
	if c, err := strconv.Atoi(fields[1]); err == nil && c > 0 {
		cols = c
	}
	return cols, rows
}

// rawMode stops the local pty from echoing keystrokes into a read-only mirror.
func rawMode() func() {
	if _, err := sttyOutput("raw", "-echo"); err != nil {
		return func() {}
	}
	return func() { _, _ = sttyOutput("sane") }
}

func sttyOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = nil
	return cmd.Output()
}
