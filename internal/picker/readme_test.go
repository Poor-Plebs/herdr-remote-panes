package picker

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestTheREADMEShowsTheKeysTheMenuOffers holds the illustration to the menu.
//
// The two lines at the foot of it list every key, which is the interface: they
// are how somebody learns that d disconnects and m toggles mirroring. Changing
// a key without changing the README teaches the wrong thing to everyone who
// reads it before pressing anything.
func TestTheREADMEShowsTheKeysTheMenuOffers(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
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

	readme, err := os.ReadFile("../../README.md")
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
