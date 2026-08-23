package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestTheREADMEDocumentsTheRealDefaults holds the settings table to the code.
//
// The table says what happens when a setting is left out, which is the whole
// reason somebody reads it: they are deciding whether to write the setting at
// all. Twenty of them were checked by eye once, which lasts exactly as long as
// the memory of having done it -- and one of the settings audited that way
// turned out not to work at all.
func TestTheREADMEDocumentsTheRealDefaults(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}

	// The defaults rendered the way the file writes them, so the comparison is
	// against what somebody would actually find in it.
	raw, err := json.Marshal(Defaults().normalized())
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(raw, &actual); err != nil {
		t.Fatal(err)
	}

	rows := regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| `([^`]*)` \\|").FindAllStringSubmatch(string(readme), -1)
	if len(rows) < 10 {
		t.Fatalf("found %d documented defaults, which is too few -- the table has moved", len(rows))
	}

	checked := 0
	for _, row := range rows {
		name, documented := row[1], row[2]
		value, ok := actual[name]
		if !ok {
			t.Errorf("the table documents %q, which is not a setting", name)
			continue
		}
		if got := fmt.Sprintf("%v", value); got != documented {
			t.Errorf("%s: the table says %q, the default is %q", name, documented, got)
		}
		checked++
	}
	t.Logf("checked %d documented defaults against the code", checked)
}

func TestEverySettingWithADefaultIsInTheTable(t *testing.T) {
	// The other direction: a setting this fills in and never mentions is one
	// somebody cannot know about.
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(Defaults().normalized())
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(raw, &actual); err != nil {
		t.Fatal(err)
	}

	for name := range actual {
		if name == "hosts" {
			continue // documented a row at a time as hosts[].something
		}
		if !strings.Contains(string(readme), "| `"+name+"` |") {
			t.Errorf("the settings table does not mention %q", name)
		}
	}
}
