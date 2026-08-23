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
		modified := false
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
		if len(revision) > 7 {
			revision = revision[:7]
		}
		if revision == "" {
			// Built outside a checkout, which is normal for `go run` and tests.
			revision = "unknown"
			return
		}
		if modified {
			revision += "-dirty"
		}
	})
	return revision
}
