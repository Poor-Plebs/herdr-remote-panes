package syncd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
)

// answeringAfter is an ssh that refuses the first n asks and answers after
// them, standing in for a Herdr server that is still coming up.
func answeringAfter(t *testing.T, n int) func() int {
	t.Helper()
	dir := t.TempDir()
	count := filepath.Join(dir, "asked")
	script := "#!/bin/sh\n" +
		"case \"$*\" in *command\\ -v\\ herdr*) echo /usr/bin/herdr; exit 0;; esac\n" +
		"asked=$(cat " + count + " 2>/dev/null || echo 0)\n" +
		"asked=$((asked+1)); echo $asked > " + count + "\n" +
		"if [ \"$asked\" -le " + strconv.Itoa(n) + " ]; then\n" +
		"  echo '{\"error\":{\"code\":\"server_not_running\",\"message\":\"no herdr server is running\"},\"id\":\"x\"}'\n" +
		"  exit 1\n" +
		"fi\n" +
		"echo '{\"result\":{\"panes\":[]},\"id\":\"x\"}'\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		raw, err := os.ReadFile(count)
		if err != nil {
			return 0
		}
		n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		return n
	}
}

// TestAMachineWhoseHerdrIsStillStartingIsWaitedFor holds the point of waiting.
//
// The server is launched detached over SSH, so it is not listening when the
// launch returns. Giving up on the first refusal reports a machine as having
// no Herdr when it has one a moment later -- and the reply somebody sees is
// "connected, but no terminal opened", about a machine that was fine.
func TestAMachineWhoseHerdrIsStillStartingIsWaitedFor(t *testing.T) {
	asked := answeringAfter(t, 2)

	if err := waitForRemote(remote.New("bot", ""), remoteStartTimeout); err != nil {
		t.Fatalf("gave up on a machine that answered: %v", err)
	}
	if got := asked(); got < 3 {
		t.Errorf("asked %d times; it answered on the third, so anything less "+
			"means this passed without waiting for anything", got)
	}
}

// TestAMachineThatNeverAnswersIsGivenUpOn holds the other half. Waiting for
// something that is not coming is a connect that never returns.
func TestAMachineThatNeverAnswersIsGivenUpOn(t *testing.T) {
	answeringAfter(t, 1<<30) // never

	// The bound is an argument rather than something to reach in and change:
	// a package variable written here is written while daemons started by
	// other tests are still reading it, which the race detector reported
	// within an hour of it being one.
	start := time.Now()
	err := waitForRemote(remote.New("bot", ""), 300*time.Millisecond)
	took := time.Since(start)

	if err == nil {
		t.Fatal("a machine that never answered was reported as up")
	}
	if took > 5*time.Second {
		t.Errorf("waited %v for a machine that was never coming", took.Round(time.Millisecond))
	}
	// What it returns has to be what went wrong, not a bare deadline: this is
	// what reaches the menu when connecting fails.
	if !strings.Contains(err.Error(), "herdr") && !strings.Contains(err.Error(), "server") {
		t.Errorf("the error is %q, which does not say what the machine said", err)
	}
}
