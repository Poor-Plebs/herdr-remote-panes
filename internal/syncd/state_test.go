package syncd

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketPathFor(t *testing.T) {
	const temp = "/tmp"
	const stateDir = "/home/u/.local/state/herdr/plugins/p"

	t.Run("a short path stays in the state directory", func(t *testing.T) {
		// Keeping it there means the socket sits with the rest of the plugin's
		// state, under a name a person can recognise.
		got := socketPathFor(stateDir, "hub", temp)
		if !strings.HasSuffix(got, "control-hub.sock") || !strings.HasPrefix(got, stateDir) {
			t.Errorf("got %q, want it named for the session inside the state directory", got)
		}
	})

	t.Run("sessions do not share a socket", func(t *testing.T) {
		// Each session runs its own daemon out of a shared state directory.
		if socketPathFor(stateDir, "hub", temp) == socketPathFor(stateDir, "other", temp) {
			t.Error("two sessions resolved to the same socket")
		}
	})

	t.Run("an overlong path falls back and stays bindable", func(t *testing.T) {
		// Not hypothetical: macOS temp directories are already near the limit,
		// and binding past it fails with "invalid argument", which says
		// nothing about the cause.
		long := "/" + strings.Repeat("deeply-nested-directory/", 6)
		got := socketPathFor(long, strings.Repeat("session", 5), temp)
		if len(got) > maxUnixSocketPath {
			t.Errorf("fallback is %d bytes, still too long: %s", len(got), got)
		}
		if !strings.HasPrefix(got, temp) {
			t.Errorf("got %q, want the fallback under %q", got, temp)
		}
	})

	t.Run("the fallback is deterministic and per session", func(t *testing.T) {
		// The actions must look for the socket the daemon actually bound.
		long := "/" + strings.Repeat("deeply-nested-directory/", 6)
		first := socketPathFor(long, "hub", temp)
		second := socketPathFor(long, "hub", temp)
		if first != second {
			t.Errorf("the fallback path is not stable: %q and %q", first, second)
		}
		if socketPathFor(long, "hub", temp) == socketPathFor(long, "other", temp) {
			t.Error("two sessions collided in the fallback")
		}
	})
}

func TestControlSocketBinds(t *testing.T) {
	// Whatever the platform's directories look like, the result must bind.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	socket, err := ControlSocket()
	if err != nil {
		t.Fatalf("ControlSocket: %v", err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen on %q (%d bytes): %v", socket, len(socket), err)
	}
	listener.Close()
	os.Remove(socket)
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
	// Written the way the daemon writes it: it renders the snapshot, compares
	// it with what it last wrote, and only then puts it on disk.
	raw, err := marshalSnapshot(want)
	if err != nil {
		t.Fatalf("marshalSnapshot: %v", err)
	}
	if err := writeSnapshot(raw); err != nil {
		t.Fatalf("writeSnapshot: %v", err)
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

func TestASnapshotHoldingNothingStillComesBackUsable(t *testing.T) {
	// Empty is not the same as broken, and the two arrive by different routes.
	// Anything that will not parse is thrown away for a fresh one, which is
	// what the test above holds. What this holds is the rest: a file that
	// parses perfectly well and says nothing.
	//
	// Those come back with no map at all rather than an empty one, and the
	// difference does not show until something is written to it, at which
	// point the daemon stops with "assignment to entry in nil map" -- on
	// startup, before anything it could have been blamed on.
	for _, body := range []string{
		"{}",             // an object with nothing in it
		`{"hosts":null}`, // the field, saying nothing
		"null",           // the whole document, saying nothing
		"",               // nothing at all
		"[]",             // the right JSON, the wrong shape
		"{not json",      // not JSON, which the test above covers from the front
	} {
		t.Run(body, func(t *testing.T) {
			t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
			t.Setenv("HERDR_SESSION", "hub")
			path, err := snapshotPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			got := loadSnapshot()
			if got.Hosts == nil {
				t.Fatalf("a snapshot of %q came back with no map to write into", body)
			}
			// The map being there is the claim; writing to it is the proof,
			// since that is what the daemon does next and what a nil one
			// would stop on.
			got.Hosts["bot"] = hostSnapshot{}
			if len(got.Hosts) != 1 {
				t.Errorf("a snapshot of %q gave a map that did not take an entry", body)
			}
		})
	}
}

func TestASessionNameBecomesAFilenameWithoutTwoOfThemColliding(t *testing.T) {
	// The name goes into a socket path, so anything a filesystem or a shell
	// would read as something else is replaced. What matters is that the
	// replacing does not run two different sessions together: they would then
	// derive the same socket, and the second daemon would find the first one's
	// and exit.
	for _, tt := range []struct{ in, want string }{
		// Every edge of every range the allow-list names, because an
		// off-by-one at any of them silently rewrites an ordinary character.
		{"az", "az"},
		{"AZ", "AZ"},
		{"09", "09"},
		{"a-z_A-Z_0-9", "a-z_A-Z_0-9"},
		{"hub", "hub"},
		// Just outside each range, in the ASCII order: these are replaced.
		{"`", "-"}, // before 'a'
		{"{", "-"}, // after 'z'
		{"@", "-"}, // before 'A'
		{"[", "-"}, // after 'Z'
		{"/", "-"}, // before '0'
		{":", "-"}, // after '9'
		{"a/b", "a-b"},
		{"..", "--"},
		{"work session", "work-session"},
		{"日本語", "---"},
		// Nothing usable left, and a socket still has to be named something.
		{"", "default"},
	} {
		if got := sanitize(tt.in); got != tt.want {
			t.Errorf("sanitize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	// The property behind the cases: two names that differ still differ after.
	// Only for names made of characters the allow-list keeps -- outside it,
	// running them together is the whole point.
	seen := map[string]string{}
	for _, name := range []string{"hub", "work", "a", "z", "A", "Z", "0", "9", "a-z", "a_z"} {
		got := sanitize(name)
		if other, clash := seen[got]; clash {
			t.Errorf("sessions %q and %q both become %q, so they share a socket", other, name, got)
		}
		seen[got] = name
	}
}

func TestTheSocketPathStaysShortEnoughToBind(t *testing.T) {
	// Binding a path over the kernel's limit fails with "invalid argument",
	// which says nothing about the cause, so an overlong one falls back to a
	// short deterministic path instead. The boundary is the whole of it: one
	// byte either side decides between a path that binds and an error nobody
	// can read.
	//
	// A literal directory rather than t.TempDir(): this only joins strings, and
	// a test directory on macOS is itself long enough to push the fallback over
	// the limit -- which is a fact about t.TempDir() and not about the code.
	// os.TempDir() there is some fifty bytes, leaving room to spare.
	const temp = "/tmp"
	const name = "control-hub.sock"

	for _, over := range []int{-1, 0, 1} {
		dir := "/" + strings.Repeat("d", maxUnixSocketPath-len(name)-1+over)
		want := filepath.Join(dir, name)
		got := socketPathFor(dir, "hub", temp)

		if fits, kept := len(want) <= maxUnixSocketPath, got == want; fits != kept {
			if fits {
				t.Errorf("a path of %d bytes fits but was moved to %q", len(want), got)
			} else {
				t.Errorf("a path of %d bytes is over the limit but was kept: %q", len(want), got)
			}
		}
	}

	// And the fallback is short: whatever it is handed, it adds a fixed name to
	// it rather than carrying any of the length that caused the problem.
	long := "/" + strings.Repeat("d", 300)
	fallback := socketPathFor(long, strings.Repeat("session", 20), temp)
	if extra := len(fallback) - len(temp); extra > 32 {
		t.Errorf("the fallback adds %d bytes to the temp directory: %q", extra, fallback)
	}
	if !strings.HasPrefix(fallback, temp) {
		t.Errorf("the fallback is not in the temp directory: %q", fallback)
	}
}

func TestEachSessionGetsItsOwnSocket(t *testing.T) {
	// Herdr's plugin state directory is shared by every session, but each
	// session runs its own daemon. Two of them on one socket means the second
	// finds the first one already listening and exits -- so the second window
	// has a menu that talks to the first window's machines, which is the kind
	// of wrong that takes a long evening to work out.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	t.Setenv("HERDR_SESSION", "hub")
	hub, err := ControlSocket()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_SESSION", "work")
	work, err := ControlSocket()
	if err != nil {
		t.Fatal(err)
	}
	if hub == work {
		t.Errorf("two sessions were given the same socket, %q, so the second "+
			"daemon would find the first one's and give up", hub)
	}

	// No session named at all is still one session rather than none: it wants
	// the same socket every time, not one named after nothing.
	t.Setenv("HERDR_SESSION", "")
	unnamed, err := ControlSocket()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_SESSION", "default")
	named, err := ControlSocket()
	if err != nil {
		t.Fatal(err)
	}
	if unnamed != named {
		t.Errorf("with no session named the socket is %q, but the default session's "+
			"is %q, so the two would not find each other", unnamed, named)
	}
}
