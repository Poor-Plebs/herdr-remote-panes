package syncd

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestTheREADMESortsFailuresTheWayTheCodeDoes holds a claim in prose to the
// table it is about.
//
// The README tells people which failures are worth waiting out and which need
// them, by naming the causes. The two can drift apart without anything
// noticing: nothing about editing a row makes a sentence in another file wrong
// in a way a compiler can see.
func TestTheREADMESortsFailuresTheWayTheCodeDoes(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(readme)

	// The phrases the README uses, and the ssh message each one is about.
	cases := []struct {
		said    string
		message string
		settled bool
	}{
		{"a changed host key", "REMOTE HOST IDENTIFICATION HAS CHANGED", true},
		{"a name that does not resolve", "Name or service not known", true},
		{"a key the machine will not take", "Permission denied (publickey).", true},
		{"refused", "ssh: connect to host bot port 22: Connection refused", false},
		{"timed out", "ssh: connect to host bot port 22: Connection timed out", false},
		{"no route", "ssh: connect to host bot port 22: No route to host", false},
	}

	for _, tt := range cases {
		t.Run(tt.said, func(t *testing.T) {
			if !strings.Contains(text, tt.said) {
				t.Fatalf("the README no longer says %q; this test is describing a "+
					"document that has moved on", tt.said)
			}
			if got := planGiveUp(0, errors.New(tt.message)); got != tt.settled {
				what := "waited out"
				if tt.settled {
					what = "given up on at once"
				}
				t.Errorf("the README says %q is %s, and %q is not",
					tt.said, what, tt.message)
			}
		})
	}

	// And the sentence that sorts them, so the claim itself cannot quietly go.
	for _, claim := range []string{
		"gets a second try",
		"gets none",
		"would fail in exactly the same way",
	} {
		if !strings.Contains(text, claim) {
			t.Errorf("the README no longer says %q, so nothing there explains the split", claim)
		}
	}
}

// TestTheREADMEDoesNotClaimPlainSSHMachinesArePolled guards a sentence that was
// wrong for a while.
//
// "polls each connected machine every two seconds" described mirroring, and the
// default is not mirroring: a plain SSH machine is never contacted by the
// daemon at all. The paragraph directly under it said so, which is how the
// mistake survived -- each half read as a qualification of the other.
func TestTheREADMEDoesNotClaimPlainSSHMachinesArePolled(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(readme)

	if strings.Contains(text, "polls each connected machine") {
		t.Error("the README says every connected machine is polled; a plain SSH one is not")
	}
	// What makes the plain SSH mode need nothing installed, and what makes a
	// machine going away take a moment to notice.
	for _, claim := range []string{
		"the daemon never talks to the machine at all",
		"Only a mirrored machine is polled",
	} {
		if !strings.Contains(text, claim) {
			t.Errorf("the README no longer says %q", claim)
		}
	}
}
