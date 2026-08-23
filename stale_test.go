package main

import "testing"

func TestStaleDaemonIsSpottedButNotOverreported(t *testing.T) {
	// Installing an update leaves the running daemon alone, so the new build
	// sits on disk while the old one keeps answering. Nothing said so, which
	// made it possible to watch an old build behave like an old build and
	// conclude the update had not worked.
	tests := []struct {
		name               string
		running, installed string
		want               bool
	}{
		{"same build says nothing", "9fcc667", "9fcc667", false},
		{"a different build is stale", "427e2ad", "9fcc667", true},
		{"a daemon too old to report its build is stale", "", "9fcc667", true},
		// A binary built outside a checkout has nothing to compare against, and
		// warning every time would train someone to ignore the warning.
		{"an unknown local build stays quiet", "9fcc667", "unknown", false},
		{"an unknown local build stays quiet against an old daemon", "", "unknown", false},
		{"a dirty build differs from the clean one it came from", "9fcc667", "9fcc667-dirty", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := staleDaemon(tt.running, tt.installed); got != tt.want {
				t.Errorf("staleDaemon(%q, %q) = %v, want %v",
					tt.running, tt.installed, got, tt.want)
			}
		})
	}
}
