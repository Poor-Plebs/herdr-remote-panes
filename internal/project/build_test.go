package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTheBuildSaysWhenGoIsMissing runs the build with nothing to build with.
//
// This is the one path CI cannot take: a runner always has Go, so the message
// that exists for a machine without it is never printed and could rot without
// anything noticing — and it is the whole reason the manifest spawns a script
// rather than the compiler. Herdr spawning `go` on a machine that has none
// fails with "No such file or directory" before anything of ours runs, which
// reads like the plugin is broken rather than like a missing dependency.
func TestTheBuildSaysWhenGoIsMissing(t *testing.T) {
	inRoot(t)

	script, err := os.ReadFile("build.sh")
	if err != nil {
		t.Fatal(err)
	}

	// Every absolute place the script looks, pointed somewhere there is
	// nothing, so that "found nowhere" can be reached on a machine that does
	// have Go. The relative ones fall away with HOME below.
	absolute := regexp.MustCompile(`(?m)^\s*(/\S*/go)\b`)
	if !absolute.Match(script) {
		t.Fatal("the script no longer looks in any absolute location; this test is checking nothing")
	}
	stripped := absolute.ReplaceAllString(string(script), "\t\t/nonexistent-for-this-test$1")

	dir := t.TempDir()
	copied := filepath.Join(dir, "build.sh")
	if err := os.WriteFile(copied, []byte(stripped), 0o755); err != nil {
		t.Fatal(err)
	}

	// A PATH with the shell's own tools and no compiler.
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"sh", "cat"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s to build a stand-in PATH with: %v", tool, err)
		}
		if err := os.Symlink(found, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command(filepath.Join(bin, "sh"), copied)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + bin, "HOME=" + filepath.Join(dir, "no-home")}
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("a build with no Go anywhere reported success:\n%s", out)
	}
	said := string(out)
	// What somebody needs: what is missing, that it is needed, and where to
	// get it. Not "No such file or directory".
	for _, want := range []string{"Go was not found", "go.dev/dl", "install the plugin again"} {
		if !strings.Contains(said, want) {
			t.Errorf("the build does not say %q:\n%s", want, said)
		}
	}
	// And the other reading of the same symptom, which is the commoner one on
	// a desktop: Go is there, and the session Herdr runs in cannot see it.
	if !strings.Contains(said, "PATH") {
		t.Errorf("the build does not mention PATH, which is the other way this happens:\n%s", said)
	}
}
