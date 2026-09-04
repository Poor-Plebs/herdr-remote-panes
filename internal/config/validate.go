package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
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
			"mode %q is not one of %s; machines default to a plain SSH terminal",
			c.Mode, orList(modes)))
	}
	if !knownPlacement(c.Placement) {
		problems = append(problems, fmt.Sprintf(
			"placement %q is not one of %s; terminals will open as tabs",
			c.Placement, orList(placements)))
	}
	if !knownScope(c.Scope) {
		problems = append(problems, fmt.Sprintf(
			"scope %q is not %s; only the shared space is mirrored", c.Scope, orList(scopes)))
	}
	// {pane} tells them apart as well as {name} does: it is the terminal's own
	// id and no two share one, so a format built on it is not the fault this
	// reports -- and saying it is sends somebody to add a placeholder they had
	// no use for. {agent} does not count, because a machine's ordinary shells
	// have no agent and would all be named alike.
	if !strings.Contains(c.LabelFormat, "{name}") && !strings.Contains(c.LabelFormat, "{pane}") {
		problems = append(problems, fmt.Sprintf(
			"label_format %q has neither {name} nor {pane}, so every terminal from a "+
				"machine will be named the same", c.LabelFormat))
	}
	// "every machine" was too much: a machine that names its own workspace
	// never consults the format, so a config where they all do would be told
	// its machines were about to collide when nothing of the kind could
	// happen. The condition is still right -- the setting is wrong for any
	// machine added later that does not name one -- so it is the sentence that
	// says which machines it means.
	if c.Workspace == "" && !strings.Contains(c.WorkspaceFormat, "{host}") {
		problems = append(problems, fmt.Sprintf(
			"workspace_format %q has no {host}, so every machine that does not name its "+
				"own space shares one with the rest", c.WorkspaceFormat))
	}
	// The same fault in the name used while a machine is unreachable, under
	// the same condition: an explicitly chosen workspace is used as given, so
	// neither format is consulted at all. Missed when the check above was
	// written, and worse where it lands -- machines collide exactly when
	// several are unreachable at once, which is the network going down rather
	// than anything anybody did to the config that day.
	if c.Workspace == "" && !strings.Contains(c.WorkspaceFormatDown, "{host}") {
		problems = append(problems, fmt.Sprintf(
			"workspace_format_down %q has no {host}, so every machine that does not name "+
				"its own space shares one with the rest while it cannot be reached",
			c.WorkspaceFormatDown))
	}
	// remote_workspace_format is deliberately not checked for {hub}. It names
	// the space made on the machine, so what would collide is two of these
	// hubs pointing at one machine -- and a literal name with no placeholder
	// is the documented way out of exactly that: each hub is given a name of
	// its own. "from my laptop" is the example in the troubleshooting page.
	// A check here would warn about the remedy.

	// How often every machine is reached over ssh. Unparseable or too fast and
	// the default goes back in its place -- so "30", meaning half a minute,
	// polls fifteen times a second more often than that instead, quietly, on
	// every machine at once. The one setting here whose surprise costs
	// somebody else's network.
	if written, err := time.ParseDuration(c.PollInterval); err != nil {
		problems = append(problems, fmt.Sprintf(
			"poll_interval %q is not a length of time, so machines are polled every %s; "+
				"write it with a unit, as 30s or 500ms", c.PollInterval, c.Interval()))
	} else if written < MinPollInterval {
		problems = append(problems, fmt.Sprintf(
			"poll_interval %q is faster than %s, which is more than a machine can keep up "+
				"with over ssh; machines are polled every %s instead",
			c.PollInterval, MinPollInterval, c.Interval()))
	}

	// A path the remote shell would have to expand. It is passed quoted, as
	// any path holding a space or a metacharacter must be, so "~" and "$HOME"
	// arrive as those two characters and the machine reports no such file --
	// and what somebody sees for it is "no herdr found on the machine, set
	// herdr_bin if it is installed elsewhere there", which is the one thing
	// they have already done.
	// Both sentences say that, rather than "this is not run through a shell",
	// which is what they said while the paragraph above explained the quoting.
	// `ssh host <cmd>` DOES run a shell on the machine -- remote.go's own start
	// line is `nohup ... >/dev/null 2>&1 </dev/null &`, which is shell syntax
	// and works
	// for no other reason -- so the old clause was false about ssh, and the
	// reader could not check it against anything. internal/remote holds the
	// half these now name: TestConfiguredBinIsUsedVerbatim asserts the argv
	// carries '~/.local/bin/herdr' with the tilde inside the quotes.
	if unexpanded(c.HerdrBin) {
		problems = append(problems, fmt.Sprintf(
			"herdr_bin %q is expanded by a shell, and this reaches the machine "+
				"quoted so nothing expands it; write the path out in full", c.HerdrBin))
	}
	for _, h := range c.Hosts {
		if unexpanded(h.HerdrBin) {
			problems = append(problems, fmt.Sprintf(
				"host %q has herdr_bin %q, which is expanded by a shell, and this "+
					"reaches the machine quoted so nothing expands it; write the "+
					"path out in full",
				h.Target, h.HerdrBin))
		}
	}

	problems = append(problems, c.ignored...)
	problems = append(problems, c.repeated...)
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
			// Not "the last entry counts", which is what this said and is not
			// what happens. Nothing merges the entries, so each reader picks
			// one for itself: the daemon reaches the machine under the first,
			// because hostConfig returns the first target that matches, while
			// the menu draws the last, because it overwrites the row it
			// already has for that target. Telling somebody the last one wins
			// sends them to edit the entry that does not decide how their
			// machine is reached.
			problems = append(problems, fmt.Sprintf(
				"host %q is listed more than once; it is reached using the first entry "+
					"and shown in the menu from the last, so remove one", host.Target))
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
			// all -- so wherever the formats ask for this machine's name, there
			// is nothing to put.
			//
			// It used to promise "its space and its terminals would be named
			// after nothing", which is true of the defaults and of nothing
			// else. A space named outright is called what it was called; a
			// workspace_format or label_format built without {host} never asks
			// for the machine at all, and comes out exactly as intended. The
			// condition is still right -- the machine has no name of its own,
			// and putting {host} back would show that -- so it is the sentence
			// that says what depends on it.
			if name == "" {
				// Either half can be the cause. A target of nothing but spaces
				// is a valid destination -- ssh reaches `Host "my server"` --
				// but it cannot name anything once it is trimmed.
				cause := fmt.Sprintf("label %q", host.Label)
				if host.Label == "" {
					cause = "target, which is nothing but spaces,"
				}
				problems = append(problems, fmt.Sprintf(
					"host %q has a %s that is empty once it is made safe to draw, "+
						"so wherever the formats ask for its name there is nothing to put",
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

		// A label that is some other machine's target. The two are not the
		// same kind of name -- a target is the address a machine is reached
		// at, a label is what it is called here -- so this is not the
		// collision above, and the machines are shown under different names.
		//
		// What it costs is the label itself. A label reaches its machine when
		// nothing else answers to that name; here the targeted machine answers
		// first, so the one wearing the label cannot be reached by it at all.
		//
		// This used to say the menu showed this machine under the name, and
		// that is not what the menu does. It draws "web (prod)" and gives the
		// bare name to the machine targeted that way, which is the one you
		// would reach by typing it -- and where two names would come out the
		// same, namesWithin puts one back to its full form for exactly this
		// reason. The menu is the half that gets this right.
		if host.Label != "" {
			for _, other := range c.Hosts {
				if other.Disabled || other.Target != host.Label || other.Target == host.Target {
					continue
				}
				problems = append(problems, fmt.Sprintf(
					"host %q is labelled %q, which is the target of another machine; "+
						"asking for %q reaches that one, so this machine cannot be "+
						"reached by the name it is labelled with",
					host.Target, host.Label, host.Label))
			}
		}

		// These two named the valid values and stopped there, which leaves out
		// the part that is particular to a machine: a setting written on one
		// REPLACES the top-level setting rather than being checked against it.
		// ModeFor and PlacementFor return whatever the machine says as soon as
		// it is not empty, so a misspelling is handed straight on and lands on
		// the hard default -- ssh, and a tab -- rather than falling back to
		// what the rest of the machines are using. Somebody who set mode
		// "attach" once at the top and typed it wrong on one machine has that
		// machine alone on a plain SSH terminal, and nothing in the file looks
		// wrong. The top-level twins do not need this sentence: there is
		// nothing above them to fall back to.
		if host.Mode != "" && !knownMode(host.Mode) {
			problems = append(problems, fmt.Sprintf(
				"host %q has mode %q, which is not one of %s; it opens a plain SSH "+
					"terminal, and a mode set for the rest does not reach it",
				host.Target, host.Mode, orList(modes)))
		}
		if host.Placement != "" && !knownPlacement(host.Placement) {
			problems = append(problems, fmt.Sprintf(
				"host %q has placement %q, which is not one of %s; its terminals open as "+
					"tabs, and a placement set for the rest does not reach it",
				host.Target, host.Placement, orList(placements)))
		}
	}
	return problems
}

// The values each of these settings takes, in the order its complaint lists
// them. Every complaint below is BUILT from one of these rather than writing
// the values out again, because a hand-written list goes stale in silence: at
// d23f616 a sixth value added to knownPlacement passed the whole suite, and so
// did dropping a value from either per-host sentence. Only the top-level
// placement sentence was held, and only because a test matched it whole.
var (
	modes      = []Mode{ModeSSH, ModeAttach, ModeObserve}
	placements = []string{"follow", "split", "tab", "zoomed", "overlay"}
	scopes     = []string{ScopeShared, ScopeAll}
)

// orList writes a list the way these complaints read it: "shared or all",
// "ssh, attach or observe", "follow, split, tab, zoomed or overlay".
func orList[T ~string](values []T) string {
	if len(values) == 0 {
		return ""
	}
	all := make([]string, len(values))
	for i, v := range values {
		all[i] = string(v)
	}
	if len(all) == 1 {
		return all[0]
	}
	return strings.Join(all[:len(all)-1], ", ") + " or " + all[len(all)-1]
}

func knownMode(mode Mode) bool {
	return slices.Contains(modes, mode)
}

// Placements lists the values the placement setting takes, for the package
// that acts on them: internal/syncd holds every one of these to a case of its
// own, so a value added here cannot quietly start opening tabs.
func Placements() []string {
	return slices.Clone(placements)
}

func knownPlacement(placement string) bool {
	return slices.Contains(placements, placement)
}

func knownScope(scope string) bool {
	return slices.Contains(scopes, scope)
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
	// FIRST, so that it bounds every message below it. Each of those quotes
	// the target back, and this is the only check that does not -- so with it
	// anywhere else, whichever refusals come before it are unbounded. It sat
	// under the dash check, which is the one a selection is most likely to
	// trip: a diff hunk begins with "-", and dragging over one produced a
	// refusal as long as the selection. Measured before moving it, a 200 KB
	// selection beginning with a dash gave a 200 KB message where every other
	// overlong selection gave 68 bytes.
	//
	// Not a machine, whoever offered it. A name reaches here from a file
	// somebody edited or from whatever text was selected when an action was
	// invoked, and a selection is a line of somebody else's output as readily
	// as a name -- a base64 blob or a long URL has no space in it and no dash
	// at the front, which is everything else this asks about.
	//
	// Bounded rather than merely refused later by ssh, because what it costs
	// is not the failed connection: the target is in the message that failure
	// is reported with, and that message goes into a log that rolls at a
	// quarter of a megabyte. One selection could take the history with it.
	if len(target) > maxTargetBytes {
		// Bytes rather than characters, which is what this counts and what
		// maxTargetBytes bounds: 400 emoji are 1600 bytes, and calling that
		// "1600 characters" overstates the name by four times.
		return fmt.Errorf("target is %d bytes, which is longer than any machine's name",
			len(target))
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

// maxTargetBytes is the longest a machine's name may be. A hostname stops at
// 253 by the standard that defines them, and what goes in front of one here is
// a user name; anything past this was not typed by somebody naming a machine.
const maxTargetBytes = 320

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
	// Before the general checks, because this is the likeliest way to arrive
	// here by accident and the general answer for it is "contains a control
	// character" -- true, and no help at all to somebody who dragged over two
	// lines and pressed a key. The selection is trimmed before it gets here,
	// so a break left in it is one somebody selected across.
	if strings.ContainsAny(target, "\n\r") {
		return errors.New("the selection covers more than one line, so it is not a machine")
	}
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

// unexpanded reports whether a path only means what it looks like once a shell
// has been at it. A leading "~" is a home directory and "$" is a variable, and
// neither survives being quoted for the remote command line.
func unexpanded(path string) bool {
	return strings.HasPrefix(path, "~") || strings.Contains(path, "$")
}
