package syncd

import (
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
