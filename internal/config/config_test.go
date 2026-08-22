package config

import "testing"

func TestSessionFor(t *testing.T) {
	cfg := Defaults()

	if got := cfg.SessionFor(Host{Target: "workbox"}); got != "remote" {
		t.Errorf("unconfigured host session = %q, want %q", got, "remote")
	}
	if got := cfg.SessionFor(Host{Target: "workbox", Session: "agents"}); got != "agents" {
		t.Errorf("host override = %q, want %q", got, "agents")
	}
	// "default" is how a user opts back into the remote's unnamed session,
	// which Herdr addresses with an empty HERDR_SESSION.
	if got := cfg.SessionFor(Host{Target: "workbox", Session: "default"}); got != "" {
		t.Errorf(`session "default" = %q, want ""`, got)
	}

	cfg.Session = "shared"
	if got := cfg.SessionFor(Host{Target: "workbox"}); got != "shared" {
		t.Errorf("top-level session = %q, want %q", got, "shared")
	}
}

func TestNormalizedFillsDefaults(t *testing.T) {
	cfg := Config{Hosts: []Host{{Target: "workbox"}}}.normalized()
	if cfg.Session != "remote" || cfg.Mode != ModeAttach || cfg.MaxMirrors != 32 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.Interval().String() != "2s" {
		t.Errorf("interval = %s, want 2s", cfg.Interval())
	}
}
