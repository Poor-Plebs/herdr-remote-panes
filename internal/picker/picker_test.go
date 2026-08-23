package picker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectListsSSHConfigMachines(t *testing.T) {
	// The menu must offer machines from ~/.ssh/config even when the plugin has
	// never been configured, since that is how a machine is connected to the
	// first time.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"),
		[]byte("Host bot\n  HostName 1.2.3.4\n\nHost prod\n  HostName 5.6.7.8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())

	entries, warning := collect()
	if warning != "" {
		t.Errorf("unexpected warning: %s", warning)
	}
	found := map[string]bool{}
	for _, e := range entries {
		found[e.Target] = true
	}
	for _, want := range []string{"bot", "prod"} {
		if !found[want] {
			t.Errorf("%q missing from the menu: %+v", want, entries)
		}
	}
}

func TestCollectWarnsRatherThanDroppingMachines(t *testing.T) {
	// Falling back silently would leave machines that are only in the plugin
	// config missing from the menu, which reads as the plugin forgetting them.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", "")

	_, warning := collect()
	if warning == "" {
		t.Error("an unreadable config should be reported, not swallowed")
	}
}

func TestCollectPutsConfiguredMachinesFirst(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	// "aaa" sorts first alphabetically but is not configured, so a configured
	// machine should still lead: those are the ones being worked on.
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"),
		[]byte("Host aaa\nHost bot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"hosts":[{"target":"bot"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, _ := collect()
	if len(entries) == 0 || entries[0].Target != "bot" {
		t.Errorf("configured machine should lead, got %+v", entries)
	}
	// And a machine must never be listed twice for being in both places.
	seen := map[string]int{}
	for _, e := range entries {
		seen[e.Target]++
	}
	if seen["bot"] != 1 {
		t.Errorf("bot listed %d times, want once", seen["bot"])
	}
}

func TestPlanLayout(t *testing.T) {
	tests := []struct {
		name                  string
		count, selected, rows int
		wantFirst, wantLast   int
	}{
		{
			name:  "everything fits",
			count: 6, selected: 0, rows: 20, wantFirst: 0, wantLast: 6,
		},
		{
			// Writing more lines than the popup has scrolls the top away,
			// taking the first machine and the heading with it. This is what
			// made the menu appear to start at "2.".
			name:  "a short popup shows a window, not an overflowing list",
			count: 6, selected: 0, rows: 8, wantFirst: 0, wantLast: 2,
		},
		{
			name:  "the window follows the selection",
			count: 10, selected: 8, rows: 8, wantFirst: 7, wantLast: 9,
		},
		{
			name:  "the window never runs past the end",
			count: 10, selected: 9, rows: 8, wantFirst: 8, wantLast: 10,
		},
		{
			// Even absurdly small popups must show the selected machine
			// rather than nothing at all.
			name:  "a tiny popup still shows one machine",
			count: 6, selected: 3, rows: 2, wantFirst: 3, wantLast: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := planLayout(tt.count, tt.selected, tt.rows, 0)
			first, last := frame.first, frame.last
			if first != tt.wantFirst || last != tt.wantLast {
				t.Errorf("planLayout(%d, %d, %d) = %d..%d, want %d..%d",
					tt.count, tt.selected, tt.rows, first, last, tt.wantFirst, tt.wantLast)
			}
			if tt.selected < tt.count && (tt.selected < first || tt.selected >= last) {
				t.Errorf("selected %d is outside the window %d..%d", tt.selected, first, last)
			}
		})
	}
}

func TestParseKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  key
	}{
		// A terminal in application cursor mode sends ESC O A for Up rather
		// than ESC [ A. Handling only the second form left the arrow keys
		// silently dead, with j/k the only way to move.
		{"up, normal mode", "\x1b[A", keyUp},
		{"up, application mode", "\x1bOA", keyUp},
		{"down, normal mode", "\x1b[B", keyDown},
		{"down, application mode", "\x1bOB", keyDown},

		{"page up", "\x1b[5~", keyPageUp},
		{"page down", "\x1b[6~", keyPageDown},
		{"home", "\x1b[H", keyTop},
		{"end", "\x1b[F", keyBottom},
		{"home, numeric", "\x1b[1~", keyTop},
		{"end, numeric", "\x1b[4~", keyBottom},

		{"k moves up", "k", keyUp},
		{"j moves down", "j", keyDown},
		{"g jumps to the top", "g", keyTop},
		{"G jumps to the end", "G", keyBottom},
		{"enter selects", "\r", keyEnter},
		{"q cancels", "q", keyQuit},
		{"m toggles", "m", keyToggle},
		{"ctrl+c cancels", "\x03", keyQuit},
		{"bare escape cancels", "\x1b", keyQuit},
		{"a digit is passed through", "3", key('3')},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseKey(strings.NewReader(tt.input)); got != tt.want {
				t.Errorf("parseKey(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestMove(t *testing.T) {
	// Paging past either end should stop there. Wrapping from the bottom back
	// to the top is disorienting when the jump is a whole page.
	if got := move(0, -5, 6); got != 0 {
		t.Errorf("paging up from the top = %d, want 0", got)
	}
	if got := move(5, 5, 6); got != 5 {
		t.Errorf("paging down from the end = %d, want 5", got)
	}
	if got := move(2, 2, 6); got != 4 {
		t.Errorf("paging down = %d, want 4", got)
	}
}

func TestParseKeyReadsDisconnect(t *testing.T) {
	for _, in := range []string{"d", "D"} {
		if got := parseKey(strings.NewReader(in)); got != keyDisconnect {
			t.Errorf("parseKey(%q) = %v, want keyDisconnect", in, got)
		}
	}
	// Every key must be its own value, or one case shadows another. This was
	// caught by the compiler on the way in: the new key was given a number
	// another already had.
	seen := map[key]string{}
	for name, k := range map[string]key{
		"up": keyUp, "down": keyDown, "enter": keyEnter, "quit": keyQuit,
		"none": keyNone, "toggle": keyToggle, "pageUp": keyPageUp,
		"pageDown": keyPageDown, "top": keyTop, "bottom": keyBottom,
		"disconnect": keyDisconnect,
	} {
		if other, clash := seen[k]; clash {
			t.Errorf("%s and %s are the same key", name, other)
		}
		seen[k] = name
	}
}

func TestCollectHidesADisabledMachine(t *testing.T) {
	// "disabled" is meant to skip a machine without removing it. The menu
	// skipped it while reading the plugin config and then swept ~/.ssh/config,
	// which is where such a machine came from -- so it came straight back,
	// stripped of its label and mode, looking like one that had never been
	// configured at all.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"),
		[]byte("Host bot\nHost retired\nHost prod\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgDir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfgDir)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(
		`{"hosts":[{"target":"bot"},{"target":"retired","disabled":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, _ := collect()
	for _, e := range entries {
		if e.Target == "retired" {
			t.Errorf("a disabled machine is still offered: %+v", e)
		}
	}

	// The others are all still there, including the one only ~/.ssh/config
	// knows about, so this hides one machine rather than a category of them.
	found := map[string]bool{}
	for _, e := range entries {
		found[e.Target] = true
	}
	for _, want := range []string{"bot", "prod"} {
		if !found[want] {
			t.Errorf("%q went missing: %+v", want, entries)
		}
	}
}

func TestParseKeyConsumesWholeEscapeSequences(t *testing.T) {
	// Giving up partway through a sequence leaves the rest in the buffer, and
	// the next read takes those bytes for keystrokes of their own. ctrl+up is
	// ESC [ 1 ; 5 A, and the "5" left behind was read as picking the fifth
	// machine in the list -- which connects to it. Pressing a modified arrow
	// opened an SSH terminal somewhere nobody had asked for.
	tests := []struct {
		name string
		in   string
		want key
	}{
		{"up", "\x1b[A", keyUp},
		{"down", "\x1b[B", keyDown},
		{"up in application mode", "\x1bOA", keyUp},
		{"down in application mode", "\x1bOB", keyDown},
		{"ctrl+up still moves up", "\x1b[1;5A", keyUp},
		{"shift+down still moves down", "\x1b[1;2B", keyDown},
		{"home", "\x1b[H", keyTop},
		{"end", "\x1b[F", keyBottom},
		{"home as a number", "\x1b[1~", keyTop},
		{"end as a number", "\x1b[4~", keyBottom},
		{"page up", "\x1b[5~", keyPageUp},
		{"page down", "\x1b[6~", keyPageDown},
		{"page up with a modifier", "\x1b[5;5~", keyPageUp},
		{"delete is not a key here", "\x1b[3~", keyNone},
		{"a paste beginning", "\x1b[200~", keyNone},
		{"something unrecognised", "\x1b[99;99;99R", keyNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := strings.NewReader(tt.in)
			if got := parseKey(r); got != tt.want {
				t.Errorf("parseKey(%q) = %v, want %v", tt.in, got, tt.want)
			}
			// Nothing left over. This is the whole point: a leftover byte is
			// read as a keypress nobody made.
			if r.Len() != 0 {
				rest := make([]byte, r.Len())
				_, _ = r.Read(rest)
				t.Errorf("parseKey(%q) left %q unread", tt.in, rest)
			}
		})
	}
}

func TestParseKeyOnATruncatedSequence(t *testing.T) {
	// A sequence that stops halfway means the input has gone, which is the same
	// as being told to quit.
	for _, in := range []string{"\x1b", "\x1b[", "\x1b[1", "\x1b[1;5"} {
		if got := parseKey(strings.NewReader(in)); got != keyQuit {
			t.Errorf("parseKey(%q) = %v, want keyQuit", in, got)
		}
	}
}

func TestParseKeyDoesNotReadForever(t *testing.T) {
	// A stream of parameter bytes that never ends a sequence must not be read
	// until it does.
	endless := strings.NewReader("\x1b[" + strings.Repeat("1;", 4096) + "A")
	if got := parseKey(endless); got != keyNone {
		t.Errorf("parseKey on an endless sequence = %v, want keyNone", got)
	}
}
