package syncd

import (
	"io"
	"log"
	"net"
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
