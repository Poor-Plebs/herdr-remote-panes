package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTheREADMEShowsAWarningThisCanProduce guards an example against the code
// it claims to be an example of.
//
// The troubleshooting section shows what a misspelled setting looks like, so
// somebody can recognise it. An example that has drifted from the wording is
// worse than none: it is recognisable as something this never says.
func TestTheREADMEShowsAWarningThisCanProduce(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("validate.go")
	if err != nil {
		t.Fatal(err)
	}

	shown := regexp.MustCompile("(?s)```\nconfig: (.+?)\n```").FindAllStringSubmatch(string(readme), -1)
	if len(shown) == 0 {
		t.Fatal("no config warning is shown in the README; this test needs rewriting")
	}

	for _, m := range shown {
		example := strings.TrimSpace(m[1])
		// The quoted value is whatever somebody typed; the wording around it is
		// what this produces.
		shape := regexp.MustCompile(`"[^"]*"`).ReplaceAllString(example, "%q")
		if !strings.Contains(string(source), shape) {
			t.Errorf("the README shows a warning this does not produce:\n  %s", example)
		}
	}
}

func TestTheWarningInTheREADMEIsOneAMisspellingActuallyCauses(t *testing.T) {
	// Beyond the wording matching: writing that mode into a config really does
	// produce it, which is the thing the example is promising.
	cfg := Defaults()
	cfg.Mode = "shh"

	problems := strings.Join(cfg.Problems(), "\n")
	if !strings.Contains(problems, `mode "shh" is not one of`) {
		t.Errorf("a misspelled mode did not produce the documented warning: %q", problems)
	}
	if !strings.Contains(problems, "plain SSH terminal") {
		t.Errorf("the warning no longer says what happens instead: %q", problems)
	}
}

// TestTheREADMEsExamplesAreConfigThisCanRead checks the JSON somebody will
// paste.
//
// An example that does not parse is worse than none: it is pasted into the
// file, the whole config stops being readable, and every machine in it
// disappears at once. Nothing checked that these were even JSON.
func TestTheREADMEsExamplesAreConfigThisCanRead(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}

	blocks := regexp.MustCompile("(?s)```json\n(.*?)\n```").FindAllStringSubmatch(string(readme), -1)
	if len(blocks) < 2 {
		t.Fatalf("found %d JSON examples in the README; it shows more than that", len(blocks))
	}

	for _, block := range blocks {
		example := block[1]
		t.Run(strings.Join(strings.Fields(example), " "), func(t *testing.T) {
			// A whole file, or a single machine out of one: the README shows
			// both, and the difference is whether it has hosts in it.
			if strings.Contains(example, `"hosts"`) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(example), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)

				cfg, err := Load()
				if err != nil {
					t.Fatalf("the README's config does not load: %v", err)
				}
				if len(cfg.Hosts) == 0 {
					t.Error("the README's config has no machines in it once read")
				}
				// And nothing in it is a setting that means nothing, which is
				// what a stale example turns into.
				for _, problem := range cfg.Problems() {
					t.Errorf("the README's config has a problem with it: %s", problem)
				}
				return
			}

			var host Host
			if err := json.Unmarshal([]byte(example), &host); err != nil {
				t.Fatalf("the README's machine entry does not parse: %v", err)
			}
			if err := ValidTarget(host.Target); err != nil {
				t.Errorf("the machine in the example is not usable: %v", err)
			}

			// Read as a config rather than only as a struct. Unmarshalling
			// ignores a key that is not a field, so an example telling somebody
			// to set something that is not a per-machine setting parses
			// perfectly and does nothing -- which is a worse answer than
			// failing, because they have no reason to doubt it. Loading is what
			// notices, and it is the same path their file takes.
			dir := t.TempDir()
			whole := `{"hosts":[` + example + `]}`
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(whole), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("the README's machine entry does not load: %v", err)
			}
			for _, problem := range cfg.Problems() {
				t.Errorf("the README's machine entry has a problem with it: %s", problem)
			}
			// The prose around this one is about turning mirroring on, so the
			// mode has to be one that does.
			if host.Mode != "" && !Defaults().Mirrors(host) {
				t.Errorf("the example sets mode %q, which is not mirroring, and the text says it is",
					host.Mode)
			}
		})
	}
}

func TestTheREADMEQuotesTheCollisionWarningWordForWord(t *testing.T) {
	// The README quotes this so it can be recognised when it appears. Written
	// out by hand it agrees with the code until the wording changes, and then
	// it describes a message nobody has ever seen.
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	const marker = "hosts \"bot\" and \"ci\" are both called"
	i := strings.Index(string(readme), marker)
	if i < 0 {
		t.Fatalf("the README no longer quotes the collision warning")
	}
	shown := string(readme)[i:]
	shown = shown[:strings.Index(shown, "\n")]

	cfg := Defaults()
	cfg.Hosts = []Host{{Target: "bot", Label: "build"}, {Target: "ci", Label: "build"}}
	var want string
	for _, problem := range cfg.Problems() {
		if strings.Contains(problem, "both called") {
			want = problem
		}
	}
	if want == "" {
		t.Fatal("the code no longer reports a collision for two machines with one label")
	}
	if shown != want {
		t.Errorf("the README quotes\n\t%q\nbut the code says\n\t%q", shown, want)
	}
}

func TestTheREADMEInstallsThisRepository(t *testing.T) {
	// The install line is the first command anybody runs, and it names the
	// repository by hand in a file that has no other reason to know it. If this
	// project is ever moved or renamed, that line keeps pointing at where it
	// used to be — and the failure is somebody else's plugin being installed,
	// or none.
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}

	// github.com/Owner/repo -> Owner/repo, which is what the install takes.
	var want string
	for _, line := range strings.Split(string(mod), "\n") {
		if rest, found := strings.CutPrefix(strings.TrimSpace(line), "module github.com/"); found {
			want = strings.TrimSpace(rest)
			break
		}
	}
	if want == "" {
		t.Fatal("go.mod names no GitHub module, so there is nothing to compare against")
	}

	if !strings.Contains(string(readme), "herdr plugin install "+want) {
		t.Errorf("the README does not tell anybody to install %q", want)
	}
	// And the pinned form beside it, which is the same name again.
	if !strings.Contains(string(readme), "herdr plugin install "+want+" --ref ") {
		t.Errorf("the README's pinned install does not name %q", want)
	}
}

func TestEveryBadgeNamesThisRepository(t *testing.T) {
	// The badges hardcode owner and repository in a file that has no other
	// reason to know either, the same as the install line does. If this project
	// is moved or renamed they keep pointing at where it used to be — and a
	// badge that points somewhere else does not break, it reports on somebody
	// else's project.
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}

	var want string
	for _, line := range strings.Split(string(mod), "\n") {
		if rest, found := strings.CutPrefix(strings.TrimSpace(line), "module github.com/"); found {
			want = strings.TrimSpace(rest)
			break
		}
	}
	if want == "" {
		t.Fatal("go.mod names no GitHub module")
	}

	badges := 0
	for _, line := range strings.Split(string(readme), "\n") {
		if !strings.HasPrefix(line, "[![") {
			continue
		}
		badges++
		// Once per address, not once per line. A badge carries two — the image
		// and where clicking it goes — and checking only that the name appears
		// somewhere passes when one of the two points at another project.
		if urls, named := strings.Count(line, "https://"), strings.Count(line, want); named != urls {
			t.Errorf("a badge has %d addresses but names %q %d times: %s", urls, want, named, line)
		}
	}
	// Finding none would pass every check above, which is the failure mode of
	// reading a file in a test.
	if badges < 4 {
		t.Fatalf("found %d badges, want the four this README has", badges)
	}
}
