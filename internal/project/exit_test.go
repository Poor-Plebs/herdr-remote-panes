package project

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestTheBinaryExitsWithWhatMainDecided holds the one line in package main.
//
// cli.Main returns a code -- 2 for a command nobody gave, 1 for one that
// failed, 0 for one that worked -- and main.go's whole body is os.Exit of it.
// That line is the only thing turning the decision into something a machine can
// read, and it sits in the one package with no tests at all: replacing the body
// with `cli.Main(); os.Exit(0)` passes every check in this repository, measured,
// while the plugin reports success for everything it does.
//
// The status is what Herdr reads. An action invoked as `herdr plugin action
// invoke poorplebs.remote-panes.connect` that could not reach the machine would
// come back as done, and so would any script joining the command with `&&`.
//
// All three codes are asserted rather than "zero and non-zero": a body that
// always failed would satisfy the two failing rows, and one that always
// succeeded would satisfy the passing one. Only passing the code THROUGH
// satisfies every row.
func TestTheBinaryExitsWithWhatMainDecided(t *testing.T) {
	root := inRoot(t)

	binary := filepath.Join(t.TempDir(), "herdr-remote-panes")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the plugin: %v\n%s", err, out)
	}

	tests := []struct {
		name string
		args []string
		want int
	}{
		// None of the three needs a daemon, a config or a machine, which is
		// what makes the status askable at all here.
		{"nothing to do at all", nil, 2},
		{"a command that is not one", []string{"bogus"}, 1},
		{"a command that works", []string{"version"}, 0},
	}

	// The table says something only because the three differ. Were two rows to
	// expect the same code, a body that threw the answer away and exited with a
	// constant would look exactly like one that passed it on.
	seen := map[int]bool{}
	for _, tt := range tests {
		seen[tt.want] = true
	}
	if len(seen) != len(tests) {
		t.Fatal("the rows no longer expect different codes, so a constant exit " +
			"and a code passed through cannot be told apart here")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = t.TempDir()
			code := 0
			if err := cmd.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("running the plugin with %v: %v", tt.args, err)
				}
				code = exit.ExitCode()
			}
			if code != tt.want {
				t.Errorf("the plugin run with %v exited %d, want %d: that status is "+
					"what Herdr reads to know whether the action worked", tt.args, code, tt.want)
			}
		})
	}
}
