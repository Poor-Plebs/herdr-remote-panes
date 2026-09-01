package sshconfig

import (
	"fmt"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/project"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestOnlyMachinesSomebodyCouldConnectToAreOffered(t *testing.T) {
	// Hosts() returns machines, so the filtering belongs here rather than in
	// whichever caller thought to do it. The one caller there was did check --
	// so a second one would have inherited a menu row for `Host -oProxy...`,
	// which ssh reads as an option and not a destination.
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "an alias ssh would read as an option",
			content: "Host bot\nHost -oProxyCommand=something\n",
			want:    []string{"bot"},
		},
		{
			name:    "an empty pair of quotes names nothing",
			content: "Host \"\"\nHost bot\n",
			want:    []string{"bot"},
		},
		{
			name:    "a quoted name with a space is a machine",
			content: "Host \"my server\"\nHost bot\n",
			want:    []string{"my server", "bot"},
		},
		{
			name:    "patterns are rules about machines, not machines",
			content: "Host *\nHost !prod\nHost web?\nHost bot\n",
			want:    []string{"bot"},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			got := hostsFrom(path, 0)
			if len(got) != len(tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got %q, want %q", got, tt.want)
					break
				}
			}
		})
	}
}

func TestAnIncludeThatIncludesItselfStops(t *testing.T) {
	// maxIncludeDepth exists so a cycle cannot loop forever, and nothing tested
	// it -- mutation testing walked the limit off by one and every test stayed
	// green. A config that includes itself is not exotic: two machines sharing
	// a dotfiles repo where each end includes the other's fragment is the
	// ordinary way to arrive at one.
	home := t.TempDir()
	t.Setenv("HOME", home)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}

	// a includes b, b includes a, and both name a machine so the answer is
	// checkable rather than merely finite.
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(ssh, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("config", "Include a\nHost bot\n")
	write("a", "Include b\nHost from-a\n")
	write("b", "Include a\nHost from-b\n")

	done := make(chan []string, 1)
	go func() { done <- Hosts() }()

	select {
	case hosts := <-done:
		want := map[string]bool{"bot": true, "from-a": true, "from-b": true}
		for _, host := range hosts {
			if !want[host] {
				t.Errorf("unexpected machine %q from %q", host, hosts)
			}
			delete(want, host)
		}
		if len(want) > 0 {
			t.Errorf("a cycle swallowed machines that are in the files: %v", want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a config that includes itself never finished being read")
	}
}

func TestAnIncludeIsFoundRelativeToTheSSHDirectory(t *testing.T) {
	// ssh resolves a bare Include against ~/.ssh, and "~/" against the home
	// directory. Getting either wrong loses every machine in the included file
	// and says nothing, because a missing include is not an error to ssh.
	home := t.TempDir()
	t.Setenv("HOME", home)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(ssh, "conf.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		if err := os.WriteFile(filepath.Join(home, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".ssh/config", "Include conf.d/*\nInclude ~/elsewhere\nHost bot\n")
	write(".ssh/conf.d/work", "Host work-box\n")
	write("elsewhere", "Host tilde-box\n")

	got := map[string]bool{}
	for _, host := range Hosts() {
		got[host] = true
	}
	for _, want := range []string{"bot", "work-box", "tilde-box"} {
		if !got[want] {
			t.Errorf("machine %q was not found; got %v", want, got)
		}
	}
}

func TestIncludesAreFollowedAsDeepAsSSHFollowsThem(t *testing.T) {
	// The depth limit is there for cycles, and it has to sit past any nesting
	// somebody actually wrote -- which means past anything ssh will read, since
	// a machine this stops short of is one ssh can still connect to and the
	// menu cannot offer. It was set to half what ssh allows.
	//
	// An off-by-one here does not fail loudly. The machines past the cut are
	// simply not in the menu, and a missing Include is not an error to ssh
	// either, so there is nothing to read anywhere.
	home := t.TempDir()
	t.Setenv("HOME", home)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}

	// config includes link0, link0 includes link1, and so on, each naming a
	// machine. The main config is depth zero and each Include is one deeper, so
	// link0 is read at depth one and link<n-1> at exactly the limit. One more
	// is written past it, to hold the other side of the boundary.
	const beyond = maxIncludeDepth
	for i := 0; i <= beyond; i++ {
		content := fmt.Sprintf("Host machine%d\n", i)
		if i < beyond {
			content = fmt.Sprintf("Include link%d\n", i+1) + content
		}
		if err := os.WriteFile(filepath.Join(ssh, fmt.Sprintf("link%d", i)),
			[]byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ssh, "config"), []byte("Include link0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, host := range Hosts() {
		got[host] = true
	}
	for i := 0; i < beyond; i++ {
		if machine := fmt.Sprintf("machine%d", i); !got[machine] {
			t.Errorf("%q was not found: a chain of includes ssh would follow was cut short", machine)
		}
	}
	// And the far side, so the limit is pinned in both directions rather than
	// only against being too small.
	if machine := fmt.Sprintf("machine%d", beyond); got[machine] {
		t.Errorf("%q was read from past the depth limit", machine)
	}
}

func TestAnIncludeThatCannotBeReadIsSaidSo(t *testing.T) {
	// An Include is how a config is kept in pieces -- a directory per team, a
	// file per environment, generated fragments -- so a file that cannot be
	// read is a whole group of machines missing from the menu at once.
	//
	// Unreadable used to look at the top-level file alone: stat it, size it,
	// scan it. Every one of those had a twin in the reading, and the two
	// drifted twice. First over the size bound, which is d3fa765. Then over
	// Include, which this one never followed -- so a config whose fragment was
	// unreadable was pronounced fine while its machines were gone.
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	inc := filepath.Join(ssh, "config.d")
	if err := os.MkdirAll(inc, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	top := filepath.Join(ssh, "config")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(top, "Include "+inc+"/*\n\nHost toplevel\n")
	write(filepath.Join(inc, "10-fine"), "Host fromtheinclude\n")

	// Nothing wrong yet: an ordinary split config.
	if why := Unreadable(); why != "" {
		t.Fatalf("an ordinary config with an include is reported as %q", why)
	}
	if hosts := Hosts(); len(hosts) != 2 {
		t.Fatalf("the machines are %v, and there are two", hosts)
	}

	// One fragment past the size bound. Its machines go, and that is the whole
	// of what somebody sees unless this says otherwise.
	write(filepath.Join(inc, "20-big"),
		"Host toobig\n"+strings.Repeat("# padding\n", (theConfigLimit/10)+10))

	hosts := Hosts()
	for _, host := range hosts {
		if host == "toobig" {
			t.Fatal("the oversized fragment was read after all, so this tests nothing")
		}
	}
	why := Unreadable()
	if why == "" {
		t.Fatalf("machines are missing (%v) and nothing says why", hosts)
	}
	// Which file, because the menu says "could not read ~/.ssh/config" around
	// this and that is not the file to go and look at.
	if !strings.Contains(why, "20-big") {
		t.Errorf("the reason is %q, which does not name the file it is about", why)
	}
	if !strings.Contains(why, "larger than") {
		t.Errorf("the reason is %q, which does not say what is wrong with it", why)
	}
}

func TestTheBoundsTheDocsGiveAreTheBoundsThatApply(t *testing.T) {
	// The page says why machines can be missing from the menu, and every
	// reason is a number here. Numbers in prose go stale in silence: the size
	// bound already moved once, taking a silent failure with it, and the page
	// that explains it to somebody staring at an empty menu is the last place
	// that should still be quoting the old one.
	//
	// Derived where a number can be written the way the page writes it, and
	// held to the constant where it cannot.
	docs, err := project.DocsText()
	if err != nil {
		t.Fatal(err)
	}

	// Sizes are given in megabytes, because "1048576 bytes" in a sentence is
	// a number nobody checks.
	if maxConfigBytes != 1<<20 || maxConfigLine != 1<<20 {
		t.Errorf("a config may now be %d bytes and a line %d, and the page says 1 MB "+
			"for both", maxConfigBytes, maxConfigLine)
	}
	if !strings.Contains(docs, "larger than 1 MB") {
		t.Error("the page does not say how large a config may be")
	}

	for what, bound := range map[string]int{
		"files one Include may match": maxIncludeMatches,
		"how deep Includes may nest":  maxIncludeDepth,
	} {
		if !strings.Contains(docs, fmt.Sprintf("%d", bound)) {
			t.Errorf("the page does not give %s, which is %d", what, bound)
		}
	}
	if !strings.Contains(docs, "two seconds") {
		t.Errorf("the page does not say how long an Include may take to expand, "+
			"which is %s", includeGlobBudget)
	}
	if includeGlobBudget != 2*time.Second {
		t.Errorf("an Include may now take %s to expand and the page says two seconds",
			includeGlobBudget)
	}
}

func TestAHostLineThatNamesNoMachineSaysSo(t *testing.T) {
	// A Host line that ssh cannot be pointed at is left out of the menu, which
	// is right, and was done in silence, which is how a machine somebody wrote
	// down looks like one they deleted. `Host -oProxyCommand=...` is the one
	// that looks most like a machine and is read by ssh as an option.
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ssh, "config"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The ordinary shapes stay quiet: every config has a wildcard block, and
	// naming nothing is a legal line.
	write("Host *\n  ServerAliveInterval 15\n\nHost \"\"\n\nHost bot\n")
	if why := Unreadable(); why != "" {
		t.Errorf("an ordinary config is reported as %q", why)
	}
	if hosts := Hosts(); len(hosts) != 1 || hosts[0] != "bot" {
		t.Errorf("the machines are %v, and there is one", hosts)
	}

	// A name ssh would read as an option is not offered, and now says so.
	write("Host bot\n\nHost -oProxyCommand=curl\n")
	hosts := Hosts()
	if len(hosts) != 1 || hosts[0] != "bot" {
		t.Fatalf("the machines are %v; the fixture wants one usable and one not", hosts)
	}
	why := Unreadable()
	if why == "" {
		t.Fatal("a Host line was left out of the menu and nothing says why")
	}
	if !strings.Contains(why, "dash") {
		t.Errorf("the reason is %q, which does not say what is wrong with the name", why)
	}
}

func TestOneReadKeepsAHandfulOfReasonsRatherThanAllOfThem(t *testing.T) {
	// Only the first reason is ever shown: the menu has one line for it. A
	// config of nothing but unusable Host lines -- which a generator can
	// produce -- built one string per line and read none of them, on every
	// menu that opened.
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	var b strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, "Host -opt%d\n", i)
	}
	b.WriteString("Host bot\n")
	if err := os.WriteFile(filepath.Join(ssh, "config"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	read := newReading(Path())
	hosts := hostsRead(Path(), 0, read)
	// A number, not the constant. Comparing against maxReasonsKept means
	// raising it raises what this expects, so the bound could grow to anything
	// and this would go on passing -- which it did: an audit that multiplied
	// every max in the tree by a thousand found this one held by nothing.
	if len(read.why) > 16 {
		t.Errorf("a config with five thousand unusable lines kept %d reasons, "+
			"and the point of the bound is that it is a handful", len(read.why))
	}
	// Kept enough to say something, and the first is still the first.
	if len(read.why) == 0 {
		t.Fatal("five thousand unusable lines produced no reason at all")
	}
	if !strings.Contains(read.why[0], "-opt0") {
		t.Errorf("the first reason is %q, and the first bad line names -opt0", read.why[0])
	}
	// And the machine after them is still found: giving up on collecting
	// reasons must not be giving up on reading.
	if len(hosts) != 1 || hosts[0] != "bot" {
		t.Errorf("the machines are %v, and the config has one usable line", hosts)
	}
}
