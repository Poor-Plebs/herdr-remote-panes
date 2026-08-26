package config

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Problems lists settings that are readable but will not do what they look
// like they say.
//
// Every one of these used to pass silently and behave as something else: a
// mode spelled wrong counted as mirroring, which quietly turned on the
// experimental feature; an unknown placement became a tab; an unknown scope
// became the default. A typo in a config file deserves to be told about, not
// guessed at.
func (c Config) Problems() []string {
	var problems []string

	if !knownMode(c.Mode) {
		problems = append(problems, fmt.Sprintf(
			"mode %q is not one of ssh, attach or observe; machines default to a plain SSH terminal", c.Mode))
	}
	if !knownPlacement(c.Placement) {
		problems = append(problems, fmt.Sprintf(
			"placement %q is not one of split, tab, zoomed or overlay; terminals will open as tabs", c.Placement))
	}
	if c.Scope != ScopeShared && c.Scope != ScopeAll {
		problems = append(problems, fmt.Sprintf(
			"scope %q is not shared or all; only the shared space is mirrored", c.Scope))
	}
	if !strings.Contains(c.LabelFormat, "{name}") {
		problems = append(problems, fmt.Sprintf(
			"label_format %q has no {name}, so every terminal from a machine will be named the same", c.LabelFormat))
	}
	if c.Workspace == "" && !strings.Contains(c.WorkspaceFormat, "{host}") {
		problems = append(problems, fmt.Sprintf(
			"workspace_format %q has no {host}, so every machine will share one space", c.WorkspaceFormat))
	}

	problems = append(problems, c.ignored...)
	for _, name := range c.unknown {
		problems = append(problems, fmt.Sprintf(
			"%q is not a setting and is being ignored", name))
	}

	// Reported from what normalized() recorded, not from the list: it drops
	// these before anything here can see them, so the check that used to sit in
	// the loop below could never fire.
	problems = append(problems, c.dropped...)

	seen := map[string]bool{}
	// Which machine claimed each name. A label names the machine's space, the
	// panes in it, and the suffix those are matched against, so two machines
	// answering to one name are not two machines: they share a space, and each
	// pass reads the other's terminals as strays in its own space and closes
	// them. Connecting both leaves one of them with nothing, and nothing
	// anywhere said why.
	labelledBy := map[string]string{}

	for _, host := range c.Hosts {
		if err := ValidTarget(host.Target); err != nil {
			problems = append(problems, err.Error())
		}
		if seen[host.Target] {
			problems = append(problems, fmt.Sprintf(
				"host %q is listed more than once; only the last entry counts", host.Target))
		}
		seen[host.Target] = true

		// Not for a machine that is switched off: it is not connected to, so
		// it cannot collide with anything, and saying so would be a warning
		// about a setting that is behaving itself.
		if !host.Disabled {
			// The name it answers to, which is its target when it has no label
			// of its own -- so a label copied from another machine's target
			// collides just as surely as one copied from its label.
			name := host.DisplayLabel()
			// A label is made safe to draw before it is used anywhere, and one
			// made only of things that cannot be drawn is left with nothing at
			// all -- so the machine's space and every terminal in it end up
			// named after nothing, and the file says otherwise.
			if name == "" {
				// Either half can be the cause. A target of nothing but spaces
				// is a valid destination -- ssh reaches `Host "my server"` --
				// but it cannot name anything once it is trimmed.
				cause := fmt.Sprintf("label %q", host.Label)
				if host.Label == "" {
					cause = "target, which is nothing but spaces,"
				}
				problems = append(problems, fmt.Sprintf(
					"host %q has a %s that is empty once it is made safe to draw; "+
						"its space and its terminals would be named after nothing",
					host.Target, cause))
			}
			if first, taken := labelledBy[name]; taken && first != host.Target {
				problems = append(problems, fmt.Sprintf(
					"hosts %q and %q are both called %q, so they would share one space "+
						"and close each other's terminals; give one of them its own label",
					first, host.Target, name))
			} else if !taken {
				labelledBy[name] = host.Target
			}
		}

		if host.Mode != "" && !knownMode(host.Mode) {
			problems = append(problems, fmt.Sprintf(
				"host %q has mode %q, which is not one of ssh, attach or observe", host.Target, host.Mode))
		}
		if host.Placement != "" && !knownPlacement(host.Placement) {
			problems = append(problems, fmt.Sprintf(
				"host %q has placement %q, which is not one of split, tab, zoomed or overlay",
				host.Target, host.Placement))
		}
	}
	return problems
}

func knownMode(mode Mode) bool {
	switch mode {
	case ModeSSH, ModeAttach, ModeObserve:
		return true
	}
	return false
}

func knownPlacement(placement string) bool {
	switch placement {
	case "split", "tab", "zoomed", "overlay":
		return true
	}
	return false
}

// ValidTarget reports why a target is unsafe to hand ssh, or nil.
//
// The target is handed to ssh as an argument. ssh takes options on the command
// line, and -oProxyCommand=... runs a command, so a target beginning with a
// dash is an instruction rather than a machine. A control character has no
// business in a host name and would reach the terminal on its way to being
// drawn.
//
// A space is not on this list. It is safe -- the target is one element of an
// argument list and never goes near a shell -- and it is legal: ssh allows
// `Host "my server"` and connects to it as `ssh "my server"`. Refusing it here
// meant such a machine was read correctly out of ~/.ssh/config and then dropped
// from the menu without a word. What a space does mean is that a name nobody
// declared is probably not a name at all, which is PlausibleTarget's business.
func ValidTarget(target string) error {
	if target == "" {
		return errors.New("no target")
	}
	if strings.HasPrefix(target, "-") {
		return fmt.Errorf("target %q starts with a dash, which ssh reads as an option", target)
	}
	for _, r := range target {
		if unicode.IsControl(r) {
			return fmt.Errorf("target %q contains a control character", target)
		}
	}
	return nil
}

// PlausibleTarget reports why a target nobody wrote down cannot be used.
//
// Targets do not only come from a file somebody edited: connect falls back to
// whatever text is selected in the terminal, which is how a line of someone
// else's output becomes an argument to ssh. Such a name has to look like a
// name, and a line with a space in it is a sentence.
//
// Held apart from ValidTarget because the two answer different questions. This
// one is a guess about what somebody meant, and a machine written down in
// ~/.ssh/config needs no guessing.
func PlausibleTarget(target string) error {
	if err := ValidTarget(target); err != nil {
		return err
	}
	for _, r := range target {
		if unicode.IsSpace(r) {
			return fmt.Errorf("target %q contains a space, so it is unlikely to be a machine", target)
		}
	}
	return nil
}
