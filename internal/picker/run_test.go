package picker

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The menu's loop had no test at all: it reads a key, moves or acts, and
// redraws, and every one of those is a promise the README makes about which key
// does what. It reads os.Stdin and draws to os.Stdout, both of which a test can
// stand in for, and it takes what it does on enter, d and m as arguments -- so
// the only thing missing was doing it.

// menuRun runs the menu over a fixed run of keystrokes and reports what it did.
type menuRun struct {
	connected []string
	modes     [][2]string
	closed    []string
	drawn     string
	err       error
}

// runMenu presses keys at the menu and waits for it to finish.
//
// Closing stdin is what stops it: a read that returns nothing is treated as
// quit, so a run that presses fewer keys than the menu wants ends rather than
// waiting for a key that is never coming.
func runMenu(t *testing.T, machines, keys string) menuRun {
	t.Helper()
	return runMenuConfigured(t, machines, "", keys)
}

// runMenuConfigured is runMenu with a plugin config as well, for the machines
// whose settings the menu reads rather than only their names: whether one is
// mirrored decides which way its toggle goes.
func runMenuConfigured(t *testing.T, machines, pluginConfig, keys string) menuRun {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(machines), 0o600); err != nil {
		t.Fatal(err)
	}
	// No daemon, so none of them is connected. With no plugin config either,
	// every machine is one ~/.ssh/config knows about and nothing more.
	configDir := t.TempDir()
	if pluginConfig != "" {
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(pluginConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "no-daemon-here")

	keysIn, keysOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	drawnIn, drawnOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	savedIn, savedOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = keysIn, drawnOut
	t.Cleanup(func() { os.Stdin, os.Stdout = savedIn, savedOut })

	go func() { _, _ = keysOut.WriteString(keys); _ = keysOut.Close() }()
	drawn := make(chan string, 1)
	go func() { b, _ := io.ReadAll(drawnIn); drawn <- string(b) }()

	var got menuRun
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		got.err = Run(
			func(target string) (string, error) {
				got.connected = append(got.connected, target)
				return "connected to " + target, nil
			},
			func(target, mode string) (string, error) {
				got.modes = append(got.modes, [2]string{target, mode})
				return "changed", nil
			},
			func(target string) (string, error) {
				got.closed = append(got.closed, target)
				return "closed", nil
			},
		)
	}()

	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		os.Stdin, os.Stdout = savedIn, savedOut
		t.Fatal("the menu did not finish: it is waiting for a key that is not coming")
	}
	os.Stdin, os.Stdout = savedIn, savedOut
	_ = drawnOut.Close()
	got.drawn = <-drawn
	return got
}

const threeMachines = "Host alpha\nHost beta\nHost gamma\n"

func TestTheMenuConnectsToWhatTheCursorIsOn(t *testing.T) {
	for _, tt := range []struct {
		what, keys, want string
	}{
		{"enter takes the first without moving", "\r", "alpha"},
		{"j moves down", "j\r", "beta"},
		{"j twice", "jj\r", "gamma"},
		{"k from the top wraps to the bottom", "k\r", "gamma"},
		{"j past the bottom wraps to the top", "jjj\r", "alpha"},
		{"the down arrow does what j does", "\x1b[B\r", "beta"},
		{"and the up arrow what k does", "\x1b[A\r", "gamma"},
		{"G goes to the last", "G\r", "gamma"},
		{"g comes back to the first", "Gg\r", "alpha"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			got := runMenu(t, threeMachines, tt.keys)
			if got.err != nil {
				t.Fatalf("the menu returned %v", got.err)
			}
			if len(got.connected) != 1 || got.connected[0] != tt.want {
				t.Errorf("connected to %v, want just %q", got.connected, tt.want)
			}
		})
	}
}

func TestADigitConnectsWithoutMovingFirst(t *testing.T) {
	// "1-9 pick" is what the menu offers, and it is the only way to reach a
	// machine without walking to it.
	for keys, want := range map[string]string{"1": "alpha", "2": "beta", "3": "gamma"} {
		got := runMenu(t, threeMachines, keys)
		if len(got.connected) != 1 || got.connected[0] != want {
			t.Errorf("pressing %q connected to %v, want %q", keys, got.connected, want)
		}
	}

	// A digit past the end of the list is not a machine, so it does nothing at
	// all -- rather than connecting to the last one, which is what an index
	// clamped instead of refused would do.
	got := runMenu(t, threeMachines, "9")
	if len(got.connected) != 0 {
		t.Errorf("pressing 9 with three machines connected to %v", got.connected)
	}
}

func TestQuittingTheMenuDoesNothingAtAll(t *testing.T) {
	// The way out has to be the way out: somebody who opens the menu by
	// accident and presses q must not have connected to anything.
	for _, keys := range []string{"q", "Q", "\x03", "\x1b"} {
		// A digit after the quit, which would connect to the first machine if
		// the quit were not read. Without it this passes for a menu that never
		// ran at all: "nothing happened" is exactly what a broken one does.
		got := runMenu(t, threeMachines, keys+"1")
		if got.err != nil {
			t.Errorf("quitting with %q returned %v", keys, got.err)
		}
		if len(got.connected)+len(got.closed)+len(got.modes) != 0 {
			t.Errorf("quitting with %q did something: connected=%v closed=%v modes=%v",
				keys, got.connected, got.closed, got.modes)
		}
		if !strings.Contains(got.drawn, "alpha") {
			t.Errorf("quitting with %q: the menu was never drawn, so this proves nothing", keys)
		}
	}
}

func TestDisconnectingSomethingThatIsNotConnectedDoesNothing(t *testing.T) {
	// d closes a machine's panes here. With nothing open there is nothing to
	// close, and asking the daemon anyway would answer "not connected" -- an
	// error message for pressing a key that could have done nothing quietly.
	// Followed by moving and choosing, so the run proves d was read and did
	// nothing rather than proving the menu never got that far: with only "dq"
	// this passes for a menu that quit before reading anything.
	got := runMenu(t, threeMachines, "dj\r")

	if len(got.closed) != 0 {
		t.Errorf("d on a machine that is not connected asked to close %v", got.closed)
	}
	if len(got.connected) != 1 || got.connected[0] != "beta" {
		t.Errorf("connected to %v after d, want beta: d swallowed what came next", got.connected)
	}
}

func TestTheMenuIsDrawnBeforeItIsAskedForAKey(t *testing.T) {
	// Reading first and drawing after would leave somebody looking at a blank
	// popup until they pressed something.
	got := runMenu(t, threeMachines, "q")
	for _, want := range []string{"alpha", "beta", "gamma", "Connect to a machine"} {
		if !strings.Contains(got.drawn, want) {
			t.Errorf("the menu was never drawn with %q in it", want)
		}
	}
}

func TestTogglingMirroringStaysInTheMenu(t *testing.T) {
	// m is the one key that changes something and does not leave: the change
	// and what it did to the machine's line are meant to be visible together.
	// So the menu is still there afterwards, and still takes keys.
	got := runMenu(t, threeMachines, "jmq")

	if len(got.modes) != 1 {
		t.Fatalf("pressing m changed %d machines, want 1: %v", len(got.modes), got.modes)
	}
	if got.modes[0][0] != "beta" {
		t.Errorf("m changed %q, not the machine under the cursor", got.modes[0][0])
	}
	// Not mirroring yet, so m turns it on.
	if got.modes[0][1] != "attach" {
		t.Errorf("m set mode %q on a machine that was not mirroring, want attach", got.modes[0][1])
	}
	// And q after it was still read, which it would not have been if m had
	// returned out of the loop.
	if len(got.connected) != 0 {
		t.Errorf("the menu connected to %v after a toggle", got.connected)
	}
}

func TestTheMenuKeepsTakingKeysAfterAToggle(t *testing.T) {
	// The list is rebuilt after a change, and the cursor has to survive that.
	// Rebuilding and resetting to the top would move the selection out from
	// under somebody between one keystroke and the next.
	got := runMenu(t, threeMachines, "jjm\r")

	if len(got.modes) != 1 || got.modes[0][0] != "gamma" {
		t.Fatalf("m changed %v, want gamma", got.modes)
	}
	if len(got.connected) != 1 || got.connected[0] != "gamma" {
		t.Errorf("enter after the toggle connected to %v, want gamma: "+
			"the cursor moved when the list was rebuilt", got.connected)
	}
}

// endsWithTheLastByte hands back its final byte together with io.EOF, which
// io.Reader permits and a terminal closing on a keypress does.
type endsWithTheLastByte struct {
	keys string
	at   int
}

func (r *endsWithTheLastByte) Read(p []byte) (int, error) {
	if r.at >= len(r.keys) {
		return 0, io.EOF
	}
	p[0] = r.keys[r.at]
	r.at++
	if r.at == len(r.keys) {
		return 1, io.EOF
	}
	return 1, nil
}

func TestAKeyArrivingWithTheEndOfTheStreamIsStillAKey(t *testing.T) {
	// io.Reader may return what it read and the error that ended the stream in
	// one call, and its contract says to use the bytes before considering the
	// error. Reading that as nothing loses the keypress -- and what it is lost
	// as is a quit, so the menu shuts instead of moving, which looks like the
	// arrow keys closing the menu.
	for _, tt := range []struct {
		keys string
		want key
	}{
		{"j", keyDown},
		{"k", keyUp},
		{"\x1b[B", keyDown},
		{"\x1b[A", keyUp},
		{"\r", keyEnter},
	} {
		if got := parseKey(&endsWithTheLastByte{keys: tt.keys}); got != tt.want {
			t.Errorf("%q arriving with the end of the stream read as %v, want %v",
				tt.keys, got, tt.want)
		}
	}

	// And a stream with nothing left in it is still a quit, which is what the
	// menu does when its input goes away.
	if got := parseKey(&endsWithTheLastByte{}); got != keyQuit {
		t.Errorf("an empty stream read as %v, want a quit", got)
	}
}

func TestPagingMovesTheCursorAndConnectsToWhereItLands(t *testing.T) {
	// "pgup/pgdn g/G jump" is on the menu's own key hints, and the two paging
	// keys were the only ones on that line never pressed here. The rest of the
	// movement keys have a case each; these had none, so how far a page moves
	// -- worked out from the layout, which is a different calculation from the
	// one that draws it -- was never taken through the menu at all.
	for _, tt := range []struct {
		what, keys, want string
	}{
		// A page is the whole list when the list fits, so paging down lands on
		// the last machine and paging up on the first.
		{"page down goes to the end", "\x1b[6~\r", "gamma"},
		{"page up from the top stays", "\x1b[5~\r", "alpha"},
		{"down then up comes back", "\x1b[6~\x1b[5~\r", "alpha"},
		// And paging past an end stops there rather than wrapping, which is
		// what a page-sized jump has to do to stay predictable.
		{"page down twice is still the end", "\x1b[6~\x1b[6~\r", "gamma"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			got := runMenu(t, threeMachines, tt.keys)
			if got.err != nil {
				t.Fatalf("the menu returned %v", got.err)
			}
			if len(got.connected) != 1 || got.connected[0] != tt.want {
				t.Errorf("connected to %v, want just %q", got.connected, tt.want)
			}
		})
	}
}

func TestTheToggleAsksForTheOppositeOfWhatAMachineIs(t *testing.T) {
	// m toggles mirroring for the machine under the cursor. Which way it goes
	// depends on what the machine already is, and only one of the two
	// directions was ever pressed: a machine that is not mirrored is asked to
	// attach. A machine that is mirrored has to be asked for plain SSH, and
	// asking it to attach again would be a key that looks broken.
	mirrored := `{"hosts":[{"target":"alpha","mode":"attach"}]}`

	got := runMenuConfigured(t, threeMachines, mirrored, "m")
	if len(got.modes) != 1 {
		t.Fatalf("pressing m asked for %v, want one change", got.modes)
	}
	if target, mode := got.modes[0][0], got.modes[0][1]; target != "alpha" || mode != "ssh" {
		t.Errorf("pressing m on a mirrored machine asked for %q on %q, want ssh on alpha", mode, target)
	}

	// And the other way, which is the direction that was already covered:
	// the same key on a machine that is not mirrored asks it to mirror.
	got = runMenuConfigured(t, threeMachines, `{"hosts":[{"target":"alpha"}]}`, "m")
	if len(got.modes) != 1 {
		t.Fatalf("pressing m asked for %v, want one change", got.modes)
	}
	if mode := got.modes[0][1]; mode != "attach" {
		t.Errorf("pressing m on a machine that is not mirrored asked for %q, want attach", mode)
	}
}

func TestWithNoMachinesTheMenuSaysWhereToPutSome(t *testing.T) {
	// The first thing somebody sees, and the one state where there is no menu
	// to draw: nothing in ~/.ssh/config and nothing in the plugin's config.
	// An empty list with no explanation is indistinguishable from a menu that
	// failed to load, and the way out of it is a file they have not written
	// yet.
	got := runMenu(t, "", "")
	if got.err != nil {
		t.Fatalf("the menu returned %v", got.err)
	}
	if len(got.connected) != 0 {
		t.Errorf("with no machines it connected to %v", got.connected)
	}
	if !strings.Contains(got.drawn, "No machines found") {
		t.Errorf("nothing says the list is empty:\n%s", got.drawn)
	}
	// Both files, because either one is a place to put a machine and somebody
	// who has neither has no reason to prefer one.
	for _, where := range []string{"~/.ssh/config", "config.json"} {
		if !strings.Contains(got.drawn, where) {
			t.Errorf("nothing points at %s:\n%s", where, got.drawn)
		}
	}
}

func TestWithNoMachinesAndABrokenConfigItSaysBoth(t *testing.T) {
	// The empty list has two causes and they need telling apart. One is having
	// written nothing yet. The other is having written something the plugin
	// cannot read -- in which case the machines in it are missing precisely
	// because of the fault, and saying only "add some" sends somebody to write
	// what they have already written.
	got := runMenuConfigured(t, "", "{not json", "")
	if got.err != nil {
		t.Fatalf("the menu returned %v", got.err)
	}
	if !strings.Contains(got.drawn, "No machines found") {
		t.Errorf("nothing says the list is empty:\n%s", got.drawn)
	}
	if !strings.Contains(got.drawn, "Could not read the plugin config") {
		t.Errorf("the config could not be read and the menu does not say so:\n%s", got.drawn)
	}
}

func TestTheToggleWillNotQuietlyMakeAReadOnlyMachineWritable(t *testing.T) {
	// m only ever wrote "attach" or "ssh". On a machine set to observe it
	// therefore read as a toggle and was a one-way door: observe went to ssh,
	// ssh went to attach, and nothing in the menu went back. Two presses turned
	// a machine deliberately chosen to be read-only into one that can be typed
	// into, and the only way back was editing the config by hand.
	observe := `{"hosts":[{"target":"alpha","mode":"observe"}]}`

	// m, then a key to dismiss what it says.
	got := runMenuConfigured(t, threeMachines, observe, "m ")
	if len(got.modes) != 0 {
		t.Fatalf("pressing m on a read-only machine asked for %v, want no change at all", got.modes)
	}
	// Refusing silently would be a key that looks broken, so it has to say
	// both that it will not and where the setting actually lives.
	for _, want := range []string{"read-only", "observe", "config"} {
		if !strings.Contains(got.drawn, want) {
			t.Errorf("refusing to change a read-only machine never mentioned %q:\n%s", want, got.drawn)
		}
	}

	// A machine that is only in ~/.ssh/config gets the mode from the top of the
	// config, so observe reaches it too and m has to refuse it the same way.
	// This is a separate path: it is settled in a different loop, which also
	// runs over the machines the first loop already settled.
	everything := `{"mode":"observe"}`
	got = runMenuConfigured(t, threeMachines, everything, "m ")
	if len(got.modes) != 0 {
		t.Errorf("with observe set for every machine, m on one from ~/.ssh/config "+
			"asked for %v, want no change", got.modes)
	}

	// And the two loops together: a machine named in both, whose own entry says
	// observe. The second loop hands back the entry the first one made, and
	// working the mode out again from a bare target reads the top-level default
	// instead of what the machine was set to -- which quietly made it writable
	// again, by the same key this is here to stop.
	got = runMenuConfigured(t, threeMachines, observe, "m ")
	if len(got.modes) != 0 {
		t.Errorf("a machine set to observe and also in ~/.ssh/config was changed "+
			"by m: %v", got.modes)
	}

	// The menu is still usable afterwards: a refusal that ate the keyboard
	// would be worse than the thing it is preventing.
	got = runMenuConfigured(t, threeMachines, observe, "m j\r")
	if len(got.connected) != 1 || got.connected[0] != "beta" {
		t.Errorf("after refusing, the menu connected to %v, want beta: the refusal "+
			"left the menu unable to take keys", got.connected)
	}
}

func TestAReadOnlyMachineSaysSoBeforeAnybodyTriesToType(t *testing.T) {
	// Observe mirrors a machine's terminals and does not let you type into
	// them. The line said "mirrored" for both it and attach, so the two were
	// indistinguishable right up until somebody typed into one and nothing
	// happened -- and now that m refuses to change it, the line is the only
	// place the difference is visible at all.
	for _, tt := range []struct {
		what  string
		entry Entry
	}{
		{"connected and mirroring", Entry{Connected: true, Mirroring: true, Mirrors: 3}},
		{"configured, not connected", Entry{Configured: true, Mirroring: true}},
		{"unreachable", Entry{GaveUp: true, Mirroring: true}},
	} {
		writable := plainOf(statusSpans(tt.entry))
		readOnly := tt.entry
		readOnly.ReadOnly = true
		got := plainOf(statusSpans(readOnly))

		if got == writable {
			t.Errorf("%s: read-only and attach both say %q, so nothing in the menu "+
				"tells them apart", tt.what, got)
		}
		if !strings.Contains(got, "read-only") {
			t.Errorf("%s: a read-only machine's line is %q", tt.what, got)
		}
		if strings.Contains(got, "mirrored") {
			t.Errorf("%s: a read-only machine still says mirrored: %q", tt.what, got)
		}
	}
}
