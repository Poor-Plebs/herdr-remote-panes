// Package config loads the user-editable plugin configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
)

// Mode selects how a remote pane is bridged into a local mirror pane.
type Mode string

const (
	// ScopeShared mirrors only this machine's own space on the remote, keeping
	// both ends identical. ScopeAll mirrors every pane the machine has.
	ScopeShared = "shared"
	ScopeAll    = "all"
)

const (
	// DefaultSessionName means the machine's own default session: the one
	// plain `herdr` opens there. Mirroring into it is the default, because a
	// dedicated session is invisible unless you know to ask for it by name.
	DefaultSessionName = "default"
)

const (
	// ModeAttach mirrors the machine's terminals with `herdr terminal attach`
	// over SSH. Experimental: it needs Herdr on the machine, is exclusive per
	// remote terminal, and pins that terminal's size.
	ModeAttach Mode = "attach"
	// ModeSSH opens plain SSH panes and is the default. The machine needs
	// nothing but an SSH login: no Herdr, no session, nothing to keep in step.
	ModeSSH Mode = "ssh"
	// ModeObserve decodes `herdr terminal session observe` frames: read-only,
	// safe to run concurrently with other viewers, and it never locks resize.
	ModeObserve Mode = "observe"
)

// Host is a single SSH target whose panes are mirrored into this session.
type Host struct {
	// Target is the SSH destination, as accepted by ssh(1) and ~/.ssh/config.
	Target string `json:"target"`
	// Label overrides the "@suffix" appended to mirrored pane names.
	// Defaults to Target.
	Label string `json:"label,omitempty"`
	// Session overrides the remote HERDR_SESSION name for this host. Empty
	// falls back to the top-level Session. Use DefaultSessionName to target
	// the remote's unnamed default session instead.
	Session string `json:"session,omitempty"`
	// Mode overrides the global mode for this host.
	Mode Mode `json:"mode,omitempty"`
	// Placement overrides the global placement for this host.
	Placement string `json:"placement,omitempty"`
	// Workspace is the local workspace label mirrors from this host land in.
	// It is created when missing. Empty falls back to the top-level Workspace.
	Workspace string `json:"workspace,omitempty"`
	// HerdrBin overrides the remote Herdr binary path for this host.
	HerdrBin string `json:"herdr_bin,omitempty"`
	// Disabled skips the host without removing it from the config.
	Disabled bool `json:"disabled,omitempty"`
}

// DisplayLabel is the suffix used when naming panes from this host.
func (h Host) DisplayLabel() string {
	// Made safe here rather than at each place it is drawn. It ends up in a
	// pane's name, in the name of the machine's space, and in the suffix those
	// are matched against -- and only the first of those was cleaning it, so a
	// stray escape in a hand-edited label reached the sidebar by the other two.
	// Doing it once also keeps the three agreeing, which is what lets a pane be
	// recognised as belonging to its machine.
	if h.Label != "" {
		return text.Sanitize(h.Label)
	}
	return text.Sanitize(h.Target)
}

// Config is the whole plugin configuration.
type Config struct {
	// ignored holds settings written with a value that cannot be used, and so
	// was replaced by the default. Kept for the same reason as unknown below:
	// silently substituting a value leaves the file saying one thing and the
	// plugin doing another, with nothing to look at.
	ignored []string
	// written holds the settings the file names, which is not the same as the
	// settings in force: everything absent takes its default, and since a
	// first run writes nothing down that is most of them. Kept so that a value
	// somebody chose can be told from one that came with the version.
	written []string
	// repeated holds the settings the file names more than once under
	// spellings that differ only in case. Kept for the same reason as ignored
	// above: one of them is running and the rest are dead text, and the file
	// gives a reader no way to tell which.
	repeated []string
	// unknown holds settings read from the file that mean nothing here. Not a
	// setting itself, so it is neither read from nor written back to JSON.
	unknown []string
	// dropped names the machines in the file that could not be used, in the
	// words of the file rather than the index of a slice nobody can see.
	dropped []string

	// PollInterval is how often each host is polled for pane changes.
	PollInterval string `json:"poll_interval,omitempty"`
	// Session is the Herdr session mirrored on each machine. It defaults to
	// the machine's own default session, so plain `herdr` there shows the
	// shared terminals. Name a session to keep them separate instead.
	Session string `json:"session,omitempty"`
	// Mode is how a machine's terminals are reached, for hosts that do not
	// override it. Defaults to plain SSH panes; mirroring is opt-in.
	Mode Mode `json:"mode,omitempty"`
	// Placement is the default plugin pane placement: split, tab or zoomed.
	Placement string `json:"placement,omitempty"`
	// LabelFormat builds the mirrored pane name. {name} is the remote pane's
	// own label or terminal title, {host} is the host's display label.
	LabelFormat string `json:"label_format,omitempty"`
	// HerdrBin is the remote Herdr binary path used for hosts that do not
	// override it. Empty probes the usual install locations, which is needed
	// because `ssh host <command>` does not run a login shell.
	HerdrBin string `json:"herdr_bin,omitempty"`
	// Workspace is the local workspace label every host's mirrors share. Empty
	// gives each host its own workspace named by WorkspaceFormat.
	Workspace string `json:"workspace,omitempty"`
	// WorkspaceFormat names a host's own workspace and marks it as remote, so
	// it is distinguishable from local workspaces in the sidebar. {host} is the
	// host's display label. A shared Workspace is used verbatim instead.
	//
	// Two spaces after the glyph: a cloud is ambiguous-width, so terminals that
	// draw it in two cells would otherwise crowd the name.
	WorkspaceFormat string `json:"workspace_format,omitempty"`
	// Takeover evicts a stale remote attach when mirroring in attach mode. A
	// direct attach is exclusive and can outlive the pane that started it, so
	// without this a killed mirror blocks its terminal until that process dies.
	Takeover *bool `json:"takeover,omitempty"`
	// WorkspaceFormatDown names a machine's space while it cannot be reached.
	// Herdr joins sidebar tokens with " · ", so a separate marker token always
	// sits a dot away from the name; carrying the marker in the name itself is
	// the only way to have it directly beside it.
	WorkspaceFormatDown string `json:"workspace_format_down,omitempty"`
	// RemoteWorkspaceFormat names the workspace this plugin creates on the
	// remote machine. {hub} is this machine's hostname, so that sitting on the
	// remote you can tell which machine those panes are shared with.
	RemoteWorkspaceFormat string `json:"remote_workspace_format,omitempty"`
	// Scope decides which of a machine's terminals are shared. "shared" mirrors
	// only the space this plugin owns there, so both ends always show the same
	// tabs in the same order. "all" mirrors every pane on the machine, which
	// also surfaces work started there but makes the two sides differ.
	Scope string `json:"scope,omitempty"`
	// ClosePropagates closes the terminal on the machine when its mirror is
	// closed here. Mirroring is otherwise two-way for everything except
	// closing, which is surprising: the tab goes but the work carries on.
	ClosePropagates *bool `json:"close_propagates,omitempty"`
	// CaptureNewPanes replaces a local pane opened inside a machine's space
	// with a terminal on that machine. Herdr's new-tab keybinding and the plus
	// icon in the tab bar both create a local shell, and neither can be
	// intercepted by a plugin, so they are corrected after the fact.
	CaptureNewPanes *bool `json:"capture_new_panes,omitempty"`
	// AutoStart launches the remote Herdr session when it is not running, so
	// a host only needs the herdr binary installed and reachable over SSH.
	AutoStart *bool `json:"auto_start,omitempty"`
	// MaxMirrors caps how many panes one host may mirror, so a remote with a
	// runaway pane count cannot flood the local session.
	MaxMirrors int `json:"max_mirrors,omitempty"`
	// Hosts are the SSH targets to mirror.
	Hosts []Host `json:"hosts"`
}

// Defaults returns a configuration with every optional field populated.
func Defaults() Config {
	return Config{
		PollInterval:          "2s",
		Session:               DefaultSessionName,
		Mode:                  ModeSSH,
		Scope:                 ScopeShared,
		Placement:             "follow",
		LabelFormat:           "{name}@{host}",
		WorkspaceFormat:       "☁  {host}",
		WorkspaceFormatDown:   "⚠  {host}",
		RemoteWorkspaceFormat: "☁  {hub}",
		CaptureNewPanes:       boolPtr(true),
		ClosePropagates:       boolPtr(true),
		Takeover:              boolPtr(true),
		AutoStart:             boolPtr(true),
		MaxMirrors:            32,
		Hosts:                 []Host{},
	}
}

// Path returns the config file location inside the plugin config directory.
func Path() (string, error) {
	dir := os.Getenv("HERDR_PLUGIN_CONFIG_DIR")
	if dir == "" {
		return "", errors.New("HERDR_PLUGIN_CONFIG_DIR is not set; run this through Herdr")
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config file, writing one out when it is absent.
//
// What it writes is every setting at its default, which is what makes them
// discoverable in the file rather than only in the README -- and which pins
// them: a value written down is a value chosen as far as anything here can
// tell, so a default improved in a later version reaches new installs only.
// TestAConfigWrittenByAnOlderVersionKeepsTheDefaultsOfItsDay is where that is
// spelled out.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}

	raw, err := readConfigFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		cfg := Defaults()
		// An empty configuration rather than the defaults written out. What is
		// run is the same either way -- everything absent is filled from
		// Defaults on the way in -- but what is on disk is the difference
		// between a file that records what somebody chose and one that records
		// what this version happened to think in the month they installed it.
		//
		// Nothing downstream can tell those apart, so writing them pins them:
		// placement defaulted to "split" until v0.4.0, and everyone who
		// installed before that kept it through the change that existed to
		// take it away from them.
		//
		// Files already written are left alone. This is not something to
		// correct on somebody's behalf -- a value in the file may well be one
		// they chose -- so it fixes what happens next rather than what
		// happened.
		if err := saveRaw(Config{Hosts: []Host{}}); err != nil {
			return cfg, err
		}
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	// A byte-order mark, which several editors write and JSON does not allow.
	// Dropped rather than reported: the decoder's complaint names a character
	// that does not appear anywhere in the file as the file's author sees it,
	// and there is nothing to fix in a file that is otherwise correct. Dropped
	// here, before anything reads offsets out of raw, so the line numbers in
	// any later complaint still match the file.
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))

	cfg := Defaults()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, &ParseError{Path: path, Detail: describeJSONError(raw, err)}
	}
	cfg = cfg.normalized()
	cfg.unknown = unknownKeys(raw)
	cfg.ignored = ignoredValues(raw)
	cfg.repeated = repeatedSettings(raw)
	cfg.written = writtenKeys(raw)
	return cfg, nil
}

// ParseError is a config file that could not be read, holding which file it
// was and what is wrong with it separately.
//
// They are kept apart because the two are worth different amounts depending on
// where the error is shown. In the daemon's log the path matters -- it says
// which of several files to open. In the menu there is room for two lines and
// only ever one plugin config, so a full path spends that room saying something
// the reader already knows, and pushes the part that says what to fix off the
// end.
type ParseError struct {
	Path   string
	Detail string
}

func (e *ParseError) Error() string { return e.Path + ": " + e.Detail }

// describeJSONError says what is wrong with the file in the terms somebody
// editing it is thinking in.
//
// The decoder's own wording is accurate and about Go: "cannot unmarshal string
// into Go struct field Config.max_mirrors of type int". That sentence ends up
// in the menu and in the status listing, where nobody is thinking about Go
// structs -- they are looking at a file they just edited and want to know which
// line to change.
func describeJSONError(raw []byte, err error) string {
	// "unexpected end of JSON input" is what the decoder says about a file with
	// nothing in it, and it reads as though something was cut off partway. An
	// empty config is a state a file gets into by itself -- a truncated write,
	// a redirect that clobbered it -- and the way out is worth saying, since
	// the plugin writes a fresh one whenever the file is missing entirely.
	if len(bytes.TrimSpace(raw)) == 0 {
		// Not "with the defaults in it". Load writes `{"hosts": []}` for a
		// missing file and deliberately not the defaults -- the comment there
		// explains why at length: a file recording what this version happened
		// to think pins those values for good. So somebody who followed this
		// advice deleted the file, opened the fresh one to find the defaults
		// laid out to edit, and found an empty object instead, which reads
		// like the write having failed.
		//
		// Measured: after deleting, the file holds `{"hosts": []}` while
		// mode=ssh, placement=follow and max_mirrors=32 are all in force.
		return "the file is empty; delete it and a fresh one will be written, holding " +
			"nothing: every setting takes its default until you write one down"
	}

	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return fmt.Sprintf("%s should be %s, not %s%s",
			plainField(typeErr.Field), plainType(typeErr.Type), plainValue(typeErr.Value),
			atLine(raw, typeErr.Offset))
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return plainSyntax(raw, syntaxErr)
	}
	return err.Error()
}

// plainSyntax names the two mistakes a hand-edited file usually has.
//
// The decoder describes where it stopped rather than what is wrong: a comma
// before a closing bracket comes back as "invalid character ']' looking for
// beginning of value", which is accurate and tells somebody nothing about the
// comma they left behind. Both of these are things nearly every other format
// allows and JSON does not, which is exactly why they are the ones people write.
func plainSyntax(raw []byte, err *json.SyntaxError) string {
	fallback := fmt.Sprintf("%s%s", err, atLine(raw, err.Offset))

	// Offset is how much had been read when the decoder stopped, so the byte it
	// stopped on is the one before it.
	at := int(err.Offset) - 1
	if at < 0 || at >= len(raw) {
		return fallback
	}
	switch raw[at] {
	case ']', '}':
		before := bytes.TrimRight(raw[:at], " \t\r\n")
		if len(before) > 0 && before[len(before)-1] == ',' {
			return fmt.Sprintf("a comma just before the %c, which JSON does not allow%s",
				raw[at], atLine(raw, err.Offset))
		}
	case '\'':
		return "single quotes, which JSON does not allow — use double quotes" +
			atLine(raw, err.Offset)
	}
	return fallback
}

// plainField names the setting at fault, without the position of the machine
// it is in.
//
// The decoder spells that position differently depending on which Go built the
// plugin -- "hosts.disabled" on some, "hosts.0.disabled" on others -- and this
// is built from source on whatever toolchain the machine has, so leaving it as
// given makes the same broken file produce different wording on two machines.
// The index is no loss: it counts from zero, which does not match how anyone
// reads the file, and the line number that follows says exactly which entry
// anyway.
func plainField(field string) string {
	parts := strings.Split(field, ".")
	kept := parts[:0]
	for _, part := range parts {
		if part == "" || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		return field
	}
	return strings.Join(kept, ".")
}

// atLine says which line an offset falls on, since that is what an editor shows.
func atLine(raw []byte, offset int64) string {
	if offset <= 0 || int(offset) > len(raw) {
		return ""
	}
	return fmt.Sprintf(" (line %d)", 1+bytes.Count(raw[:offset], []byte("\n")))
}

// plainType names a Go type the way the setting reads in the file.
func plainType(t reflect.Type) string {
	if t == nil {
		return "something else"
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "text"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Map, reflect.Struct:
		return "a set of settings"
	default:
		return t.String()
	}
}

// plainValue names what the decoder found, which it reports as a JSON word.
func plainValue(found string) string {
	switch found {
	case "string":
		return "text"
	case "number":
		return "a number"
	case "bool":
		return "true or false"
	case "array":
		return "a list"
	case "object":
		return "a set of settings"
	default:
		return found
	}
}

// ignoredValues lists settings written with a value this cannot use.
//
// A cap of zero or less is not a cap, so normalizing puts the default back --
// and somebody who wrote 0 meaning "no limit" then meets "at the mirror limit,
// raise max_mirrors" with a file in front of them that already says 0. The
// substitution is right; doing it in silence is not.
//
// Read from the raw file because by the time anything can ask, the value has
// already been replaced: an absent setting and one written as 0 are the same
// zero in the struct.
func ignoredValues(raw []byte) []string {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	written, ok := top["max_mirrors"]
	if !ok {
		return nil
	}
	var cap int
	if json.Unmarshal(written, &cap) != nil || cap > 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"max_mirrors is %d, which is not a cap on anything; mirroring stays capped at %d",
		cap, Defaults().MaxMirrors)}
}

// repeatedSettings lists settings the file names more than once.
//
// Two keys are one setting when they differ only in case, because the decoder
// takes an exact match if there is one and folds otherwise -- so a file saying
// both "mode" and "Mode" has written one setting twice, and only one of them
// can be in force. Which one is decided by the order they appear in the file:
// measured both ways round, the later wins, and it wins whether or not it is
// the exact spelling.
//
// Nothing said anything about it. unknownKeys is quiet because both spellings
// ARE known, ignoredValues because both values are usable, and Describe prints
// the setting once with the value that won -- so the line that lost looks, to
// somebody reading their own file, exactly like the line that is running.
//
// Only spellings that DIFFER can be seen from here. A key written twice
// byte-for-byte collapses in the map this parse produces, and finding that
// would mean reading the document as a stream of tokens rather than a map.
// Only the top level, too: a machine entry can name the same setting twice and
// the reasoning is the same, but the complaint would have to say which machine,
// which is a different sentence rather than a longer one.
func repeatedSettings(raw []byte) []string {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	known := jsonNames(reflect.TypeOf(Config{}))
	spellings := map[string][]string{}
	for name := range top {
		field, ok := fieldFor(known, name)
		if !ok {
			// Not a setting at all, which unknownKeys is the one to say.
			continue
		}
		spellings[field] = append(spellings[field], name)
	}

	var out []string
	for field, names := range spellings {
		if len(names) < 2 {
			continue
		}
		// Sorted so the sentence reads the same twice running: these come out
		// of a map, and the file's own order is not what this can see.
		sort.Strings(names)
		quoted := make([]string, len(names))
		for i, name := range names {
			quoted[i] = fmt.Sprintf("%q", name)
		}
		out = append(out, fmt.Sprintf(
			"%s is written %d times, as %s and %s; those are one setting here, so the "+
				"last of them in the file is what runs and the others do nothing",
			field, len(names),
			strings.Join(quoted[:len(quoted)-1], ", "), quoted[len(quoted)-1]))
	}
	sort.Strings(out)
	return out
}

// writtenKeys is the settings the file names that this version knows about.
//
// The other half of unknownKeys, from the same parse: that one reports what was
// written and means nothing, this one what was written and means something.
// Which matters because the file no longer says what is in force -- it holds
// what somebody chose, and everything else takes its default.
func writtenKeys(raw []byte) []string {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	known := jsonNames(reflect.TypeOf(Config{}))
	var out []string
	seen := map[string]bool{}
	for name := range top {
		// The canonical name, so that a file spelling it "Mode" still marks
		// mode as chosen: Describe looks these up by the field's own name.
		field, ok := fieldFor(known, name)
		if !ok || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

// Describe is every setting and what it is set to, for the daemon's log.
//
// The file used to be the answer to "what is this running with", holding every
// setting at its value. It no longer does -- it holds what somebody chose --
// so something else has to, and the log is where the troubleshooting page
// already sends people.
func (c Config) Describe() []string {
	fromFile := map[string]bool{}
	for _, name := range c.written {
		fromFile[name] = true
	}

	shape := reflect.TypeOf(c)
	value := reflect.ValueOf(c)
	// What this version would use for anything the file leaves out, so a
	// setting written down can be told from one that is doing something.
	fallback := reflect.ValueOf(Defaults())
	out := make([]string, 0, shape.NumField())
	for i := 0; i < shape.NumField(); i++ {
		name, _, _ := strings.Cut(shape.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		field := value.Field(i)
		if name == "hosts" {
			// A list of machines rather than a setting, and the machines
			// themselves are reported one by one as they are connected to.
			out = append(out, fmt.Sprintf("config: %s = %d", name, field.Len()))
			continue
		}
		// A pointer is a setting that can be turned off explicitly, so that
		// leaving it out and setting it to false stay different things.
		if field.Kind() == reflect.Ptr {
			if field.IsNil() {
				continue
			}
			field = field.Elem()
		}
		// Quoted, so that a setting left empty is visibly empty rather than a
		// line that trails off, and so that the two spaces in the workspace
		// formats are countable -- they are there on purpose, and a report
		// nobody can check the spacing in is no use for the one question these
		// get asked.
		shown := fmt.Sprintf("%v", field.Interface())
		if field.Kind() == reflect.String {
			shown = strconv.Quote(field.String())
		}
		line := fmt.Sprintf("config: %s = %s", name, shown)
		if fromFile[name] {
			// Which of them somebody chose is the question being asked when a
			// setting is not doing what the README says it does.
			line += " (config.json"
			// And whether choosing it made any difference. A file written by
			// an older version holds every setting at whatever the default was
			// then, so most of its lines say what would have happened anyway --
			// until a default improves, at which point those lines are what
			// stops it arriving. Thirteen of the fourteen in one real config
			// are this.
			//
			// Worth saying here rather than left to be worked out: from the
			// file there is no telling the two apart, and this is the one
			// place that has both to hand.
			if same(field, fallback.Field(i)) {
				line += ", unchanged from the default"
			}
			line += ")"
		}
		out = append(out, line)
	}
	sort.Strings(out)

	// The machines that are not simply taking the settings above. A per-machine
	// override is where "why is this one attaching when the default is ssh"
	// comes from, and the count on its own answers none of it.
	//
	// After the settings and in the order the file lists them, which is the
	// order somebody wrote them in and the order the daemon reports them in.
	// Sorted with the rest, "host" would land between herdr_bin and hosts and
	// read as another setting.
	for _, h := range c.Hosts {
		if set := h.overrides(c); len(set) > 0 {
			out = append(out, fmt.Sprintf("config: host %s: %s",
				strconv.Quote(h.Target), strings.Join(set, ", ")))
		}
	}
	return out
}

// same reports whether a setting holds what this version would have used
// anyway. Both are read through the pointer where there is one, so a setting
// written as false and a default of false are the same answer.
func same(written, fallback reflect.Value) bool {
	if fallback.Kind() == reflect.Ptr {
		if fallback.IsNil() {
			return false
		}
		fallback = fallback.Elem()
	}
	if written.Kind() != fallback.Kind() {
		return false
	}
	return written.Interface() == fallback.Interface()
}

// overrides is what a machine says for itself, rather than taking the setting
// above it. Only what is set: a machine with nothing of its own has nothing
// worth a line, and most of them have nothing of their own.
//
// Whether it is saying anything different is the other half. An override that
// repeats the setting above is exactly as much of a nothing as a top-level
// setting that repeats the default -- which the lines above already say -- and
// it is the same thing that keeps a default from arriving when it improves.
// One real config has a machine set to the mode it would have had anyway.
func (h Host) overrides(parent Config) []string {
	shape := reflect.TypeOf(h)
	value := reflect.ValueOf(h)
	var out []string
	for i := 0; i < shape.NumField(); i++ {
		name, _, _ := strings.Cut(shape.Field(i).Tag.Get("json"), ",")
		// target is which machine this is rather than something set about it,
		// and it is already in the line.
		if name == "" || name == "-" || name == "target" {
			continue
		}
		field := value.Field(i)
		if field.IsZero() {
			continue
		}
		shown := fmt.Sprintf("%v", field.Interface())
		if field.Kind() == reflect.String {
			shown = strconv.Quote(field.String())
		}
		line := fmt.Sprintf("%s = %s", name, shown)
		if above := reflect.ValueOf(parent).FieldByName(shape.Field(i).Name); above.IsValid() &&
			same(field, above) {
			line += " (same as the setting above)"
		}
		out = append(out, line)
	}
	return out
}

// unknownKeys lists settings in the file that mean nothing here.
//
// Anything not recognised is dropped in silence by the decoder, so a setting
// spelled wrong, or a per-machine one written at the top level, looks exactly
// like one that is being obeyed. That is the same trouble as a value spelled
// wrong, which is already reported.
func unknownKeys(raw []byte) []string {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}

	var out []string
	known := jsonNames(reflect.TypeOf(Config{}))
	for name := range top {
		if _, ok := fieldFor(known, name); !ok {
			out = append(out, name)
		}
	}

	if hosts, ok := top["hosts"]; ok {
		var entries []map[string]json.RawMessage
		if json.Unmarshal(hosts, &entries) == nil {
			knownHost := jsonNames(reflect.TypeOf(Host{}))
			seen := map[string]bool{}
			for _, entry := range entries {
				for name := range entry {
					if _, ok := fieldFor(knownHost, name); ok {
						continue
					}
					if !seen[name] {
						seen[name] = true
						out = append(out, "hosts[]."+name)
					}
				}
			}
		}
	}

	sort.Strings(out)
	return out
}

// fieldFor returns the setting a key from the file names, and whether it names
// one at all.
//
// encoding/json prefers an exact match and otherwise takes any field whose name
// differs only in case, so "Mode" and "MODE" both set mode. Matching only
// exactly made this disagree with the decoder in both directions at once: the
// key was reported as "not a setting and is being ignored" while its value was
// in force, and Describe left the file off the line, so a setting the file
// chose looked like one it never mentioned. Measured before the fix:
// {"Mode":"attach"} loaded as mode=attach, was called ignored, and was logged
// as `mode = "attach"` with no source.
//
// The canonical name comes back rather than the spelling in the file, because
// that is what the rest of this compares against.
func fieldFor(known map[string]bool, name string) (string, bool) {
	if known[name] {
		return name, true
	}
	for candidate := range known {
		if strings.EqualFold(candidate, name) {
			return candidate, true
		}
	}
	return "", false
}

// jsonNames is the set of field names a struct accepts from JSON.
func jsonNames(t reflect.Type) map[string]bool {
	names := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		names[strings.Split(tag, ",")[0]] = true
	}
	return names
}

// Save writes the config back to disk.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return saveRaw(cfg.normalized())
}

// saveRaw writes a configuration exactly as given, without filling anything in.
func saveRaw(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomically(path, append(raw, '\n')); err != nil {
		// Named, and said as a write. The file is replaced rather than written
		// through, so what comes back names the temporary alongside it --
		// "config.json.139785810: permission denied" -- which is a file the
		// person reading it has never seen and cannot look for.
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// readConfigFile reads the settings file, refusing anything that is not a
// regular one.
//
// os.ReadFile on a named pipe waits for somebody to write to it, and on a
// terminal for somebody to type. The daemon reads this file on a timer, so
// either is the whole daemon stopped -- no menu, no reconcile, and nothing
// anywhere saying why. The same hazard was found in the SSH config, which is
// read to draw the menu; this one is worse because of how often it is read.
//
// An error rather than silence: a daemon whose configuration could not be read
// already says so, in the menu, and that is a better answer than defaults
// nobody chose.
func readConfigFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file, so it is not read", path)
	}
	return os.ReadFile(path)
}

// loadRaw reads the file as written, without filling in defaults.
//
// Changing one setting used to go through Load and Save, both of which fill in
// what is missing, so toggling mirroring wrote back every setting somebody had
// left out, pinned to whatever it defaulted to that day. Nothing changed at the
// time; it did mean those settings quietly stopped following the default.
func loadRaw() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	raw, err := readConfigFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, &ParseError{Path: path, Detail: describeJSONError(raw, err)}
	}
	// Carried like Load does. Without it, toggling mirroring handed the daemon
	// a configuration that had forgotten which of its settings mean nothing,
	// and the warning about them vanished until Herdr was restarted -- with the
	// file unchanged and still wrong.
	cfg.unknown = unknownKeys(raw)
	cfg.ignored = ignoredValues(raw)
	cfg.repeated = repeatedSettings(raw)
	cfg.written = writtenKeys(raw)
	return cfg, nil
}

// writeFileAtomically replaces a file's contents in one step.
//
// Writing in place truncates first, so an interruption — a crash, a full disk,
// the machine losing power — leaves the file empty or half written. This file
// is edited by hand and holds the list of machines, and it is rewritten
// whenever mirroring is toggled from the menu, so losing it that way is a real
// prospect rather than a theoretical one.
func writeFileAtomically(path string, data []byte) error {
	// A config symlinked into a dotfiles repo has to be written through rather
	// than replaced: renaming onto the link itself would swap it for a regular
	// file and quietly detach it from the repo it belongs to. Resolving first
	// also keeps the temporary file on the same filesystem as its target, which
	// is what lets the rename be atomic at all.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}

	// Keep whatever permissions the file already had; only a file being created
	// for the first time gets the private default.
	perm := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}

	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName) // No-op once the rename below succeeds.

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	// Flush to disk before the rename, so a power loss cannot leave the new
	// name pointing at a file whose contents never arrived.
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempName, perm); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func (c Config) normalized() Config {
	d := Defaults()
	if c.PollInterval == "" {
		c.PollInterval = d.PollInterval
	}
	if c.Session == "" {
		c.Session = d.Session
	}
	if c.Mode == "" {
		c.Mode = d.Mode
	}
	if c.Placement == "" {
		c.Placement = d.Placement
	}
	if c.LabelFormat == "" {
		c.LabelFormat = d.LabelFormat
	}
	if c.WorkspaceFormat == "" {
		c.WorkspaceFormat = d.WorkspaceFormat
	}
	if c.WorkspaceFormatDown == "" {
		c.WorkspaceFormatDown = d.WorkspaceFormatDown
	}
	if c.RemoteWorkspaceFormat == "" {
		c.RemoteWorkspaceFormat = d.RemoteWorkspaceFormat
	}
	if c.Scope == "" {
		c.Scope = d.Scope
	}
	if c.CaptureNewPanes == nil {
		c.CaptureNewPanes = d.CaptureNewPanes
	}
	if c.ClosePropagates == nil {
		c.ClosePropagates = d.ClosePropagates
	}
	if c.Takeover == nil {
		c.Takeover = d.Takeover
	}
	if c.AutoStart == nil {
		c.AutoStart = d.AutoStart
	}
	if c.MaxMirrors <= 0 {
		c.MaxMirrors = d.MaxMirrors
	}
	// A host with no target cannot be reached, and leaving it in produces a
	// space named after nothing and an ssh command with no destination. Which
	// ones went is remembered: dropping an entry somebody deliberately wrote
	// and saying nothing is how a mistyped "targt" turns into a machine that
	// is simply missing from the menu, with nowhere to look for why.
	// A new slice, not a filter in place. Config is taken by value but its
	// Hosts share the caller's backing array, so compacting into it rewrites
	// what the caller is still holding -- and the caller here is whoever asked
	// for a normalized copy, which is not the same as asking for theirs to be
	// changed.
	kept := make([]Host, 0, len(c.Hosts))
	for i, host := range c.Hosts {
		if host.Target != "" {
			kept = append(kept, host)
			continue
		}
		c.dropped = append(c.dropped, describeDropped(i, host))
	}
	c.Hosts = kept
	if c.Hosts == nil {
		c.Hosts = []Host{}
	}
	return c
}

// describeDropped points at a machine entry in terms someone can find it by.
func describeDropped(index int, h Host) string {
	// Its label if it has one -- that is what its author would recognise --
	// and otherwise its position, counted the way the file reads.
	if h.Label != "" {
		return fmt.Sprintf("the machine labelled %q has no target and is ignored", h.Label)
	}
	return fmt.Sprintf("machine %d under hosts has no target and is ignored", index+1)
}

// MinPollInterval is the fastest poll a machine could be asked to keep up with
// over ssh. Below it the default is used instead.
//
// Named so that the clamp and the warning about it cannot drift: a message
// quoting a number written down beside the check is a message that goes stale
// the first time the check moves.
const MinPollInterval = 500 * time.Millisecond

// Interval parses PollInterval, clamping it to something a remote can sustain.
func (c Config) Interval() time.Duration {
	d, err := time.ParseDuration(c.PollInterval)
	if err != nil || d < MinPollInterval {
		return 2 * time.Second
	}
	return d
}

// SessionFor resolves which Herdr session on a machine is mirrored.
//
// The machine's own default session is addressed with an empty HERDR_SESSION,
// so DefaultSessionName maps to "". Naming any other session keeps the shared
// terminals separate from the machine's own work, at the cost of having to run
// `herdr --session <name>` there to see them.
func (c Config) SessionFor(h Host) string {
	name := h.Session
	if name == "" {
		name = c.Session
	}
	if name == DefaultSessionName {
		return ""
	}
	return name
}

func boolPtr(b bool) *bool { return &b }

// SharedOnly reports whether only this machine's own space is mirrored.
func (c Config) SharedOnly() bool {
	return c.Scope != ScopeAll
}

// ShouldClosePropagate reports whether closing a mirror closes the terminal on
// the machine too.
func (c Config) ShouldClosePropagate() bool {
	return c.ClosePropagates == nil || *c.ClosePropagates
}

// ShouldCaptureNewPanes reports whether a local pane opened in a machine's
// space should be replaced with a terminal on that machine.
func (c Config) ShouldCaptureNewPanes() bool {
	return c.CaptureNewPanes == nil || *c.CaptureNewPanes
}

// ShouldTakeover reports whether a stale remote attach may be evicted.
func (c Config) ShouldTakeover() bool {
	return c.Takeover == nil || *c.Takeover
}

// ShouldAutoStart reports whether a missing remote session may be started.
func (c Config) ShouldAutoStart() bool {
	return c.AutoStart == nil || *c.AutoStart
}

// WorkspaceFor resolves the local workspace label a host's mirrors belong in.
//
// Per-host wins, then a shared top-level workspace, then the host's own name.
// Pointing several hosts at one label puts panes from different machines in a
// single layout; the default keeps each machine separate.
func (c Config) WorkspaceFor(h Host) string {
	if h.Workspace != "" {
		return h.Workspace
	}
	if c.Workspace != "" {
		return c.Workspace
	}
	return strings.ReplaceAll(c.WorkspaceFormat, "{host}", h.DisplayLabel())
}

// SharesWorkspace reports whether a machine's terminals land in a space named
// outright rather than one of its own.
//
// Such a space can hold several machines at once, so nothing about one
// machine's state belongs on it: two machines in different states would each
// mark it as their own, every couple of seconds, for as long as both were
// connected.
func (c Config) SharesWorkspace(h Host) bool {
	return h.Workspace != "" || c.Workspace != ""
}

// WorkspaceLabelFor names a host's space for its current reachability.
func (c Config) WorkspaceLabelFor(h Host, reachable bool) string {
	if h.Workspace != "" || c.Workspace != "" {
		// An explicitly chosen name is used as given, marker and all.
		return c.WorkspaceFor(h)
	}
	format := c.WorkspaceFormat
	if !reachable {
		format = c.WorkspaceFormatDown
	}
	return strings.ReplaceAll(format, "{host}", h.DisplayLabel())
}

// RemoteWorkspaceLabel is the workspace label used on the remote machine.
func (c Config) RemoteWorkspaceLabel() string {
	return strings.ReplaceAll(c.RemoteWorkspaceFormat, "{hub}", HubName())
}

// HubName is what this machine is called in the space it creates on another
// one. Exposed because finding that space again needs the name without
// whatever the format puts around it.
func HubName() string {
	return hubName(os.Hostname())
}

// hubName is HubName with the answer handed to it, since os.Hostname cannot be
// made to fail or to come back empty from a test -- and coming back empty is
// the case worth having an answer for. A machine that does not know its own
// name would otherwise name the space it creates on every other machine after
// nothing, and none of those spaces could be told apart.
func hubName(hostname string, err error) string {
	if err != nil || hostname == "" {
		return "herdr"
	}
	return hostname
}

// EffectiveMode is how a machine is actually reached.
//
// A mode that is not recognised falls back to a plain SSH terminal rather than
// mirroring. Treating anything that is not "ssh" as mirroring meant a mode
// spelled wrong silently turned on the experimental feature, which is the
// opposite of what someone typing a typo wants.
func (c Config) EffectiveMode(h Host) Mode {
	switch mode := c.ModeFor(h); mode {
	case ModeAttach, ModeObserve, ModeSSH:
		return mode
	default:
		return ModeSSH
	}
}

// Mirrors reports whether a host's terminals are kept in step with the
// machine, rather than being a plain SSH session.
func (c Config) Mirrors(h Host) bool {
	return c.EffectiveMode(h) != ModeSSH
}

// SetHostMode records a machine's mode on disk, adding the host when it is not
// configured yet, and returns the updated configuration.
func SetHostMode(target string, mode Mode) (Config, error) {
	// The file as written, so that changing one setting does not write back
	// every other one that was being left to its default.
	cfg, err := loadRaw()
	if errors.Is(err, fs.ErrNotExist) {
		// No file yet: Load writes one from the defaults, which is the only
		// time filling them in is what somebody wants.
		if cfg, err = Load(); err != nil {
			return cfg, err
		}
	} else if err != nil {
		return Config{}, err
	}

	found := false
	for i := range cfg.Hosts {
		if cfg.Hosts[i].Target == target {
			cfg.Hosts[i].Mode = mode
			found = true
			break
		}
	}
	if !found {
		cfg.Hosts = append(cfg.Hosts, Host{Target: target, Mode: mode})
	}

	if err := saveRaw(cfg); err != nil {
		return Config{}, err
	}
	// The caller runs on this, so it gets the filled-in one; the file keeps
	// what it had.
	return cfg.normalized(), nil
}

// BinFor resolves the remote Herdr binary for a host. An empty result means
// the path is probed on the remote machine.
func (c Config) BinFor(h Host) string {
	if h.HerdrBin != "" {
		return h.HerdrBin
	}
	return c.HerdrBin
}

// ModeFor resolves the effective bridge mode for a host.
func (c Config) ModeFor(h Host) Mode {
	if h.Mode != "" {
		return h.Mode
	}
	return c.Mode
}

// PlacementFor resolves the effective pane placement for a host.
func (c Config) PlacementFor(h Host) string {
	if h.Placement != "" {
		return h.Placement
	}
	return c.Placement
}
