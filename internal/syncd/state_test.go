package syncd

import (
	"os"
	"strings"
	"testing"
)

func TestControlSocketIsPerSession(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	t.Setenv("HERDR_SESSION", "")
	def, err := ControlSocket()
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("HERDR_SESSION", "hub")
	hub, err := ControlSocket()
	if err != nil {
		t.Fatal(err)
	}

	// Each session runs its own daemon out of a shared state directory, so
	// their control sockets must not collide.
	if def == hub {
		t.Fatalf("sessions share a control socket: %s", def)
	}
	if !strings.Contains(hub, "control-hub.sock") {
		t.Errorf("socket %q is not named for the session", hub)
	}
}

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{
		"hub":     "hub",
		"my work": "my-work",
		"../evil": "---evil",
		"":        "default",
	} {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	// A daemon that has never run must start clean rather than error.
	if got := loadSnapshot(); len(got.Hosts) != 0 {
		t.Fatalf("missing snapshot should load empty, got %+v", got)
	}

	want := snapshot{Hosts: map[string]hostSnapshot{
		"bot": {
			Mirrors:   map[string]string{"term_a": "w1:p2"},
			Dismissed: []string{"term_b"},
		},
	}}
	if err := saveSnapshot(want); err != nil {
		t.Fatalf("saveSnapshot: %v", err)
	}

	got := loadSnapshot()
	host, ok := got.Hosts["bot"]
	if !ok {
		t.Fatalf("host missing after reload: %+v", got)
	}
	if host.Mirrors["term_a"] != "w1:p2" {
		t.Errorf("mirrors = %+v, want term_a -> w1:p2", host.Mirrors)
	}
	if len(host.Dismissed) != 1 || host.Dismissed[0] != "term_b" {
		t.Errorf("dismissed = %+v, want [term_b]", host.Dismissed)
	}
}

func TestSnapshotIsPerSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)

	t.Setenv("HERDR_SESSION", "hub")
	hub, err := snapshotPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_SESSION", "other")
	other, err := snapshotPath()
	if err != nil {
		t.Fatal(err)
	}
	if hub == other {
		t.Fatalf("sessions share a snapshot file: %s", hub)
	}
}

func TestCorruptSnapshotLoadsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SESSION", "hub")

	path, err := snapshotPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Unreadable bookkeeping must not stop the daemon starting.
	if got := loadSnapshot(); len(got.Hosts) != 0 {
		t.Fatalf("corrupt snapshot should load empty, got %+v", got)
	}
}
