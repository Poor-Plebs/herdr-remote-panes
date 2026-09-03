package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
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

func TestProblemsCatchesAMisspelledPlacement(t *testing.T) {
	// The per-host placement below is checked; this is the one that applies to
	// every machine, and no test had ever misspelled it. Its three siblings --
	// mode, scope and label_format -- were all reached.
	cfg := Defaults()
	cfg.Placement = "tabb"

	problems := cfg.Problems()
	// Only this one is wrong, so anything else reported means the fixture broke
	// something other than what this is about.
	if len(problems) != 1 {
		t.Fatalf("a good config with one misspelled placement reported %d problems: %v",
			len(problems), problems)
	}
	if !strings.Contains(problems[0], "tabb") {
		t.Errorf("problem %q should name the value", problems[0])
	}

	// And the values it offers instead are values this actually takes. A list
	// written out in a message is a second copy of what knownPlacement decides,
	// and the two can part company: adding a placement without touching the
	// message leaves it advertising four of five, and renaming one leaves it
	// naming something that will be refused.
	listed := placementsNamedIn(t, problems[0])
	if len(listed) < 5 {
		t.Fatalf("read %d placements out of %q, which is fewer than it names -- "+
			"the message is no longer written the way this reads it", len(listed), problems[0])
	}
	for _, name := range listed {
		if !knownPlacement(name) {
			t.Errorf("the message offers %q and knownPlacement refuses it", name)
		}
	}
}

// placementsNamedIn pulls the values a placement complaint offers out of it.
func placementsNamedIn(t *testing.T, problem string) []string {
	t.Helper()
	_, rest, ok := strings.Cut(problem, "is not one of ")
	if !ok {
		t.Fatalf("no list of values in %q", problem)
	}
	list, _, ok := strings.Cut(rest, ";")
	if !ok {
		t.Fatalf("the list in %q does not end where this expects", problem)
	}
	var out []string
	for _, part := range strings.Split(list, ",") {
		for _, name := range strings.Split(part, " or ") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
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

func TestAFormatThatNamesTerminalsByTheirIDIsNotAProblem(t *testing.T) {
	// The complaint is that terminals cannot be told apart, and {pane} tells
	// them apart: it is the terminal's own id and no two share one. Reporting
	// it anyway sent somebody to add {name} they had no use for, or to distrust
	// a config that was doing exactly what they asked.
	//
	// {agent} is the other way about. It varies between panes that have agents
	// and is empty for the ones that do not, so a machine's ordinary shells
	// would all be named alike -- which is the fault this reports.
	for _, one := range []struct {
		format string
		report bool
	}{
		{"{name}@{host}", false},
		{"{pane}@{host}", false},
		{"{name} {pane}", false},
		{"{agent}@{host}", true},
		{"remote", true},
	} {
		cfg := Defaults()
		cfg.LabelFormat = one.format

		reported := false
		for _, p := range cfg.Problems() {
			if strings.HasPrefix(p, "label_format") {
				reported = true
			}
		}
		if reported != one.report {
			t.Errorf("label_format %q reported = %v, want %v", one.format, reported, one.report)
		}
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
	// line of output from somewhere else can end up here. That second source is
	// what PlausibleTarget guards; this one is about what ssh can be handed at
	// all, whoever wrote it.
	refused := []string{
		"",
		"-oProxyCommand=touch /tmp/x",
		"-F/dev/null",
		"--",
		"-",
		// Tabs and newlines are control characters, so they stay here even
		// though a plain space no longer does.
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
	    {"target": "ci", "mode": "ssh", "targt": "typo", "auto_start": true}
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

	// The same unknown key in two machines is said once. Both entries above
	// carry auto_start for this: with it in only one of them the count could
	// never reach two, so the check read as though it held the collapsing and
	// held nothing -- deleting the line that does the collapsing broke no test
	// at all.
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

func TestUnknownSettingsSurviveAToggle(t *testing.T) {
	// Toggling mirroring hands the daemon a fresh configuration to run on, and
	// that one is built from the file directly rather than through Load. It
	// missed the list of settings that mean nothing, so pressing m made the
	// warning about them disappear until Herdr was restarted -- with the file
	// unchanged and still wrong.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	raw := `{"wokspace_format": "x", "hosts": [{"target": "bot"}]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(loaded.Problems(), "\n"), "wokspace_format") {
		t.Fatal("Load did not report the unknown setting")
	}

	toggled, err := SetHostMode("bot", ModeAttach)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(toggled.Problems(), "\n"), "wokspace_format") {
		t.Errorf("after a toggle the unknown setting is no longer reported: %v", toggled.Problems())
	}
}

func TestSharesWorkspace(t *testing.T) {
	// A space named outright can hold several machines at once, so nothing
	// about one machine's state belongs on it. Two machines in different states
	// would each mark it as their own, every couple of seconds, for as long as
	// both were connected.
	cfg := Defaults()
	if cfg.SharesWorkspace(Host{Target: "bot"}) {
		t.Error("a machine with a space of its own is not sharing")
	}

	// Named for one machine.
	if !cfg.SharesWorkspace(Host{Target: "bot", Workspace: "remote"}) {
		t.Error("a machine given a space by name is sharing")
	}

	// Named for all of them.
	shared := Defaults()
	shared.Workspace = "remote"
	if !shared.SharesWorkspace(Host{Target: "bot"}) {
		t.Error("every machine is sharing when one space is named for all")
	}

	// And the name is used as given either way, so the down marker does not
	// rewrite a space somebody else's machine is also in.
	for _, reachable := range []bool{true, false} {
		if got := shared.WorkspaceLabelFor(Host{Target: "bot"}, reachable); got != "remote" {
			t.Errorf("WorkspaceLabelFor(reachable=%v) = %q, want the name as given",
				reachable, got)
		}
	}

	// A machine with its own space still gets the marker in its name.
	own := Defaults()
	up := own.WorkspaceLabelFor(Host{Target: "bot"}, true)
	down := own.WorkspaceLabelFor(Host{Target: "bot"}, false)
	if up == down {
		t.Errorf("a machine's own space reads the same up and down: %q", up)
	}
}

func TestSettingsThatDefaultToOn(t *testing.T) {
	// These four are documented as defaulting to true, and each is a pointer so
	// that "not written down" and "written down as false" can be told apart.
	// Getting that backwards would turn a setting off for everybody who had
	// never heard of it, which is the sort of thing a reading of the code
	// misses -- as one of these audits did miss, on a setting since fixed.
	unset := Config{}
	for name, got := range map[string]bool{
		"close_propagates":  unset.ShouldClosePropagate(),
		"capture_new_panes": unset.ShouldCaptureNewPanes(),
		"takeover":          unset.ShouldTakeover(),
		"auto_start":        unset.ShouldAutoStart(),
	} {
		if !got {
			t.Errorf("%s defaults to off; the README says on", name)
		}
	}

	no := false
	off := Config{ClosePropagates: &no, CaptureNewPanes: &no, Takeover: &no, AutoStart: &no}
	for name, got := range map[string]bool{
		"close_propagates":  off.ShouldClosePropagate(),
		"capture_new_panes": off.ShouldCaptureNewPanes(),
		"takeover":          off.ShouldTakeover(),
		"auto_start":        off.ShouldAutoStart(),
	} {
		if got {
			t.Errorf("%s was written down as false and is still on", name)
		}
	}

	// And the defaults this ships actually have them on, rather than relying on
	// the nil case to do it.
	d := Defaults()
	if !d.ShouldClosePropagate() || !d.ShouldCaptureNewPanes() ||
		!d.ShouldTakeover() || !d.ShouldAutoStart() {
		t.Error("a default config does not have all four on")
	}
}

func TestScopeDecidesWhatIsMirrored(t *testing.T) {
	// "shared" mirrors one space on the machine; "all" mirrors everything it
	// has. Anything unrecognised means the safer of the two, which is the one
	// that leaves the machine's other work alone.
	if !Defaults().SharedOnly() {
		t.Error("the default should mirror only the shared space")
	}
	if (Config{Scope: ScopeAll}).SharedOnly() {
		t.Error("scope all should mirror everything")
	}
	for _, scope := range []string{ScopeShared, "", "evrything", "ALL"} {
		if !(Config{Scope: scope}).SharedOnly() {
			t.Errorf("scope %q should leave the machine's other spaces alone", scope)
		}
	}
}

func TestRemoteWorkspaceLabelNamesThisMachine(t *testing.T) {
	// The space this creates on the machine is named after the hub, so that
	// sitting on the machine you can tell whose it is.
	cfg := Defaults()
	label := cfg.RemoteWorkspaceLabel()

	if strings.Contains(label, "{hub}") {
		t.Errorf("label = %q, the placeholder was not filled in", label)
	}
	if strings.TrimSpace(strings.TrimPrefix(label, "☁")) == "" {
		t.Errorf("label = %q, it names no machine", label)
	}
	// Whatever the hostname is, it is in there.
	if host, err := os.Hostname(); err == nil && host != "" {
		if !strings.Contains(label, host) {
			t.Errorf("label = %q, want it to contain %q", label, host)
		}
	}

	// A format naming nothing still produces something rather than an empty
	// label, which Herdr would show as a space with no name.
	if got := (Config{RemoteWorkspaceFormat: "hub"}).RemoteWorkspaceLabel(); got != "hub" {
		t.Errorf("label = %q, want it used as given", got)
	}
}

func TestPerMachineOverridesFallBack(t *testing.T) {
	// Four settings work per machine as well as globally, which was undocumented
	// until recently and so worth holding to.
	cfg := Defaults()
	cfg.Placement = "split"
	cfg.HerdrBin = "/usr/bin/herdr"

	plain := Host{Target: "bot"}
	if got := cfg.PlacementFor(plain); got != "split" {
		t.Errorf("PlacementFor = %q, want the global %q", got, "split")
	}
	if got := cfg.BinFor(plain); got != "/usr/bin/herdr" {
		t.Errorf("BinFor = %q, want the global one", got)
	}

	own := Host{Target: "ci", Placement: "tab", HerdrBin: "/opt/herdr"}
	if got := cfg.PlacementFor(own); got != "tab" {
		t.Errorf("PlacementFor = %q, want the machine's own %q", got, "tab")
	}
	if got := cfg.BinFor(own); got != "/opt/herdr" {
		t.Errorf("BinFor = %q, want the machine's own", got)
	}

	// Nothing anywhere means nothing, not an empty string pretending to be a
	// path: probing is what an empty herdr_bin asks for.
	if got := (Config{}).BinFor(plain); got != "" {
		t.Errorf("BinFor = %q, want empty so the path is probed", got)
	}
}

func TestHubNameAlwaysNamesSomething(t *testing.T) {
	// It goes into the name of the space this machine creates on another one,
	// and is what finds that space again afterwards. An empty one would make a
	// space called nothing, and match every space when looking for it.
	hub := HubName()
	if hub == "" {
		t.Fatal("HubName is empty")
	}
	if strings.ContainsAny(hub, " \t\n") {
		t.Errorf("HubName = %q, want a single word", hub)
	}
	// It is what the label is built from.
	if !strings.Contains(Defaults().RemoteWorkspaceLabel(), hub) {
		t.Errorf("the remote label %q does not contain the hub name %q",
			Defaults().RemoteWorkspaceLabel(), hub)
	}
}

func TestADisplayLabelIsSafeWhereverItIsDrawn(t *testing.T) {
	// A machine's name here reaches three places: a pane's name, the name of
	// the machine's space, and the suffix those are matched against to decide
	// which panes belong to it. Only the first was cleaning it, so an escape or
	// a newline in a hand-edited label reached the sidebar by the other two.
	cfg := Defaults()
	host := Host{Target: "bot", Label: "my\x1b[31mbot\nlabel"}

	label := host.DisplayLabel()
	for _, bad := range []string{"\x1b", "\n", "\r", "\x00"} {
		if strings.Contains(label, bad) {
			t.Errorf("DisplayLabel() = %q, still carries %q", label, bad)
		}
	}

	// The space named after it is safe for the same reason, without needing to
	// know to do it again.
	for _, reachable := range []bool{true, false} {
		space := cfg.WorkspaceLabelFor(host, reachable)
		for _, bad := range []string{"\x1b", "\n"} {
			if strings.Contains(space, bad) {
				t.Errorf("the space is named %q, which carries %q", space, bad)
			}
		}
		if !strings.Contains(space, "bot") {
			t.Errorf("the space is named %q, which no longer says which machine", space)
		}
	}

	// And a target with nothing wrong with it is untouched, so the name in the
	// sidebar is the name somebody typed.
	if got := (Host{Target: "build-01.example.com"}).DisplayLabel(); got != "build-01.example.com" {
		t.Errorf("DisplayLabel() = %q, want it unchanged", got)
	}
}

func TestTheSameNameIsUsedForNamingAndForMatching(t *testing.T) {
	// Panes are recognised as a machine's by the suffix on their name, so the
	// name used to build them and the name used to match them have to be the
	// same string -- which is the other reason to clean it once.
	host := Host{Target: "bot", Label: "b\x1bot"}

	naming := host.DisplayLabel()
	matching := host.DisplayLabel()
	if naming != matching {
		t.Errorf("naming uses %q and matching uses %q", naming, matching)
	}
	if strings.Contains(Defaults().WorkspaceFor(host), "\x1b") {
		t.Error("the space is named with something the pane names would not match")
	}
}

func TestAMistakeInTheConfigIsDescribedInTheFilesTerms(t *testing.T) {
	// The decoder's own wording is accurate and about Go: "cannot unmarshal
	// string into Go struct field Config.max_mirrors of type int". That
	// sentence ends up in the menu and in the status listing, where nobody is
	// thinking about Go structs -- they are looking at a file they just edited
	// and want to know which line to change.
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "a number written as text",
			content: "{\n  \"max_mirrors\": \"lots\"\n}",
			want:    []string{"max_mirrors", "should be a number", "not text", "line 2"},
		},
		{
			name:    "text written as a number",
			content: "{\n  \"poll_interval\": 2\n}",
			want:    []string{"poll_interval", "should be text", "not a number", "line 2"},
		},
		{
			name:    "a setting inside a machine",
			content: "{\n  \"hosts\": [\n    {\"disabled\": \"yes\"}\n  ]\n}",
			// Named without the machine's position: the decoder spells that
			// differently by Go version, and the line number already says
			// which entry.
			want: []string{"hosts.disabled", "true or false", "not text", "line 3"},
		},
		{
			name:    "a list written as one thing",
			content: "{\n  \"hosts\": {\"target\": \"bot\"}\n}",
			want:    []string{"hosts", "should be a list", "line 2"},
		},
		{
			// The comma ends one line and the bracket begins the next, which is
			// how an indented config is written and where the mistake hides.
			name:    "a trailing comma",
			content: "{\n  \"hosts\": [\n    {\"target\": \"bot\"},\n  ]\n}",
			want:    []string{"a comma just before", "line 4"},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)

			_, err := Load()
			if err == nil {
				t.Fatal("a config this cannot read was read")
			}
			got := err.Error()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("error %q should contain %q", got, want)
				}
			}
			// None of Go's vocabulary.
			for _, jargon := range []string{"unmarshal", "Go struct", "config.Host"} {
				if strings.Contains(got, jargon) {
					t.Errorf("error %q still talks about %q", got, jargon)
				}
			}
		})
	}
}

func TestAMachineThatCannotBeUsedIsNotDroppedInSilence(t *testing.T) {
	// A machine with no target cannot be reached, so it is dropped -- but
	// dropping something somebody deliberately wrote and saying nothing is how
	// a mistyped "targt" becomes a machine simply missing from the menu, with
	// nowhere to look for why. The check for this existed but sat after the
	// point where these entries had already been removed, so it never fired.
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "an entry with nothing in it",
			content: `{"hosts":[{}]}`,
			want:    "machine 1 under hosts has no target",
		},
		{
			name:    "an entry counted the way the file reads",
			content: `{"hosts":[{"target":"bot"},{"session":"work"}]}`,
			want:    "machine 2 under hosts has no target",
		},
		{
			name:    "an entry that has a label to name it by",
			content: `{"hosts":[{"label":"the build box"}]}`,
			want:    `the machine labelled "the build box" has no target`,
		},
		{
			name:    "a target misspelt into a setting that is not one",
			content: `{"hosts":[{"targt":"bot"}]}`,
			want:    "has no target",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			problems := strings.Join(cfg.Problems(), "; ")
			if !strings.Contains(problems, tt.want) {
				t.Errorf("problems %q should mention %q", problems, tt.want)
			}
		})
	}
}

func TestAUsableMachineIsNotComplainedAbout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"hosts":[{"target":"bot"},{"target":"prod","label":"prod"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, problem := range cfg.Problems() {
		if strings.Contains(problem, "no target") {
			t.Errorf("a config with nothing wrong reported %q", problem)
		}
	}
	if len(cfg.Hosts) != 2 {
		t.Errorf("hosts = %d, want 2", len(cfg.Hosts))
	}
}

func TestTheSameBrokenFileReadsTheSameOnEveryToolchain(t *testing.T) {
	// Go spells the path to a setting inside a list differently by version:
	// "hosts.disabled" on 1.25 and 1.26, "hosts.0.disabled" on newer. This
	// plugin is built from source on whatever toolchain the machine has, so
	// taking that as given would have the same broken file produce different
	// wording on two machines -- and it did: the wording was written against a
	// local toolchain and failed on CI's.
	for _, given := range []string{"hosts.disabled", "hosts.0.disabled", "hosts.12.disabled"} {
		if got := plainField(given); got != "hosts.disabled" {
			t.Errorf("plainField(%q) = %q, want %q", given, got, "hosts.disabled")
		}
	}

	// A plain setting is untouched.
	if got := plainField("max_mirrors"); got != "max_mirrors" {
		t.Errorf("plainField(max_mirrors) = %q", got)
	}
	// And a path with nothing but a position keeps something to say.
	if got := plainField("0"); got == "" {
		t.Error("a field of only a position should not vanish")
	}
}

func TestASpaceIsSafeButOnlyMeansSomethingWhenNobodyWroteItDown(t *testing.T) {
	// These answer different questions and used to be one. ValidTarget asks
	// whether ssh can be handed this at all; PlausibleTarget asks whether
	// somebody meant it as a machine, which only matters for a name nobody
	// declared -- connect falls back to the terminal's selection, so a line of
	// someone else's output can arrive as a target.
	//
	// Merging them cost a real machine: `Host "my server"` is legal ssh, read
	// correctly out of ~/.ssh/config, and then dropped from the menu in silence.
	t.Run("a space is safe to hand ssh", func(t *testing.T) {
		if err := ValidTarget("my server"); err != nil {
			t.Errorf("ValidTarget(my server) = %v, want nil", err)
		}
	})
	t.Run("but a name nobody wrote down needs to look like one", func(t *testing.T) {
		if err := PlausibleTarget("my server"); err == nil {
			t.Error("a selected line with a space was taken as a machine")
		}
	})

	// Both still refuse what is actually dangerous, whoever it came from.
	for _, target := range []string{"", "-oProxyCommand=touch /tmp/x", "bot\x00", "a\x1b[31mb", "bot\n"} {
		if err := ValidTarget(target); err == nil {
			t.Errorf("ValidTarget(%q) = nil, want a refusal", target)
		}
		if err := PlausibleTarget(target); err == nil {
			t.Errorf("PlausibleTarget(%q) = nil, want a refusal", target)
		}
	}

	// And both accept an ordinary machine.
	for _, target := range []string{"bot", "user@10.0.0.1", "prod.example.com", "[2001:db8::1]"} {
		if err := ValidTarget(target); err != nil {
			t.Errorf("ValidTarget(%q) = %v, want nil", target, err)
		}
		if err := PlausibleTarget(target); err != nil {
			t.Errorf("PlausibleTarget(%q) = %v, want nil", target, err)
		}
	}
}

func TestProblemsCatchesAMisspelledScope(t *testing.T) {
	// scope decides whether a machine's whole Herdr is mirrored or only the
	// space it shares with this one. A typo fell back to shared, silently, so
	// somebody who asked for "all" got half of what they asked for and nothing
	// said why -- the same shape as the misspelled mode above.
	cfg := Defaults()
	cfg.Scope = "everything"

	problems := strings.Join(cfg.Problems(), "\n")
	if !strings.Contains(problems, "everything") {
		t.Errorf("a misspelled scope should be reported and named, got %q", problems)
	}
	if !strings.Contains(problems, "shared") {
		t.Errorf("the report should say what happens instead, got %q", problems)
	}

	// And both spellings that do work must not be reported, or the warning
	// becomes noise that gets ignored.
	for _, ok := range []string{ScopeShared, ScopeAll} {
		cfg := Defaults()
		cfg.Scope = ok
		for _, problem := range cfg.Problems() {
			if strings.Contains(problem, "scope") {
				t.Errorf("scope %q is valid but was reported: %q", ok, problem)
			}
		}
	}
}

func TestProblemsCatchesTwoMachinesAnsweringToOneName(t *testing.T) {
	// A label names the machine's space, the panes in it, and the suffix those
	// are matched against. Two machines answering to one name therefore share a
	// space, and each pass reads the other's terminals as strays in its own
	// space and closes them: connecting both leaves one with nothing, and
	// nothing said why. Only the duplicate target was reported, and these have
	// different targets.
	t.Run("two labels the same", func(t *testing.T) {
		cfg := Defaults()
		cfg.Hosts = []Host{{Target: "bot", Label: "build"}, {Target: "ci", Label: "build"}}
		problems := strings.Join(cfg.Problems(), "\n")
		for _, want := range []string{"bot", "ci", "build"} {
			if !strings.Contains(problems, want) {
				t.Errorf("the report should name %q: %q", want, problems)
			}
		}
	})

	t.Run("a label copied from another machine's target", func(t *testing.T) {
		// The one with no label of its own answers to its target, so this
		// collides just as surely.
		cfg := Defaults()
		cfg.Hosts = []Host{{Target: "bot"}, {Target: "ci", Label: "bot"}}
		if problems := strings.Join(cfg.Problems(), "\n"); !strings.Contains(problems, "ci") {
			t.Errorf("a label copied from another machine's target should be reported: %q", problems)
		}
	})

	t.Run("a machine that is switched off collides with nothing", func(t *testing.T) {
		// It is not connected to, so saying anything here is a warning about a
		// setting that is behaving itself.
		cfg := Defaults()
		cfg.Hosts = []Host{{Target: "bot", Label: "build"}, {Target: "ci", Label: "build", Disabled: true}}
		for _, problem := range cfg.Problems() {
			if strings.Contains(problem, "both called") {
				t.Errorf("a disabled machine was reported as a collision: %q", problem)
			}
		}
	})

	t.Run("the same machine listed twice is one mistake, not two", func(t *testing.T) {
		// It is already reported as a duplicate target; saying it also collides
		// with itself describes one typo as two problems.
		cfg := Defaults()
		cfg.Hosts = []Host{{Target: "bot"}, {Target: "bot"}}
		for _, problem := range cfg.Problems() {
			if strings.Contains(problem, "both called") {
				t.Errorf("a machine was reported as colliding with itself: %q", problem)
			}
		}
	})

	t.Run("ordinary machines are not reported", func(t *testing.T) {
		cfg := Defaults()
		cfg.Hosts = []Host{{Target: "bot"}, {Target: "ci"}, {Target: "prod", Label: "production"}}
		for _, problem := range cfg.Problems() {
			if strings.Contains(problem, "both called") {
				t.Errorf("machines with distinct names were reported: %q", problem)
			}
		}
	})
}

func TestProblemsCatchesALabelThatCannotBeDrawn(t *testing.T) {
	// A label is made safe to draw before it is used, and one made only of
	// things that cannot be drawn is left with nothing. The machine's space
	// then has no name in it and its terminals are called "shell@" -- while the
	// file says otherwise, and nothing anywhere connects the two.
	for _, label := range []string{"\x01\x02", "   ", "\n\t", "\x7f"} {
		cfg := Defaults()
		cfg.Hosts = []Host{{Target: "bot", Label: label}}
		problems := strings.Join(cfg.Problems(), "\n")
		if !strings.Contains(problems, "named after nothing") {
			t.Errorf("label %q should be reported as unusable, got %q", label, problems)
		}
		// And it says which half is at fault. Told to look at the target when
		// the label is the problem, somebody goes and checks a target that is
		// perfectly fine.
		if !strings.Contains(problems, "label") {
			t.Errorf("the report for label %q does not say the label is the problem: %q", label, problems)
		}
		if strings.Contains(problems, "nothing but spaces") {
			t.Errorf("the report for label %q blames the target instead: %q", label, problems)
		}
		// The report must not carry the label's own control characters into
		// the log or the menu; %q escapes them.
		for _, r := range problems {
			if r < 0x20 && r != '\n' {
				t.Errorf("the report for %q carries a control character itself: %q", label, problems)
				break
			}
		}
	}

	// A label with something in it is fine, control characters or not.
	// "\x1b[31m" belongs here, not above: the escape byte goes and "[31m" is
	// left, which is a name -- an odd one, but drawable and its own.
	for _, label := range []string{"build", "build\x01", " build ", "\x1b[31m"} {
		cfg := Defaults()
		cfg.Hosts = []Host{{Target: "bot", Label: label}}
		for _, problem := range cfg.Problems() {
			if strings.Contains(problem, "named after nothing") {
				t.Errorf("label %q is usable but was reported: %q", label, problem)
			}
		}
	}

	// A machine with no label answers to its target, so an ordinary one must
	// not be reported.
	cfg := Defaults()
	cfg.Hosts = []Host{{Target: "bot"}}
	for _, problem := range cfg.Problems() {
		if strings.Contains(problem, "named after nothing") {
			t.Errorf("a machine with no label was reported: %q", problem)
		}
	}

	// But a target of nothing but spaces leaves it just as nameless. ssh
	// reaches `Host "my server"`, so a space in a target is allowed and this
	// gets that far -- and then has nothing left to name anything with.
	cfg = Defaults()
	cfg.Hosts = []Host{{Target: "   "}}
	problems := strings.Join(cfg.Problems(), "\n")
	if !strings.Contains(problems, "named after nothing") {
		t.Errorf("a target of nothing but spaces should be reported, got %q", problems)
	}
	// The other way round: this one has no label, so blaming one sends
	// somebody looking for a setting they never wrote.
	if !strings.Contains(problems, "nothing but spaces") {
		t.Errorf("the report does not say the target is the problem: %q", problems)
	}
}

func TestAConfigFileWithNothingInItSaysSo(t *testing.T) {
	// "unexpected end of JSON input" is what the decoder says about an empty
	// file, and it reads as though something was cut off partway. A config gets
	// into that state by itself -- a truncated write, a redirect that clobbered
	// it -- so it is worth saying what it is and what to do, since the plugin
	// writes a fresh one whenever the file is missing entirely.
	for _, body := range []string{"", "   ", "\n\n", "\t \n"} {
		dir := t.TempDir()
		t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load()
		if err == nil {
			t.Fatalf("an empty config was accepted (%q)", body)
		}
		if !strings.Contains(err.Error(), "empty") || !strings.Contains(err.Error(), "delete") {
			t.Errorf("the error for %q does not say what is wrong or what to do: %v", body, err)
		}
		if strings.Contains(err.Error(), "unexpected end") {
			t.Errorf("the decoder's own wording survived for %q: %v", body, err)
		}
	}
}

func TestAByteOrderMarkIsNotAMistakeToReportBackToSomebody(t *testing.T) {
	// Several editors write one. JSON does not allow it, and the decoder's
	// complaint names a character that appears nowhere in the file as its
	// author sees it -- with nothing to fix in a file that is otherwise
	// correct. So it is skipped.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	body := append([]byte("\xef\xbb\xbf"), []byte(`{"poll_interval":"9s","hosts":[{"target":"bot"}]}`)...)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("a config with a byte-order mark was refused: %v", err)
	}
	if cfg.Interval().String() != "9s" {
		t.Errorf("poll_interval came out %s, want 9s", cfg.Interval())
	}
	if len(cfg.Hosts) != 1 || cfg.Hosts[0].Target != "bot" {
		t.Errorf("the machines came out %+v", cfg.Hosts)
	}

	// And the line numbers still match the file, since the mark goes before
	// anything reads an offset out of it.
	dir = t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	broken := append([]byte("\xef\xbb\xbf"), []byte("{\n  \"poll_interval\": 2\n}")...)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("a broken config after a mark was accepted")
	} else if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("the line number does not match the file: %v", err)
	}
}

func TestASettingsTypeIsNamedTheWayTheFileReads(t *testing.T) {
	// The decoder's own wording is about Go: "cannot unmarshal string into Go
	// struct field Config.max_mirrors of type int". Somebody editing a JSON
	// file is not thinking in Go types, so both halves of "should be X, not Y"
	// are translated. Every kind the config actually uses is here, and so is
	// the answer for one it does not.
	var flag *bool
	for _, tt := range []struct {
		what string
		typ  reflect.Type
		want string
	}{
		{"a string setting", reflect.TypeOf(""), "text"},
		{"a number", reflect.TypeOf(0), "a number"},
		{"a wider number", reflect.TypeOf(int64(0)), "a number"},
		{"a fractional one", reflect.TypeOf(0.5), "a number"},
		{"a flag", reflect.TypeOf(true), "true or false"},
		// The flags are pointers so that "false" can be told from "unset", and
		// the name of the thing pointed at is what somebody wrote.
		{"a flag as it is actually declared", reflect.TypeOf(flag), "true or false"},
		{"the list of machines", reflect.TypeOf([]Host{}), "a list"},
		{"one machine", reflect.TypeOf(Host{}), "a set of settings"},
		{"a map", reflect.TypeOf(map[string]string{}), "a set of settings"},
		// Nothing in the config is one of these, but a decoder that reports one
		// should not produce an empty sentence.
		{"something with no plain name", reflect.TypeOf(make(chan int)), "chan int"},
		{"nothing at all", nil, "something else"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			if got := plainType(tt.typ); got != tt.want {
				t.Errorf("plainType(%v) = %q, want %q", tt.typ, got, tt.want)
			}
		})
	}
}

func TestWhatWasFoundIsNamedTheSameWay(t *testing.T) {
	// The other half of the sentence. The decoder reports what it found as a
	// JSON word, and "should be text, not string" would be half translated.
	for found, want := range map[string]string{
		"string": "text",
		"number": "a number",
		"bool":   "true or false",
		"array":  "a list",
		"object": "a set of settings",
		// Passed through rather than dropped: an empty "not " is worse than an
		// unfamiliar word.
		"null":     "null",
		"":         "",
		"whatever": "whatever",
	} {
		if got := plainValue(found); got != want {
			t.Errorf("plainValue(%q) = %q, want %q", found, got, want)
		}
	}
}

func TestACapThatIsNotACapSaysSo(t *testing.T) {
	// Zero or less is not a cap, so the default goes back in its place. Done
	// in silence, somebody who wrote 0 meaning "no limit" meets "at the mirror
	// limit — raise max_mirrors" with a file in front of them that says 0.
	for _, tt := range []struct {
		what, body string
		wantSaid   bool
	}{
		{"zero", `{"max_mirrors":0,"hosts":[{"target":"bot"}]}`, true},
		{"negative", `{"max_mirrors":-1,"hosts":[{"target":"bot"}]}`, true},
		// Not written at all is not a mistake, and is the usual case: an
		// absent setting and one written as 0 are the same zero in the struct,
		// which is why this is read from the file.
		{"absent", `{"hosts":[{"target":"bot"}]}`, false},
		{"a real cap", `{"max_mirrors":4,"hosts":[{"target":"bot"}]}`, false},
	} {
		t.Run(tt.what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)

			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			said := false
			for _, problem := range cfg.Problems() {
				if strings.Contains(problem, "max_mirrors") {
					said = true
				}
			}
			if said != tt.wantSaid {
				t.Errorf("%s: said %v, want %v; problems were %v", tt.what, said, tt.wantSaid, cfg.Problems())
			}
			// Whatever was written, the cap in force is a usable one.
			if cfg.MaxMirrors <= 0 {
				t.Errorf("the cap in force is %d", cfg.MaxMirrors)
			}
		})
	}
}

func TestAPathOnlyAShellCouldReadIsReported(t *testing.T) {
	// herdr_bin is put on the remote command line quoted, as any path holding
	// a space or a metacharacter has to be. So "~" arrives as a tilde and
	// "$HOME" as five characters, the machine reports no such file, and what
	// somebody is shown for it is "no herdr found on the machine — set
	// herdr_bin if it is installed elsewhere there": the one thing they have
	// already done.
	for _, tt := range []struct {
		what, body string
		wantSaid   bool
	}{
		{"a home-relative path", `{"hosts":[{"target":"bot","herdr_bin":"~/bin/herdr"}]}`, true},
		{"a variable in it", `{"hosts":[{"target":"bot","herdr_bin":"$HOME/bin/herdr"}]}`, true},
		{"the same at the top level", `{"herdr_bin":"~/bin/herdr","hosts":[{"target":"bot"}]}`, true},
		// Both of these are read by the machine as they are written: an
		// absolute path, and a relative one, which ssh resolves from the home
		// directory it drops you in.
		{"an absolute path", `{"hosts":[{"target":"bot","herdr_bin":"/usr/local/bin/herdr"}]}`, false},
		{"a relative path", `{"hosts":[{"target":"bot","herdr_bin":"bin/herdr"}]}`, false},
		{"none given", `{"hosts":[{"target":"bot"}]}`, false},
	} {
		t.Run(tt.what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)

			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			said := false
			for _, problem := range cfg.Problems() {
				if strings.Contains(problem, "herdr_bin") {
					said = true
				}
			}
			if said != tt.wantSaid {
				t.Errorf("%s: said %v, want %v; problems were %v", tt.what, said, tt.wantSaid, cfg.Problems())
			}
		})
	}
}

func TestAPollIntervalThatIsNotUsedSaysSo(t *testing.T) {
	// This is how often every machine is reached over ssh, and anything the
	// clamp refuses becomes two seconds. Somebody writing "30" for half a
	// minute gets fifteen times the traffic they asked for, on every machine,
	// with a file in front of them saying 30.
	for _, tt := range []struct {
		what, body string
		wantSaid   bool
		want       time.Duration
	}{
		{"no unit", `{"poll_interval":"30","hosts":[]}`, true, 2 * time.Second},
		{"words", `{"poll_interval":"2 seconds","hosts":[]}`, true, 2 * time.Second},
		{"nothing at all", `{"poll_interval":"0s","hosts":[]}`, true, 2 * time.Second},
		{"backwards", `{"poll_interval":"-5s","hosts":[]}`, true, 2 * time.Second},
		{"faster than any machine could answer", `{"poll_interval":"10ms","hosts":[]}`, true, 2 * time.Second},

		// Used as written, so nothing to say.
		{"half a minute", `{"poll_interval":"30s","hosts":[]}`, false, 30 * time.Second},
		{"the floor itself", `{"poll_interval":"500ms","hosts":[]}`, false, 500 * time.Millisecond},
		{"absent", `{"hosts":[]}`, false, 2 * time.Second},
	} {
		t.Run(tt.what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)

			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			said := false
			for _, problem := range cfg.Problems() {
				if strings.Contains(problem, "poll_interval") {
					said = true
				}
			}
			if said != tt.wantSaid {
				t.Errorf("said %v, want %v; problems were %v", said, tt.wantSaid, cfg.Problems())
			}
			// And what is said matches what is done. Written out rather than
			// measured against Interval(), which would agree with itself.
			if got := cfg.Interval(); got != tt.want {
				t.Errorf("machines are polled every %s, want %s", got, tt.want)
			}
		})
	}
}

func TestTheNameForAnUnreachableMachineIsHeldToTheSameRuleAsTheOther(t *testing.T) {
	// workspace_format was checked for {host} and this one was not, though
	// they are the same string used for the same purpose a moment apart. It is
	// the worse of the two to get wrong: machines collide only while they are
	// unreachable, so nothing is wrong until several go at once -- the network
	// dropping rather than anything done to the config that day -- and then
	// every unreachable machine is the same space.
	c := Defaults()
	c.WorkspaceFormatDown = "offline"
	found := false
	for _, p := range c.Problems() {
		if strings.Contains(p, "workspace_format_down") {
			found = true
		}
	}
	if !found {
		t.Errorf("a down-format with no {host} was accepted: %q", c.Problems())
	}

	// And the condition the other check carries: an explicitly chosen
	// workspace name is used as given, so neither format is consulted and
	// neither is worth a warning.
	c.Workspace = "one space for the lot"
	for _, p := range c.Problems() {
		if strings.Contains(p, "workspace_format_down") {
			t.Errorf("warned about a format that is not used: %q", p)
		}
	}
}

func TestATargetLongerThanAnyMachineNameIsRefused(t *testing.T) {
	// A target reaches here from a file somebody edited or from whatever text
	// was selected when an action was invoked, and a selection is a line of
	// somebody else's output as readily as a name. A base64 blob or a long URL
	// has no space in it and no dash at the front, which is everything else
	// this asks about -- so sixty thousand characters were accepted as a
	// machine.
	//
	// The failed connection is not what it costs. The target is in the message
	// the failure is reported with, and that message goes into a log that rolls
	// at a quarter of a megabyte: one selection could take the history with it.
	long := strings.Repeat("aB3", 20000)
	err := ValidTarget(long)
	if err == nil {
		t.Fatalf("a %d-character selection was accepted as a machine", len(long))
	}
	// The message must not carry the thing it is refusing, which would put it
	// in the log by another route.
	if len(err.Error()) > 200 {
		t.Errorf("refusing it produced a %d-character message", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "longer than any machine") {
		t.Errorf("the reason given is %q", err)
	}

	// The guess about a selection refuses it too, since it asks this first.
	if PlausibleTarget(long) == nil {
		t.Error("a selection that long is offered as a machine to connect to")
	}

	// And a name of an ordinary length is untouched, including a long one:
	// this is a bound on what cannot be a name, not a limit somebody has to
	// think about.
	for _, ok := range []string{
		"bot",
		"deploy@bot",
		"deploy@" + strings.Repeat("a", 200) + ".example.com",
	} {
		if err := ValidTarget(ok); err != nil {
			t.Errorf("ValidTarget(%d characters) = %v", len(ok), err)
		}
	}
}

func TestASelectionAcrossTwoLinesIsToldWhatIsWrong(t *testing.T) {
	// Dragging over more than one line and pressing the connect key is the
	// likeliest way to arrive here by accident. The general answer for it was
	// "contains a control character", which is true and no help: it names the
	// newline rather than the mistake.
	//
	// This check is the one for names that came from nowhere -- a name written
	// in ~/.ssh/config is held to less, since somebody wrote it down.
	err := PlausibleTarget("bot\nci")
	if err == nil {
		t.Fatal("two lines were offered as a machine to connect to")
	}
	if !strings.Contains(err.Error(), "more than one line") {
		t.Errorf("the reason given is %q, which does not say what was selected", err)
	}

	// A name written down is still held to the plainer rule, which names the
	// control character: there is no selection to talk about, and whoever put
	// one in a config file wants to know which.
	if err := ValidTarget("bot\nci"); err == nil {
		t.Error("a target with a newline in it was accepted from a config file")
	} else if !strings.Contains(err.Error(), "control character") {
		t.Errorf("a written-down target says %q rather than naming the character", err)
	}

	// And the checks either side of it still answer for themselves.
	if err := PlausibleTarget("bot ci"); err == nil || !strings.Contains(err.Error(), "space") {
		t.Errorf("a selection with a space says %v", err)
	}
	if err := PlausibleTarget("deploy@bot"); err != nil {
		t.Errorf("an ordinary machine was refused: %v", err)
	}
}

func TestATargetOfExactlyTheLimitIsStillAMachine(t *testing.T) {
	// The limit is a bound on what goes into a log line, not a judgement about
	// the name, so the last length it allows has to be allowed. Nothing pinned
	// which side of maxTargetBytes the check falls on, and turning `>` into
	// `>=` -- rejecting a name of exactly the limit -- changed no test.
	atLimit := strings.Repeat("a", maxTargetBytes)
	if err := ValidTarget(atLimit); err != nil {
		t.Errorf("a target of exactly %d bytes was refused: %v", maxTargetBytes, err)
	}
	overLimit := strings.Repeat("a", maxTargetBytes+1)
	if err := ValidTarget(overLimit); err == nil {
		t.Errorf("a target of %d bytes was allowed", maxTargetBytes+1)
	} else if strings.Contains(err.Error(), overLimit) {
		// The reason this bound exists: the target goes into the message, and
		// the message goes into a log.
		t.Errorf("the refusal quotes the whole overlong target back: %v", err)
	}
}

func TestALabelThatIsAnotherMachinesTargetIsReported(t *testing.T) {
	// Not the collision beside it: these two are shown under different names,
	// so nothing about the sidebar is ambiguous. What is ambiguous is the
	// name itself -- typed or selected, "prod" reaches the machine targeted
	// that way, while the menu shows the other machine under it.
	cfg := Defaults()
	cfg.Hosts = []Host{
		{Target: "web", Label: "prod"},
		{Target: "prod", Label: "primary"},
	}
	problems := cfg.Problems()
	if len(problems) != 1 {
		t.Fatalf("want the one problem, got %v", problems)
	}
	for _, want := range []string{`"web"`, `"prod"`, "reaches that one"} {
		if !strings.Contains(problems[0], want) {
			t.Errorf("the problem reads %q, without %q", problems[0], want)
		}
	}

	// A label nothing else is targeted by is nobody's business.
	cfg.Hosts = []Host{{Target: "web", Label: "prod"}, {Target: "db"}}
	if problems := cfg.Problems(); len(problems) != 0 {
		t.Errorf("a label that is no other machine's target was reported: %v", problems)
	}

	// A machine labelled with its own target says nothing: it is the name it
	// already answers to.
	cfg.Hosts = []Host{{Target: "web", Label: "web"}}
	if problems := cfg.Problems(); len(problems) != 0 {
		t.Errorf("a machine labelled with its own target was reported: %v", problems)
	}

	// And a machine switched off collides with nothing, as above.
	cfg.Hosts = []Host{{Target: "web", Label: "prod"}, {Target: "prod", Disabled: true}}
	if problems := cfg.Problems(); len(problems) != 0 {
		t.Errorf("a machine that is switched off was treated as a collision: %v", problems)
	}
}
