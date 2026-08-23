package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
)

// Every machine this returns is offered in the menu and then handed to ssh as a
// destination, so what it may return is not a matter of taste. ~/.ssh/config is
// written by hand, sometimes generated, sometimes shared between machines with
// tooling that quotes differently -- and it is the one input here that this
// plugin neither writes nor validates before reading.

func FuzzHostsFrom(f *testing.F) {
	for _, seed := range []string{
		"Host bot\n",
		"Host bot prod\nHostName 10.0.0.1\n",
		"Host \"my server\"\n",
		"Host bot # work laptop\n",
		"Host=bot\n",
		"Host  =  bot\n",
		"Host *\nHost !prod\nHost web?\n",
		"Host \"\"\n",
		"Host \"unclosed\n",
		"host BOT\nHOST prod\n",
		"Include other\nHost bot\n",
		"Include /etc/does-not-exist\n",
		"Host -oProxyCommand=touch/x\n",
		"Host a\\tb\n",
		"\x00\x01Host bot\n",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, content string) {
		// Contained: Include resolves relative to ~/.ssh, and globbing it must
		// not reach anything real.
		home := t.TempDir()
		t.Setenv("HOME", home)
		path := filepath.Join(home, "config")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		hosts := hostsFrom(path, 0)

		seen := map[string]bool{}
		for _, host := range hosts {
			if host == "" {
				t.Fatalf("an empty machine name came out of %q", content)
			}
			if seen[host] {
				t.Fatalf("%q was offered twice from %q", host, content)
			}
			seen[host] = true

			// A pattern is a rule about machines, not a machine. Offering one
			// puts a row in the menu that cannot be connected to.
			if isPattern(host) {
				t.Fatalf("pattern %q was offered as a machine, from %q", host, content)
			}

			// The one that matters: this name becomes an argument to ssh, and
			// this file is the source nobody validates before reading it.
			//
			// Safety, not plausibility: a quoted alias may hold a space, which
			// is legal ssh and safe as one element of an argument list.
			if err := config.ValidTarget(host); err != nil {
				t.Fatalf("machine %q from %q would not be a safe ssh destination: %v",
					host, content, err)
			}
		}
	})
}

func FuzzSplitDirective(f *testing.F) {
	for _, seed := range []string{
		"", "   ", "Host bot", `Host "my server"`, "Host bot # note",
		`Host "a#b"`, "Host=bot", "Host = bot", `Host ""`, `Host "unclosed`,
		"a=b=c", `"quoted key" value`, "\tHost\tbot\t",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		fields := splitDirective(line)
		for _, field := range fields {
			// A field is a keyword or a value. Neither can hold a separator, or
			// it was never split in the first place.
			if strings.ContainsAny(field, " \t") && !strings.Contains(line, `"`) {
				t.Fatalf("splitDirective(%q) gave %q, which was not split", line, field)
			}
			if strings.ContainsRune(field, '"') {
				t.Fatalf("splitDirective(%q) gave %q, still carrying its quotes", line, field)
			}
		}
		// A line with nothing on it is not a directive.
		if strings.TrimSpace(stripComment(line)) == "" && len(fields) != 0 {
			t.Fatalf("splitDirective(%q) gave %d fields for a blank line", line, len(fields))
		}
	})
}
