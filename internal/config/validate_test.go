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
			want:    []string{"hosts.disabled", "true or false", "not text", "line 3"},
		},
		{
			name:    "a list written as one thing",
			content: "{\n  \"hosts\": {\"target\": \"bot\"}\n}",
			want:    []string{"hosts", "should be a list", "line 2"},
		},
		{
			name:    "a trailing comma",
			content: "{\n  \"hosts\": [\n    {\"target\": \"bot\"},\n  ]\n}",
			want:    []string{"invalid character", "line 4"},
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
