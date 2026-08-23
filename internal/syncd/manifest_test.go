package syncd

import (
	"os"
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

	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "description = ") {
			continue
		}
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

	for _, line := range strings.Split(string(manifest), "\n") {
		if !strings.HasPrefix(line, "id = ") {
			continue
		}
		id := strings.Trim(strings.TrimPrefix(line, "id = "), `"`)
		if strings.Contains(id, ".") {
			continue // the plugin's own id, not an action
		}
		// Looked for as a quoted word rather than as "case X", because several
		// share a case: open and open-tab differ only in the placement they
		// ask for.
		if !strings.Contains(string(main), `"`+id+`"`) {
			t.Errorf("the manifest offers %q but nothing handles it", id)
		}
	}
}
