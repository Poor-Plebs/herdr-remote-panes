package picker

import (
	"errors"
	"fmt"
	"io"
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
	// No daemon answers in a test, and the menu now says so. Anything else
	// would be a warning about the config, which is what this cares about.
	if warning != "" && !strings.Contains(warning, "daemon is not running") {
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

	// One step off each end, which is how the end is actually reached: with
	// the arrow keys, one machine at a time. Paging overshoots by a whole
	// page and so cannot tell "stop at the last" from "stop one past it" --
	// and one past the last is an entry that is not there.
	if got := move(5, 1, 6); got != 5 {
		t.Errorf("stepping down from the last of 6 = %d, want 5: %d is past the end", got, got)
	}
	if got := move(0, -1, 6); got != 0 {
		t.Errorf("stepping up from the first = %d, want 0", got)
	}
	// And a list with one thing in it, where the two ends are the same entry.
	if got := move(0, 1, 1); got != 0 {
		t.Errorf("stepping down in a list of one = %d, want 0", got)
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
		// A paste beginning with nothing after it is a paste that never ends,
		// which is covered with the other truncated sequences below.
		{"something unrecognised", "\x1b[99;99;99R", keyNone},
		// The ends of the range a final byte lives in. A sequence ending in
		// one of these is still a whole sequence, and one not recognised as
		// ending is one that goes on eating the keys typed after it. "~" is
		// covered above by page up and the rest; "@" is the other end, and
		// nothing reached it.
		{"a sequence ending at the bottom of the range", "\x1b[@", keyNone},
		{"the same with parameters", "\x1b[1;2@", keyNone},
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
	for _, in := range []string{"\x1b", "\x1b[", "\x1b[1", "\x1b[1;5", "\x1b[200~", "\x1b[200~half"} {
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

func TestAPasteDoesNotPressAnything(t *testing.T) {
	// A paste is not typing. Left as keystrokes it is several decisions:
	// pasting the word "prod" presses p, r, o and then d, which disconnects the
	// machine under the cursor, and any digit that follows picks a machine and
	// connects to it.
	r := strings.NewReader("\x1b[200~prod 3\x1b[201~")
	if got := parseKey(r); got != keyNone {
		t.Errorf("a paste = %v, want nothing pressed", got)
	}
	if r.Len() != 0 {
		rest := make([]byte, r.Len())
		_, _ = r.Read(rest)
		t.Errorf("the pasted text was left to be read as keys: %q", rest)
	}

	// Whatever is typed after it still works.
	r = strings.NewReader("\x1b[200~anything at all\x1b[201~q")
	if got := parseKey(r); got != keyNone {
		t.Errorf("a paste = %v, want nothing pressed", got)
	}
	if got := parseKey(r); got != keyQuit {
		t.Errorf("the keypress after a paste = %v, want it to still work", got)
	}
}

func TestAPasteContainingEscapesIsStillSwallowed(t *testing.T) {
	// Pasted text can hold anything, including something that looks like the
	// end marker without being it.
	for _, in := range []string{
		"\x1b[200~\x1b[A\x1b[201~",    // an arrow key, pasted
		"\x1b[200~\x1b[999~\x1b[201~", // something unrecognised
		"\x1b[200~ESC[201~\x1b[201~",  // the marker as text
	} {
		r := strings.NewReader(in)
		if got := parseKey(r); got != keyNone {
			t.Errorf("parseKey(%q) = %v, want nothing pressed", in, got)
		}
		if r.Len() != 0 {
			t.Errorf("parseKey(%q) left %d bytes unread", in, r.Len())
		}
	}
}

func TestAPasteThatNeverEnds(t *testing.T) {
	// Something claiming to be a paste and never finishing is a stream to stop
	// reading, not one to keep waiting on.
	r := strings.NewReader("\x1b[200~" + strings.Repeat("x", 1<<17))
	if got := parseKey(r); got != keyNone {
		t.Errorf("an unfinished paste = %v, want nothing pressed", got)
	}
}

func TestAConfigWarningSaysWhatToFixInTheRoomItGets(t *testing.T) {
	// The warning gets two lines of the popup. An error that opens with the
	// full path of the config spends both of them on the one thing the reader
	// already knows -- there is a single plugin config -- and the part naming
	// the setting to fix falls off the end.
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte("{\n  \"max_mirrors\": \"lots\"\n}"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, warning := collect()
	if warning == "" {
		t.Fatal("a config that cannot be parsed should be reported")
	}

	// Not the warning as built, but the warning as drawn, at a popup width
	// Herdr actually uses.
	frame := render([]Entry{{Target: "bot"}}, 0, 80, 20, warning)
	for _, want := range []string{"max_mirrors", "should be a number", "line 2"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the drawn menu never shows %q:\n%s", want, visible(frame))
		}
	}
}

func TestPagingNeverStepsOverAMachine(t *testing.T) {
	// The step used to be the popup height less a constant, which stopped
	// matching once the frame learned to give up its parts as room ran short.
	// It was two rows out at every size, so paging through a long list went
	// past two machines each time without ever drawing them -- and nothing
	// about the menu showed that it had happened.
	//
	// The property is not "the step is N". It is that pressing the page key
	// repeatedly puts every machine on screen at some point, at whatever size
	// the popup happens to be and whether or not a warning is taking up room.
	sizes := []struct{ cols, rows int }{
		{80, 24}, {80, 12}, {80, 8}, {80, 6}, {80, 5}, {40, 10}, {200, 40}, {30, 7},
	}
	warnings := []string{
		"",
		"Could not read the plugin config: max_mirrors should be a number, not text (line 2). Only ~/.ssh/config machines are listed.",
	}

	for _, size := range sizes {
		for _, warning := range warnings {
			for _, count := range []int{1, 2, 5, 9, 30, 100} {
				entries := make([]Entry, count)
				for i := range entries {
					entries[i] = Entry{Target: fmt.Sprintf("machine-%d", i)}
				}

				seen := map[int]bool{}
				selected := 0
				// Enough presses to cross the list several times over, so a
				// step that is too large shows up as a gap rather than as
				// slowness.
				for press := 0; press < 4*count+8; press++ {
					frame := planLayout(count, selected, size.rows, len(warningLines(size.cols, warning)))
					for i := frame.first; i < frame.last && i < count; i++ {
						seen[i] = true
					}
					seen[selected] = true
					selected = move(selected, pageStepIn(count, selected, size.cols, size.rows, warning), count)
				}

				for i := 0; i < count; i++ {
					if !seen[i] {
						t.Fatalf("paging a list of %d in a %dx%d popup (warning: %v) "+
							"never showed machine %d", count, size.cols, size.rows, warning != "", i)
					}
				}
			}
		}
	}
}

func TestAPageStepIsWhatIsOnScreen(t *testing.T) {
	// Asking the layout is the only way the two stay in agreement, so this
	// holds them to each other directly rather than to a number.
	for _, rows := range []int{4, 5, 6, 8, 12, 24, 40} {
		count := 50
		frame := planLayout(count, 0, rows, 0)
		step := pageStepIn(count, 0, 80, rows, "")

		if drawn := frame.last - frame.first; drawn > 0 && step != drawn {
			t.Errorf("at %d rows the menu draws %d machines but pages by %d",
				rows, drawn, step)
		}
		// A step of zero would leave the page keys dead with no clue why.
		if step < 1 {
			t.Errorf("at %d rows the page step is %d", rows, step)
		}
	}
}

// captureNotices runs something and returns exactly what it drew, escapes and
// all.
//
// Raw on purpose. Stripping them here would make it impossible to ask whether
// any escaped -- which is most of what these screens have to be right about,
// since a machine's name comes from ~/.ssh/config and is drawn straight to a
// terminal. Callers that want to read the words wrap this in visible.
func captureNotices(t *testing.T, run func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = saved }()

	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(read)
		done <- string(out)
	}()

	run()
	write.Close()
	return <-done
}

// aKeyIsWaiting puts a keypress on stdin so a screen that waits for one does
// not wait for ever.
func aKeyIsWaiting(t *testing.T) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.WriteString("q"); err != nil {
		t.Fatal(err)
	}
	write.Close()
	saved := os.Stdin
	os.Stdin = read
	t.Cleanup(func() { os.Stdin = saved })
}

func TestPickingAMachineThatWillNotAnswerSaysWhy(t *testing.T) {
	// The screen after pressing enter is the one place somebody is certainly
	// looking, so it has to name the machine and say what went wrong -- and
	// then wait, because a message that flashes past is no message at all.
	aKeyIsWaiting(t)

	drawn := captureNotices(t, func() {
		err := choose(Entry{Target: "prod"}, func(string) (string, error) {
			return "", errors.New("host key changed — verify it, then update ~/.ssh/known_hosts")
		})
		if err != nil {
			t.Errorf("choose returned %v; a machine that will not answer is not the menu failing", err)
		}
	})

	for _, want := range []string{"prod", "host key changed", "known_hosts", "Press any key"} {
		if !strings.Contains(visible(drawn), want) {
			t.Errorf("the screen does not mention %q:\n%s", want, drawn)
		}
	}
}

func TestPickingAMachineSaysWhatHappened(t *testing.T) {
	drawn := captureNotices(t, func() {
		if err := choose(Entry{Target: "bot"}, func(string) (string, error) {
			return "connected to bot and opened a terminal", nil
		}); err != nil {
			t.Fatalf("choose: %v", err)
		}
	})

	if !strings.Contains(visible(drawn), "Connecting to bot") {
		t.Errorf("the screen never said it was connecting:\n%s", drawn)
	}
	if !strings.Contains(visible(drawn), "opened a terminal") {
		t.Errorf("the screen does not say what happened:\n%s", drawn)
	}
}

func TestAMachineNameOnThatScreenIsMadeSafe(t *testing.T) {
	// The name can come from ~/.ssh/config, and this draws it straight to the
	// terminal.
	aKeyIsWaiting(t)

	drawn := captureNotices(t, func() {
		_ = choose(Entry{Target: "prod\x1b[31m\nrest"}, func(string) (string, error) {
			return "", errors.New("nope")
		})
	})
	// The colour the name tried to set, which the menu never asks for itself.
	if strings.Contains(drawn, "\x1b[31m") {
		t.Errorf("the screen carries an escape from the machine's name:\n%q", drawn)
	}
	if strings.Contains(drawn, "\nrest") {
		t.Errorf("the name broke the line it was drawn on:\n%q", drawn)
	}
}

func TestADigitPicksTheMachineItIsBeside(t *testing.T) {
	// The menu numbers nine machines and offers "1-9 pick". A digit that picks
	// the wrong one connects you somewhere you did not ask for, and the
	// machines move about as they connect and disconnect, so the number under a
	// digit is not the same one it was a moment ago.
	for _, tt := range []struct {
		pressed key
		count   int
		want    int
		ok      bool
		what    string
	}{
		{'1', 5, 0, true, "the first"},
		{'5', 5, 4, true, "the last of five"},
		{'9', 12, 8, true, "the ninth, which is as far as digits go"},
		{'6', 5, 0, false, "past the end of a short list"},
		{'0', 5, 0, false, "zero, which numbers nothing"},
		{'a', 5, 0, false, "a letter that is not a digit"},
		// ':' is '1' plus nine, so subtracting lands exactly on the tenth
		// machine. Nothing in the menu offers it and it must not connect.
		{':', 12, 0, false, "the key just past nine"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			got, ok := planDigitChoice(tt.pressed, tt.count)
			if ok != tt.ok {
				t.Fatalf("pressing %q with %d machines picked=%v, want %v",
					rune(tt.pressed), tt.count, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("pressing %q picked machine %d, want %d", rune(tt.pressed), got, tt.want)
			}
		})
	}

	// Every digit the menu offers reaches a machine when there are enough.
	for digit := '1'; digit <= '9'; digit++ {
		if _, ok := planDigitChoice(key(digit), 9); !ok {
			t.Errorf("%q is offered by the menu and picks nothing", digit)
		}
	}
}

func TestTheCursorStaysOnTheListWhenItShrinks(t *testing.T) {
	// Disconnecting a machine can take it out of the list. A cursor left past
	// the end draws nothing and moves nowhere: the menu looks frozen.
	if got := planSelectionAfterChange(4, 2); got != 0 {
		t.Errorf("with the cursor at 4 and two machines left it is at %d, want the top", got)
	}
	if got := planSelectionAfterChange(1, 5); got != 1 {
		t.Errorf("a cursor still on the list moved to %d", got)
	}
	// The last machine is on the list; the one after it is not.
	if got := planSelectionAfterChange(4, 5); got != 4 {
		t.Errorf("the last machine is a fine place to be, got %d", got)
	}
	// Exactly one past the end, which is where a cursor lands when the machine
	// it was on is the one that went.
	if got := planSelectionAfterChange(5, 5); got != 0 {
		t.Errorf("a cursor one past the end stayed at %d, which draws nothing", got)
	}
	if got := planSelectionAfterChange(0, 0); got != 0 {
		t.Errorf("with nothing left the cursor is at %d", got)
	}
}

func TestWhichMachinesDCanDoSomethingTo(t *testing.T) {
	// d closes a machine's panes here. On a machine that was never connected
	// there is nothing to close, so it does nothing rather than reporting an
	// error at somebody for pressing a key the menu offers.
	//
	// A machine that was given up on is the case worth having: giving up
	// leaves its terminals on screen wearing the failure, and d is how they go.
	// It is also the one that reads as an exception, so it is the one that gets
	// dropped when this is rewritten.
	for _, tt := range []struct {
		what  string
		entry Entry
		want  bool
	}{
		{"connected", Entry{Connected: true}, true},
		{"given up on", Entry{GaveUp: true}, true},
		{"given up on while connected", Entry{Connected: true, GaveUp: true}, true},
		{"never connected", Entry{}, false},
		// Configured but not connected is the ordinary state of a machine in
		// the list, and by far the commonest thing the cursor sits on.
		{"configured but idle", Entry{Configured: true, Mirroring: true}, false},
	} {
		if got := worthDisconnecting(tt.entry); got != tt.want {
			t.Errorf("d on a machine %s: %v, want %v", tt.what, got, tt.want)
		}
	}
}

func TestTwoWarningsShareTheOneLine(t *testing.T) {
	// The menu has one line for warnings and two things that can want it: the
	// daemon not answering, and an installed copy newer than the one running.
	// Both are worth saying, and the second is worth saying precisely when
	// somebody is confused -- an update that has not taken effect looks exactly
	// like a fix that did not work.
	const daemon = "The daemon is not running"
	const stale = "A newer version is installed than the one running"

	if got := bothWarnings(daemon, stale); got != daemon+" · "+stale {
		t.Errorf("two warnings came out as %q", got)
	}
	if got := bothWarnings(daemon, ""); got != daemon {
		t.Errorf("one warning came out as %q, which ends in a separator with "+
			"nothing after it -- read as a message cut off", got)
	}
	if got := bothWarnings("", stale); got != stale {
		t.Errorf("a warning with nothing before it came out as %q", got)
	}
	if got := bothWarnings("", ""); got != "" {
		t.Errorf("no warnings at all came out as %q, which draws a line "+
			"reserving room for nothing", got)
	}
}
