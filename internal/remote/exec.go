package remote

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

func runCommand(argv []string) ([]byte, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	return stdout.Bytes(), nil
}
