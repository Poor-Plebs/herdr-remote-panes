package syncd

import (
	"errors"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
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
	//
	// The loop, not the daemon: connecting does talk to a plain SSH machine,
	// once, to check it answers. This used to pin the wider claim, and pinning
	// it is what caught the wording going stale when that check was added.
	for _, claim := range []string{
		"the loop never talks to the machine at all",
		"Only a mirrored machine is polled",
	} {
		if !strings.Contains(text, claim) {
			t.Errorf("the README no longer says %q", claim)
		}
	}
}

// TestTheREADMEsSidebarIsNamedTheWayThisNamesThings holds the other picture in
// the README to the code that produces what it shows.
//
// Every name in it comes from this plugin: a machine's space from
// workspace_format, one that is not answering from workspace_format_down, and
// its terminals from label_format. Change any of those and the picture is
// describing a different program, with nothing to say so -- which is what had
// happened to the picture of the menu.
func TestTheREADMEsSidebarIsNamedTheWayThisNamesThings(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	const opening = "Your sidebar ends up looking like this, one space per machine:\n\n```\n"
	start := strings.Index(string(readme), opening)
	if start < 0 {
		t.Fatal("the README no longer shows the sidebar")
	}
	start += len(opening)
	end := strings.Index(string(readme)[start:], "\n```\n")
	if end < 0 {
		t.Fatal("the sidebar block in the README is not closed")
	}
	shown := string(readme)[start : start+end]

	cfg := config.Defaults()
	d := New(cfg)
	reachable := config.Host{Target: "workbox"}
	unreachable := config.Host{Target: "ci"}

	// Each line of the picture, and the call that produces the name in it.
	for _, tt := range []struct {
		what string
		name string
	}{
		{"a machine you can reach", cfg.WorkspaceLabelFor(reachable, true)},
		{"a machine that is not answering", cfg.WorkspaceLabelFor(unreachable, false)},
		{"a terminal on it", d.label(reachable, herdrcli.Pane{}, "shell")},
		{"another one", d.label(reachable, herdrcli.Pane{}, "build")},
	} {
		t.Run(tt.what, func(t *testing.T) {
			if !strings.Contains(shown, tt.name) {
				t.Errorf("the sidebar shows no %q, which is what this names it:\n%s",
					tt.name, shown)
			}
			// On the line it belongs to, so the picture is not merely
			// containing the right words somewhere.
			for _, line := range strings.Split(shown, "\n") {
				if strings.Contains(line, tt.what) && !strings.Contains(line, tt.name) {
					t.Errorf("the line for %q reads %q, and this names it %q",
						tt.what, strings.TrimSpace(line), tt.name)
				}
			}
		})
	}
}

// TestTheREADMERequiresWhatTheProjectRequires holds one sentence to three
// files.
//
// "Requires Herdr 0.8.0+ and Go 1.25+, on Linux or macOS" is a claim about the
// manifest and about go.mod, and it is the sentence somebody reads before
// deciding whether they can install this at all. Nothing kept it honest, and
// each of the three moves independently of it: the Go floor was raised once
// already, which is exactly the change that would have stranded it.
func TestTheREADMERequiresWhatTheProjectRequires(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(readme)

	t.Run("the Go version go.mod asks for", func(t *testing.T) {
		mod, err := os.ReadFile("../../go.mod")
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		for _, line := range strings.Split(string(mod), "\n") {
			if after, found := strings.CutPrefix(strings.TrimSpace(line), "go "); found {
				want = strings.TrimSpace(after)
				break
			}
		}
		if want == "" {
			t.Fatal("go.mod names no Go version")
		}
		// Written as a floor, since that is what it is: anything newer builds.
		if !strings.Contains(text, "Go "+want+"+") {
			t.Errorf("go.mod asks for Go %s and the README does not say so", want)
		}
	})

	t.Run("the Herdr version the manifest asks for", func(t *testing.T) {
		manifest, err := os.ReadFile("../../herdr-plugin.toml")
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		for _, line := range strings.Split(string(manifest), "\n") {
			if after, found := strings.CutPrefix(strings.TrimSpace(line), "min_herdr_version = "); found {
				want = strings.Trim(strings.TrimSpace(after), `"`)
				break
			}
		}
		if want == "" {
			t.Fatal("the manifest names no minimum Herdr version")
		}
		if !strings.Contains(text, "Herdr "+want+"+") {
			t.Errorf("the manifest needs Herdr %s and the README does not say so", want)
		}
	})

	t.Run("the platforms the manifest claims", func(t *testing.T) {
		manifest, err := os.ReadFile("../../herdr-plugin.toml")
		if err != nil {
			t.Fatal(err)
		}
		// Herdr refuses to install a plugin on a platform its manifest does not
		// list, so this decides who can use it at all.
		for _, platform := range []struct{ manifest, prose string }{
			{`"linux"`, "Linux"},
			{`"macos"`, "macOS"},
		} {
			listed := strings.Contains(string(manifest), platform.manifest)
			said := strings.Contains(text, platform.prose)
			if listed != said {
				t.Errorf("the manifest %s %s and the README %s",
					map[bool]string{true: "supports", false: "does not support"}[listed],
					platform.prose,
					map[bool]string{true: "says it does", false: "does not"}[said])
			}
		}
	})
}
