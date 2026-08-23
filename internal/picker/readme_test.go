package picker

import (
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
