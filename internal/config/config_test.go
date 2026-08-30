package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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

func TestWorkspaceLabelFor(t *testing.T) {
	cfg := Defaults()
	bot := Host{Target: "bot"}

	// Herdr joins sidebar tokens with " · ", so a marker token always sits a
	// dot away from the name. Carrying it in the name is the only way to have
	// it directly beside the machine.
	if got := cfg.WorkspaceLabelFor(bot, true); got != "☁  bot" {
		t.Errorf("reachable = %q, want %q", got, "☁  bot")
	}
	// The state still shows, since colour is not available in a name.
	if got := cfg.WorkspaceLabelFor(bot, false); got != "⚠  bot" {
		t.Errorf("unreachable = %q, want %q", got, "⚠  bot")
	}

	// A name the user chose is theirs, marker and all.
	cfg.Workspace = "remote"
	if got := cfg.WorkspaceLabelFor(bot, false); got != "remote" {
		t.Errorf("shared workspace = %q, want %q", got, "remote")
	}
	cfg.Workspace = ""
	if got := cfg.WorkspaceLabelFor(Host{Target: "bot", Workspace: "mine"}, false); got != "mine" {
		t.Errorf("per-host workspace = %q, want %q", got, "mine")
	}
}

func TestHowOftenMachinesAreChecked(t *testing.T) {
	// A poll runs an ssh command against every connected machine, so this has
	// a floor: below it the machines spend their time answering rather than
	// being worked on. Anything unreadable falls back to the default rather
	// than to nothing, since a zero interval is a busy loop.
	for _, tt := range []struct {
		what, set, want string
	}{
		{"unset is the documented default", "", "2s"},
		{"a plain duration is honoured", "5s", "5s"},
		{"and a longer one", "1m30s", "1m30s"},
		{"the floor itself is allowed", "500ms", "500ms"},
		{"just under it is not", "499ms", "2s"},
		{"nor is anything far below", "1ms", "2s"},
		{"nor is zero, which would be a busy loop", "0s", "2s"},
		{"nor is a negative one", "-5s", "2s"},
		{"and nonsense falls back rather than failing", "soon", "2s"},
		{"as does a bare number, which has no unit", "2", "2s"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			cfg := Config{PollInterval: tt.set}
			if got := cfg.Interval().String(); got != tt.want {
				t.Errorf("poll_interval %q gives %s, want %s", tt.set, got, tt.want)
			}
		})
	}
}

func TestASettingYouWriteIsTheSettingThatIsUsed(t *testing.T) {
	// Blanks are filled in from the defaults, and a version of that which
	// filled in everything rather than only the blanks would leave every
	// setting in the README's table silently doing nothing. Each one is
	// checked, because that mistake is per-field: it was made for some of
	// these and not others, and only the ones nobody had written a test for.
	//
	// Every value here differs from its default, or it proves nothing.
	set := Config{
		PollInterval:          "9s",
		Session:               "work",
		Mode:                  ModeAttach,
		Scope:                 ScopeAll,
		Placement:             "tab",
		LabelFormat:           "{host}:{name}",
		WorkspaceFormat:       "remote {host}",
		WorkspaceFormatDown:   "down {host}",
		RemoteWorkspaceFormat: "from {hub}",
		CaptureNewPanes:       boolPtr(false),
		ClosePropagates:       boolPtr(false),
		Takeover:              boolPtr(false),
		AutoStart:             boolPtr(false),
		MaxMirrors:            4,
	}
	d := Defaults()
	got := set.normalized()

	for _, f := range []struct {
		name                string
		got, set, byDefault any
	}{
		{"poll_interval", got.PollInterval, set.PollInterval, d.PollInterval},
		{"session", got.Session, set.Session, d.Session},
		{"mode", got.Mode, set.Mode, d.Mode},
		{"scope", got.Scope, set.Scope, d.Scope},
		{"placement", got.Placement, set.Placement, d.Placement},
		{"label_format", got.LabelFormat, set.LabelFormat, d.LabelFormat},
		{"workspace_format", got.WorkspaceFormat, set.WorkspaceFormat, d.WorkspaceFormat},
		{"workspace_format_down", got.WorkspaceFormatDown, set.WorkspaceFormatDown, d.WorkspaceFormatDown},
		{"remote_workspace_format", got.RemoteWorkspaceFormat, set.RemoteWorkspaceFormat, d.RemoteWorkspaceFormat},
		{"max_mirrors", got.MaxMirrors, set.MaxMirrors, d.MaxMirrors},
	} {
		if f.set == f.byDefault {
			t.Errorf("%s is set to its own default here, so this checks nothing", f.name)
		}
		if f.got != f.set {
			t.Errorf("%s was written as %v but came out %v", f.name, f.set, f.got)
		}
	}

	// The flags are pointers so that "false" can be told from "unset", which is
	// the whole reason turning one off is possible at all.
	for _, f := range []struct {
		name string
		got  *bool
	}{
		{"capture_new_panes", got.CaptureNewPanes},
		{"close_propagates", got.ClosePropagates},
		{"takeover", got.Takeover},
		{"auto_start", got.AutoStart},
	} {
		if f.got == nil {
			t.Errorf("%s came out unset", f.name)
			continue
		}
		if *f.got {
			t.Errorf("%s was turned off but came out on", f.name)
		}
	}
}

func TestAnEmptyConfigComesOutAsTheDefaults(t *testing.T) {
	// The other half: every blank is filled, so nothing downstream has to ask
	// whether a setting was written down.
	got := Config{}.normalized()
	d := Defaults()

	if got.PollInterval != d.PollInterval || got.Session != d.Session ||
		got.Mode != d.Mode || got.Scope != d.Scope || got.Placement != d.Placement {
		t.Errorf("blanks were not filled in: %+v", got)
	}
	if got.LabelFormat != d.LabelFormat || got.WorkspaceFormat != d.WorkspaceFormat ||
		got.WorkspaceFormatDown != d.WorkspaceFormatDown ||
		got.RemoteWorkspaceFormat != d.RemoteWorkspaceFormat {
		t.Errorf("a name format was left blank, which would name things after nothing: %+v", got)
	}
	if got.MaxMirrors != d.MaxMirrors {
		t.Errorf("max_mirrors = %d, want the default %d", got.MaxMirrors, d.MaxMirrors)
	}
	for name, flag := range map[string]*bool{
		"capture_new_panes": got.CaptureNewPanes,
		"close_propagates":  got.ClosePropagates,
		"takeover":          got.Takeover,
		"auto_start":        got.AutoStart,
	} {
		if flag == nil {
			t.Errorf("%s was left unset, so nothing downstream can tell what it means", name)
		}
	}
}

func TestNormalizingLeavesTheConfigItWasGivenAlone(t *testing.T) {
	// Config is taken by value, which makes normalized() look like it cannot
	// touch its caller's. Its Hosts share the caller's backing array, though,
	// so dropping a host by compacting into that array rewrites what the caller
	// is still holding: the entry after the dropped one moves down a place, and
	// the last one appears twice.
	//
	// Nothing calls it that way today. It is one allocation to make the shape
	// safe rather than to leave it depending on that staying true.
	original := Config{Hosts: []Host{
		{Target: "", Label: "mistyped"},
		{Target: "bot"},
		{Target: "ci"},
	}}
	held := original.Hosts

	got := original.normalized()

	for i, want := range []string{"", "bot", "ci"} {
		if held[i].Target != want {
			t.Errorf("the caller's host %d is now %q, want %q -- normalizing rewrote it",
				i, held[i].Target, want)
		}
	}
	// And it did its own job: the machine with no target is not in the result.
	if len(got.Hosts) != 2 || got.Hosts[0].Target != "bot" || got.Hosts[1].Target != "ci" {
		t.Errorf("the normalized copy holds %+v, want bot and ci", got.Hosts)
	}
	if len(got.Problems()) == 0 {
		t.Error("dropping a machine somebody wrote down should be reported")
	}
}

func TestWhatThisMachineIsCalledOnAnother(t *testing.T) {
	// This names the space this machine creates on a remote one, so it has to
	// be something. A machine that cannot say what it is called would otherwise
	// name every such space after nothing, and none of them could be told apart
	// from the other end.
	for _, tt := range []struct {
		what, hostname string
		err            error
		want           string
	}{
		{"an ordinary hostname is used", "workbox", nil, "workbox"},
		{"a fully qualified one is kept whole", "bot.example.com", nil, "bot.example.com"},
		{"no hostname at all falls back", "", nil, "herdr"},
		{"and so does an error", "workbox", errors.New("no hostname"), "herdr"},
		{"an error wins even with a name beside it", "", errors.New("no hostname"), "herdr"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			if got := hubName(tt.hostname, tt.err); got != tt.want {
				t.Errorf("hubName(%q, %v) = %q, want %q", tt.hostname, tt.err, got, tt.want)
			}
		})
	}

	// And the real one is never empty, whatever this machine is called.
	if HubName() == "" {
		t.Error("HubName came back empty")
	}
}

func TestWhichLineAnErrorIsOn(t *testing.T) {
	// The line number is the useful half of a config error: it says which entry
	// to look at. It is worked out by counting newlines up to an offset the
	// decoder handed over, and that offset is not this package's to trust --
	// slicing past the end of the file would take the daemon down on a config
	// it could not parse, which is the moment it is most needed.
	raw := []byte("{\n  \"a\": 1,\n  \"b\": 2\n}")
	for _, tt := range []struct {
		what   string
		offset int64
		want   string
	}{
		{"the first line", 1, " (line 1)"},
		{"after one newline", 5, " (line 2)"},
		{"the very end", int64(len(raw)), " (line 4)"},
		{"an offset of zero says nothing", 0, ""},
		{"nor does a negative one", -1, ""},
		{"nor one past the end", int64(len(raw)) + 1, ""},
		{"nor one far past it", 1 << 20, ""},
	} {
		t.Run(tt.what, func(t *testing.T) {
			// A panic here is the failure being guarded against, so it is
			// caught rather than left to take the whole run down with it.
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("offset %d panicked: %v", tt.offset, r)
				}
			}()
			if got := atLine(raw, tt.offset); got != tt.want {
				t.Errorf("atLine(offset %d) = %q, want %q", tt.offset, got, tt.want)
			}
		})
	}
}

func TestTheSettingAtFaultIsNamedWithoutItsPosition(t *testing.T) {
	// The decoder spells the position differently depending on which Go built
	// the plugin -- "hosts.disabled" on some, "hosts.0.disabled" on others --
	// so the index is dropped and the line number that follows says which entry
	// anyway. Every index, not just the first: nine is a digit like the rest,
	// and a config with ten machines in it is not unusual.
	for _, tt := range []struct{ in, want string }{
		{"poll_interval", "poll_interval"},
		{"hosts.disabled", "hosts.disabled"},
		{"hosts.0.disabled", "hosts.disabled"},
		{"hosts.1.target", "hosts.target"},
		{"hosts.9.target", "hosts.target"},
		{"hosts.10.target", "hosts.target"},
		{"hosts.99.mode", "hosts.mode"},
		// Nothing left to name is worse than a name with an index in it.
		{"0", "0"},
		{"", ""},
	} {
		if got := plainField(tt.in); got != tt.want {
			t.Errorf("plainField(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTheCommonestHandEditingMistakesSayWhatTheyAre(t *testing.T) {
	// The decoder describes where it stopped, not what is wrong: a comma before
	// a closing bracket comes back as "invalid character ']' looking for
	// beginning of value", which is accurate and says nothing about the comma.
	//
	// Both of these are allowed by nearly every other format and not by JSON,
	// which is exactly why they are the two people write into a config by hand.
	for _, tt := range []struct{ what, body, want, notWant string }{
		{
			what: "a comma before a closing brace",
			body: `{"hosts":[],}`,
			want: "a comma just before the }",
		},

		{
			what: "single quotes",
			body: `{'hosts':[]}`,
			want: "single quotes",
		},

		// The three below are what the guards around those two messages are
		// for. Each is a file somebody could actually save, and each lands on
		// the same code by a different route.
		{
			// Pasted out of a shell command, quotes and all. The offending
			// byte is the first one in the file, which is the edge of the
			// range check that decides whether there is a byte to look at.
			what: "the whole file wrapped in single quotes",
			body: `'{"hosts":[]}'`,
			want: "single quotes",
		},
		{
			// A closing brace with nothing before it: there is no character
			// preceding it to ask about, and asking anyway reads off the front
			// of the file.
			what: "nothing but a closing brace",
			body: `}`,
			want: "invalid character",
		},
		{
			// A missing value, not a stray comma. The decoder stops on the
			// same byte for both, and only what comes before it tells them
			// apart -- so this is the case that turns a helpful message into
			// a confidently wrong one.
			what:    "a key with no value",
			body:    `{"hosts": }`,
			want:    "invalid character",
			notWant: "a comma just before",
		},
	} {
		t.Run(tt.what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)

			_, err := Load()
			if err == nil {
				t.Fatalf("%s was accepted", tt.what)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%s reads as %q, which does not say %q", tt.what, err, tt.want)
			}
			if tt.notWant != "" && strings.Contains(err.Error(), tt.notWant) {
				t.Errorf("%s reads as %q, which says %q and should not", tt.what, err, tt.notWant)
			}
		})
	}

	// Anything else keeps the decoder's own words, which are better than a
	// guess: a message naming the wrong mistake sends somebody to the wrong
	// line, and there is no shortage of ways to write JSON wrongly.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"hosts":[{"target":"bot"}] "extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	_, err := Load()
	if err == nil {
		t.Fatal("a missing comma was accepted")
	}
	if !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("a mistake with no name of its own reads as %q, and should keep "+
			"the decoder's words rather than being guessed at", err)
	}
}

func TestAFirstRunThatCannotWriteItsConfigSaysSo(t *testing.T) {
	// With no config file, Load writes the defaults so there is something to
	// edit. That can fail -- a read-only home, a directory somebody else owns
	// -- and both halves of what happens then matter.
	//
	// The plugin has to keep working: the defaults are perfectly usable, and
	// refusing to start because a file could not be created would take the
	// menu and every action with it. And it has to say so, because otherwise
	// the config somebody then writes is one nothing ever reads back.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	// Put it back, or the temporary directory cannot be cleaned up.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)

	cfg, err := Load()

	if err == nil {
		t.Fatal("a first run that could not write its config reported success")
	}
	// Named as the config, and as a write. What comes back from replacing a
	// file names the temporary beside it, which is a file nobody has seen.
	if !strings.Contains(err.Error(), "config.json:") {
		t.Errorf("the error does not name the config file: %v", err)
	}
	if !strings.Contains(err.Error(), "write") {
		t.Errorf("the error does not say what it was doing: %v", err)
	}

	// And what came back is usable, so the daemon can run on it.
	if cfg.Interval() <= 0 || cfg.MaxMirrors <= 0 || cfg.Mode == "" {
		t.Errorf("the defaults handed back are not usable: %+v", cfg)
	}
}

func TestAConfigWrittenByAnOlderVersionKeepsTheDefaultsOfItsDay(t *testing.T) {
	// Load writes every setting at its default when there is no file, which is
	// what makes them discoverable in the file rather than only in the README.
	// The cost is recorded here rather than left to be found: a value written
	// down is a value chosen, as far as anything downstream can tell, so
	// changing a default reaches new installs and nobody else.
	//
	// It has already happened once. placement defaulted to "split" until
	// v0.4.0, where the README says a mirror that does not mirror the shape is
	// not something anybody should have to discover a setting for -- and
	// everyone who installed before that has "placement": "split" written in
	// their file by this function, so the change reached none of them.
	//
	// This is not an assertion that the behaviour is right. It is here so that
	// whoever changes a default finds out who will not get it, which is not
	// visible from Defaults() and was not visible from the comment on Load.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	written := `{"poll_interval": "2s", "mode": "ssh", "placement": "split", "hosts": []}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Placement != "split" {
		t.Errorf("a setting written in the file came back as %q; the file is what "+
			"the machine is run with, whoever wrote it", cfg.Placement)
	}
	if Defaults().Placement == "split" {
		t.Skip("the default is split again, so this no longer tells the two apart")
	}
	// The half worth knowing: it differs from the current default and nothing
	// anywhere says so. A machine set up before the change behaves one way,
	// the README describes another, and neither the menu nor `status` reports
	// a difference -- there is nothing wrong with the setting, only with how
	// it came to be there.
	if problems := cfg.Problems(); len(problems) > 0 {
		t.Errorf("a config pinning an old default is reported as faulty: %v", problems)
	}
}

func TestAFirstRunWritesNothingItWouldHaveToLiveWith(t *testing.T) {
	// Writing every setting at its default made them discoverable in the file
	// and pinned them there: nothing downstream can tell a value somebody
	// chose from a value this wrote for them, so a default improved later
	// reached new installs only. placement went from "split" to "follow" in
	// v0.4.0 and reached nobody who was already here.
	//
	// So the file records what somebody chose, which on a first run is
	// nothing.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("a first run left no config file at all: %v", err)
	}
	shape := reflect.TypeOf(Config{})
	for i := 0; i < shape.NumField(); i++ {
		name, _, _ := strings.Cut(shape.Field(i).Tag.Get("json"), ",")
		// hosts is the one thing written: it is what somebody came to the file
		// to add, and an empty list says where it goes.
		if name == "" || name == "-" || name == "hosts" {
			continue
		}
		if strings.Contains(string(written), `"`+name+`"`) {
			t.Errorf("a first run writes %s down, which pins whatever it means today:\n%s",
				name, written)
		}
	}

	// What is run has to be unchanged by any of that. Every setting absent
	// from the file takes the current default, and the four that default to on
	// are the ones this could quietly get wrong -- a bool that is missing and
	// a bool that is false are the same bool, which is why they are pointers.
	if cfg.Placement != Defaults().Placement || cfg.MaxMirrors != Defaults().MaxMirrors {
		t.Errorf("a first run is not running the defaults: placement %q, max_mirrors %d",
			cfg.Placement, cfg.MaxMirrors)
	}
	for what, on := range map[string]bool{
		"close_propagates":  cfg.ShouldClosePropagate(),
		"capture_new_panes": cfg.ShouldCaptureNewPanes(),
		"auto_start":        cfg.ShouldAutoStart(),
		"takeover":          cfg.ShouldTakeover(),
	} {
		if !on {
			t.Errorf("%s is off on a first run, having been left out of the file", what)
		}
	}

	// And reading back what was just written gives the same thing, which is
	// the half that would break if an absent setting were read as its zero.
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if again.Placement != cfg.Placement || !again.ShouldAutoStart() {
		t.Errorf("the file this wrote does not read back the same: placement %q, auto_start %v",
			again.Placement, again.ShouldAutoStart())
	}
}

func TestTheDaemonCanSayWhatItIsRunningWith(t *testing.T) {
	// The file used to answer this by holding every setting at its value. It
	// now holds what somebody chose, so "why is placement split for me" has
	// nowhere else to go -- and that question is asked precisely when a
	// setting is not doing what the README says the default is.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	written := `{"placement":"split","close_propagates":false,"hosts":[{"target":"bot"}]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	said := strings.Join(cfg.Describe(), "\n")

	// Every setting, whether or not the file mentions it: what is in force is
	// the question, and most of it will not be in the file.
	shape := reflect.TypeOf(Config{})
	for i := 0; i < shape.NumField(); i++ {
		name, _, _ := strings.Cut(shape.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		if !strings.Contains(said, "config: "+name+" = ") {
			t.Errorf("%s is not in what the daemon says it is running with:\n%s", name, said)
		}
	}

	// And which of them somebody chose, which is the whole point: a value that
	// came with the version and one written down behave the same and mean
	// completely different things when something is wrong.
	if !strings.Contains(said, `config: placement = "split" (config.json)`) {
		t.Errorf("a setting from the file is not marked as coming from it:\n%s", said)
	}
	if !strings.Contains(said, `config: mode = "ssh"`) || strings.Contains(said, `config: mode = "ssh" (config.json)`) {
		t.Errorf("a setting nobody wrote down is reported as chosen:\n%s", said)
	}
	// A setting turned off on purpose is written down as much as one turned
	// on, and the pointer is what keeps those apart.
	if !strings.Contains(said, "config: close_propagates = false (config.json)") {
		t.Errorf("a setting turned off in the file is not shown as such:\n%s", said)
	}
	// Quoted, so an empty setting is visibly empty and the two spaces in the
	// workspace formats can be counted.
	if !strings.Contains(said, `config: herdr_bin = ""`) {
		t.Errorf("an empty setting trails off instead of showing as empty:\n%s", said)
	}
}

func TestAMachineWithSettingsOfItsOwnSaysSo(t *testing.T) {
	// The count of machines answers none of the question a per-machine setting
	// raises. "Why is this one attaching when the default is ssh" is asked
	// about one machine, and the answer is one line in the file that the
	// report of the settings above it cannot contain.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	written := `{"hosts":[
		{"target":"plain"},
		{"target":"ci","mode":"attach","placement":"tab"},
		{"target":"old","disabled":true}
	]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	said := cfg.Describe()
	joined := strings.Join(said, "\n")

	if !strings.Contains(joined, `config: host "ci": mode = "attach", placement = "tab"`) {
		t.Errorf("a machine with settings of its own does not report them:\n%s", joined)
	}
	// Turned off is a setting like any other, and the one most likely to
	// explain a machine that is not there at all.
	if !strings.Contains(joined, `config: host "old": disabled = true`) {
		t.Errorf("a machine turned off in the file does not say so:\n%s", joined)
	}
	// A machine taking everything from above has nothing worth a line, and
	// most machines are that. A line each would bury the ones that differ.
	if strings.Contains(joined, `host "plain"`) {
		t.Errorf("a machine with nothing of its own takes a line anyway:\n%s", joined)
	}
	// Which machine it is, rather than something set about it.
	if strings.Contains(joined, "target = ") {
		t.Errorf("target is reported as though somebody had overridden it:\n%s", joined)
	}

	// After the settings, not sorted in among them: "host" falls between
	// herdr_bin and hosts, where it reads as another setting.
	settingsEnd := -1
	for i, line := range said {
		if strings.HasPrefix(line, "config: host ") && settingsEnd == -1 {
			settingsEnd = i
		}
		if settingsEnd != -1 && !strings.HasPrefix(line, "config: host ") {
			t.Errorf("a setting is listed after the machines, at line %d:\n%s", i, joined)
			break
		}
	}
}

func TestASettingThatSaysWhatWouldHaveHappenedAnywaySaysSo(t *testing.T) {
	// A file written by an older version holds every setting at whatever the
	// default was then, so most of its lines say what would have happened
	// without them -- until a default improves, at which point those lines are
	// exactly what stops it arriving. Thirteen of the fourteen settings in one
	// real config are this.
	//
	// From the file there is no telling those apart from the one that was
	// chosen, and this report is the one place with both to hand.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	d := Defaults()
	written := fmt.Sprintf(`{"max_mirrors": %d, "placement": "tab", `+
		`"close_propagates": false, "takeover": true, "hosts": []}`, d.MaxMirrors)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	said := strings.Join(cfg.Describe(), "\n")

	// Written, and the same as this version would have used anyway.
	if !strings.Contains(said, fmt.Sprintf("config: max_mirrors = %d (config.json, unchanged from the default)", d.MaxMirrors)) {
		t.Errorf("a setting restating the default is not marked as one:\n%s", said)
	}
	// Written, and doing something. Marked as chosen and nothing more: saying
	// "unchanged" here would be false, and this is the line somebody is
	// looking for.
	if !strings.Contains(said, `config: placement = "tab" (config.json)`) ||
		strings.Contains(said, `config: placement = "tab" (config.json, unchanged`) {
		t.Errorf("the one setting that was chosen is not shown as chosen:\n%s", said)
	}
	// The settings that can be turned off are pointers, so that leaving one
	// out and setting it to false stay different things -- and the comparison
	// has to read through that. Both sides are needed: one written to what the
	// default already is, and one written against it. Without the first, never
	// dereferencing at all passes, since nothing would be equal to a pointer.
	if !strings.Contains(said, "config: takeover = true (config.json, unchanged from the default)") {
		t.Errorf("a setting written to what it would have been anyway is not "+
			"marked as one, because it is a pointer:\n%s", said)
	}
	// A setting turned off where the default is on.
	if !strings.Contains(said, "config: close_propagates = false (config.json)") ||
		strings.Contains(said, "config: close_propagates = false (config.json, unchanged") {
		t.Errorf("a setting turned off against the default is called unchanged:\n%s", said)
	}
	// Not in the file at all: neither mark belongs, since nothing was written
	// down to be unchanged from.
	if strings.Contains(said, "config: scope = \"shared\" (") {
		t.Errorf("a setting nobody wrote down is marked as though they had:\n%s", said)
	}
}
