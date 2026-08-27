// Package logfile keeps a bounded log on disk.
//
// The daemon writes its diagnostics to standard error, which Herdr collects and
// shows once a plugin's command has finished. The daemon does not finish, so
// everything it had to say -- a machine it gave up on, a mirror that kept
// failing, a pane listing that would not run -- was written somewhere nobody
// could read it.
package logfile

import (
	"os"
	"sync"
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
	n, err := f.file.Write(p)
	f.written += int64(n)
	return n, err
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
