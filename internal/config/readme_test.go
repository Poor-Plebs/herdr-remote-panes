package config

import (
	"os"
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
