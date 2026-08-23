package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The help text is a fourth place that describes this plugin, after the
// manifest, the package comments and the notification -- and like those, it had
// gone on describing it as it was when mirroring was the only thing it did. Two
// contracts are checkable and worth holding: every command the binary handles
// is listed, and every command listed is one it handles.

func commandsInSource(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	body = body[strings.Index(body, "func run("):]
	body = body[:strings.Index(body, "func usage(")]

	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`case ("[a-z-]+"(?:, "[a-z-]+")*):`).FindAllStringSubmatch(body, -1) {
		for _, name := range strings.Split(m[1], ", ") {
			found[strings.Trim(name, `"`)] = true
		}
	}
	// A test that finds no commands would pass every assertion below without
	// checking anything, which is the failure mode of reading source in a test.
	for _, want := range []string{"daemon", "connect", "status", "open-tab"} {
		if !found[want] {
			t.Fatalf("the command parser found %v, which does not include %q -- it has stopped working",
				keysOf(found), want)
		}
	}
	return found
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestUsageListsEveryCommand(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	help := string(raw)
	help = help[strings.Index(help, "func usage("):]

	for name := range commandsInSource(t) {
		switch name {
		case "help", "-h", "--help":
			continue // listing the help in the help would be odd
		}
		if !strings.Contains(help, "\n  "+name+" ") && !strings.Contains(help, "\n  "+name+"\n") {
			t.Errorf("the help does not mention %q, so nobody will find it", name)
		}
	}
}

func TestUsageDoesNotOfferWhatIsNotThere(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	help := body[strings.Index(body, "func usage("):]
	handled := commandsInSource(t)

	// Every indented word at the start of a help line should be a command.
	for _, line := range strings.Split(help, "\n") {
		m := regexp.MustCompile(`^  ([a-z][a-z-]+) `).FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if !handled[m[1]] {
			t.Errorf("the help offers %q but nothing handles it: %q", m[1], strings.TrimSpace(line))
		}
	}
}

func TestUsageDoesNotCallMirroringTheUsualThing(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	help := string(raw)
	help = help[strings.Index(help, "func usage("):]

	// The first line is what somebody reads before anything else.
	first := strings.SplitN(help[strings.Index(help, "herdr-remote-panes"):], "\n", 2)[0]
	if strings.Contains(strings.ToLower(first), "mirror") {
		t.Errorf("the first line of the help is about mirroring, which is off by default: %q", first)
	}
}
