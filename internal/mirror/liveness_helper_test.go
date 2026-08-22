package mirror

import (
	"os/exec"
	"testing"
)

func startTrueCommand(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	return cmd
}
