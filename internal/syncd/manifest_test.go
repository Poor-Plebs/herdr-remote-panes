package syncd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/project"
)

// cliSource is where the argument a command was invoked with is dispatched.
// Named once because these tests read it to find out which commands exist, and
// it has moved out of the repository root once already.
const cliSource = "internal/cli/cli.go"

// repoFile names a file at the top of the repository.
//
// These tests are about files that live there -- the manifest, the README, the
// build script -- read from a package two levels down. Asking where the top is
// rather than counting ".." means moving this package does not silently start
// reading nothing.
func repoFile(t *testing.T, name string) string {
	t.Helper()
	root, err := project.Root()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, name)
}

// docsText is every page of documentation joined together: the README and the
// pages under docs/. These tests are about something the documentation shows
// agreeing with what the code does, and which page shows it is a decision that
// has already changed twice -- the troubleshooting and contributor sections
// both moved out of the README. Reading the set rather than one file keeps the
// check about the agreement rather than about where the prose currently sits.
func docsText(t *testing.T) string {
	t.Helper()
	text, err := project.DocsText()
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// TestManifestDescriptionsDoNotClaimMirroring guards a surface that has no
// other check on it.
//
// The action titles and descriptions are what Herdr shows in its own list of
// what a plugin can do, and nothing here reads them, so they went stale without
// anything noticing. They described the plugin as it was when mirroring was the
// main mode rather than an experimental one -- and disconnect's said it closed
// "its mirror panes", which was an accurate description of a bug: it left a
// plain SSH machine's terminals open.
func TestManifestDescriptionsDoNotClaimMirroring(t *testing.T) {
	raw, err := os.ReadFile(repoFile(t, "herdr-plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}

	descriptions := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "description = ") {
			continue
		}
		descriptions++
		lower := strings.ToLower(line)
		// Mirroring is off by default, so a description that only describes
		// mirroring describes what most people will not be doing. Saying it is
		// optional, or naming it alongside the ordinary case, is fine.
		mentions := strings.Contains(lower, "mirror")
		hedged := strings.Contains(lower, "optional") ||
			strings.Contains(lower, "when mirroring") ||
			strings.Contains(lower, "turns mirroring")
		if mentions && !hedged {
			t.Errorf("description describes mirroring as though it were the usual thing:\n  %s", line)
		}
	}

	// A test that finds no descriptions checks nothing while passing, which is
	// the failure mode of reading a file in a test.
	if descriptions < 5 {
		t.Fatalf("found %d descriptions in the manifest; the format has moved", descriptions)
	}
}

func TestManifestListsWhatTheCodeImplements(t *testing.T) {
	// Every action in the manifest runs this binary with its id as the
	// argument, so an id here that main does not handle is a menu entry that
	// fails when picked.
	manifest, err := os.ReadFile(repoFile(t, "herdr-plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(repoFile(t, cliSource))
	if err != nil {
		t.Fatal(err)
	}

	found := 0
	for _, line := range strings.Split(string(manifest), "\n") {
		if !strings.HasPrefix(line, "id = ") {
			continue
		}
		id := strings.Trim(strings.TrimPrefix(line, "id = "), `"`)
		if strings.Contains(id, ".") {
			continue // the plugin's own id, not an action
		}
		found++
		// Looked for as a quoted word rather than as "case X", because several
		// share a case: open and open-tab differ only in the placement they
		// ask for.
		if !strings.Contains(string(source), `"`+id+`"`) {
			t.Errorf("the manifest offers %q but nothing handles it", id)
		}
	}

	// As above: finding nothing must not read as finding nothing wrong.
	if found < 5 {
		t.Fatalf("found %d actions in the manifest; the format has moved", found)
	}
}

func TestTheREADMEOnlyBindsActionsThatExist(t *testing.T) {
	// The README shows keybindings to copy into config.toml. An action id that
	// does not exist produces a binding that does nothing, and Herdr will not
	// say why -- the same silence as a keybinding that clashes with a built-in.
	readme := docsText(t)
	manifest, err := os.ReadFile(repoFile(t, "herdr-plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}

	referenced := regexp.MustCompile(`poorplebs\.remote-panes\.([a-z-]+)`).
		FindAllStringSubmatch(readme, -1)
	if len(referenced) == 0 {
		t.Fatal("the documentation references no actions; this test needs rewriting")
	}

	for _, m := range referenced {
		if !strings.Contains(string(manifest), `id = "`+m[1]+`"`) {
			t.Errorf("the documentation binds %q, which is not an action this offers", m[0])
		}
	}
}

// manifestCommands returns every command line the manifest tells Herdr to run,
// as the argument list it would run.
func manifestCommands(t *testing.T) (build []string, rest [][]string) {
	t.Helper()
	raw, err := os.ReadFile(repoFile(t, "herdr-plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[[") {
			section = strings.Trim(trimmed, "[]")
			continue
		}
		inner, ok := strings.CutPrefix(trimmed, "command = [")
		if !ok {
			continue
		}
		var args []string
		for _, field := range strings.Split(strings.TrimSuffix(inner, "]"), ",") {
			args = append(args, strings.Trim(strings.TrimSpace(field), `"`))
		}
		if section == "build" {
			build = args
			continue
		}
		rest = append(rest, args)
	}
	return build, rest
}

func TestEveryCommandTheManifestRunsIsOneThisBinaryHas(t *testing.T) {
	// The test above holds the ids to what main handles. Herdr does not run
	// ids: it runs the command arrays, and nothing held those. An argument
	// changed without its id, or a binary built somewhere other than where the
	// actions look for it, leaves the manifest reading correctly and every
	// entry in it failing when picked.
	build, commands := manifestCommands(t)
	if len(build) == 0 {
		t.Fatal("the manifest has no build command; the format has moved")
	}
	if len(commands) < 8 {
		t.Fatalf("found %d commands to check, which is fewer than the manifest has", len(commands))
	}

	// Where the build puts the binary, taken from the build itself. The
	// manifest runs a script rather than the compiler -- a machine with no Go
	// on it can then be told so, where spawning `go` fails before anything of
	// ours runs -- so the -o is one level down, in what that script does.
	buildSource := strings.Join(build, " ")
	if len(build) > 1 && build[0] == "sh" {
		script, err := os.ReadFile(repoFile(t, build[1]))
		if err != nil {
			t.Fatalf("the manifest builds with %q and there is no such script: %v", build[1], err)
		}
		buildSource = string(script)
	}

	built := ""
	if at := regexp.MustCompile(`-o (\S+)`).FindStringSubmatch(buildSource); at != nil {
		built = at[1]
	}
	if built == "" {
		t.Fatal("the build does not say where it puts the binary")
	}

	source, err := os.ReadFile(repoFile(t, cliSource))
	if err != nil {
		t.Fatal(err)
	}
	// The words main dispatches on, taken from its switch rather than guessed.
	handled := map[string]bool{}
	for _, line := range strings.Split(string(source), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "case \"") {
			continue
		}
		for _, part := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(trimmed, "case "), ":"), ",") {
			handled[strings.Trim(strings.TrimSpace(part), `"`)] = true
		}
	}

	for _, args := range commands {
		if got, want := args[0], "./"+built; got != want {
			t.Errorf("the manifest runs %q, but the build puts the binary at %q", got, want)
		}
		if len(args) < 2 {
			t.Errorf("the manifest runs %v with no command at all", args)
			continue
		}
		if sub := args[len(args)-1]; !handled[sub] {
			t.Errorf("the manifest runs %q, which main does not dispatch on", sub)
		}
	}
}

func TestTheVersionIsTheSameInBothPlacesItIsWritten(t *testing.T) {
	// A release is a tag, and the tag is not in the repository at the moment
	// the version is bumped -- so nothing can hold either of these to it. What
	// can be held is that the two agree with each other, which is what went
	// wrong: v0.2.0 was tagged and released with the manifest still saying
	// 0.1.0, because the release notes had it written down that the README was
	// the only place a version appears.
	manifest, err := os.ReadFile(repoFile(t, "herdr-plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	declared := regexp.MustCompile(`(?m)^version = "([^"]+)"`).FindSubmatch(manifest)
	if declared == nil {
		t.Fatal("the manifest no longer declares a version")
	}

	readme, err := os.ReadFile(repoFile(t, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	pinned := regexp.MustCompile(`--ref v([0-9]+\.[0-9]+\.[0-9]+)`).FindSubmatch(readme)
	if pinned == nil {
		t.Fatal("the README no longer shows how to pin a version")
	}

	if string(declared[1]) != string(pinned[1]) {
		t.Errorf("the manifest says %q and the README pins v%s; a release bumps both",
			declared[1], pinned[1])
	}
}

func TestThePluginIDIsTheSameEverywhereItIsWritten(t *testing.T) {
	// PluginID carries a comment saying it must match the id in the manifest,
	// which is a note to whoever reads it rather than anything that would stop
	// it drifting. What it feeds is what somebody is told to run when nothing
	// is answering -- `herdr plugin log list --plugin <id>` -- and an id that
	// no longer matches sends them to a plugin Herdr has never heard of, at
	// the moment they already cannot see what is wrong.
	//
	// Same reasoning as the version above: nothing can hold either of these to
	// an outside truth, but they can be held to each other.
	manifest, err := os.ReadFile(repoFile(t, "herdr-plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	declared := regexp.MustCompile(`(?m)^id = "([^"]+)"`).FindSubmatch(manifest)
	if declared == nil {
		t.Fatal("the manifest no longer declares an id")
	}
	if string(declared[1]) != PluginID {
		t.Errorf("the manifest says %q and the code says %q", declared[1], PluginID)
	}

	// And the docs, which spell it out for copying into a shell: the config
	// directory is found with it, and there is no other way to know where the
	// config file is.
	for _, name := range []string{"README.md", "docs/troubleshooting.md", "docs/development.md"} {
		page, err := os.ReadFile(repoFile(t, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, found := range regexp.MustCompile(`poorplebs\.[a-z0-9-]+`).FindAll(page, -1) {
			if string(found) != PluginID {
				t.Errorf("%s names the plugin %q, and it is %q", name, found, PluginID)
			}
		}
	}
}
