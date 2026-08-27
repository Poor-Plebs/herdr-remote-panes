package picker

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/project"
)

// readmePath is the README at the top of the repository.
//
// Asked for rather than counted to with "..": this package has moved once
// already, and a path that is merely wrong reads no file, which these tests
// would report as the README having lost something.
func readmePath(t *testing.T) string {
	t.Helper()
	root, err := project.Root()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "README.md")
}

// TestTheREADMEShowsTheKeysTheMenuOffers holds the illustration to the menu.
//
// The two lines at the foot of it list every key, which is the interface: they
// are how somebody learns that d disconnects and m toggles mirroring. Changing
// a key without changing the README teaches the wrong thing to everyone who
// reads it before pressing anything.
func TestTheREADMEShowsTheKeysTheMenuOffers(t *testing.T) {
	readme, err := os.ReadFile(readmePath(t))
	if err != nil {
		t.Fatal(err)
	}

	// The widest form, which is what an ordinary popup shows.
	for _, hint := range hintLines(200) {
		if !strings.Contains(string(readme), hint) {
			t.Errorf("the README does not show this line from the menu:\n  %s", hint)
		}
	}
}

func TestEveryKeyTheMenuActsOnIsMentioned(t *testing.T) {
	// The hints are what the README shows, so a key that works and is not in
	// them is one nobody finds -- in the menu or in the README.
	hints := strings.Join(hintLines(200), " ")

	for key, what := range map[string]string{
		"enter": "connect",
		"q":     "cancel",
		"d":     "disconnect",
		"m":     "mirroring",
		"j":     "move down",
		"k":     "move up",
		"g":     "jump to the top",
		"G":     "jump to the end",
	} {
		if !strings.Contains(hints, key) {
			t.Errorf("the menu acts on %q (%s) and does not say so", key, what)
		}
	}
}

// TestEveryKeyTheMenuOffersDoesSomething presses what the hints advertise.
//
// The tests above hold the hints and the README to each other, and the hints to
// a list of keys written out beside them. None of them presses anything, so a
// key that stopped being read would leave both documents still promising it and
// both tests still passing -- and a dead key in a menu is silent: you press it,
// nothing happens, and there is nothing to read about why.
func TestEveryKeyTheMenuOffersDoesSomething(t *testing.T) {
	hints := strings.Join(hintLines(200), " ")

	offered := []struct {
		what  string // as the hints write it
		press string // what the terminal actually sends
		want  key
	}{
		{"↑", "\x1b[A", keyUp},
		{"↓", "\x1b[B", keyDown},
		// A terminal in application cursor mode sends these instead, which is
		// not a choice the menu gets to make.
		{"↑", "\x1bOA", keyUp},
		{"↓", "\x1bOB", keyDown},
		{"j", "j", keyDown},
		{"k", "k", keyUp},
		{"pgup", "\x1b[5~", keyPageUp},
		{"pgdn", "\x1b[6~", keyPageDown},
		{"g", "g", keyTop},
		{"G", "G", keyBottom},
		{"enter", "\r", keyEnter},
		{"enter", "\n", keyEnter},
		{"d", "d", keyDisconnect},
		{"m", "m", keyToggle},
		{"q", "q", keyQuit},
	}

	for _, offer := range offered {
		if !strings.Contains(hints, offer.what) {
			t.Errorf("this test presses %q, which the menu no longer offers", offer.what)
			continue
		}
		if got := parseKey(bytes.NewReader([]byte(offer.press))); got != offer.want {
			t.Errorf("the menu offers %q and pressing it gives %v, want %v",
				offer.what, got, offer.want)
		}
	}

	// "1-9 pick" is a range rather than a key, so it is held separately: every
	// one of them has to reach the menu as itself.
	if !strings.Contains(hints, "1-9") {
		t.Fatal("the menu no longer offers numbers")
	}
	for _, digit := range "123456789" {
		if got := parseKey(bytes.NewReader([]byte{byte(digit)})); got != key(digit) {
			t.Errorf("pressing %q gives %v, so that machine cannot be picked", digit, got)
		}
	}
}

// TestTheREADMEShowsTheMenuThatIsDrawn holds the picture to the thing.
//
// The example in the README is what somebody decides from before installing
// anything, and nothing kept it honest. It had drifted twice over without a
// murmur: the column of names was still the wide fixed one from before it was
// fitted to the names, and the unreachable rows still stopped at "unreachable"
// after they had learned to say why.
//
// Rendered here rather than compared loosely, because a picture that is nearly
// right is the kind of wrong nobody notices.
func TestTheREADMEShowsTheMenuThatIsDrawn(t *testing.T) {
	// The machines in the README, and the width the block is drawn at.
	entries := []Entry{
		{Target: "workbox", Configured: true, Connected: true, Terminals: 2},
		{Target: "ci", Configured: true, GaveUp: true,
			Reason: shortReason("host key changed — verify it")},
		{Target: "buildbox", Configured: true, GaveUp: true, Mirroring: true,
			Reason: shortReason("connection refused")},
		{Target: "gh-runner"},
	}
	const cols = 84

	var drawn []string
	for _, line := range strings.Split(visible(render(entries, 0, cols, 20, "")), "\r\n") {
		drawn = append(drawn, strings.TrimRight(line, " "))
	}
	want := strings.Join(drawn, "\n")

	readme, err := os.ReadFile(readmePath(t))
	if err != nil {
		t.Fatal(err)
	}
	const opening = "The key you bound opens this:\n\n```\n"
	start := strings.Index(string(readme), opening)
	if start < 0 {
		t.Fatal("the README no longer shows the menu")
	}
	start += len(opening)
	end := strings.Index(string(readme)[start:], "\n```\n")
	if end < 0 {
		t.Fatal("the menu block in the README is not closed")
	}
	got := string(readme)[start : start+end]

	if got != want {
		t.Errorf("the README shows a menu this does not draw.\n--- README ---\n%s\n--- drawn ---\n%s", got, want)
	}
}

// TestEveryStateTheMenuCanShowIsInTheREADME holds the menu's vocabulary to the
// README, the way the status line's is held to it next door.
//
// The menu is where most people meet these words: it is the first screen, and
// the one somebody is looking at when they wonder what "read-only" means or
// why a machine says three are elsewhere. A phrase the menu can show and the
// README never mentions is a word with nowhere to look it up.
//
// Every combination rather than a list written here, because a list written
// here is a list that stops being complete. Counts vary, so a number stands
// for any number.
func TestEveryStateTheMenuCanShowIsInTheREADME(t *testing.T) {
	raw, err := os.ReadFile(readmePath(t))
	if err != nil {
		t.Fatal(err)
	}
	prose := strings.Join(strings.Fields(string(raw)), " ")

	shape := reflect.TypeOf(Entry{})
	var fields []string
	for i := 0; i < shape.NumField(); i++ {
		switch name := shape.Field(i).Name; name {
		case "Target", "Label", "Reason":
			// What somebody wrote or what a machine said, which the README
			// cannot be expected to quote.
		default:
			fields = append(fields, name)
		}
	}

	digits := regexp.MustCompile(`\d+`)
	seen := map[string]bool{}
	for mask := 0; mask < 1<<uint(len(fields)); mask++ {
		var entry Entry
		holder := reflect.ValueOf(&entry).Elem()
		for i, name := range fields {
			if mask&(1<<uint(i)) == 0 {
				continue
			}
			switch value := holder.FieldByName(name); value.Kind() {
			case reflect.Bool:
				value.SetBool(true)
			case reflect.Int:
				value.SetInt(3)
			}
		}
		for _, span := range statusSpans(entry) {
			// A line is several phrases with separators between them, and the
			// README explains them one at a time.
			for _, part := range strings.Split(span.text, "·") {
				if phrase := strings.TrimSpace(part); phrase != "" {
					seen[phrase] = true
				}
			}
		}
	}
	if len(seen) < 8 {
		t.Fatalf("found %d phrases the menu can show, which is fewer than there "+
			"are -- this is checking nothing", len(seen))
	}

	for phrase := range seen {
		// Any number where this one has one: the README shows an example with
		// its own counts, and holding it to these would be holding it to a
		// number nothing means.
		pattern := digits.ReplaceAllString(regexp.QuoteMeta(phrase), `\d+`)
		matched, err := regexp.MatchString(pattern, prose)
		if err != nil {
			t.Fatalf("phrase %q made a pattern that will not compile: %v", phrase, err)
		}
		if !matched {
			t.Errorf("the menu can say %q and the README never mentions it, which "+
				"is where somebody goes to find out what it means", phrase)
		}
	}
}
