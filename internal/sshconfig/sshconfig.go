// Package sshconfig reads the host aliases out of the user's SSH config, so
// the picker can offer the machines they already have set up.
package sshconfig

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Path is the user's SSH config location.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

// Hosts returns the concrete host aliases declared in the SSH config, in the
// order they appear. Patterns are skipped: "Host *" is a settings block, not a
// machine anyone can connect to.
func Hosts() []string {
	return hostsFrom(Path(), 0)
}

// maxIncludeDepth stops a cyclic Include from looping forever.
const maxIncludeDepth = 8

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
				if isPattern(alias) || seen[alias] {
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
func splitDirective(line string) []string {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	// "Host=bot" and "Host = bot" both mean "Host bot".
	if i := strings.IndexByte(line, '='); i >= 0 {
		if len(strings.Fields(line[:i])) == 1 {
			line = line[:i] + " " + line[i+1:]
		}
	}
	return strings.Fields(line)
}

// isPattern reports whether an alias is a wildcard or a negation rather than a
// machine that can be connected to.
func isPattern(alias string) bool {
	return strings.ContainsAny(alias, "*?!")
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
