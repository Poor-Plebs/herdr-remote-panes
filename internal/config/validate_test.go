package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

func TestSaveWritesThroughASymlink(t *testing.T) {
	// Configs are often symlinked into a dotfiles repo. Writing atomically by
	// renaming a new file into place replaces the link with a regular file,
	// silently detaching it from the repo -- so the link has to be resolved
	// first and the real file written through.
	dir := t.TempDir()
	real := t.TempDir() + "/config.json"
	if err := os.WriteFile(real, []byte(`{"hosts":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, dir+"/config.json"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)

	cfg := Defaults()
	cfg.Hosts = []Host{{Target: "bot"}}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(dir + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file; a dotfiles setup would be broken")
	}
	raw, err := os.ReadFile(real)
	if err != nil || len(raw) == 0 {
		t.Errorf("the real file was not written through: %v", err)
	}
}

func TestValidTargetRefusesAnOptionDressedAsAMachine(t *testing.T) {
	// The target is handed to ssh as an argument, and ssh takes options on the
	// command line: -oProxyCommand=... runs a command. A target beginning with
	// a dash is therefore an instruction, not a machine.
	//
	// It matters because targets do not only come from a file someone typed.
	// connect falls back to whatever text is selected in the terminal, so a
	// line of output from somewhere else can end up here.
	refused := []string{
		"",
		"-oProxyCommand=touch /tmp/x",
		"-F/dev/null",
		"--",
		"-",
		"host with spaces",
		"host\twith\ttabs",
		"host\nnewline",
		"host\x00null",
		"host\x1b[31m",
	}
	for _, target := range refused {
		if err := ValidTarget(target); err == nil {
			t.Errorf("ValidTarget(%q) allowed it", target)
		}
	}

	// Ordinary destinations must keep working, including the shapes ssh
	// accepts beyond a bare name.
	allowed := []string{
		"bot",
		"bot.example.com",
		"deploy@bot",
		"deploy@10.0.0.4",
		"[2001:db8::1]",
		"ssh://deploy@bot:2222",
		"bot-1_2.internal",
	}
	for _, target := range allowed {
		if err := ValidTarget(target); err != nil {
			t.Errorf("ValidTarget(%q) refused an ordinary destination: %v", target, err)
		}
	}
}

func TestProblemsReportsATargetThatIsAnOption(t *testing.T) {
	cfg := Defaults()
	cfg.Hosts = []Host{{Target: "-oProxyCommand=touch /tmp/x"}}

	problems := strings.Join(cfg.Problems(), "\n")
	if !strings.Contains(problems, "dash") {
		t.Errorf("problems %q should explain why the target is refused", problems)
	}
}

func TestProblemsReportsSettingsThatAreNotSettings(t *testing.T) {
	// Anything the decoder does not recognise is dropped in silence, so a
	// setting spelled wrong, or a per-machine one written at the top level,
	// looks exactly like one that is being obeyed.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	raw := `{
	  "wokspace_format": "☁  {host}",
	  "poll_interval": "2s",
	  "herdr_bin": "/usr/bin/herdr",
	  "hosts": [
	    {"target": "bot", "auto_start": false},
	    {"target": "ci", "mode": "ssh", "targt": "typo"}
	  ]
	}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	problems := strings.Join(cfg.Problems(), "\n")

	for _, want := range []string{
		`"wokspace_format"`,    // misspelled
		`"hosts[].auto_start"`, // real setting, wrong place
		`"hosts[].targt"`,      // misspelled inside a machine
	} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems should mention %s:\n%s", want, problems)
		}
	}

	// Settings that are real must not be reported, wherever they legitimately
	// appear: herdr_bin is both a global and a per-machine setting.
	for _, wrong := range []string{`"poll_interval"`, `"herdr_bin"`, `"target"`, `"mode"`} {
		if strings.Contains(problems, wrong+" is not a setting") {
			t.Errorf("%s is a real setting but was reported:\n%s", wrong, problems)
		}
	}

	// A machine listing the same unknown key twice is said once.
	if strings.Count(problems, `"hosts[].auto_start"`) > 1 {
		t.Errorf("the same unknown setting was reported more than once:\n%s", problems)
	}
}

func TestAGoodConfigReportsNoUnknownSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	cfg := Defaults()
	cfg.Hosts = []Host{{Target: "bot", Mode: ModeAttach, HerdrBin: "/usr/bin/herdr"}}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range loaded.Problems() {
		if strings.Contains(p, "is not a setting") {
			t.Errorf("a config this wrote itself was reported: %q", p)
		}
	}
}

func TestSetHostModeLeavesTheRestOfTheFileAlone(t *testing.T) {
	// Changing one setting used to go through Load and Save, both of which fill
	// in what is missing, so toggling mirroring wrote back every setting
	// somebody had left out, pinned to whatever it defaulted to that day.
	// Nothing changed at the time; it did mean those settings quietly stopped
	// following the default.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	path := filepath.Join(dir, "config.json")

	minimal := `{
  "hosts": [
    {
      "target": "bot"
    },
    {
      "target": "ci",
      "mode": "attach"
    }
  ]
}
`
	if err := os.WriteFile(path, []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := SetHostMode("bot", ModeAttach)
	if err != nil {
		t.Fatalf("SetHostMode: %v", err)
	}

	// What the caller runs on is filled in, because it has to be usable.
	if cfg.WorkspaceFormat == "" || cfg.PollInterval == "" {
		t.Error("the returned config was not filled in for the caller")
	}

	// What the file holds is what it held, plus the one change.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("the file is no longer valid JSON: %v", err)
	}
	if len(onDisk) != 1 {
		keys := make([]string, 0, len(onDisk))
		for k := range onDisk {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Errorf("the file gained settings: %v", keys)
	}

	hosts, _ := onDisk["hosts"].([]any)
	if len(hosts) != 2 {
		t.Fatalf("hosts = %v, want both kept", hosts)
	}
	first, _ := hosts[0].(map[string]any)
	if first["target"] != "bot" || first["mode"] != string(ModeAttach) {
		t.Errorf("bot = %v, want its mode changed", first)
	}
	second, _ := hosts[1].(map[string]any)
	if second["target"] != "ci" || second["mode"] != string(ModeAttach) {
		t.Errorf("ci = %v, want it untouched", second)
	}
}

func TestSetHostModeAddsAMachineThatWasNotThere(t *testing.T) {
	// Toggling mirroring for something found only in ~/.ssh/config has to write
	// it down, since there is nowhere else to record the choice.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"hosts":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := SetHostMode("laptop", ModeObserve)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hosts) != 1 || cfg.Hosts[0].Target != "laptop" || cfg.Hosts[0].Mode != ModeObserve {
		t.Errorf("hosts = %+v, want laptop recorded", cfg.Hosts)
	}

	// And it survives a read back.
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Hosts) != 1 || loaded.Hosts[0].Mode != ModeObserve {
		t.Errorf("after reloading, hosts = %+v", loaded.Hosts)
	}
}

func TestSetHostModeWithNoFileYet(t *testing.T) {
	// Nothing to preserve, so this is the one time writing the defaults out is
	// what somebody wants: it gives them a file to edit.
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())

	cfg, err := SetHostMode("bot", ModeAttach)
	if err != nil {
		t.Fatalf("SetHostMode with no file: %v", err)
	}
	if len(cfg.Hosts) != 1 || cfg.Hosts[0].Target != "bot" {
		t.Errorf("hosts = %+v, want bot", cfg.Hosts)
	}
	if _, err := Load(); err != nil {
		t.Errorf("the file written is not readable: %v", err)
	}
}
