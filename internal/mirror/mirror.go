// Package mirror is what runs inside a pane this plugin opens.
//
// By default that is an SSH session on the machine, and nothing needs to be
// installed there for it. With mirroring turned on, which is experimental, it
// bridges one of the machine's own Herdr terminals into the pane instead, so
// what is on screen is a live terminal rather than a copy of one.
package mirror

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/capped"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/logfile"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
)

// errResized ends a stream so the next one can negotiate the new pane size.
var errResized = errors.New("pane resized")

// errStreamAbandoned ends a stream this side stopped reading, so it is retried
// rather than read as the remote terminal having gone.
var errStreamAbandoned = errors.New("stopped reading the stream")

// Env var names the daemon sets on each mirror pane.
const (
	EnvTarget   = "HRP_TARGET"
	EnvSession  = "HRP_SESSION"
	EnvTerminal = "HRP_TERMINAL"
	EnvMode     = "HRP_MODE"
	EnvBin      = "HRP_BIN"
	EnvTakeover = "HRP_TAKEOVER"
	EnvName     = "HRP_NAME"
)

// Run bridges the remote terminal named by the environment into this pane.
//
// Herdr does not capture a pane process's stderr, and a pane closes the moment
// its command exits, so a failure is reported three ways: into the pane itself,
// into the plugin state directory, and through the exit status.
func Run() error {
	err := bridge()
	if shouldReportFailure(err, stopped.Load()) {
		reportFailure(err)
	}
	return err
}

// shouldReportFailure reports whether an exit was a dropped connection rather
// than a deliberate close.
//
// Closing a pane signals this process, which makes the bridge exit with an
// error too, so the error alone cannot tell them apart. Getting this backwards
// means either reopening a terminal someone just closed, or never recovering
// from a dropped link.
func shouldReportFailure(err error, askedToStop bool) bool {
	return err != nil && !askedToStop
}

// stopped records that this process was asked to exit, which means the pane was
// closed rather than the connection lost.
var stopped atomic.Bool

// watchForStop forwards a termination signal to the bridge and remembers that
// the exit was asked for. The returned function stops watching.
func watchForStop(proc *os.Process) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	done := make(chan struct{})
	go func() {
		select {
		case <-signals:
			stopped.Store(true)
			if proc != nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
		case <-done:
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
	}
}

// describeCommand names a command for an error message without repeating every
// option it was given.
//
// The ssh options here are the same on every call -- control socket, keepalive,
// timeouts -- and printing them buried the one part that differs, the machine,
// behind a hundred and fifty characters of noise in a pane that is about to
// close. Everything after "--" is the destination and whatever is being run on
// it, which is the part worth reading.
func describeCommand(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	// The first "--", not the last: ssh's own separator is the one before the
	// destination, and anything further along belongs to the command being run
	// on the machine. Scanning from the end took that one instead, which threw
	// away the destination -- the single thing this is here to show.
	for i := 1; i < len(argv); i++ {
		if argv[i] == "--" {
			return strings.Join(append([]string{argv[0]}, argv[i+1:]...), " ")
		}
	}
	return strings.Join(argv, " ")
}

// holdOpen is how long a pane that failed stays on screen before it goes, so
// the message on it can be read. A variable so a test need not wait it out.
var holdOpen = 5 * time.Second

// unrecordedFailure says what failing to record a failure costs, which depends
// on what the attempt left behind.
//
// MarkFailed fails two ways and they leave opposite states. When nothing was
// written -- the directory the marks live in could not be made -- the daemon
// finds no mark, and a pane that went with no mark beside it is the description
// of a terminal somebody shut; with close_propagates on, that closes the
// terminal on the machine. When something is at the path and could not be
// replaced -- a stale mark that cannot be written over, or a disk that took the
// file and then would not take its contents, since os.WriteFile creates before
// it writes and leaves what it made behind -- the daemon finds a mark after
// all. It then reads a failure without the reason, which sends it by the count
// of dropped terminals rather than by what went wrong.
//
// Measured, because the two are a stat apart: with the marks directory blocked,
// Failed is false; with a directory or an unwritable file at the mark's own
// path, MarkFailed fails and Failed is true. Asking Failed here asks exactly
// what the daemon will ask.
func unrecordedFailure(paneID string, err error) string {
	detail := text.Truncate(text.Sanitize(err.Error()), maxSaidWidth)
	if Failed(paneID) {
		return fmt.Sprintf("and the reason could not be recorded for the daemon: "+
			"%s -- it will see that this pane failed but not why", detail)
	}
	return fmt.Sprintf("and the failure could not be recorded for the daemon: "+
		"%s -- it will read this pane as one you closed", detail)
}

// reportFailure leaves a trace a user can actually find.
func reportFailure(err error) {

	// A plain SSH pane has neither a name nor a remote terminal, so this read
	// as "[herdr-remote-panes] : exit status 255" -- a colon introducing
	// nothing. The machine is what identifies the pane in that case.
	name := text.Sanitize(firstNonEmpty(
		os.Getenv(EnvName), os.Getenv(EnvTerminal), os.Getenv(EnvTarget)))

	// Whatever the far side said arrives in here as it was written: an error
	// from a command carries that command's standard error verbatim, and this
	// writes it to a pane and to a file the troubleshooting page tells people
	// to cat. A machine's banner is the far side's to choose, so it can hold
	// the escape that clears the screen, or moves the cursor, or renames the
	// window -- and up to capped.Max of it can arrive, which is eight
	// megabytes on one line.
	//
	// The same treatment the package's own command errors already get in
	// lastLine, applied where it is written rather than where it is made, so
	// that it covers the errors that come from elsewhere too.
	//
	// Newlines become spaces before the rest goes: dropping them outright
	// would run the last word of one line into the first of the next, and an
	// entry that keeps them is several lines in a log that is one line per
	// entry with the time at the front.
	detail := text.Truncate(text.Sanitize(strings.ReplaceAll(err.Error(), "\n", " ")), maxSaidWidth)
	message := fmt.Sprintf("[herdr-remote-panes] %s", detail)
	if name != "" {
		message = fmt.Sprintf("[herdr-remote-panes] %s: %s", name, detail)
	}

	// The record the daemon reads gets the same bounded text. It is written to
	// a file of its own and read back by something that shows it, so it has
	// both of the same problems.
	if err := MarkFailed(os.Getenv("HERDR_PANE_ID"), detail); err != nil {
		// Said in the same file the failure itself goes to, since the mark is
		// how the daemon tells this from a terminal somebody shut. Whoever
		// reads the log afterwards has the failure and the reason the plugin
		// could not record it, one after the other.
		message += fmt.Sprintf(" (%s)",
			unrecordedFailure(os.Getenv("HERDR_PANE_ID"), err))
	}

	if dir := os.Getenv("HERDR_PLUGIN_STATE_DIR"); dir != "" {
		appendToLog(filepath.Join(dir, "mirror.log"), message)
	}

	// Hold the pane open long enough for the message to be read.
	fmt.Fprintf(os.Stdout, "\r\n%s\r\n", message)
	time.Sleep(holdOpen)
}

func bridge() error {
	// Record liveness so the daemon can tell a running mirror from a pane that
	// Herdr restored without its command.
	defer markLive(os.Getenv("HERDR_PANE_ID"), os.Getenv(EnvTerminal))()

	// Whatever the far side turned on, turned off again. The pane outlives the
	// stream -- Herdr keeps it, showing the last of what arrived -- and
	// everything below hands it to a program on another machine that can set
	// modes this end has no other way of clearing.
	defer restorePane(os.Stdout)

	target := os.Getenv(EnvTarget)
	if target == "" {
		// Seen twice in a real session, five seconds each time, saying the
		// name of an environment variable to somebody who has never heard of
		// it. This runs as a pane's command and is given the machine in its
		// environment; without one there is nothing it could connect to, and
		// nothing it can do about that from here.
		//
		// The likely cause is Herdr bringing a pane back by running its
		// command again without what the daemon set alongside it. The daemon
		// notices such a pane and replaces it, so the thing to do is wait or
		// open one from the menu -- neither of which "HRP_TARGET must be set"
		// suggests.
		return fmt.Errorf("this terminal was started without a machine to connect to "+
			"(%s is unset), which happens when a pane is restored without its "+
			"settings; open a terminal from the menu instead", EnvTarget)
	}
	client := remote.NewWithBin(target, os.Getenv(EnvSession), os.Getenv(EnvBin))

	// A plain SSH pane mirrors nothing, so it needs no remote terminal id.
	if config.Mode(os.Getenv(EnvMode)) == config.ModeSSH {
		return shell(client)
	}

	terminal := os.Getenv(EnvTerminal)
	if terminal == "" {
		// The same, one step further along: the machine is known and which of
		// its terminals to mirror is not. A pane that is mirroring is always
		// told both.
		return fmt.Errorf("this terminal knows which machine it belongs to and not "+
			"which terminal on it to mirror (%s is unset), which happens when a pane "+
			"is restored without its settings; open a terminal from the menu instead",
			EnvTerminal)
	}

	switch config.Mode(os.Getenv(EnvMode)) {
	case config.ModeObserve:
		return observe(client, terminal)
	default:
		return attach(client, terminal)
	}
}

// tail keeps the last of what is written to it, so a command's own account of
// why it failed can be put in the record without holding on to everything it
// ever said.
//
// ssh writes its reason to standard error, which is the pane -- so somebody
// watching sees "Host key verification failed." and somebody reading the log
// afterwards saw "exit status 255", which is the number for "ssh could not
// connect" and not a reason at all. The README points at that file for why a
// terminal would not open, so it had better say.
type tail struct {
	max  int
	seen []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.seen = append(t.seen, p...)
	if len(t.seen) > t.max {
		t.seen = t.seen[len(t.seen)-t.max:]
	}
	return len(p), nil
}

// lastLine is the last thing said that was worth saying, made safe to write to
// a file somebody will read in a terminal.
//
// The last rather than the first: ssh announces a changed host key with sixty
// characters of "@" and a paragraph of explanation, and the sentence that names
// the problem is at the bottom of it.
func (t *tail) lastLine() string {
	for _, line := range reverse(strings.Split(string(t.seen), "\n")) {
		if clean := text.Truncate(text.Sanitize(line), maxSaidWidth); clean != "" {
			return clean
		}
	}
	return ""
}

func reverse(lines []string) []string {
	out := make([]string, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		out = append(out, lines[i])
	}
	return out
}

// maxSaid bounds what is kept from a command's standard error, and maxSaidWidth
// how much of one line of it is written down.
const (
	maxSaid      = 4 << 10
	maxSaidWidth = 200
)

// failed describes a command that would not run, with whatever it said about
// itself when there is anything worth repeating.
// The tails are asked in order and the first that has anything wins, so a
// caller can offer the place a reason usually is and then the place it
// sometimes is. What ssh says on standard error is the better answer where
// there is one: it is a reason by construction, where the other is whatever
// the far side last put on the screen.
func failed(err error, argv []string, said ...*tail) error {
	for _, t := range said {
		if line := t.lastLine(); line != "" {
			return fmt.Errorf("%w running: %s: %s", err, describeCommand(argv), line)
		}
	}
	return fmt.Errorf("%w running: %s", err, describeCommand(argv))
}

// shell opens a plain interactive SSH session. Nothing about it needs Herdr on
// the far side, so it works against any machine you can log in to.
func shell(client *remote.Client) error {
	argv := client.ShellArgv()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	// Still the pane, so nothing changes for somebody watching it; kept as
	// well, so the record says why rather than only that.
	said := &tail{max: maxSaid}
	cmd.Stderr = io.MultiWriter(os.Stderr, said)
	if err := cmd.Start(); err != nil {
		return failed(err, argv, said)
	}
	stop := watchForStop(cmd.Process)
	defer stop()

	if err := cmd.Wait(); err != nil {
		// ssh reports its own failures as 255 and passes through anything else,
		// so a different status is the session on the machine ending rather
		// than the connection to it failing. `exit` with no argument returns
		// the last command's status, so leaving a session after something went
		// wrong is an ordinary way to go with a non-zero one -- as is pressing
		// ctrl-C. Reopening those put the pane back a moment after somebody had
		// finished with it.
		if code := exitStatus(err); code > 0 && code != sshOwnFailure {
			return nil
		}
		return failed(err, argv, said)
	}
	return nil
}

// sshOwnFailure is the status ssh exits with when the failure is its own -- it
// could not connect, or the connection broke -- rather than the remote
// command's own status passed through.
const sshOwnFailure = 255

// exitStatus is the status a command exited with, or -1 when it was killed by
// a signal or never ran.
func exitStatus(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// attach hands the pane straight to `herdr terminal attach` on the far side.
// Herdr's own direct-attach client then owns input, resize and scrollback.
//
// --takeover is passed because a direct attach is exclusive and the remote
// client does not always die with its SSH channel: a mirror pane that is killed
// can leave `herdr terminal attach` running on the remote host, and every later
// attempt to mirror that terminal then fails with "already has an attached
// client". Taking over evicts that stale client. This assumes one hub per
// remote terminal, which is the documented arrangement; use observe mode when
// several machines need to watch the same pane.
func attach(client *remote.Client, terminal string) error {
	args := []string{"terminal", "attach", terminal}
	if takeoverEnabled() {
		args = append(args, "--takeover")
	}
	argv, err := client.Argv(true, args...)
	if err != nil {
		return err
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	// Through the gate: the attach client turns mouse reporting on for itself
	// in its handshake, which leaves the pane unable to select text. What the
	// far side asks for afterwards is its own business and passes through.
	gate := newMouseGate(os.Stdout)
	defer gate.flush()
	// And a tail of what the far side sent, for when it is the far side that
	// failed rather than ssh. `herdr terminal attach` writes its own refusals
	// through the pty, which arrives here rather than on standard error -- so
	// somebody watching saw why and the log recorded "exit status 1". One real
	// mirror.log has sixty-six of those.
	//
	// Second to standard error, not instead of it: this is whatever was last
	// on the screen, which is a reason when the attach refused and the tail of
	// a working terminal when the connection dropped under it. Sanitised and
	// cut to one line either way.
	screen := &tail{max: maxSaid}
	cmd.Stdout = io.MultiWriter(gate, screen)
	// Still the pane, so nothing changes for somebody watching it; kept as
	// well, so the record says why rather than only that. The same thing shell
	// does, for the same reason -- ssh writes why it could not connect to
	// standard error, and that is the pane, so the file the troubleshooting
	// page tells people to read held "exit status 255" and nothing else. One
	// real mirror.log has a hundred and forty-one of those and not one reason
	// among them.
	//
	// The remote command's own output comes back through the pty rather than
	// here, so what this keeps is ssh's side of it: the host key, the refused
	// connection, the key it would not take.
	said := &tail{max: maxSaid}
	cmd.Stderr = io.MultiWriter(os.Stderr, said)
	if err := cmd.Start(); err != nil {
		return failed(err, argv, said, screen)
	}
	stop := watchForStop(cmd.Process)
	defer stop()

	if err := cmd.Wait(); err != nil {
		return failed(err, argv, said, screen)
	}
	return nil
}

// paneModes are the terminal modes a program on another machine can turn on
// and a pane must not be left in once that program has gone.
//
// stty puts the line discipline back and nothing else: these belong to the
// terminal emulator and outlive it. Mouse reporting is the one that shows --
// with it on, the terminal hands every drag to the application instead of
// selecting text, so a pane left that way cannot be selected from at all, and
// the application it is reporting to is not there any more.
const paneModes = "\x1b[?1000l" + // mouse: clicks
	"\x1b[?1002l" + // mouse: drags
	"\x1b[?1003l" + // mouse: all movement
	"\x1b[?1006l" + // mouse: SGR encoding
	"\x1b[?2004l" + // bracketed paste
	"\x1b[?25h" + // the cursor, which a full-screen program may have hidden
	"\x1b[0m" // colours and attributes

// cancelPending abandons a control sequence in progress. ECMA-48 gives CAN
// exactly that meaning, and a terminal not in the middle of one does nothing
// with it -- unlike SUB, which some terminals draw a character for.
const cancelPending = "\x18"

// restorePane puts those modes back. Turning off one that was never on is
// nothing, so this is safe to do whatever the far side did or did not set.
func restorePane(w io.Writer) {
	fmt.Fprint(w, paneModes)
}

// takeoverEnabled reports whether a stale remote attach may be evicted.
func takeoverEnabled() bool {
	return os.Getenv(EnvTakeover) != "false"
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
		// Abandon anything the last stream left half-written. A connection
		// that drops ends wherever it happened to be, which can be inside an
		// escape sequence -- and the terminal then waits for the rest of it,
		// swallowing the first bytes of whatever arrives next and acting on
		// the two together as though they were one sequence. What that turns
		// into is not predictable; that it is not what either side meant, is.
		fmt.Print(cancelPending)
		err := streamOnce(client, terminal, cols, rows, winch)
		switch next, reset := planObserveNext(err, attempt); next {
		case observeStop:
			return err
		case observeAgainNow:
			attempt = reset
			// A drag across a divider is a run of resizes, and every one of
			// them ends the stream. Reconnecting on the first means an ssh per
			// step of the drag, each one asking the machine to render the
			// whole screen at a size that is already out of date. Waiting for
			// the size to stop changing makes it one.
			//
			// This line is not held by a test, unlike settleResize itself.
			// Reaching it means a real SIGWINCH at the process, and a test
			// built on that is one that fails on a loaded machine for reasons
			// having nothing to do with the code. What it costs if it goes is
			// a reconnect per drag step -- slower, not wrong -- which is a
			// different bargain from the wirings that were worth holding.
			settleResize(winch)
			continue
		}
		select {
		case <-time.After(time.Duration(attempt+1) * observeRetryStep):
		case <-winch:
		}
		attempt++
	}
}

// resizeSettle is how long a size has to hold still before it is worth
// reconnecting at. Long enough to swallow a drag, short enough that a single
// resize is not noticeable.
var resizeSettle = 120 * time.Millisecond

// settleResize waits for the window to stop changing size.
//
// It returns as soon as nothing has arrived for resizeSettle, so one resize
// costs that and a drag costs it once rather than once per step.
func settleResize(winch <-chan os.Signal) {
	timer := time.NewTimer(resizeSettle)
	defer timer.Stop()
	for {
		select {
		case <-winch:
			// Still moving. Wait again from here.
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(resizeSettle)
		case <-timer.C:
			return
		}
	}
}

// observeNext is what to do after a stream has ended.
type observeNext int

const (
	// observeStop closes the pane, with whatever the stream ended with.
	observeStop observeNext = iota
	// observeAgainNow opens another straight away, at the attempt count given.
	observeAgainNow
	// observeAgainSoon waits first, and counts the attempt.
	observeAgainSoon
)

// planObserveNext decides what a stream ending means.
//
// Three endings that look alike and are not. A stream that ends with no error
// is the terminal on the machine going away, so the pane has nothing left to
// show and closes. A resize ends it too, and that is not a failure at all --
// the far side renders at the size it was told, so the stream has to start
// again and say the new one, and the count of attempts goes back to nothing.
// Anything else is the connection failing, which is worth another go until it
// has had a few.
//
// Held apart from the loop that carries it out because the loop cannot be
// reached without a stream and a signal: what a resize costs is a count, and
// counting a resize as a failure means a pane closing on somebody who did
// nothing but drag a window edge four times.
func planObserveNext(err error, attempt int) (next observeNext, resetTo int) {
	switch {
	case err == nil:
		return observeStop, attempt
	case errors.Is(err, errResized):
		return observeAgainNow, 0
	case attempt >= maxObserveAttempts:
		return observeStop, attempt
	}
	return observeAgainSoon, attempt
}

// maxObserveAttempts is how many times a broken stream is picked up again
// before the pane gives up and closes. A stream that ends cleanly is not one of
// these: that is the terminal on the machine going away.
const maxObserveAttempts = 4

// observeRetryStep is the unit the wait between attempts grows by. A variable
// so a test can shorten it, since the policy is what matters and not the
// seconds.
var observeRetryStep = time.Second

// streamOnce runs one observe stream, returning nil when the remote terminal
// is gone and an error when the stream should be retried. A resize ends the
// stream so the next one can negotiate the new size.
func streamOnce(client *remote.Client, terminal string, cols, rows int, winch <-chan os.Signal) error {
	argv, err := client.Argv(false, "terminal", "session", "observe", terminal,
		"--cols", strconv.Itoa(cols), "--rows", strconv.Itoa(rows))
	if err != nil {
		return err
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// Still the pane, so nothing changes for somebody watching it; kept as
	// well, so the record says why rather than only that. The third of the
	// three modes to get this: shell had it, attach was given it, and this one
	// returned ssh's bare exit status with neither the reason nor the command
	// -- the barest of the three, for the mode whose whole job is to show what
	// a machine is doing.
	said := &tail{max: maxSaid}
	cmd.Stderr = io.MultiWriter(os.Stderr, said)
	// Killing this is not the same as being done waiting for it, and this is
	// the one place that kills on purpose. Wait blocks until nothing holds the
	// other end of the pipes it reads -- and standard error is a pipe here,
	// since it is copied two ways, so anything the far side left behind keeps
	// it open. Giving up on a stream is then exactly as slow as not giving up,
	// which is what it was meant to avoid.
	//
	// runCommand and the Herdr client have had this for the same reason. This
	// one was found by a test that gave the pane nowhere to write: it passed on
	// Linux, where the shell replaces itself with its last command, and hung
	// for the full twenty seconds on macOS, where it does not.
	cmd.WaitDelay = waitDelay
	if err := cmd.Start(); err != nil {
		return failed(err, argv, said)
	}

	// The same supervision the other two modes have. Without it a stop signal
	// killed this process outright and left the ssh it had started to notice on
	// its own, and nothing recorded that the pane had been closed deliberately
	// rather than dropping.
	stop := watchForStop(cmd.Process)
	defer stop()

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
	frames.Buffer(make([]byte, 0, 64*1024), maxFrameBytes)
	abandoned := false
	for frames.Scan() {
		raw, ok := decodeFrame(frames.Bytes())
		if !ok {
			continue
		}
		if _, err := os.Stdout.Write(raw); err != nil {
			// Nowhere left to put it: the pane has gone.
			abandoned = true
			break
		}
	}
	if err := frames.Err(); err != nil {
		// A frame too large for the buffer ends the scan, and treating that as
		// a closed terminal would quietly shut the pane. Reconnecting is the
		// right response: the stream resumes from the terminal's current state.
		log.Printf("stream from %s: %v", client.Target, err)
		abandoned = true
	}

	// Waiting on a stream this has stopped reading never returns. The far side
	// is still sending -- that is what an oversized frame means -- and once the
	// pipe fills it blocks, so the process never exits and this never gets
	// past the wait. The pane then sits there for ever: no output, no
	// reconnect, and alive as far as anything watching can tell. Giving up on a
	// stream means ending it, not waiting politely for it to finish.
	if abandoned {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		close(done)
		_ = cmd.Wait()
		if err := frames.Err(); err != nil {
			return err
		}
		return errStreamAbandoned
	}
	close(done)

	waitErr := cmd.Wait()
	if wasResized.Load() {
		return errResized
	}
	// ssh reports its own failures as 255 and passes through anything else, so
	// that is the connection dying rather than the stream ending.
	//
	// It used to depend on whether anything had been shown yet: a stream that
	// had delivered frames and then died was taken for the terminal going away,
	// so the pane closed. And a mirror pane that goes leaving no record of a
	// failure is read as a tab somebody shut, which closes the terminal on the
	// machine. A connection dropping halfway through therefore destroyed the
	// work it had been showing.
	if exitStatus(waitErr) == sshOwnFailure {
		// Wrapped, so what ssh said travels with it. planObserveNext asks
		// errors.Is and exitStatus asks errors.As, and both see through the
		// wrapping, so how this retries is unchanged.
		return failed(waitErr, argv, said)
	}
	// Anything else is the command on the machine ending, which means the
	// terminal went away: let the pane close.
	return nil
}

// waitDelay bounds how long Wait may go on after the process itself has gone.
//
// Wait returns once the process exits and everything copying its output has
// finished, and a child that outlived it inherits those pipes -- so a stream
// given up on could still be waited for as long as whatever the far side left
// running. The same value and the same reason as the one in the remote client.
const waitDelay = 2 * time.Second

// maxFrameBytes bounds one frame from the stream. Terminal output arrives in
// small pieces; anything approaching this is a stream that has gone wrong.
//
// Taken from capped rather than written again here. It is the same size a
// command may print back -- both bound what arrives from a machine -- and the
// number stood in both places, which is where a pair of bounds drifts apart.
const maxFrameBytes = capped.Max

// decodeFrame reads one line of the observe stream, which carries terminal
// output as base64 inside a JSON envelope. It reports whether the line held
// anything to write.
//
// Anything unreadable is skipped rather than fatal: the stream is a live feed,
// and one bad line is no reason to tear down a working terminal.
func decodeFrame(line []byte) ([]byte, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return nil, false
	}
	var frame struct {
		Bytes string `json:"bytes"`
	}
	if err := json.Unmarshal(line, &frame); err != nil || frame.Bytes == "" {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(frame.Bytes)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

// windowSize reports this pane's size, falling back to a sane default when the
// pty cannot be queried.
func windowSize() (cols, rows int) {
	out, err := sttyOutput("size")
	if err != nil {
		return defaultCols, defaultRows
	}
	return parseWindowSize(string(out))
}

// Sizes used when the pty cannot be measured. The stream is opened at whatever
// size is requested, so a wrong guess only means the remote wraps oddly until
// the next resize.
const (
	defaultCols = 120
	defaultRows = 40
)

// parseWindowSize reads "rows cols" as stty size reports it.
func parseWindowSize(out string) (cols, rows int) {
	cols, rows = defaultCols, defaultRows
	fields := strings.Fields(out)
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

// maxLogBytes is how large the failure log may grow before it is rolled over.
// One generation is kept, so the space used is bounded at twice this.
const maxLogBytes = 256 * 1024

// appendToLog records a failure.
//
// Nothing trimmed this once. Every failed pane appended to it and nothing ever
// shortened it, so it grew for as long as the plugin was installed -- slowly,
// but with no end to it, in a directory nobody thinks to look in. The bounding
// is shared with the daemon's own log rather than written twice, since two
// copies of a policy are two things to keep in step.
func appendToLog(path, message string) {
	f, err := logfile.Open(path, maxLogBytes)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), message)
}

// firstNonEmpty returns the first value that is not empty.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
