package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
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
	readme, err := os.ReadFile(repoFile(t, "README.md"))
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
	readme, err := os.ReadFile(repoFile(t, "README.md"))
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

func TestEveryPerMachineSettingIsInTheTableAndNoOthersAre(t *testing.T) {
	// The settings table is where somebody finds out what may go in a machine
	// entry, and the two ways it can be wrong are opposite. A field missing
	// from it is a setting nobody can know about. A row for something that is
	// not a field is worse: it reads as an instruction, the key is accepted by
	// the file, and it does nothing -- which is how a global setting written
	// inside a machine entry looks exactly like it worked.
	readme, err := os.ReadFile(repoFile(t, "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	fields := jsonNames(reflect.TypeOf(Host{}))
	if len(fields) == 0 {
		t.Fatal("no fields found on a machine entry; the reflection has moved")
	}
	for name := range fields {
		if !strings.Contains(string(readme), "| `hosts[]."+name+"` |") {
			t.Errorf("a machine entry takes %q and the settings table does not mention it", name)
		}
	}

	// And the other way: every row claiming to be a per-machine setting is one.
	rows := regexp.MustCompile(`\| `+"`"+`hosts\[\]\.(\w+)`+"`"+` \|`).FindAllStringSubmatch(string(readme), -1)
	if len(rows) == 0 {
		t.Fatal("no per-machine rows found in the README; the table has moved")
	}
	for _, row := range rows {
		if !fields[row[1]] {
			t.Errorf("the settings table offers hosts[].%s, which a machine entry "+
				"does not take -- so it is accepted, ignored, and looks like it worked", row[1])
		}
	}
}

func TestTheSidebarPictureIsWhatTheDefaultsDraw(t *testing.T) {
	// The first thing in the README is a picture of the sidebar, and it is
	// entirely made of defaults: the glyph beside a machine, the two spaces
	// after it, and the shape of a terminal's name. Change any of those and the
	// picture is wrong -- and a format string is not something anybody would
	// think to check a picture against.
	//
	// Built from the defaults rather than compared as text, so that a machine
	// renamed in the picture, or a line added to it, needs nothing here.
	readme, err := os.ReadFile(repoFile(t, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	d := Defaults()

	up := d.WorkspaceLabelFor(Host{Target: "workbox"}, true)
	if !strings.Contains(string(readme), up) {
		t.Errorf("a machine that can be reached is drawn as %q, and the sidebar "+
			"picture does not show that", up)
	}
	down := d.WorkspaceLabelFor(Host{Target: "ci"}, false)
	if !strings.Contains(string(readme), down) {
		t.Errorf("a machine that is not answering is drawn as %q, and the sidebar "+
			"picture does not show that", down)
	}
	// The two must differ, or the picture is showing one thing twice and the
	// marker says nothing.
	if up == down {
		t.Errorf("a reachable machine and an unreachable one are both drawn as %q", up)
	}

	// And a terminal on one of them, named the way terminals are named.
	terminal := strings.NewReplacer("{name}", "shell", "{host}", "workbox").Replace(d.LabelFormat)
	if !strings.Contains(string(readme), terminal) {
		t.Errorf("a terminal is named %q, and the sidebar picture does not show that", terminal)
	}
}

func TestTheREADMESaysHowSoonAMachineStops(t *testing.T) {
	// The troubleshooting entry says how long a machine gets before it is left
	// alone, in seconds, because that is the question somebody has when their
	// machines have all stopped after a laptop woke up. The number is the poll
	// interval, and a poll interval changed here would leave the page saying
	// something that used to be true.
	readme, err := os.ReadFile(repoFile(t, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	poll := Defaults().PollInterval
	if poll == "" {
		t.Fatal("there is no default poll interval")
	}
	if !strings.Contains(string(readme), "which is `"+poll+"` later by default") {
		t.Errorf("the README does not say the second attempt comes %s later, "+
			"which is what the poll interval makes it", poll)
	}
}

func TestTheProseAgreesWithTheTableAboutDefaults(t *testing.T) {
	// The settings table is held to the code, and the prose around it is not.
	// So the paragraph explaining placement went on saying it defaults to
	// "split" for twenty commits after the code stopped -- while the table two
	// hundred lines below said "follow" and passed its own check.
	//
	// A reader meets the paragraph first, and it is the half that explains
	// what the setting means rather than listing it.
	raw, err := os.ReadFile(repoFile(t, "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	actual := map[string]string{}
	body, err := json.Marshal(Defaults().normalized())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for name, value := range fields {
		if text, ok := value.(string); ok {
			actual[name] = text
		}
	}

	// Every "defaults to `value`" the prose makes, matched back to the nearest
	// setting named before it. Written this way round because the two are
	// often in different clauses and across a line break -- "That is
	// `placement`, and it defaults to\n`follow`" -- so a pattern that wants
	// them adjacent matches neither of the two claims in the file, and passes
	// by finding nothing.
	prose := string(raw)
	claim := regexp.MustCompile("defaults to\\s+`([^`]+)`")
	named := regexp.MustCompile("`([a-z_]+)`")
	checked := 0
	for _, m := range claim.FindAllStringSubmatchIndex(prose, -1) {
		claimed := prose[m[2]:m[3]]
		from := max(0, m[0]-300)
		var setting string
		for _, back := range named.FindAllStringSubmatch(prose[from:m[0]], -1) {
			if _, known := actual[back[1]]; known {
				setting = back[1]
			}
		}
		if setting == "" {
			continue
		}
		checked++
		if want := actual[setting]; claimed != want {
			t.Errorf("the prose says %s defaults to %q and it defaults to %q",
				setting, claimed, want)
		}
	}
	if checked == 0 {
		t.Error("no claim about a default was found in the prose, so this is " +
			"checking nothing; the wording it looks for has changed")
	}
}
