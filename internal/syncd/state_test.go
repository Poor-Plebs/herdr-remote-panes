package syncd

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

// TestASnapshotIsNeverWrittenHalfWay holds that two writers cannot leave a
// torn snapshot behind.
//
// Two daemons share a state directory and a session during an upgrade: the
// replacement starts while the old one is still reconciling, which is the
// handover TestAnUpgradeHandsTheSocketOver in internal/project is written for.
// Within one daemon it is the same story -- persist releases d.mu before
// calling writeSnapshot, deliberately, so that a failed write cannot be
// recorded as a saved one, and two passes can be inside the write at once.
//
// While the temporary had a fixed name both writers truncated and filled the
// same file, and whichever renamed first moved whatever was in it: 46 of these
// 200 rounds left a snapshot that does not parse. What that costs is not the
// file but what reads it -- an unparseable snapshot is one the next daemon
// starts without, and the machines that live only there are the ones picked
// out of ~/.ssh/config, which the config file never names.
func TestASnapshotIsNeverWrittenHalfWay(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "handover")

	// Both valid, and far enough apart in length that a mixture of the two
	// cannot be mistaken for either.
	small, err := marshalSnapshot(snapshot{Hosts: map[string]hostSnapshot{"bot": {}}})
	if err != nil {
		t.Fatal(err)
	}
	many := map[string]hostSnapshot{}
	for i := 0; i < 400; i++ {
		many[fmt.Sprintf("machine-%03d", i)] = hostSnapshot{}
	}
	large, err := marshalSnapshot(snapshot{Hosts: many})
	if err != nil {
		t.Fatal(err)
	}
	if len(large) < 10*len(small) {
		t.Fatalf("the two payloads are %d and %d bytes, which is not far enough "+
			"apart for a mixture of them to be obvious", len(small), len(large))
	}

	path, err := snapshotPath()
	if err != nil {
		t.Fatal(err)
	}

	const rounds, writers = 200, 6
	overlapped := false
	for round := 0; round < rounds; round++ {
		var wg sync.WaitGroup
		// When each call was inside writeSnapshot. One slot per writer, each
		// written by its own goroutine and read after they have all finished,
		// so nothing here needs a lock of its own.
		type span struct{ in, out time.Time }
		spans := make([]span, writers)
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				raw := small
				if i%2 == 0 {
					raw = large
				}
				spans[i].in = time.Now()
				err := writeSnapshot(raw)
				spans[i].out = time.Now()
				if err != nil {
					t.Errorf("writeSnapshot: %v", err)
				}
			}(i)
		}
		wg.Wait()

		for i := 0; i < writers && !overlapped; i++ {
			for j := i + 1; j < writers; j++ {
				if spans[i].in.Before(spans[j].out) && spans[j].in.Before(spans[i].out) {
					overlapped = true
					break
				}
			}
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		switch {
		case bytes.Equal(got, append(append([]byte{}, small...), '\n')):
		case bytes.Equal(got, append(append([]byte{}, large...), '\n')):
		default:
			t.Fatalf("round %d: the snapshot is neither of the two things written "+
				"to it (%d bytes, small is %d and large is %d): a writer moved a "+
				"file another was still filling, and the next daemon starts "+
				"without its bookkeeping", round, len(got), len(small), len(large))
		}
	}

	// The control that the writers really were inside at once, which is the
	// whole hazard: serialise them -- by accident in the fixture, or by
	// writeSnapshot growing a lock -- and every round above passes with
	// nothing ever having been contended.
	//
	// This used to assert that BOTH payloads landed at least once across the
	// rounds, and that is a scheduling OUTCOME rather than the claim. Under
	// the load of the whole gate, where `go test ./...` runs the packages in
	// parallel, the larger writer finished last every single time and one
	// payload won all two hundred rounds -- a test failing against working
	// code. Overlap is what the fixture needs, so overlap is what is measured,
	// and load can only make it likelier.
	if !overlapped {
		t.Errorf("no two of the %d writers were ever inside writeSnapshot at the "+
			"same time across %d rounds, so nothing here was contended and the "+
			"check above holds nothing", writers, rounds)
	}
}

// TestTheSnapshotIsNotReadableByAnybodyElse holds the mode of the file the
// daemon keeps its bookkeeping in.
//
// It names the machines somebody connects to and the terminals mirrored from
// each, which is exactly what internal/config's
// TestSaveKeepsThePermissionsPrivate exists for one file over. The two are the
// same claim about the same kind of content, and only one of them was held.
//
// There is no line in writeSnapshot to point at: os.CreateTemp makes its file
// 0600 and the rename carries that mode to the destination. That is the reason
// to assert it rather than a reason not to -- the mode is a property of the
// call that was chosen, so a switch to os.Create, which takes 0666 through the
// umask, turns the file world-readable with nothing in the diff that so much
// as mentions permissions.
func TestTheSnapshotIsNotReadableByAnybodyElse(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "private")

	raw, err := marshalSnapshot(snapshot{Hosts: map[string]hostSnapshot{"bot": {}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshot(raw); err != nil {
		t.Fatal(err)
	}

	path, err := snapshotPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the snapshot is mode %o, want 600: it names the machines "+
			"somebody connects to, and anyone with an account on this machine "+
			"can read it", perm)
	}

	// The control that the mode above belongs to this write. A file left by
	// something else at that path would be stat-ed just as happily, and the
	// state directory is fresh, so what is there has to be what was written.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append(append([]byte{}, raw...), '\n')) {
		t.Fatalf("the file at %s is not what writeSnapshot wrote, so its mode "+
			"says nothing about that write", path)
	}
}

// TestAFailedSnapshotWriteLeavesNoTemporaryBehind holds the removal behind the
// temporary, on the only path where it removes anything.
//
// Every write that succeeds carries its temporary off in the rename, so the
// line reads as exercised while being a no-op -- exactly the gap
// internal/config had until 1522955, and this line was copied from there at
// 2ec4892 without the test that holds it.
//
// It costs more here than it did there. internal/config's temporary is written
// when somebody toggles a machine; this one is written on every pass that
// changed anything, and the name is unique per write, so what is left behind
// is not one stale file but one PER FAILED WRITE. A state directory that
// cannot be renamed into therefore fills at the poll interval. Measured with
// the removal gone: five failed writes leave five.
func TestAFailedSnapshotWriteLeavesNoTemporaryBehind(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SESSION", "handover")

	path, err := snapshotPath()
	if err != nil {
		t.Fatal(err)
	}
	// A directory where the file goes. Everything works until the rename,
	// which is late enough to have made the temporary -- unlike a directory
	// nothing can write into, where there would be nothing to leave behind.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	raw, err := marshalSnapshot(snapshot{Hosts: map[string]hostSnapshot{"bot": {}}})
	if err != nil {
		t.Fatal(err)
	}

	const writes = 5
	for i := 0; i < writes; i++ {
		err := writeSnapshot(raw)
		if err == nil {
			t.Fatalf("write %d over a directory reported success", i)
		}
		// The control that a temporary was taken before asking whether it was
		// given back, and it has to name the RENAME: a write refused earlier
		// reports the open, and would leave nothing behind whatever this line
		// did.
		if !strings.Contains(err.Error(), "rename") {
			t.Fatalf("write %d failed before the rename, so it never made a "+
				"temporary and this is not the path the removal is for: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	left := []string{}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			left = append(left, e.Name())
		}
	}
	if len(left) != 0 {
		t.Errorf("%d of %d failed writes left a temporary in the state directory "+
			"(%v): the name is unique per write, so these do not replace one "+
			"another -- one arrives per pass the daemon cannot save",
			len(left), writes, left)
	}
}
