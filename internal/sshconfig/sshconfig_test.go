package sshconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostsFrom(t *testing.T) {
	dir := t.TempDir()
	included := filepath.Join(dir, "extra")
	if err := os.WriteFile(included, []byte("Host fromInclude\n  HostName 10.0.0.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	config := filepath.Join(dir, "config")
	body := `# comment
Host prod staging
  HostName example.com
  User root

Host *
  ServerAliveInterval 15

Host !nope
  HostName nowhere

Host bot
  HostName 1.2.3.4

Include ` + included + `
`
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got := hostsFrom(config, 0)
	want := []string{"prod", "staging", "bot", "fromInclude"}
	if len(got) != len(want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("hosts[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsPattern(t *testing.T) {
	// "Host *" is a settings block, not a machine.
	for _, alias := range []string{"*", "*.example.com", "web?", "!nope"} {
		if !isPattern(alias) {
			t.Errorf("%q should be treated as a pattern", alias)
		}
	}
	for _, alias := range []string{"bot", "prod", "gh-runner", "10.0.0.1"} {
		if isPattern(alias) {
			t.Errorf("%q should be treated as a machine", alias)
		}
	}
}

func TestReadsRealSSHConfig(t *testing.T) {
	hosts := Hosts()
	if len(hosts) == 0 {
		t.Skip("no ssh config hosts on this machine")
	}
	t.Logf("hosts: %v", hosts)
}

func TestSplitDirective(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{
			// A comment on a Host line is legal, and reading it as values
			// offered "#", "work" and "laptop" in the menu as machines.
			name: "a trailing comment is dropped",
			line: "Host bot # work laptop",
			want: []string{"Host", "bot"},
		},
		{"a whole-line comment yields nothing", "# just a note", nil},
		{"the Key=Value spelling is accepted", "Host=bot", []string{"Host", "bot"}},
		{"spaces around = are fine", "Host = bot", []string{"Host", "bot"}},
		{"indentation is ignored", "   Host   bot  ", []string{"Host", "bot"}},
		{"several aliases survive", "Host bot do-bot", []string{"Host", "bot", "do-bot"}},
		{"a blank line yields nothing", "   ", nil},
		{
			// An = inside a value, as in ProxyCommand, must not be treated as
			// the Key=Value spelling.
			name: "an = inside a value is left alone",
			line: "ProxyCommand ssh -W %h:%p jump=host",
			want: []string{"ProxyCommand", "ssh", "-W", "%h:%p", "jump=host"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitDirective(tt.line)
			if len(got) != len(tt.want) {
				t.Fatalf("splitDirective(%q) = %v, want %v", tt.line, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("field %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHostsIgnoresComments(t *testing.T) {
	dir := t.TempDir()
	body := "Host bot # my droplet\n  HostName 1.2.3.4\n\nHost=equals\n  HostName 5.6.7.8\n\n# Host commented-out\n"
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got := hostsFrom(path, 0)
	want := []string{"bot", "equals"}
	if len(got) != len(want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("host %d = %q, want %q", i, got[i], want[i])
		}
	}
}
