package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuotedHostNames(t *testing.T) {
	// ssh allows a name containing a space to be written in double quotes.
	// Splitting on spaces turned `Host "my server"` into two machines called
	// `"my` and `server"`, both offered in the menu and neither of them
	// anything ssh could connect to.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := strings.Join([]string{
		`Host "my server"`,
		`  HostName 1.2.3.4`,
		`Host plain`,
		`Host "quoted" also-plain`,
		`Host with-hash # a comment "with quotes" in it`,
		`Host "has # inside"`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, ".ssh", "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)

	got := Hosts()
	want := []string{"my server", "plain", "quoted", "also-plain", "with-hash", "has # inside"}

	if len(got) != len(want) {
		t.Fatalf("aliases = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("aliases = %q, want %q", got, want)
			break
		}
	}
	for _, alias := range got {
		if strings.Contains(alias, `"`) {
			t.Errorf("%q still carries its quotes", alias)
		}
	}
}

func TestSplitDirectiveKeepsWhatItAlwaysDid(t *testing.T) {
	// The behaviour the quoting was added around.
	cases := map[string][]string{
		"Host bot":                  {"Host", "bot"},
		"Host bot # work laptop":    {"Host", "bot"},
		"Host=bot":                  {"Host", "bot"},
		"Host = bot":                {"Host", "bot"},
		"  Host   bot   ci  ":       {"Host", "bot", "ci"},
		"# a whole line of comment": nil,
		"":                          nil,
		"   ":                       nil,
		`Host "spaced name"`:        {"Host", "spaced name"},
		`Include ~/.ssh/conf.d/*`:   {"Include", "~/.ssh/conf.d/*"},
	}
	for line, want := range cases {
		got := splitDirective(line)
		if len(got) != len(want) {
			t.Errorf("splitDirective(%q) = %q, want %q", line, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("splitDirective(%q) = %q, want %q", line, got, want)
				break
			}
		}
	}
}
