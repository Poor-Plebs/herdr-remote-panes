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
