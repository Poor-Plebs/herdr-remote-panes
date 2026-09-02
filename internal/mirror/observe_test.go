package mirror

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
)

// Observe is the read-only half of mirroring: instead of taking the remote
// terminal it decodes frames of what that terminal is showing. Any number of
// machines can watch the same pane at once, which is the point of it.
//
// None of it was exercised. What arrives is a stream from another machine, so
// the interesting parts are what happens when it stops, when it is malformed,
// and when one frame is too big to hold.

// observeSSH puts an ssh on PATH that answers an observe with the given lines,
// and answers the probe so a client can be built at all.
func observeSSH(t *testing.T, script string) *remote.Client {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\n" +
		"last=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in\n" +
		"  *command\\ -v\\ herdr*) echo /usr/bin/herdr; exit 0;;\n" +
		"  true) exit 0;;\n" +
		"esac\n" + script
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return remote.New("bot", "")
}

// whatItWrote runs a stream and returns what it put on the terminal.
func whatItWrote(t *testing.T, client *remote.Client, winch <-chan os.Signal) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = saved }()

	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(read)
		done <- string(out)
	}()

	streamErr := streamOnce(client, "term_1", 80, 24, winch)
	write.Close()
	return <-done, streamErr
}

// frame is one line of an observe stream.
func frame(text string) string {
	return `{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(text)) + `"}`
}

func TestObserveWritesWhatTheTerminalIsShowing(t *testing.T) {
	client := observeSSH(t, "echo '"+frame("hello ")+"'\necho '"+frame("world")+"'\n")

	out, err := whatItWrote(t, client, nil)
	if err != nil {
		t.Fatalf("streamOnce: %v", err)
	}
	if out != "hello world" {
		t.Errorf("the terminal shows %q, want %q", out, "hello world")
	}
}

func TestObserveIgnoresWhatIsNotAFrame(t *testing.T) {
	// Herdr prints the occasional line of its own, and a stream is a stream:
	// anything in it that is not a frame is not output, and must not stop the
	// rest arriving.
	client := observeSSH(t, "echo 'A new version is available.'\n"+
		"echo '"+frame("real output")+"'\n"+
		"echo '{\"bytes\":\"not base64 at all!!\"}'\n"+
		"echo '{}'\n")

	out, err := whatItWrote(t, client, nil)
	if err != nil {
		t.Fatalf("streamOnce: %v", err)
	}
	if out != "real output" {
		t.Errorf("the terminal shows %q, want only the frame", out)
	}
}

func TestObserveEndsWhenTheTerminalDoes(t *testing.T) {
	// A stream that closes cleanly means the terminal is gone on the machine,
	// which is not a failure to retry: the pane here should close too.
	client := observeSSH(t, "echo '"+frame("bye")+"'\nexit 0\n")

	if _, err := whatItWrote(t, client, nil); err != nil {
		t.Errorf("a stream that ended cleanly reported %v, want nothing to retry", err)
	}
}

func TestObserveRetriesWhenTheStreamBreaks(t *testing.T) {
	// A stream that dies without ever sending a frame is a connection that
	// failed rather than a terminal that ended, and is worth another go.
	client := observeSSH(t, "echo 'ssh: connection reset' >&2\nexit 255\n")

	if _, err := whatItWrote(t, client, nil); err == nil {
		t.Error("a stream that failed reported success, so nothing would reconnect")
	}
}

// theFrameLimit is what maxFrameBytes is expected to be, written out rather
// than read from it. Two things follow from writing it out.
//
// The bound is pinned. A test that says maxFrameBytes says nothing about what
// maxFrameBytes should be: raise the constant and the frame below rises with
// it, so the case passed for any value the bound could take.
//
// And the test stays cheap. The frame is built from this rather than from the
// constant, so raising the constant no longer builds a frame to match --
// maxFrameBytes+1024 at a thousandfold is eight gigabytes of 'x' with base64
// on top, which the kernel killed rather than let fail. A killed run is not a
// verdict, and this one came back held on one sweep and killed on the next.
//
// mirror.go takes maxFrameBytes from capped.Max, where the size is decided and
// pinned; this says what the mirror expects to be given.
const theFrameLimit = 8 * 1024 * 1024

func TestTheFrameLimitIsEightMegabytes(t *testing.T) {
	if maxFrameBytes != theFrameLimit {
		t.Errorf("maxFrameBytes = %d, want %d -- it is taken from capped.Max, "+
			"which bounds what one command may print back",
			maxFrameBytes, theFrameLimit)
	}
}

func TestOneEnormousFrameDoesNotEndTheMirror(t *testing.T) {
	// A frame too large for the buffer ends the scan. Treating that as a
	// closed terminal would quietly shut the pane; reconnecting resumes from
	// the terminal's current state, which is what a viewer wants.
	huge := strings.Repeat("x", theFrameLimit+1024)
	client := observeSSH(t, "echo '"+frame("before")+"'\n"+
		fmt.Sprintf("printf '%%s\\n' '%s'\n", `{"bytes":"`+base64.StdEncoding.EncodeToString([]byte(huge))+`"}`))

	_, err := whatItWrote(t, client, nil)
	if err == nil {
		t.Error("an oversized frame ended the stream as though the terminal had gone")
	}
}

func TestAResizeEndsTheStreamSoTheNextOneFitsThePane(t *testing.T) {
	// The remote terminal is rendered at the size it was told, so a pane that
	// changes size needs the stream to start again and say the new one.
	winch := make(chan os.Signal, 1)
	// exec, so the signal reaches the thing holding the stream open rather than
	// a shell that leaves a child behind. Real ssh is one process.
	client := observeSSH(t, "exec sleep 30\n")

	go func() {
		time.Sleep(50 * time.Millisecond)
		winch <- os.Signal(syscall.SIGWINCH)
	}()

	_, err := whatItWrote(t, client, winch)
	if err != errResized {
		t.Errorf("a resize ended the stream with %v, want it to be reconnected at the new size", err)
	}
}

// countingSSH answers each call with the next script in turn, so a test can say
// what happens on the first attempt and what happens after.
func countingSSH(t *testing.T, scripts ...string) func() int {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "n")
	body := "#!/bin/sh\n" +
		"last=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"case \"$last\" in *command\\ -v\\ herdr*) echo /usr/bin/herdr; exit 0;; esac\n" +
		"n=$(cat " + counter + " 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + counter + "\n" +
		"case $n in\n"
	for i, script := range scripts {
		body += fmt.Sprintf("  %d) %s;;\n", i+1, script)
	}
	body += "  *) " + scripts[len(scripts)-1] + ";;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		raw, err := os.ReadFile(counter)
		if err != nil {
			return 0
		}
		n := 0
		fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &n)
		return n
	}
}

func TestAStreamThatEndsCleanlyClosesThePane(t *testing.T) {
	// The terminal on the machine went away, so there is nothing left to show.
	// Reconnecting would be reconnecting to nothing, over and over.
	attempts := countingSSH(t, "echo '"+frame("bye")+"'; exit 0")

	if err := observe(remote.New("bot", ""), "term_1"); err != nil {
		t.Errorf("observe returned %v; a terminal that ended is not a failure", err)
	}
	if got := attempts(); got != 1 {
		t.Errorf("the stream was opened %d times, want once", got)
	}
}

func TestABrokenStreamIsPickedUpAgain(t *testing.T) {
	// A connection that broke can come good, and the pane is showing somebody's
	// work: it is worth another go before giving up on it.
	restore := observeRetryStep
	observeRetryStep = time.Millisecond
	defer func() { observeRetryStep = restore }()

	attempts := countingSSH(t,
		"echo 'ssh: connection reset' >&2; exit 255",
		"echo '"+frame("back")+"'; exit 0")

	if err := observe(remote.New("bot", ""), "term_1"); err != nil {
		t.Errorf("observe returned %v after the stream came back", err)
	}
	if got := attempts(); got != 2 {
		t.Errorf("the stream was opened %d times, want a failure and then a success", got)
	}
}

func TestAStreamThatNeverComesBackGivesUp(t *testing.T) {
	// Not for ever: a pane retrying a machine that will not answer is a pane
	// doing nothing visible and costing a connection attempt every few seconds.
	restore := observeRetryStep
	observeRetryStep = time.Millisecond
	defer func() { observeRetryStep = restore }()

	attempts := countingSSH(t, "echo 'ssh: no route to host' >&2; exit 255")

	if err := observe(remote.New("bot", ""), "term_1"); err == nil {
		t.Error("observe reported success for a stream that never came back")
	}
	if got := attempts(); got != maxObserveAttempts+1 {
		t.Errorf("the stream was opened %d times, want %d", got, maxObserveAttempts+1)
	}
}

func TestAStreamThatDropsHalfwayIsAFailureNotAnEnding(t *testing.T) {
	// It used to depend on whether anything had been shown yet. A stream that
	// had delivered frames and then died was taken for the terminal going away,
	// so the pane closed leaving no record of a failure -- and a mirror pane
	// that goes without one is read as a tab somebody shut, which closes the
	// terminal on the machine. A connection dropping halfway through therefore
	// destroyed the work it had been showing.
	restore := observeRetryStep
	observeRetryStep = time.Millisecond
	defer func() { observeRetryStep = restore }()

	attempts := countingSSH(t,
		"echo '"+frame("some output")+"'; echo 'ssh: connection reset' >&2; exit 255")

	err := observe(remote.New("bot", ""), "term_1")
	if err == nil {
		t.Fatal("a connection that dropped halfway was reported as the terminal ending, " +
			"which closes the terminal on the machine")
	}
	if got := attempts(); got != maxObserveAttempts+1 {
		t.Errorf("the stream was opened %d times, want it retried before giving up", got)
	}
}

func TestTheRemoteCommandEndingClosesThePane(t *testing.T) {
	// The other half. ssh passes through the status of what it ran, so
	// anything other than its own 255 is herdr on the machine having finished
	// -- the terminal went away, and there is nothing to reconnect to.
	restore := observeRetryStep
	observeRetryStep = time.Millisecond
	defer func() { observeRetryStep = restore }()

	attempts := countingSSH(t, "echo '"+frame("output")+"'; exit 1")

	if err := observe(remote.New("bot", ""), "term_1"); err != nil {
		t.Errorf("observe returned %v; the command on the machine ended, it did not fail", err)
	}
	if got := attempts(); got != 1 {
		t.Errorf("the stream was opened %d times, want once", got)
	}
}

func TestWhatAStreamEndingMeans(t *testing.T) {
	// Three endings that look alike from the outside and are not.
	other := errors.New("ssh: connection reset")

	for _, tt := range []struct {
		what    string
		err     error
		attempt int
		want    observeNext
		reset   int
	}{
		// The terminal on the machine went away, so there is nothing left to
		// show and the pane closes rather than reconnecting to nothing.
		{"the terminal ended", nil, 0, observeStop, 0},
		{"the terminal ended late", nil, maxObserveAttempts, observeStop, maxObserveAttempts},

		// A resize is not a failure. The far side renders at the size it was
		// told, so the stream starts again saying the new one -- straight away,
		// and without spending an attempt.
		{"a resize", errResized, 0, observeAgainNow, 0},
		{"a resize after failures", errResized, maxObserveAttempts - 1, observeAgainNow, 0},
		{"a resize wrapped in something", fmt.Errorf("stream: %w", errResized), 2, observeAgainNow, 0},

		// Anything else is the connection going, which is worth another go
		// until it has had a few.
		{"a broken stream", other, 0, observeAgainSoon, 0},
		{"a broken stream, nearly out", other, maxObserveAttempts - 1, observeAgainSoon, maxObserveAttempts - 1},
		{"a broken stream, out", other, maxObserveAttempts, observeStop, maxObserveAttempts},
	} {
		t.Run(tt.what, func(t *testing.T) {
			next, reset := planObserveNext(tt.err, tt.attempt)
			if next != tt.want {
				t.Errorf("planObserveNext(%v, %d) = %v, want %v", tt.err, tt.attempt, next, tt.want)
			}
			if next == observeAgainNow && reset != tt.reset {
				t.Errorf("planObserveNext(%v, %d) reset the count to %d, want %d",
					tt.err, tt.attempt, reset, tt.reset)
			}
		})
	}
}

func TestResizingForeverNeverClosesThePane(t *testing.T) {
	// The property the reset is for, rather than the line that does it: what a
	// resize costs is a count, and counting one as a failure means a pane
	// closing on somebody who did nothing but drag a window edge a few times.
	attempt := 0
	for i := 0; i < maxObserveAttempts*5; i++ {
		next, reset := planObserveNext(errResized, attempt)
		if next != observeAgainNow {
			t.Fatalf("resize %d of %d was answered with %v", i+1, maxObserveAttempts*5, next)
		}
		attempt = reset
	}

	// And the budget is still whole afterwards: a stream that breaks after all
	// that resizing gets its attempts, rather than being one short of them.
	if next, _ := planObserveNext(errors.New("ssh: connection reset"), attempt); next != observeAgainSoon {
		t.Errorf("after resizing, a broken stream was answered with %v, want another go", next)
	}
}

func TestADragIsOneReconnectRatherThanOnePerStep(t *testing.T) {
	// Every resize ends the observe stream, and reconnecting asks the machine
	// to render the whole screen again. Doing that on the first resize means an
	// ssh per step of a drag across a divider, each rendering a size that is
	// already out of date by the time it arrives.
	was := resizeSettle
	resizeSettle = 30 * time.Millisecond
	defer func() { resizeSettle = was }()

	winch := make(chan os.Signal, 1)
	// A drag: resizes arriving faster than the settle time, then a pause.
	go func() {
		for i := 0; i < 5; i++ {
			winch <- syscall.SIGWINCH
			time.Sleep(10 * time.Millisecond)
		}
	}()

	started := time.Now()
	settleResize(winch)
	took := time.Since(started)

	// It waited out the drag rather than returning on the first one.
	if took < 60*time.Millisecond {
		t.Errorf("settled after %s, before the drag had finished: a reconnect "+
			"per step is what this is for", took)
	}
	// And did not wait for each of them in turn.
	if took > 400*time.Millisecond {
		t.Errorf("settled after %s, which is longer than the drag plus one wait", took)
	}
}

func TestOneResizeCostsOneWait(t *testing.T) {
	// The other side of it: a single resize must not be made slow by the
	// waiting that a drag needs.
	was := resizeSettle
	resizeSettle = 30 * time.Millisecond
	defer func() { resizeSettle = was }()

	winch := make(chan os.Signal, 1)
	started := time.Now()
	settleResize(winch)
	if took := time.Since(started); took > 200*time.Millisecond {
		t.Errorf("a window that is not being resized waited %s", took)
	}
}

func TestAScreenRepaintPastTheDefaultBufferStillArrives(t *testing.T) {
	// The scanner is handed a buffer that may grow to maxFrameBytes. Without
	// that line it keeps bufio's own default, which stops a line at 64KB --
	// and a repaint of a large terminal is one frame well past that. What it
	// looks like is a mirror that ends the moment somebody clears a big
	// screen.
	//
	// Nothing held it. The oversized-frame test above sends more than eight
	// megabytes, which is past bufio's default and past maxFrameBytes both, so
	// it passes whichever bound is in force and cannot tell them apart. This
	// frame is chosen to sit between the two, where they disagree.
	const repaint = 200 * 1024
	if repaint <= 64*1024 {
		t.Fatal("the frame must be past bufio's default for this to test anything")
	}
	if repaint >= theFrameLimit {
		t.Fatal("the frame must be under maxFrameBytes, or it is the other test")
	}

	payload := strings.Repeat("x", repaint)
	client := observeSSH(t, "printf '%s\\n' '"+frame(payload)+"'\n")

	out, err := whatItWrote(t, client, nil)
	if err != nil {
		t.Fatalf("a frame of %d bytes ended the stream: %v", repaint, err)
	}
	if len(out) != repaint {
		t.Errorf("the terminal shows %d bytes of the repaint, want %d", len(out), repaint)
	}
}
