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

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(machines), 0o600); err != nil {
		t.Fatal(err)
	}
	// No plugin config and no daemon: every machine is one ~/.ssh/config knows
	// about and none of them is connected.
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
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
		got := runMenu(t, threeMachines, keys)
		if got.err != nil {
			t.Errorf("quitting with %q returned %v", keys, got.err)
		}
		if len(got.connected)+len(got.closed)+len(got.modes) != 0 {
			t.Errorf("quitting with %q did something: connected=%v closed=%v modes=%v",
				keys, got.connected, got.closed, got.modes)
		}
	}
}

func TestDisconnectingSomethingThatIsNotConnectedDoesNothing(t *testing.T) {
	// d closes a machine's panes here. With nothing open there is nothing to
	// close, and asking the daemon anyway would answer "not connected" -- an
	// error message for pressing a key that could have done nothing quietly.
	got := runMenu(t, threeMachines, "dq")
	if len(got.closed) != 0 {
		t.Errorf("d on a machine that is not connected asked to close %v", got.closed)
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
