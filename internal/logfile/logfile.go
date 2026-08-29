// Package logfile keeps a bounded log on disk.
//
// The daemon writes its diagnostics to standard error, which Herdr collects and
// shows once a plugin's command has finished. The daemon does not finish, so
// everything it had to say -- a machine it gave up on, a mirror that kept
// failing, a pane listing that would not run -- was written somewhere nobody
// could read it.
package logfile

import (
	"io"
	"os"
	"strings"
	"sync"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
)

// File is an io.Writer that rolls over rather than growing without end. One
// generation is kept, so the space used is bounded at twice max.
type File struct {
	mu      sync.Mutex
	path    string
	max     int64
	file    *os.File
	written int64
	// closed is set by Close, so a log shut on purpose is not reopened by
	// the next write the way one that failed to open is.
	closed bool
}

// Open prepares a log at path, continuing an existing one.
func Open(path string, max int64) (*File, error) {
	f := &File{path: path, max: max}
	if err := f.open(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *File) open() error {
	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		// Nothing to write to, and say so. Leaving the handle that was there
		// means writing to a closed file for the rest of the session -- the
		// nil check below passes, every write fails, and nothing says why.
		// That is the silent death this package exists to avoid.
		f.file = nil
		return err
	}
	f.file = file
	f.written = 0
	if info, err := file.Stat(); err == nil {
		f.written = info.Size()
	}
	return nil
}

func (f *File) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return len(p), nil
	}
	if f.file == nil {
		// Opening failed at some point. Try again rather than never: a full
		// disk empties, a directory comes back, and a log that gives up the
		// first time is one that is missing for the rest of the session --
		// which is exactly when somebody goes looking for it.
		if err := f.open(); err != nil {
			// Still nothing. Drop the message rather than fail the caller:
			// this is a log, and what writes to it has better things to do
			// than deal with the log's problems.
			return len(p), nil
		}
	}
	if f.written+int64(len(p)) > f.max {
		if err := f.rotate(); err != nil {
			// Rotating failed, so write on rather than losing the message: a
			// log that stops the moment the disk misbehaves is worse than one
			// that outgrows its bound.
			f.written = 0
		}
	}
	n, err := f.file.Write(safeToRead(p))
	f.written += int64(n)
	if err != nil {
		return n, err
	}
	// What was consumed rather than what was written: the two differ once
	// anything has been taken out, and a writer reporting fewer bytes than it
	// was given means a short write to everything that checks.
	return len(p), nil
}

// Sanitized wraps a writer so that what a machine said cannot act on the
// terminal showing it.
//
// The file this package keeps is only half of where the daemon's diagnostics
// go: they are written to standard error as well, which Herdr collects, and
// every command's final error goes there through the same logger. Both ends of
// that are a terminal somebody reads.
func Sanitized(w io.Writer) io.Writer { return sanitized{w} }

type sanitized struct{ w io.Writer }

func (s sanitized) Write(p []byte) (int, error) {
	if _, err := s.w.Write(safeToRead(p)); err != nil {
		return 0, err
	}
	// As in File.Write: what was consumed, not what was left after taking
	// things out of it.
	return len(p), nil
}

// safeToRead takes out what a terminal would act on rather than show.
//
// Both of these files are named in the troubleshooting page with `cat` in
// front of them, which is the one thing they are for -- and what goes into
// them is not all ours. An error from a machine carries that machine's
// standard error as it was written, and a banner is the far side's to choose,
// so the escape that clears the screen or renames the window would run in
// whatever terminal went looking for why something failed.
//
// Here rather than at the twenty-odd places that log an error, because that is
// a list nobody can finish: the next one is written by somebody who has no
// reason to think about it. This package writes nothing but these two files,
// so it can hold the rule for both.
//
// Line by line, so the newline that divides one entry from the next survives
// -- Sanitize drops it along with the rest of the control characters, and an
// entry per line with the time at the front is what makes the file readable.
func safeToRead(p []byte) []byte {
	lines := strings.Split(string(p), "\n")
	for i, line := range lines {
		lines[i] = text.Sanitize(line)
	}
	return []byte(strings.Join(lines, "\n"))
}

// rotate keeps the previous generation. Callers hold the lock.
func (f *File) rotate() error {
	// The descriptor is released whether or not Close reports a problem -- a
	// failure there is the flush complaining, not the file staying open. So
	// returning on it would leave nothing to write to, and the caller's
	// fallback of carrying on regardless would write to a closed file: the
	// log would stop for good the first time a close complained, which is the
	// thing this package exists to avoid.
	_ = f.file.Close()
	if err := os.Rename(f.path, f.path+".1"); err != nil {
		// Reopen what is there rather than leaving nothing to write to.
		_ = f.open()
		return err
	}
	return f.open()
}

// Close releases the file.
func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	// Deliberately, which is different from having nothing to write to: a
	// closed log stays closed rather than reopening itself on the next line
	// somebody logs.
	f.closed = true
	return err
}
