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

	seen := map[string]bool{}
	for _, host := range c.Hosts {
		if host.Target == "" {
			problems = append(problems, "a host has no target and is ignored")
			continue
		}
		if err := ValidTarget(host.Target); err != nil {
			problems = append(problems, err.Error())
		}
		if seen[host.Target] {
			problems = append(problems, fmt.Sprintf(
				"host %q is listed more than once; only the last entry counts", host.Target))
		}
		seen[host.Target] = true

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

// ValidTarget reports why a target cannot be used, or nil.
//
// The target is handed to ssh as an argument. ssh takes options on the command
// line, and -oProxyCommand=... runs a command, so a target beginning with a
// dash is an instruction rather than a machine. Targets do not only come from
// this file: connect falls back to whatever text is selected in the terminal,
// which is how a line of someone else's output becomes an argument to ssh.
func ValidTarget(target string) error {
	if target == "" {
		return errors.New("no target")
	}
	if strings.HasPrefix(target, "-") {
		return fmt.Errorf("target %q starts with a dash, which ssh reads as an option", target)
	}
	for _, r := range target {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("target %q contains a space or control character", target)
		}
	}
	return nil
}
