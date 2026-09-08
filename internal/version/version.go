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
		revision = buildRevision(debug.ReadBuildInfo())
	})
	return revision
}

// unknownBuild is what Short answers for a build with no revision recorded in
// it, which is every `go run`, every test binary, and anything built outside a
// checkout.
//
// Named because it means the same thing on BOTH sides of StaleMessageFor's
// comparison -- a build that cannot be identified -- and it was written out
// four times, understood on one side and not the other.
const unknownBuild = "unknown"

// buildRevision names a build from what Go recorded about it, taking what
// ReadBuildInfo returns exactly as it returns it.
//
// Separate from Short for the same reason shortRevision is, one step further
// out. Inside a test binary ReadBuildInfo answers about the test binary, which
// carries no vcs settings at all, so WHICH KEYS these are read from was out of
// reach of a test as much as what is decided from them. A key read wrong makes
// Short say "unknown" for every real build, and "unknown" is the one answer
// that silences StaleMessageFor -- so the warning this package exists to give
// would go quiet everywhere with every test still passing.
func buildRevision(info *debug.BuildInfo, ok bool) string {
	if !ok {
		return unknownBuild
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
	return shortRevision(recorded, modified)
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
		return unknownBuild
	}
	if modified {
		recorded += "-dirty"
	}
	return recorded
}

// StaleMessageFor describes a daemon running a different build from the
// installed one, or "" when there is nothing worth saying.
//
// Installing an update replaces the files but leaves the running daemon alone,
// so its fixes do nothing until Herdr restarts. An INSTALLED build with no
// revision to compare -- one built outside a checkout -- stays quiet rather
// than warning every time and teaching people to ignore it. A RUNNING daemon
// that cannot be identified is the other half and is not the same answer: there
// is something to say there, and what it says is that the build is unknown
// rather than that it is old.
//
// The installed build is handed in, and there is deliberately no longer a
// convenience form that reads Short here instead. Short cannot be anything but
// "unknown" inside a test binary and "unknown" is the one input that silences
// this whatever the daemon reported, so a caller deciding on the wrapper's
// answer was unfalsifiable by construction -- and both callers that used one
// turned out to be holding nothing, cli's status in 1593be8 and the menu after
// it. Callers ask Short one step further out, where a test can hand a build in.
func StaleMessageFor(running, installed string) string {
	if installed == "" || installed == unknownBuild || running == installed {
		return ""
	}
	if running == "" || running == unknownBuild {
		// Something answered and did not say which build it is: either a
		// daemon from before builds were reported, which sends no revision at
		// all, or one built outside a checkout, which sends unknownBuild.
		// Neither can be compared, so this says so rather than calling it "an
		// older build" when it might be newer -- and that claim is exactly what
		// the message below makes, "restart Herdr to pick up the update", which
		// this branch existed to avoid and only ever covered the empty half of.
		// `version` prints "unknown" in the daemon column for the same reason,
		// and the two lines appear together.
		return "the running daemon does not report which build it is, and " +
			installed + " is installed; restart Herdr to be sure that is the one running"
	}
	return "the running daemon is " + running + " but " + installed +
		" is installed; restart Herdr to pick up the update"
}
