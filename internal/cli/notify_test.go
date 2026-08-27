package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/syncd"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
)

// recordingHerdr puts a herdr on the path that writes down what it was asked
// to do, so a notification that was or was not raised can be told apart.
func recordingHerdr(t *testing.T) func() string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "asked")
	bin := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", bin)
	return func() string {
		raw, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
}

func TestOnlyAnActionRaisesANotification(t *testing.T) {
	// Herdr sets HERDR_PLUGIN_ACTION_ID for a command it invoked as an action,
	// and an action's stdout reaches the plugin log rather than anybody. The
	// notification is the whole of what the person who pressed the key sees.
	//
	// Nothing held this either way. Backwards, every action's result becomes
	// invisible -- which is the thing the notification exists to prevent -- and
	// every run from a terminal pops a notification at somebody who is already
	// reading the output.
	t.Run("an action notifies", func(t *testing.T) {
		asked := recordingHerdr(t)
		t.Setenv("HERDR_PLUGIN_ACTION_ID", "poorplebs.remote-panes.connect")

		notifyIfAction("connected to bot")

		got := asked()
		if !strings.Contains(got, "notification show") {
			t.Errorf("an action raised no notification, so its result went only to "+
				"the log; herdr was asked: %q", got)
		}
		if !strings.Contains(got, "connected to bot") {
			t.Errorf("the notification did not carry the message: %q", got)
		}
	})

	t.Run("a terminal does not", func(t *testing.T) {
		asked := recordingHerdr(t)
		os.Unsetenv("HERDR_PLUGIN_ACTION_ID")

		notifyIfAction("connected to bot")

		if got := asked(); got != "" {
			t.Errorf("run from a terminal it still notified: %q", got)
		}
	})
}

func TestWrappingStartsExactlyAtTheNarrowestUsefulColumn(t *testing.T) {
	// Below minWrapColumn the state would come out a word per line, which reads
	// worse than a line running off the edge, so it is left whole. The boundary
	// itself was held by nothing: at exactly that much room the choice can go
	// either way and no test noticed which.
	long := "host key changed — verify it, then update ~/.ssh/known_hosts"
	hosts := []syncd.HostInfo{{Label: "bot", GaveUp: true, SSHOnly: true, LastError: long}}

	// Where the state starts, taken from a rendering rather than worked out
	// again here: the columns are sized to what is in them, so computing it
	// separately would be a second implementation to keep in step.
	wide := statusLines(hosts, 0)
	if len(wide) != 1 {
		t.Fatalf("with no width to respect the state should stay whole: %q", wide)
	}
	indent := strings.Index(wide[0], "unreachable")
	if indent < 0 {
		t.Fatalf("no state to measure from in %q", wide[0])
	}

	// Exactly enough room to be worth wrapping into.
	atBoundary := statusLines(hosts, indent+minWrapColumn)
	if len(atBoundary) < 2 {
		t.Errorf("with room for exactly %d columns the state was left whole: %q",
			minWrapColumn, atBoundary)
	}
	for _, line := range atBoundary {
		if got := text.Width(line); got > indent+minWrapColumn {
			t.Errorf("a wrapped line is %d wide against %d: %q",
				got, indent+minWrapColumn, line)
		}
	}

	// One column narrower is where wrapping stops being worth it, and the line
	// is allowed to run off the edge instead.
	belowBoundary := statusLines(hosts, indent+minWrapColumn-1)
	if len(belowBoundary) != 1 {
		t.Errorf("with one column less than the minimum it wrapped anyway: %q",
			belowBoundary)
	}
}
