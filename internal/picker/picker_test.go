package picker

import (
	"os"
	"path/filepath"
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
