package syncd

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestManifestDescriptionsDoNotClaimMirroring guards a surface that has no
// other check on it.
//
// The action titles and descriptions are what Herdr shows in its own list of
// what a plugin can do, and nothing here reads them, so they went stale without
// anything noticing. They described the plugin as it was when mirroring was the
// main mode rather than an experimental one -- and disconnect's said it closed
// "its mirror panes", which was an accurate description of a bug: it left a
// plain SSH machine's terminals open.
func TestManifestDescriptionsDoNotClaimMirroring(t *testing.T) {
	raw, err := os.ReadFile("../../herdr-plugin.toml")
	if err != nil {
		t.Fatal(err)
	}

	descriptions := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "description = ") {
			continue
		}
		descriptions++
		lower := strings.ToLower(line)
		// Mirroring is off by default, so a description that only describes
		// mirroring describes what most people will not be doing. Saying it is
		// optional, or naming it alongside the ordinary case, is fine.
		mentions := strings.Contains(lower, "mirror")
		hedged := strings.Contains(lower, "optional") ||
			strings.Contains(lower, "when mirroring") ||
			strings.Contains(lower, "turns mirroring")
		if mentions && !hedged {
			t.Errorf("description describes mirroring as though it were the usual thing:\n  %s", line)
		}
	}

	// A test that finds no descriptions checks nothing while passing, which is
	// the failure mode of reading a file in a test.
	if descriptions < 5 {
		t.Fatalf("found %d descriptions in the manifest; the format has moved", descriptions)
	}
}

func TestManifestListsWhatTheCodeImplements(t *testing.T) {
	// Every action in the manifest runs this binary with its id as the
	// argument, so an id here that main does not handle is a menu entry that
	// fails when picked.
	manifest, err := os.ReadFile("../../herdr-plugin.toml")
	if err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile("../../main.go")
	if err != nil {
		t.Fatal(err)
	}

	found := 0
	for _, line := range strings.Split(string(manifest), "\n") {
		if !strings.HasPrefix(line, "id = ") {
			continue
		}
		id := strings.Trim(strings.TrimPrefix(line, "id = "), `"`)
		if strings.Contains(id, ".") {
			continue // the plugin's own id, not an action
		}
		found++
		// Looked for as a quoted word rather than as "case X", because several
		// share a case: open and open-tab differ only in the placement they
		// ask for.
		if !strings.Contains(string(main), `"`+id+`"`) {
			t.Errorf("the manifest offers %q but nothing handles it", id)
		}
	}

	// As above: finding nothing must not read as finding nothing wrong.
	if found < 5 {
		t.Fatalf("found %d actions in the manifest; the format has moved", found)
	}
}

func TestTheREADMEOnlyBindsActionsThatExist(t *testing.T) {
	// The README shows keybindings to copy into config.toml. An action id that
	// does not exist produces a binding that does nothing, and Herdr will not
	// say why -- the same silence as a keybinding that clashes with a built-in.
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile("../../herdr-plugin.toml")
	if err != nil {
		t.Fatal(err)
	}

	referenced := regexp.MustCompile(`poorplebs\.remote-panes\.([a-z-]+)`).
		FindAllStringSubmatch(string(readme), -1)
	if len(referenced) == 0 {
		t.Fatal("the README references no actions; this test needs rewriting")
	}

	for _, m := range referenced {
		if !strings.Contains(string(manifest), `id = "`+m[1]+`"`) {
			t.Errorf("the README binds %q, which is not an action this offers", m[0])
		}
	}
}
