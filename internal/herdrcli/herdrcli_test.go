package herdrcli

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDisplayName(t *testing.T) {
	tests := []struct {
		name string
		pane Pane
		want string
	}{
		{"explicit label wins", Pane{Label: "build", Agent: "claude", Title: "x"}, "build"},
		{"agent when unlabelled", Pane{Agent: "claude", Title: "ounos@box:~"}, "claude"},
		{"meaningful title", Pane{Title: "vim main.go"}, "vim main.go"},
		{
			// A shell banner would render as "ounos@box:~@host" once the host
			// suffix is appended, so the directory name is used instead.
			"shell banner falls back to cwd",
			Pane{Title: "ounos@box:~", Cwd: "/home/ounos/src/api"},
			"api",
		},
		{"pane id as last resort", Pane{PaneID: "w1:p3"}, "w1:p3"},
		{"root cwd is not a name", Pane{Cwd: "/", PaneID: "w1:p4"}, "w1:p4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pane.DisplayName(); got != tt.want {
				t.Errorf("DisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecodeSurfacesAPIErrors(t *testing.T) {
	_, err := Decode([]byte(`{"id":"x","error":{"code":"pane_not_found","message":"pane w1:p2 not found"}}`), []string{"pane", "read"})
	if err == nil {
		t.Fatal("expected an error for an error envelope")
	}
}

func TestParsePaneList(t *testing.T) {
	result, err := Decode([]byte(`{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"w1:p1","terminal_id":"term_a","label":"build"}]}}`), nil)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	panes, err := ParsePaneList(result)
	if err != nil {
		t.Fatalf("ParsePaneList: %v", err)
	}
	if len(panes) != 1 || panes[0].TerminalID != "term_a" {
		t.Fatalf("got %+v", panes)
	}
}

func TestParseOpenedPane(t *testing.T) {
	// The real response nests the pane under plugin_pane. Reading it from the
	// wrong level yields an empty pane id, which previously made the daemon
	// reopen a pane on every reconcile tick.
	nested := `{"type":"plugin_pane_opened","plugin_pane":{"entrypoint":"mirror","plugin_id":"p","pane":{"pane_id":"w1:p2","terminal_id":"term_b"}}}`
	pane, err := parseOpenedPane([]byte(nested))
	if err != nil {
		t.Fatalf("nested: %v", err)
	}
	if pane.PaneID != "w1:p2" {
		t.Errorf("nested pane id = %q, want w1:p2", pane.PaneID)
	}

	if _, err := parseOpenedPane([]byte(`{"type":"plugin_pane_opened"}`)); err == nil {
		t.Error("a response without a pane id must be an error, not an empty pane")
	}
}

func TestAgentState(t *testing.T) {
	// pane report-agent accepts only these four states, but a remote pane can
	// also report "done", which has to be mapped rather than rejected.
	for status, want := range map[string]string{
		"idle":    "idle",
		"working": "working",
		"blocked": "blocked",
		"unknown": "unknown",
		"done":    "idle",
		"":        "unknown",
		"weird":   "unknown",
	} {
		if got := AgentState(status); got != want {
			t.Errorf("AgentState(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestOpenPaneArgs(t *testing.T) {
	// Herdr rejects the wrong combination of targeting flags with
	// invalid_params, and ignores a flag name it does not know, so both are
	// worth pinning: neither failure is visible without reading the response.
	find := func(args []string, flag string) (string, bool) {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1], true
			}
		}
		return "", false
	}
	has := func(args []string, flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}

	t.Run("a split targets a pane", func(t *testing.T) {
		args := openPaneArgs(OpenOptions{
			PluginID: "p", Entrypoint: "mirror",
			Placement: "split", TargetPane: "w1:p1",
		})
		if got, ok := find(args, "--target-pane"); !ok || got != "w1:p1" {
			t.Errorf("args %v missing --target-pane w1:p1", args)
		}
		if has(args, "--workspace") {
			t.Errorf("args %v must not send --workspace with a split", args)
		}
	})

	t.Run("a tab targets a workspace", func(t *testing.T) {
		args := openPaneArgs(OpenOptions{
			PluginID: "p", Entrypoint: "mirror",
			Placement: "tab", Workspace: "w1",
		})
		if got, ok := find(args, "--workspace"); !ok || got != "w1" {
			t.Errorf("args %v missing --workspace w1", args)
		}
		if has(args, "--target-pane") {
			t.Errorf("args %v must not send --target-pane with a tab", args)
		}
	})

	t.Run("focus is explicit either way", func(t *testing.T) {
		// Herdr focuses by default, so a mirror opening in the background has
		// to say so, or every new pane steals focus.
		if !has(openPaneArgs(OpenOptions{Focus: true}), "--focus") {
			t.Error("a focused pane should pass --focus")
		}
		if !has(openPaneArgs(OpenOptions{Focus: false}), "--no-focus") {
			t.Error("an unfocused pane should pass --no-focus")
		}
	})

	t.Run("environment is passed as repeated flags in a stable order", func(t *testing.T) {
		args := openPaneArgs(OpenOptions{
			Env: map[string]string{"HRP_TARGET": "bot", "HRP_MODE": "ssh"},
		})
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--env HRP_MODE=ssh --env HRP_TARGET=bot") {
			t.Errorf("args %v should carry both env vars in sorted order", args)
		}
	})
}

func TestDecodeToleratesNotices(t *testing.T) {
	ok := `{"id":"x","result":{"type":"ok"}}`

	tests := []struct {
		name    string
		out     string
		wantErr bool
	}{
		{"a clean response", ok, false},
		{"a trailing newline", ok + "\n", false},
		{"blank lines around it", "\n\n" + ok + "\n\n", false},
		{
			// Herdr prints the occasional notice around its JSON, and reading
			// the output as one document made any such line fail the command.
			name: "a notice before the response",
			out:  "note: a new version of herdr is available\n" + ok,
		},
		{"a notice after the response", ok + "\nnote: restart needed", false},
		{"notices on both sides", "warning: x\n" + ok + "\nnote: y", false},
		{"no response at all", "just noise\nand more noise", true},
		{"empty output", "", false},
		{
			// An error envelope must still be reported as an error, not
			// skipped over in search of a result.
			name:    "an error envelope",
			out:     `{"id":"x","error":{"code":"pane_not_found","message":"gone"}}`,
			wantErr: true,
		},
		{
			// A later result supersedes an earlier one.
			name: "the last envelope wins",
			out:  `{"id":"a","error":{"code":"e","message":"m"}}` + "\n" + ok,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode([]byte(tt.out), []string{"pane", "list"})
			if tt.wantErr && err == nil {
				t.Errorf("Decode(%q) = nil error, want one", tt.out)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Decode(%q) = %v, want no error", tt.out, err)
			}
		})
	}
}

func TestDecodeReadsMultiLineJSON(t *testing.T) {
	// Herdr prints compact JSON today, but a response spread across lines is
	// still a response and should not be rejected for its formatting.
	pretty := "{\n  \"id\": \"x\",\n  \"result\": {\n    \"type\": \"ok\"\n  }\n}"
	if _, err := Decode([]byte(pretty), []string{"pane", "list"}); err != nil {
		t.Errorf("Decode(pretty-printed) = %v, want no error", err)
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	// An unreadable response is quoted back in the error. Cutting it by bytes
	// would leave half a character behind.
	long := strings.Repeat("☁", 400)
	got := truncate(long)
	if !utf8.ValidString(got) {
		t.Errorf("truncated text is not valid UTF-8")
	}
	if n := len([]rune(got)); n > 210 {
		t.Errorf("truncated to %d characters, want about 200", n)
	}
	// Short text is untouched.
	if got := truncate("fine"); got != "fine" {
		t.Errorf("truncate(%q) = %q", "fine", got)
	}
}
