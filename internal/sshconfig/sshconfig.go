// Package sshconfig reads the host aliases out of the user's SSH config, so
// the picker can offer the machines they already have set up.
package sshconfig

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
)

// Path is the user's SSH config location.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

// Unreadable reports why the SSH config could not be read, when it is there and
// cannot be.
//
// Hosts returns nothing in that case, which is also what it returns for the
// ordinary case of somebody having no SSH config at all -- so a file that is
// there and unreadable emptied the menu of every machine it knows about and
// said nothing. A file that is not there is not a problem and this stays quiet
// about it.
func Unreadable() string {
	path := Path()
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No SSH config, which is ordinary.
			return ""
		}
		return err.Error()
	}
	_ = file.Close()
	return ""
}

// Hosts returns the concrete host aliases declared in the SSH config, in the
// order they appear. Patterns are skipped: "Host *" is a settings block, not a
// machine anyone can connect to.
func Hosts() []string {
	return hostsFrom(Path(), 0)
}

// maxIncludeDepth stops a cyclic Include from looping forever.
//
// Set to what ssh itself allows, MAX_READCONF_DEPTH, and counted the same way:
// the main config is depth zero and each Include is one deeper. The limit is
// there for cycles, so it has to sit past any nesting somebody actually wrote
// -- and the only nesting that matters is the nesting ssh will read, since a
// machine this stops short of is one ssh can still connect to and the menu
// cannot offer. It was 8, which cut a legal chain in half without a word:
// machines past the cut are simply absent, and a missing Include is not an
// error to ssh either, so there is nothing to read anywhere.
const maxIncludeDepth = 16

func hostsFrom(path string, depth int) []string {
	if path == "" || depth > maxIncludeDepth {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	var hosts []string
	seen := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := splitDirective(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		keyword := strings.ToLower(fields[0])

		switch keyword {
		case "host":
			for _, alias := range fields[1:] {
				if !connectable(alias) || seen[alias] {
					continue
				}
				seen[alias] = true
				hosts = append(hosts, alias)
			}
		case "include":
			for _, included := range fields[1:] {
				for _, match := range expand(included) {
					for _, host := range hostsFrom(match, depth+1) {
						if !seen[host] {
							seen[host] = true
							hosts = append(hosts, host)
						}
					}
				}
			}
		}
	}
	return hosts
}

// splitDirective turns a config line into its keyword and values.
//
// It drops comments, which are legal on a Host line and would otherwise be read
// as machine names — "Host bot # work laptop" offering "#", "work" and "laptop"
// alongside "bot". It also accepts the "Key=Value" spelling that ssh allows.
//
// And it honours double quotes, which ssh allows around a name containing a
// space. Splitting on spaces turned `Host "my server"` into two machines called
// `"my` and `server"`, both offered in the menu and neither of them anything
// ssh could connect to.
func splitDirective(line string) []string {
	line = strings.TrimSpace(stripComment(line))
	if line == "" {
		return nil
	}
	// "Host=bot" and "Host = bot" both mean "Host bot".
	if i := strings.IndexByte(line, '='); i >= 0 && !strings.ContainsRune(line[:i], '"') {
		if len(strings.Fields(line[:i])) == 1 {
			line = line[:i] + " " + line[i+1:]
		}
	}

	var fields []string
	var current strings.Builder
	quoted, started := false, false
	flush := func() {
		if started || current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
			started = false
		}
	}
	for _, r := range line {
		switch {
		case r == '"':
			// An empty pair of quotes is still a value, so that a name written
			// as "" is one field rather than none.
			quoted = !quoted
			started = true
		case !quoted && (r == ' ' || r == '\t'):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return fields
}

// stripComment removes a trailing comment, leaving a # inside quotes alone.
func stripComment(line string) string {
	quoted := false
	for i, r := range line {
		switch r {
		case '"':
			quoted = !quoted
		case '#':
			if !quoted {
				return line[:i]
			}
		}
	}
	return line
}

// isPattern reports whether an alias is a wildcard or a negation rather than a
// machine that can be connected to.
func isPattern(alias string) bool {
	return strings.ContainsAny(alias, "*?!")
}

// connectable reports whether an alias names a machine somebody could connect
// to, as opposed to a rule about machines or a line ssh would refuse.
//
// Filtered here rather than left to callers. This returns machines, and the one
// caller there is happened to check -- so the next one would have inherited a
// menu entry for `Host -oProxyCommand=...`, which ssh reads as an option and
// not a destination.
//
// `Host ""` is a legal line that names nothing. Quotes are honoured so a name
// may hold a space, and the rule that allows that turns an empty pair of them
// into an empty field.
func connectable(alias string) bool {
	return alias != "" && !isPattern(alias) && config.ValidTarget(alias) == nil
}

// expand resolves an Include path, which may be relative to ~/.ssh and may
// contain globs.
func expand(pattern string) []string {
	if strings.HasPrefix(pattern, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			pattern = filepath.Join(home, pattern[2:])
		}
	}
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(filepath.Dir(Path()), pattern)
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	return matches
}
