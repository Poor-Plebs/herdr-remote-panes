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

func TestWorkspaceFor(t *testing.T) {
	cfg := Defaults()
	bot := Host{Target: "bot"}
	prod := Host{Target: "prod"}

	// Default: one workspace per machine, marked as remote so it is
	// distinguishable from a local workspace in the sidebar.
	// Two spaces: a cloud is ambiguous-width and crowds the name in terminals
	// that draw it in two cells.
	if got := cfg.WorkspaceFor(bot); got != "☁  bot" {
		t.Errorf("default workspace = %q, want %q", got, "☁  bot")
	}
	if cfg.WorkspaceFor(bot) == cfg.WorkspaceFor(prod) {
		t.Error("hosts should not share a workspace by default")
	}

	// A shared top-level workspace puts every machine in one layout.
	cfg.Workspace = "remote"
	if cfg.WorkspaceFor(bot) != "remote" || cfg.WorkspaceFor(prod) != "remote" {
		t.Error("top-level workspace should group every host together")
	}

	// A per-host workspace still wins.
	prod.Workspace = "prod-only"
	if got := cfg.WorkspaceFor(prod); got != "prod-only" {
		t.Errorf("per-host workspace = %q, want %q", got, "prod-only")
	}

	// A host label, not the target, names the workspace.
	cfg.Workspace = ""
	if got := cfg.WorkspaceFor(Host{Target: "165.227.153.104", Label: "droplet"}); got != "☁  droplet" {
		t.Errorf("workspace = %q, want %q", got, "☁  droplet")
	}

	// The marker is configurable, and a shared workspace name is used verbatim
	// because the user chose it themselves.
	cfg.WorkspaceFormat = "[remote] {host}"
	if got := cfg.WorkspaceFor(bot); got != "[remote] bot" {
		t.Errorf("custom format = %q, want %q", got, "[remote] bot")
	}
	cfg.Workspace = "shared"
	if got := cfg.WorkspaceFor(bot); got != "shared" {
		t.Errorf("shared workspace = %q, want %q", got, "shared")
	}
}
