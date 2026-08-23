package config

import (
	"os"
	"strings"
	"testing"
)

// Each of these used to pass silently and behave as something else. A config
// file is edited by hand, so a typo in it deserves to be told about.

func TestProblemsAcceptsAGoodConfig(t *testing.T) {
	cfg := Defaults()
	cfg.Hosts = []Host{
		{Target: "bot"},
		{Target: "ci", Mode: ModeAttach, Placement: "tab"},
	}
	if problems := cfg.Problems(); len(problems) != 0 {
		t.Errorf("a good config reported %v", problems)
	}
}

func TestProblemsCatchesAMisspelledMode(t *testing.T) {
	// The worst of them: anything that is not exactly "ssh" counts as
	// mirroring, so a typo quietly turned on the experimental feature.
	cfg := Defaults()
	cfg.Mode = "shh"

	problems := cfg.Problems()
	if len(problems) == 0 {
		t.Fatal("a misspelled mode should be reported")
	}
	if !strings.Contains(problems[0], "shh") {
		t.Errorf("problem %q should name the value", problems[0])
	}
}

func TestProblemsCatchesPerHostMistakes(t *testing.T) {
	cfg := Defaults()
	cfg.Hosts = []Host{
		{Target: "bot", Mode: "attatch"},
		{Target: "ci", Placement: "splitt"},
	}

	problems := strings.Join(cfg.Problems(), "\n")
	for _, want := range []string{"bot", "attatch", "ci", "splitt"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems %q should mention %q", problems, want)
		}
	}
}

func TestProblemsCatchesDuplicateAndEmptyHosts(t *testing.T) {
	cfg := Defaults()
	cfg.Hosts = []Host{{Target: "bot"}, {Target: "bot"}}
	if problems := strings.Join(cfg.Problems(), "\n"); !strings.Contains(problems, "more than once") {
		t.Errorf("a machine listed twice should be reported, got %q", problems)
	}

	cfg.Hosts = []Host{{Target: ""}}
	if problems := strings.Join(cfg.Problems(), "\n"); !strings.Contains(problems, "no target") {
		t.Errorf("a machine with no target should be reported, got %q", problems)
	}
}

func TestProblemsCatchesFormatsMissingTheirPlaceholder(t *testing.T) {
	// Without {name} every terminal from a machine gets the same label, and
	// without {host} every machine shares one space.
	cfg := Defaults()
	cfg.LabelFormat = "remote"
	cfg.WorkspaceFormat = "machines"

	problems := strings.Join(cfg.Problems(), "\n")
	if !strings.Contains(problems, "{name}") || !strings.Contains(problems, "{host}") {
		t.Errorf("problems %q should name the missing placeholders", problems)
	}

	// A workspace chosen outright is meant to be shared, so it is not a
	// problem that it has no {host} in it.
	cfg = Defaults()
	cfg.Workspace = "remote"
	for _, p := range cfg.Problems() {
		if strings.Contains(p, "workspace_format") {
			t.Errorf("a deliberate shared workspace should not be reported: %q", p)
		}
	}
}

func TestNormalizedDropsHostsWithNoTarget(t *testing.T) {
	// A host with no target cannot be reached, and leaving it in produced a
	// space named after nothing and an ssh command with no destination.
	cfg := Config{Hosts: []Host{{Target: ""}, {Target: "bot"}, {Target: ""}}}.normalized()

	if len(cfg.Hosts) != 1 || cfg.Hosts[0].Target != "bot" {
		t.Errorf("hosts = %+v, want only bot", cfg.Hosts)
	}
}

func TestNormalizedClampsNonsense(t *testing.T) {
	cfg := Config{PollInterval: "-5s", MaxMirrors: -3}.normalized()

	// Polling with a negative interval would spin as fast as the machine
	// allows, hammering every machine with SSH.
	if cfg.Interval() < 500_000_000 {
		t.Errorf("interval = %s, want it clamped to something sane", cfg.Interval())
	}
	if cfg.MaxMirrors <= 0 {
		t.Errorf("max_mirrors = %d, want a positive default", cfg.MaxMirrors)
	}
}

func TestUnknownModeFallsBackToSSH(t *testing.T) {
	// Treating anything that is not "ssh" as mirroring meant a mode spelled
	// wrong silently turned on the experimental feature — the opposite of what
	// someone who made a typo wants.
	cfg := Defaults()

	for _, mode := range []Mode{"shh", "attatch", "mirror", "", "SSH"} {
		host := Host{Target: "bot", Mode: mode}
		if mode == "" {
			// An unset per-host mode falls through to the global one, which is
			// ssh by default.
			if cfg.Mirrors(host) {
				t.Errorf("an unset mode should not mirror")
			}
			continue
		}
		if cfg.Mirrors(host) {
			t.Errorf("mode %q should not mirror", mode)
		}
		if got := cfg.EffectiveMode(host); got != ModeSSH {
			t.Errorf("EffectiveMode(%q) = %q, want %q", mode, got, ModeSSH)
		}
	}

	// The two real mirroring modes still work.
	for _, mode := range []Mode{ModeAttach, ModeObserve} {
		host := Host{Target: "bot", Mode: mode}
		if !cfg.Mirrors(host) {
			t.Errorf("mode %q should mirror", mode)
		}
		if got := cfg.EffectiveMode(host); got != mode {
			t.Errorf("EffectiveMode(%q) = %q, want it unchanged", mode, got)
		}
	}
}

func TestSaveNeverLeavesAPartialFile(t *testing.T) {
	// Writing in place truncates first, so an interruption leaves the file
	// empty or half written. This file holds the list of machines, is edited by
	// hand, and is rewritten whenever mirroring is toggled from the menu.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)

	cfg := Defaults()
	cfg.Hosts = []Host{{Target: "bot"}, {Target: "ci", Mode: ModeAttach}}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Whatever is at the path must parse, always.
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if len(loaded.Hosts) != 2 {
		t.Errorf("hosts = %+v, want both", loaded.Hosts)
	}

	// Rewriting repeatedly must leave exactly one file behind, with no
	// temporary ones abandoned beside it.
	for i := 0; i < 5; i++ {
		if err := Save(cfg); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only config.json", names)
	}
}

func TestSaveKeepsThePermissionsPrivate(t *testing.T) {
	// The file names the machines someone connects to, so it should not become
	// world-readable by being rewritten.
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	if err := Save(Defaults()); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions are %o, want 600", perm)
	}
}
