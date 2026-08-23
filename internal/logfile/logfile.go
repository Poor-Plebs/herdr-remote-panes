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

	if f.file == nil {
		return len(p), nil
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
	if err := f.file.Close(); err != nil {
		return err
	}
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
	return err
}
