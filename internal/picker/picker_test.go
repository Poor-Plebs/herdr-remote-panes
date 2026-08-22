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

func TestVisibleWindow(t *testing.T) {
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
			count: 6, selected: 0, rows: 8, wantFirst: 0, wantLast: 4,
		},
		{
			name:  "the window follows the selection",
			count: 10, selected: 8, rows: 8, wantFirst: 6, wantLast: 10,
		},
		{
			name:  "the window never runs past the end",
			count: 10, selected: 9, rows: 8, wantFirst: 6, wantLast: 10,
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
			first, last := visibleWindow(tt.count, tt.selected, tt.rows)
			if first != tt.wantFirst || last != tt.wantLast {
				t.Errorf("visibleWindow(%d, %d, %d) = %d..%d, want %d..%d",
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
