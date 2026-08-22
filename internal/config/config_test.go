package config

import "testing"

func TestSessionFor(t *testing.T) {
	cfg := Defaults()

	// The machine's own default session, which Herdr addresses with an empty
	// HERDR_SESSION, so plain `herdr` there shows the shared terminals.
	if got := cfg.SessionFor(Host{Target: "workbox"}); got != "" {
		t.Errorf("unconfigured host session = %q, want \"\"", got)
	}
	if got := cfg.SessionFor(Host{Target: "workbox", Session: "agents"}); got != "agents" {
		t.Errorf("host override = %q, want %q", got, "agents")
	}
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
	if cfg.Session != DefaultSessionName || cfg.Mode != ModeSSH || cfg.MaxMirrors != 32 {
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

func TestMirroringIsOptIn(t *testing.T) {
	// Mirroring needs Herdr on the machine and has a lot of moving parts, so a
	// plain SSH session is what an unconfigured host gets.
	cfg := Defaults()
	if cfg.Mode != ModeSSH {
		t.Errorf("default mode = %q, want %q", cfg.Mode, ModeSSH)
	}
	if cfg.Mirrors(Host{Target: "workbox"}) {
		t.Error("an unconfigured host should not be mirrored")
	}
	if !cfg.Mirrors(Host{Target: "workbox", Mode: ModeAttach}) {
		t.Error("a host set to attach should be mirrored")
	}
	if !cfg.Mirrors(Host{Target: "workbox", Mode: ModeObserve}) {
		t.Error("a host set to observe should be mirrored")
	}

	// A global mode still applies to hosts that do not override it.
	cfg.Mode = ModeAttach
	if !cfg.Mirrors(Host{Target: "workbox"}) {
		t.Error("a global attach mode should apply")
	}
	if cfg.Mirrors(Host{Target: "workbox", Mode: ModeSSH}) {
		t.Error("a per-host ssh mode should win over the global one")
	}
}

func TestSetHostMode(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())

	// Toggling from the menu must work for a machine that is not configured
	// yet, since the menu offers everything in ~/.ssh/config.
	cfg, err := SetHostMode("newbox", ModeAttach)
	if err != nil {
		t.Fatalf("SetHostMode: %v", err)
	}
	if len(cfg.Hosts) != 1 || cfg.Hosts[0].Target != "newbox" || cfg.Hosts[0].Mode != ModeAttach {
		t.Fatalf("hosts = %+v, want newbox in attach mode", cfg.Hosts)
	}

	// And it must survive a reload rather than living only in memory.
	if cfg, err = SetHostMode("newbox", ModeSSH); err != nil {
		t.Fatalf("SetHostMode: %v", err)
	}
	if len(cfg.Hosts) != 1 || cfg.Hosts[0].Mode != ModeSSH {
		t.Fatalf("hosts = %+v, want a single newbox back in ssh mode", cfg.Hosts)
	}
	reloaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded.Hosts) != 1 || reloaded.Hosts[0].Mode != ModeSSH {
		t.Errorf("reloaded hosts = %+v, want the change persisted", reloaded.Hosts)
	}
}
