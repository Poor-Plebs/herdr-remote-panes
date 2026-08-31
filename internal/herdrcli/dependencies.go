package herdrcli

// Dependency is one Herdr command this plugin runs: the words that name it,
// the flags it is passed, and the values this plugin can pass for a flag Herdr
// restricts.
//
// Written down because none of it is checked by anything that builds. A
// command this plugin no longer has, a flag that was renamed, or a value Herdr
// stopped accepting all compile perfectly and fail at the far end, one action
// at a time -- and the stand-in the tests run against is written from the same
// belief as the code, so it agrees with whatever the code believes.
//
// `make herdr` reads this and asks the installed Herdr about every line of it.
// It is not part of `make check`: it needs Herdr on the machine, which CI has
// no reason to have, and a check that cannot run everywhere is one that gets
// ignored where it can. The guard test beside this file holds the list to the
// package instead, so what is checked cannot fall behind what is called.
type Dependency struct {
	// Command is the subcommand words, as passed to herdr.
	Command []string
	// Flags is every flag this plugin passes to it.
	Flags []string
	// Values is what this plugin can send for a flag Herdr restricts, checked
	// against the "possible values" its help prints.
	Values map[string][]string
}

// Dependencies is every Herdr command this plugin runs.
var Dependencies = []Dependency{
	{Command: []string{"pane", "list"}},
	{Command: []string{"pane", "rename"}},
	{Command: []string{"pane", "close"}},
	{Command: []string{"workspace", "list"}},
	{Command: []string{"workspace", "focus"}},
	{Command: []string{"notification", "show"}},
	{Command: []string{"plugin", "pane", "close"}},
	{
		Command: []string{"plugin", "pane", "open"},
		Flags: []string{
			"--plugin", "--entrypoint", "--placement", "--workspace",
			"--target-pane", "--direction", "--cwd", "--env",
			"--focus", "--no-focus",
		},
		// planPaneTarget decides these, and popup is deliberately not among
		// them: it is a placement a manifest declares and not one this flag
		// accepts. Sending it opened nothing at all.
		Values: map[string][]string{
			"--placement": {"split", "tab", "zoomed", "overlay"},
		},
	},
	{
		Command: []string{"pane", "report-agent"},
		Flags:   []string{"--source", "--agent", "--state"},
		// What AgentState maps a remote pane's status onto.
		Values: map[string][]string{
			"--state": {"idle", "working", "blocked", "unknown"},
		},
	},
	{
		Command: []string{"pane", "release-agent"},
		Flags:   []string{"--source", "--agent"},
	},
	{
		Command: []string{"pane", "split"},
		Flags:   []string{"--direction", "--focus"},
		// The one direction anything here asks for.
		Values: map[string][]string{"--direction": {"right"}},
	},
}
