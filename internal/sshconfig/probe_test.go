package sshconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeOddSSHConfigs(t *testing.T) {
	for _, tt := range []struct{ what, body string }{
		{"ordinary", "Host bot\n  HostName 10.0.0.1\n"},
		{"several on one line", "Host bot prod ci\n"},
		{"a wildcard", "Host *\n  User deploy\n"},
		{"a partial wildcard", "Host web-*\nHost bot\n"},
		{"a negation", "Host !bad good\n"},
		{"a question mark", "Host web?\n"},
		{"equals form", "Host=bot\n"},
		{"tabs and trailing space", "\tHost\tbot \t\n"},
		{"mixed case keyword", "hOsT bot\n"},
		{"quoted with a space", "Host \"my server\"\n"},
		{"a comment after the name", "Host bot # the build machine\n"},
		{"a Match block", "Match host bot\n  User deploy\nHost ci\n"},
		{"crlf line endings", "Host bot\r\nHost ci\r\n"},
		{"no trailing newline", "Host bot"},
		{"empty file", ""},
		{"a very long name", "Host " + string(make([]byte, 0)) + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"},
		{"duplicate hosts", "Host bot\nHost bot\n"},
		{"a host called Host", "Host Host\n"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Logf("%-24s -> %q", tt.what, Hosts())
		})
	}
}
