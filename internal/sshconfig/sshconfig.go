// Package sshconfig reads the host aliases out of the user's SSH config, so
// the picker can offer the machines they already have set up.
//
// # What bounds the reading
//
// This runs to draw the menu, so anything it does slowly is a keypress that
// does not come back, and anything it stops doing quietly is machines missing
// as though they had been deleted. An Include is a glob and a glob matches
// whatever is there, so the file names work rather than describing it.
//
// Six limits, each added after something demonstrated the need, and each said
// again where it is defined:
//
//   - maxIncludeDepth, on how deep a chain of includes goes
//   - the record of files already read, on how a chain that branches
//     multiplies -- three fragments including their own directory is three to
//     the sixteenth reads without it
//   - maxIncludeMatches, on how many files one Include may pull in
//   - includeGlobBudget, on how long finding those files may take, which the
//     count cannot bound because finding them is one call
//   - maxConfigBytes, on how large a file read as configuration may be
//   - maxConfigLine, on how long one line in it may be
//
// What is not bounded is the number of distinct files reached altogether, and
// that was measured rather than assumed: two thousand real fragments read in
// two hundred milliseconds. Reading configuration is cheap. What is expensive
// is always something that is not configuration, which is what the limits
// above are about.
package sshconfig

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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

// maxConfigLine is the longest line read out of an SSH config.
//
// bufio.Scanner stops at 64KB by default and says so only through Err, which
// nothing here was asking. So one long line -- a comment, a ProxyCommand, an
// Include that matched something without newlines in it -- ended the scan, and
// every machine after that line was quietly missing from the menu. Quietly is
// the problem: a machine that is absent looks like one somebody deleted.
//
// A megabyte is past anything a person writes and still bounded, which matters
// because the line is held in memory and this file is not this plugin's to
// size.
const maxConfigLine = 1 << 20

// scanConfig reads a config a line at a time, with that bound.
func scanConfig(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxConfigLine)
	return scanner
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
	// Asked about before it is opened, for the same reason hostsFrom does:
	// opening a pipe waits for somebody to write to it, and opening a terminal
	// waits for somebody to type. This runs to draw the menu, so either is a
	// menu that never appears -- and this function exists to explain an empty
	// one, which makes hanging in it a particularly poor failure.
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No SSH config, which is ordinary.
			return ""
		}
		return err.Error()
	}
	if !info.Mode().IsRegular() {
		// Said plainly, because the file is there and readable in the sense
		// somebody would test with `cat`, and the machines are still missing.
		return "not a regular file, so it is not read"
	}
	// The same bound the reading applies. Without this the two disagreed: a
	// config past the bound was skipped by one and pronounced fine by the
	// other, so the menu was empty with nothing said -- which is the failure
	// this function exists to prevent, introduced by the bound that fixed
	// another one.
	if info.Size() > maxConfigBytes {
		return fmt.Sprintf("larger than %d bytes, so it is not read", maxConfigBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return err.Error()
	}
	defer file.Close()

	// Read through it, because opening is not the only way a config fails to
	// be read: a line past the bound ends the scan, and the machines after it
	// are missing with nothing said. This file is small; the menu already
	// reads all of it.
	scanner := scanConfig(file)
	for scanner.Scan() {
	}
	if err := scanner.Err(); err != nil {
		return err.Error()
	}
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

// maxConfigBytes is the largest file this will read as configuration.
//
// A config fragment is kilobytes: host blocks somebody typed. A megabyte is
// far past any of them, and what lies past it is not configuration at all --
// "Include /l*/d*/*" matches a hundred and eighty library files, and reading
// those line by line is sixteen seconds while somebody waits for the menu.
//
// The size comes from the same stat that asks whether it is a regular file,
// so this costs nothing to know.
const maxConfigBytes = 1 << 20

// hostsFrom reads a config and everything it includes.
//
// The entry point keeps its own record of what has been read, so a file
// reached twice is read once.
func hostsFrom(path string, depth int) []string {
	return hostsRead(path, depth, map[string]bool{})
}

// hostsRead is hostsFrom with that record in hand.
//
// Without it the depth limit is the only bound, and it bounds the wrong thing.
// Three files that each include the directory they are in multiply at every
// level: sixteen deep is three to the sixteenth reads, which is a menu that
// never opens. That shape is a copy-paste -- the same Include header in each
// fragment -- rather than anything exotic.
//
// Skipping a repeat cannot change the answer. What is collected here is host
// aliases, and those are already deduplicated; reading a file a second time
// adds nothing but the reading.
func hostsRead(path string, depth int, read map[string]bool) []string {
	if path == "" || depth > maxIncludeDepth {
		return nil
	}
	path = filepath.Clean(path)
	if read[path] {
		return nil
	}
	read[path] = true
	// Regular files only. An Include is a glob, and a glob matches whatever is
	// there: "Include /*/*/0" matches /dev/pts/0 on an ordinary machine, and
	// reading a terminal blocks until somebody types into it. This file is read
	// to draw the menu, so that is a keypress that never comes back, with
	// nothing on screen to say why.
	//
	// Checked before opening rather than after, because opening some of them
	// is itself the thing that waits.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	var hosts []string
	seen := map[string]bool{}
	scanner := scanConfig(file)
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
					for _, host := range hostsRead(match, depth+1, read) {
						if !seen[host] {
							seen[host] = true
							hosts = append(hosts, host)
						}
					}
				}
			}
		}
	}
	// Nothing to do about it here -- the machines already read are still worth
	// offering -- but Unreadable asks the same question of the top-level file
	// and reports it, so an emptier menu than yesterday's has a reason
	// somewhere.
	_ = scanner.Err()
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

// maxIncludeMatches is how many files one Include may pull in.
//
// An Include names configuration somebody wrote: a directory of host blocks, a
// file per employer, a shared file. Matching more than this is not that. It is
// a pattern with a wildcard high in an absolute path -- "Include /*/*" matches
// seventy-five thousand things on an ordinary machine -- and every one of them
// then gets asked about and possibly read.
//
// Refusing the whole pattern rather than reading the first few: a config that
// meant to include something specific is better served by nothing arriving,
// which is visible in the menu, than by an arbitrary subset that depends on
// what happens to sort first.
const maxIncludeMatches = 256

// includeGlobBudget is how long expanding one Include may take.
//
// The cap on matches bounds what is done with them; it cannot bound finding
// them, because that is one call. "Include /*/**/**/**/*///" walks five levels
// of wildcards across the filesystem and takes fourteen seconds before
// returning nothing at all -- and this runs to draw the menu.
//
// Generous against any real config, where an Include names a directory
// somebody set up and matching in it is immediate.
var includeGlobBudget = 2 * time.Second

// slowGlobs are patterns that went past that budget once.
//
// Kept for the life of the process so a menu opened again does not start the
// same walk again, and so the goroutine left behind finishing it is one rather
// than one per keypress.
var slowGlobs sync.Map

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
	if _, slow := slowGlobs.Load(pattern); slow {
		return nil
	}
	type globbed struct {
		matches []string
		err     error
	}
	done := make(chan globbed, 1)
	go func() {
		matches, err := filepath.Glob(pattern)
		done <- globbed{matches, err}
	}()

	var matches []string
	select {
	case got := <-done:
		if got.err != nil {
			return nil
		}
		matches = got.matches
	case <-time.After(includeGlobBudget):
		// Left to finish on its own and its answer dropped. Recorded so the
		// next menu does not wait for it again.
		slowGlobs.Store(pattern, struct{}{})
		return nil
	}
	if len(matches) > maxIncludeMatches {
		return nil
	}
	sort.Strings(matches)
	return matches
}
