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
