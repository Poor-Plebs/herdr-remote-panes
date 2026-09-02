package syncd

import (
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// safeLog is a log destination that goroutines write to. serveControl logs from
// the goroutine it runs in, and a strings.Builder shared with the test that
// reads it is a race the detector finds rather than a flake somebody chases.
type safeLog struct {
	mu   sync.Mutex
	said strings.Builder
}

func (l *safeLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.said.Write(p)
}

func (l *safeLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.said.String()
}

func captureLog(t *testing.T) *safeLog {
	t.Helper()
	var logged safeLog
	saved := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(saved) })
	return &logged
}

// deadConn is a connection that has nothing to say. serveExchange fails to
// decode a request from it, answers with the reason, and closes -- all without
// reaching dispatch, so a handed-off connection can be counted without the
// daemon trying to reach a machine over it.
type deadConn struct {
	closed chan struct{}
	once   sync.Once
}

func newDeadConn() *deadConn { return &deadConn{closed: make(chan struct{})} }

func (c *deadConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *deadConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *deadConn) Close() error                     { c.once.Do(func() { close(c.closed) }); return nil }
func (c *deadConn) LocalAddr() net.Addr              { return dummyAddr{} }
func (c *deadConn) RemoteAddr() net.Addr             { return dummyAddr{} }
func (c *deadConn) SetDeadline(time.Time) error      { return nil }
func (c *deadConn) SetReadDeadline(time.Time) error  { return nil }
func (c *deadConn) SetWriteDeadline(time.Time) error { return nil }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "unix" }
func (dummyAddr) String() string  { return "test" }

// scriptedListener hands out what the test says, in order, and repeats the last
// one for as long as it is asked.
type scriptedListener struct {
	mu    sync.Mutex
	steps []step
	at    int
	tries int
}

type step struct {
	conn net.Conn
	err  error
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.steps[l.at]
	if l.at < len(l.steps)-1 {
		l.at++
	}
	l.tries++
	return s.conn, s.err
}

func (l *scriptedListener) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tries
}

func (l *scriptedListener) Close() error   { return nil }
func (l *scriptedListener) Addr() net.Addr { return dummyAddr{} }

// tempError is an accept failure that passes -- too many open files is the one
// to expect, since mirroring a machine costs them and the limit is per process.
type tempError struct{}

func (tempError) Error() string { return "accept unix /tmp/x: too many open files" }

func shortRetries(t *testing.T, grace time.Duration) {
	t.Helper()
	floor, ceil, was := acceptRetryFloor, acceptRetryCeil, acceptGraceTime
	acceptRetryFloor, acceptRetryCeil, acceptGraceTime = time.Millisecond, 2*time.Millisecond, grace
	t.Cleanup(func() { acceptRetryFloor, acceptRetryCeil, acceptGraceTime = floor, ceil, was })
}

func TestTheControlSocketOutlastsABurstOfOpenFiles(t *testing.T) {
	// Accepting is the whole of the daemon's ability to be told anything, and
	// it used to stop on any error at all. Too many open files is a burst and
	// clears; the daemon it left behind mirrored on, answered nothing, and
	// reported no running daemon about a process that was right there.
	shortRetries(t, time.Minute)
	logged := captureLog(t)

	served := newDeadConn()
	listener := &scriptedListener{steps: []step{
		{err: tempError{}},
		{err: tempError{}},
		{err: tempError{}},
		{conn: served},
		{err: net.ErrClosed},
	}}

	done := make(chan struct{})
	d := New(machineConfig("bot"))
	go func() { d.serveControl(listener); close(done) }()

	select {
	case <-served.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the connection after the failures was never served: the daemon " +
			"is running and nothing can reach it, which is what this is about")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("serveControl did not return once the listener was closed")
	}

	if said := logged.String(); !strings.Contains(said, "too many open files") {
		t.Errorf("the log never said why accepting was failing, so the one clue "+
			"anybody gets is missing; it said: %q", said)
	}
	// Said once when it starts, not once per attempt: a line per retry at the
	// ceiling is a line a second for as long as this lasts, and it buries the
	// line that says what happened.
	if n := strings.Count(logged.String(), "could not accept"); n != 1 {
		t.Errorf("the log said accepting was failing %d times over 3 retries, want 1", n)
	}
}

func TestShuttingDownDoesNotLookLikeAFailure(t *testing.T) {
	// The listener closing is the daemon stopping, which is not worth a word
	// and must not be retried: the socket is gone and every attempt would fail.
	shortRetries(t, time.Minute)
	logged := captureLog(t)

	listener := &scriptedListener{steps: []step{{err: net.ErrClosed}}}
	d := New(machineConfig("bot"))

	done := make(chan struct{})
	go func() { d.serveControl(listener); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a closed listener did not stop serveControl: shutting the daemon " +
			"down would spin instead of ending")
	}
	if said := logged.String(); said != "" {
		t.Errorf("stopping the daemon complained: %q", said)
	}
}

func TestADeadControlSocketSaysSoRatherThanLookingHealthy(t *testing.T) {
	// When it does not clear, the daemon is a process that mirrors and cannot
	// be told anything. It is entitled to give up -- but the log is the only
	// place that difference is visible, because from every action it looks
	// exactly like no daemon at all.
	shortRetries(t, 20*time.Millisecond)
	logged := captureLog(t)

	listener := &scriptedListener{steps: []step{{err: tempError{}}}}
	d := New(machineConfig("bot"))

	done := make(chan struct{})
	go func() { d.serveControl(listener); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("serveControl retried a failure that never clears forever")
	}

	said := logged.String()
	if !strings.Contains(said, "giving up") {
		t.Errorf("the daemon stopped answering and the log does not say so: %q", said)
	}
	if !strings.Contains(said, "stop it and start it again") {
		t.Errorf("the log says it gave up and not what to do about it: %q", said)
	}
	if listener.calls() < 2 {
		t.Errorf("gave up after %d attempts, which is not a grace period", listener.calls())
	}
}

func TestAFailingAcceptWaitsBetweenTriesRatherThanSpinning(t *testing.T) {
	// The test above holds that a failure that never clears is retried and
	// then given up on, with "gave up after %d attempts, which is not a grace
	// period" for a floor. What it cannot see is the ceiling: an accept that
	// fails instantly and is retried instantly is a loop with nothing in it,
	// and "at least two attempts" is as true of that as of a daemon waiting
	// properly between tries.
	//
	// Nothing held the waiting. Deleting the floor leaves the delay at nought
	// on the first failure, so the sleep is for no time at all; deleting the
	// sleep does the same more directly. Either way the daemon spends its
	// grace period burning a core on a socket that is not going to answer,
	// which is the state it is in while somebody waits for the menu.
	shortRetries(t, 30*time.Millisecond)
	captureLog(t)

	listener := &scriptedListener{steps: []step{{err: tempError{}}}}
	d := New(machineConfig("bot"))

	done := make(chan struct{})
	go func() { d.serveControl(listener); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("serveControl retried a failure that never clears forever")
	}

	// A millisecond floor across thirty milliseconds is tens of attempts, and
	// the doubling makes it fewer. A spin is thousands. The bound is loose on
	// purpose: what it has to tell apart is waiting from not waiting, and a
	// slow machine must not make it fail.
	if n := listener.calls(); n > 500 {
		t.Errorf("accept was tried %d times in %s, which is a loop with no wait "+
			"in it rather than a daemon backing off", n, 30*time.Millisecond)
	}
}

func TestADaemonWritesDownWhatItIsRunningWith(t *testing.T) {
	// The config file used to be the answer to this, holding every setting at
	// its value. It now holds what somebody chose, so the log has to carry it
	// -- and the log is where the troubleshooting page already sends people
	// when a setting is not doing what the README says it does.
	//
	// Which settings are covered, and how they are marked, is
	// TestTheDaemonCanSayWhatItIsRunningWith over in config. This is the half
	// that lives here: that a daemon starting says any of it at all.
	logged := captureLog(t)

	New(machineConfig("bot")).logConfig()

	said := logged.String()
	for _, want := range []string{"config: placement = ", "config: mode = ", "config: max_mirrors = "} {
		if !strings.Contains(said, want) {
			t.Errorf("a daemon starting does not say %q:\n%s", want, said)
		}
	}

	// And that starting is when it happens. Calling the method proves the
	// method works, which is not the claim: Run is where a daemon says this,
	// and deleting the one line there leaves everything above passing. Run
	// cannot be called from a test -- it returns on a signal, and a daemon
	// left running would put its polling into the goroutine count that
	// TestNothingIsLeftRunning measures -- so the call is read instead.
	source, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	start := strings.Index(body, "func (d *Daemon) Run() error {")
	if start < 0 {
		t.Fatal("Run is no longer declared here, so this is checking nothing")
	}
	body = body[start:]
	if end := strings.Index(body[1:], "\nfunc "); end >= 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "d.logConfig()") {
		t.Error("Run no longer says what the daemon is running with, so the log " +
			"has the version and the socket and nothing about the settings")
	}
}

func TestHerdrBeingAwayIsSaidOnceRatherThanEveryPass(t *testing.T) {
	// A pass comes round every couple of seconds and cannot do anything
	// without listing the panes here, so a Herdr that has gone away fails
	// every one of them. Said each time, that is thirty lines a minute into
	// the file somebody opens to find out what happened -- which rolls at a
	// quarter of a megabyte, so a couple of hours of it takes the history with
	// it. The complaint fills the place the explanation would have been.
	//
	// Rereading the config already says its complaint once per distinct
	// message, for the same reason and on the same schedule.
	here := withFakeHerdr(t)
	d := New(machineConfig("bot"))
	d.dispatch(Command{Cmd: "connect", Host: "bot"})
	settle(t, d, here, 1)

	logged := captureLog(t)
	broken := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\necho 'gone' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", broken)

	for i := 0; i < 5; i++ {
		d.reconcileOnce()
	}
	if n := strings.Count(logged.String(), "skipping this pass"); n != 1 {
		t.Errorf("five passes with Herdr away said so %d times:\n%s", n, logged.String())
	}

	// And said when it comes back. A log that only ever reports trouble leaves
	// somebody unable to tell a problem that is over from one that is still
	// going -- which matters most here, where the complaint stops being
	// repeated precisely when nothing has changed.
	t.Setenv("HERDR_BIN_PATH", fakeHerdrBin)
	d.reconcileOnce()
	if !strings.Contains(logged.String(), "passes are running again") {
		t.Errorf("Herdr came back and nothing said so:\n%s", logged.String())
	}

	// Not said twice, either: it is news once.
	d.reconcileOnce()
	if n := strings.Count(logged.String(), "passes are running again"); n != 1 {
		t.Errorf("recovery was reported %d times", n)
	}
}

// TestTheWaitBetweenAcceptsStopsGrowing holds the ceiling on that wait.
//
// TestAFailingAcceptWaitsBetweenTriesRatherThanSpinning holds the floor, by
// counting attempts and refusing thousands of them. This is the same
// measurement in the other direction. The delay doubles after every failure,
// and the ceiling is what stops it doubling for ever: without it the wait runs
// away -- 5ms, 10, 20, and on past a minute -- on a socket the daemon is
// supposed to be retrying.
//
// Two things go wrong together. A failure that clears is not noticed until the
// sleep it is inside ends, so a burst of too many open files that passes in a
// second can leave the daemon unreachable for minutes. And the giving-up line,
// which is the one thing that tells somebody the daemon is up and cannot be
// reached, is only reached between sleeps, so it arrives long after the grace
// period it names.
//
// Measured over this grace with this listener: 238 attempts with the ceiling
// and 10 without. The bound below is loose for the reason the floor's bound is
// loose -- what it has to tell apart is a bounded wait from a runaway one, and
// a slow machine must not make it fail.
func TestTheWaitBetweenAcceptsStopsGrowing(t *testing.T) {
	shortRetries(t, 500*time.Millisecond)
	captureLog(t)

	listener := &scriptedListener{steps: []step{{err: tempError{}}}}
	d := New(machineConfig("bot"))

	done := make(chan struct{})
	go func() { d.serveControl(listener); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("serveControl never gave up on a failure that does not clear")
	}

	if n := listener.calls(); n < 40 {
		t.Errorf("accept was tried %d times in %s: with the wait bounded it is "+
			"hundreds, so this is a delay doubling with nothing to stop it, and a "+
			"daemon asleep on a socket it is meant to be retrying",
			n, 500*time.Millisecond)
	}
}
