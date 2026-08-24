// Package version reports which build is running.
//
// Installing an update replaces the files on disk but leaves the daemon that
// is already running untouched, so the fixes in an update do not take effect
// until Herdr is restarted. Nothing said so, which made it possible to watch
// an old build behave like an old build and conclude the update had not
// worked.
package version

import (
	"runtime/debug"
	"sync"
)

var (
	once     sync.Once
	revision string
)

// Short reports the build this binary was made from, as a short commit. Go
// records it at build time, so there is no version constant to keep in step.
func Short() string {
	once.Do(func() {
		info, ok := debug.ReadBuildInfo()
		if !ok {
			revision = "unknown"
			return
		}
		recorded, modified := "", false
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				recorded = setting.Value
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
		revision = shortRevision(recorded, modified)
	})
	return revision
}

// shortRevision names a build from what Go recorded about it: a short commit,
// and a mark when the tree it was built from had uncommitted changes in it.
//
// Separate from Short because Short can only ever answer one way inside a test
// binary -- there is no checkout behind it -- so everything this decides was
// out of reach of a test, including the answer that matters most: a build with
// no revision at all is not a dirty build, it is an unknown one.
func shortRevision(recorded string, modified bool) string {
	if len(recorded) > 7 {
		recorded = recorded[:7]
	}
	if recorded == "" {
		// Built outside a checkout, which is normal for `go run` and tests.
		return "unknown"
	}
	if modified {
		recorded += "-dirty"
	}
	return recorded
}

// StaleMessage describes a daemon running a different build from the installed
// one, or "" when there is nothing worth saying.
//
// Installing an update replaces the files but leaves the running daemon alone,
// so its fixes do nothing until Herdr restarts. A binary built outside a
// checkout has no revision to compare and stays quiet rather than warning every
// time and teaching people to ignore it.
func StaleMessage(running string) string {
	return staleMessage(running, Short())
}

// staleMessage is StaleMessage with the installed build handed to it, since
// Short cannot be anything but "unknown" in a test binary -- which is the one
// answer that makes this say nothing at all.
func staleMessage(running, installed string) string {
	if installed == "" || installed == "unknown" || running == installed {
		return ""
	}
	if running == "" {
		// A daemon old enough not to report its build at all.
		running = "an older build"
	}
	return "the running daemon is " + running + " but " + installed +
		" is installed; restart Herdr to pick up the update"
}
