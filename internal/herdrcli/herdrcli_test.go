package herdrcli

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
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
		{
			// An error with a notice beside it. On its own an error envelope
			// is rescued by the whole-output reading below even if the
			// line-by-line pass drops it, so the two cannot be told apart --
			// a notice is what stops the whole output being JSON, and leaves
			// the line-by-line pass as the only thing that can find it.
			name:    "an error envelope with a notice before it",
			out:     "note: a new version of herdr is available\n" + `{"id":"x","error":{"code":"pane_not_found","message":"gone"}}`,
			wantErr: true,
		},
		{
			name:    "an error envelope with a notice after it",
			out:     `{"id":"x","error":{"code":"pane_not_found","message":"gone"}}` + "\nnote: restart needed",
			wantErr: true,
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

	// The two either side of the bound. A response that is exactly as long as
	// this will carry must come back as it was: shortened, it reads as a
	// response that was cut off, and what somebody does with that is go looking
	// for the rest of a message that was whole.
	const max = 200
	for _, tt := range []struct {
		what   string
		length int
		cut    bool
	}{
		{"one short of the bound", max - 1, false},
		{"exactly the bound", max, false},
		{"one over it", max + 1, true},
	} {
		in := strings.Repeat("x", tt.length)
		got := truncate(in)
		if cut := got != in; cut != tt.cut {
			t.Errorf("%s: truncate on %d characters gave %d, and %v is not what "+
				"should happen there", tt.what, tt.length, len([]rune(got)), map[bool]string{true: "shortening it", false: "leaving it"}[cut])
		}
		if tt.cut && !strings.HasSuffix(got, "...") {
			t.Errorf("%s: a shortened response does not say it was shortened: %q", tt.what, got[len(got)-8:])
		}
	}
}

func TestLooksLikeShellTitle(t *testing.T) {
	// Any title containing "@" or ":" used to count as the shell's prompt
	// banner, which threw away most of the useful ones: a pane running
	// "npm run build:prod" was named after whichever directory it sat in.
	//
	// The shape identifies a banner, not the punctuation. The host part of
	// "user@host:dir" runs up to the colon with no spaces in it, which is
	// exactly what a command line does not do.
	banners := []string{
		"deploy@binance-futures-bot: ~",
		"ounos@L14:~",
		"ounos@L14:~/work/project",
		"root@10.0.0.4:/var/log",
		"user@host",
		"L14:~",
		"L14:/var/log",
		"L14: ~/work",
		// A prompt sitting at the root of nothing, and one whose user part is
		// missing: still the host-and-path shape, so still a banner.
		"user@host:",
		"@host:/path",
	}
	for _, title := range banners {
		if !looksLikeShellTitle(title) {
			t.Errorf("%q should be recognised as a prompt banner", title)
		}
	}

	titles := []string{
		"npm run build:prod",
		"vim: notes.md",
		"make test:unit",
		"docker:postgres",
		"ssh user@server",
		"git rebase -i HEAD~3",
		"cargo build",
		"htop",
		"psql -U deploy -h db",
		"tail -f /var/log/syslog",
		"make: *** [all] Error 1",
		"",

		// Shapes that come close to a banner and are not one. Each of these
		// is one character in the matching away from being thrown away in
		// favour of the directory the pane happens to be in.
		//
		// A colon with no host in front of it is not a prompt, whatever
		// follows it.
		":/var/log",
		":~",
		// Something after the host that is not a path. A prompt puts a colon
		// there; a command line puts a space, and what follows is the rest of
		// the command rather than a directory.
		"user@host make test",
		"user@ x",
		// An "@" with no user in front of it. "user@host" is a banner because
		// somebody is logged in somewhere; this is a mention of a host, or a
		// handle, and throwing the title away for it would leave the pane
		// named after a directory nobody asked about.
		"@host",
		"@",
	}
	for _, title := range titles {
		if looksLikeShellTitle(title) {
			t.Errorf("%q is a command, not a prompt banner", title)
		}
	}
}

func TestDisplayNamePrefersARealTitleOverTheDirectory(t *testing.T) {
	// The point of the tightening: a pane running something recognisable
	// should say so rather than being named after where it happens to be.
	p := Pane{Title: "npm run build:prod", Cwd: "/home/ounos/work", PaneID: "w1:p1"}
	if got := p.DisplayName(); got != "npm run build:prod" {
		t.Errorf("DisplayName() = %q, want the command", got)
	}

	// A prompt banner still falls through to the directory, which is the more
	// useful of the two.
	p = Pane{Title: "ounos@L14:~", Cwd: "/home/ounos/work", PaneID: "w1:p1"}
	if got := p.DisplayName(); got != "work" {
		t.Errorf("DisplayName() = %q, want the directory", got)
	}

	// An explicit label always wins, whatever the title says.
	p = Pane{Label: "build", Title: "npm run build:prod", Cwd: "/home/ounos/work"}
	if got := p.DisplayName(); got != "build" {
		t.Errorf("DisplayName() = %q, want the label", got)
	}
}

func TestSafeAgent(t *testing.T) {
	// An agent's name is set by whatever runs in the pane, which for a remote
	// pane is something at the other end of an SSH connection. It reaches a
	// sidebar by two routes -- as part of a pane's name, and through
	// report-agent -- and cleaning it at each route is how one of them came to
	// be missed.
	p := Pane{Agent: "claude\x1b[31m\nfake\r"}
	got := p.SafeAgent()

	for _, bad := range []string{"\x1b", "\n", "\r"} {
		if strings.Contains(got, bad) {
			t.Errorf("SafeAgent() = %q, still carries %q", got, bad)
		}
	}
	if !strings.Contains(got, "claude") {
		t.Errorf("SafeAgent() = %q, lost the readable part", got)
	}

	// An unbounded name would crowd out everything beside it. Sixty-four
	// written out rather than maxAgentName: measuring against the bound means
	// raising the bound raises what this expects, so five hundred characters
	// would arrive whole and this would still pass -- which it did.
	long := Pane{Agent: strings.Repeat("x", 500)}
	if n := len([]rune(long.SafeAgent())); n > 64 {
		t.Errorf("SafeAgent() is %d runes of a five hundred character name, and "+
			"it shares a sidebar with everything else", n)
	}

	// Nothing in, nothing out: an empty agent means no agent, and must not
	// become something that looks like one.
	if got := (Pane{}).SafeAgent(); got != "" {
		t.Errorf("SafeAgent() = %q, want empty", got)
	}
	if got := (Pane{Agent: "   "}).SafeAgent(); got != "" {
		t.Errorf("SafeAgent() = %q for whitespace, want empty", got)
	}
}

func TestIsNotFound(t *testing.T) {
	// A caller that has just been told the thing it was working on is gone
	// should stop asking after it. The daemon kept a machine's workspace id
	// after Herdr had removed the space, and renamed and marked it on every
	// pass for as long as it ran -- two failing calls every couple of seconds.
	_, err := Decode([]byte(
		`{"error":{"code":"workspace_not_found","message":"workspace w37 not found"}}`),
		[]string{"workspace", "rename", "w37"})
	if err == nil {
		t.Fatal("an error response decoded as success")
	}
	if !IsNotFound(err) {
		t.Errorf("%v is not recognised as a thing that is gone", err)
	}
	// The words still say what happened, since they end up in a log.
	for _, want := range []string{"workspace w37 not found", "workspace_not_found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}

	// Other refusals are not this.
	_, err = Decode([]byte(`{"error":{"code":"invalid_params","message":"bad"}}`),
		[]string{"pane", "open"})
	if IsNotFound(err) {
		t.Errorf("%v should not be read as a thing that is gone", err)
	}
	if IsNotFound(nil) {
		t.Error("nil should not be read as a thing that is gone")
	}
	if IsNotFound(errors.New("some other trouble")) {
		t.Error("an unrelated error should not be read as a thing that is gone")
	}
}

func TestRunErrorKeepsTheCodeHerdrGave(t *testing.T) {
	// Herdr signals a refusal by exiting non-zero and printing the error
	// envelope. Returning the exit status alone threw the code away -- which is
	// the part a caller can act on -- so IsNotFound could never be true for a
	// real failure, only for output decoded directly.
	envelope := []byte(`{"error":{"code":"workspace_not_found","message":"workspace w2Y not found"},"id":"cli:workspace:rename"}`)
	exit := errors.New("exit status 1")

	// Herdr prints it to stderr.
	err := RunError(exit, []string{"workspace", "rename", "w2Y"}, envelope, nil)
	if !IsNotFound(err) {
		t.Errorf("%v does not carry the code", err)
	}

	// And to stdout in some versions, so both are looked at.
	err = RunError(exit, []string{"workspace", "rename", "w2Y"}, nil, envelope)
	if !IsNotFound(err) {
		t.Errorf("%v does not carry the code when it is on stdout", err)
	}

	// A failure with no envelope still says what happened.
	err = RunError(exit, []string{"pane", "list"}, []byte("command not found"), nil)
	if IsNotFound(err) {
		t.Errorf("%v should not be read as a thing that is gone", err)
	}
	for _, want := range []string{"pane list", "exit status 1", "command not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}

	// Nothing printed at all: the exit status is all there is.
	err = RunError(exit, []string{"pane", "list"}, nil, nil)
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("error %q should still name the failure", err)
	}
}

func TestIgnoreNotFound(t *testing.T) {
	// Closing a pane is asked for when the pane should not exist, and a
	// reconciling daemon races with Herdr over which of them removes a thing
	// first. Reporting the loser of that race as a failure filled the log with
	// the daemon complaining that something it wanted gone was gone.
	gone := &APIError{Command: "pane close w1:p2", Code: "pane_not_found", Message: "pane w1:p2 not found"}
	if err := ignoreNotFound(gone); err != nil {
		t.Errorf("a pane already gone was reported as a failure: %v", err)
	}

	// Anything else is still a failure and must not be swallowed.
	refused := &APIError{Command: "pane close w1:p2", Code: "invalid_params", Message: "bad id"}
	if err := ignoreNotFound(refused); err == nil {
		t.Error("a refusal was swallowed")
	}
	other := errors.New("herdr is not installed")
	if err := ignoreNotFound(other); err == nil {
		t.Error("an unrelated failure was swallowed")
	}
	if err := ignoreNotFound(nil); err != nil {
		t.Errorf("ignoreNotFound(nil) = %v", err)
	}
}

func TestFocusingASpaceThatHasGoneIsNotAFailure(t *testing.T) {
	// Focus is asked for after connecting, and a machine whose space closed in
	// between is not a failure worth reporting over the connection having
	// worked.
	gone := &APIError{Command: "workspace focus w9", Code: "workspace_not_found", Message: "workspace w9 not found"}
	if err := ignoreNotFound(gone); err != nil {
		t.Errorf("a space already gone was reported as a failure: %v", err)
	}
}

func TestOpenPaneArgsAsksForFocusOrRefusesIt(t *testing.T) {
	// A pane opened because someone asked for it should be the one they end up
	// looking at; one opened because a link came back should not take the
	// screen from under them. Both have to be said explicitly -- leaving the
	// flag off entirely would let Herdr decide, and it decides differently for
	// different placements.
	wanted := strings.Join(openPaneArgs(OpenOptions{PluginID: "p", Entrypoint: "e", Focus: true}), " ")
	if !strings.Contains(wanted, "--focus") || strings.Contains(wanted, "--no-focus") {
		t.Errorf("args = %q, want --focus", wanted)
	}

	unwanted := strings.Join(openPaneArgs(OpenOptions{PluginID: "p", Entrypoint: "e"}), " ")
	if !strings.Contains(unwanted, "--no-focus") {
		t.Errorf("args = %q, want --no-focus", unwanted)
	}
}

func TestRunBoundsACommandThatNeverReturns(t *testing.T) {
	// These calls go to a socket on this machine and answer in milliseconds,
	// but the reconcile loop holds the daemon's lock while one runs: a call
	// that never returns takes the status listing, the menu and every machine
	// with it. The calls to other machines have been bounded since the day one
	// of them froze everything; these were not, and they run far more often.
	original := commandTimeout
	commandTimeout = 200 * time.Millisecond
	defer func() { commandTimeout = original }()

	t.Setenv("HERDR_BIN_PATH", "/bin/sleep")

	start := time.Now()
	_, err := Run("30")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a command that outlives the deadline should fail")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want it to say it timed out", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %s, want it bounded near %s", elapsed, commandTimeout)
	}
}

func TestEveryOpenOptionIsRenderedAsItsFlag(t *testing.T) {
	// Herdr ignores a flag name it does not know and takes its own default when
	// one is missing, so a flag that stops being sent fails silently: every
	// pane simply opens in the default placement, or in the wrong directory,
	// and nothing says why. That is the reason this is a separate function.
	//
	// Checked one option at a time, because the mistake is per-option: three of
	// these could be dropped without a single test noticing.
	value := func(args []string, flag string) (string, bool) {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1], true
			}
		}
		return "", false
	}
	present := func(args []string, flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}

	for _, tt := range []struct {
		flag string
		set  func(*OpenOptions)
		want string
	}{
		{"--placement", func(o *OpenOptions) { o.Placement = "zoomed" }, "zoomed"},
		{"--workspace", func(o *OpenOptions) { o.Workspace = "w4A" }, "w4A"},
		{"--target-pane", func(o *OpenOptions) { o.TargetPane = "w4A:p2" }, "w4A:p2"},
		{"--direction", func(o *OpenOptions) { o.Direction = "right" }, "right"},
		{"--cwd", func(o *OpenOptions) { o.Cwd = "/srv/app" }, "/srv/app"},
	} {
		t.Run(tt.flag, func(t *testing.T) {
			opts := OpenOptions{PluginID: "p", Entrypoint: "mirror"}
			tt.set(&opts)
			args := openPaneArgs(opts)
			got, ok := value(args, tt.flag)
			if !ok {
				t.Fatalf("%s was set but %v does not carry it", tt.flag, args)
			}
			if got != tt.want {
				t.Errorf("%s = %q, want %q", tt.flag, got, tt.want)
			}

			// And left out entirely when unset. Sending the flag with an empty
			// value is not the same as not sending it: Herdr takes it as an
			// instruction to use "", which is not what a default is.
			bare := openPaneArgs(OpenOptions{PluginID: "p", Entrypoint: "mirror"})
			if present(bare, tt.flag) {
				t.Errorf("%s is sent even when unset: %v", tt.flag, bare)
			}
		})
	}
}

func TestTheFlagNamesHerdrWouldIgnoreIfTheyWereWrong(t *testing.T) {
	// Herdr ignores a flag it does not recognise. That makes a misspelt one a
	// feature that quietly stops working: the agent never appears in the
	// sidebar, the pane never splits, and there is no error anywhere to say so.
	//
	// These names were checked against Herdr 0.8.2 by asking it what each
	// command takes. That cannot be done from a test — CI has no Herdr — so
	// what a test can do is stop them drifting afterwards.
	for _, tt := range []struct {
		what string
		got  []string
		want []string
	}{
		{
			"reporting an agent",
			reportAgentArgs("w1:p2", "poorplebs.remote-panes", "claude", "working"),
			[]string{"pane", "report-agent", "w1:p2",
				"--source", "poorplebs.remote-panes", "--agent", "claude", "--state", "working"},
		},
		{
			// No --state here, which is the difference from reporting one.
			"releasing one",
			releaseAgentArgs("w1:p2", "poorplebs.remote-panes", "claude"),
			[]string{"pane", "release-agent", "w1:p2",
				"--source", "poorplebs.remote-panes", "--agent", "claude"},
		},
		{
			// The local pane somebody gets when they ask for a terminal
			// somewhere that is not a machine's space.
			"splitting a pane here",
			splitPaneArgs("right"),
			[]string{"pane", "split", "--direction", "right", "--focus"},
		},
	} {
		t.Run(tt.what, func(t *testing.T) {
			if len(tt.got) != len(tt.want) {
				t.Fatalf("sends %v, want %v", tt.got, tt.want)
			}
			for i := range tt.want {
				if tt.got[i] != tt.want[i] {
					t.Errorf("argument %d is %q, want %q\n  sends %v", i, tt.got[i], tt.want[i], tt.got)
				}
			}
		})
	}
}

func TestAResponseIsReadAsWhatItIsRatherThanMerelyAsAFailure(t *testing.T) {
	// Two ways of getting an error back that are not the same error, and the
	// difference decides what the daemon does next.
	//
	// "pane_not_found" is benign: the pane went away between being listed and
	// being acted on, which happens on any busy session, and the caller carries
	// on. "unreadable response" is not benign. A test that asks only whether
	// something failed passes for either, so dropping the real error on the way
	// out and reporting the output as gibberish looks like a pass.
	notFound := `{"id":"x","error":{"code":"pane_not_found","message":"gone"}}`

	for _, out := range []string{
		notFound,
		"note: a new version of herdr is available\n" + notFound,
		notFound + "\nnote: restart needed",
		"warning: x\n" + notFound + "\nnote: y",
	} {
		_, err := Decode([]byte(out), []string{"pane", "close"})
		if err == nil {
			t.Errorf("Decode(%q) reported no error at all", out)
			continue
		}
		if !IsNotFound(err) {
			t.Errorf("Decode(%q) = %v, which no longer reads as a pane that has "+
				"gone -- so a pane closing under us becomes a failure", out, err)
		}
	}

	// And an envelope carrying neither a result nor an error is not a quiet
	// success. Read as one, a command that did nothing at all reports that it
	// worked, and the caller goes on to use what it did not get.
	for _, out := range []string{`{"id":"x"}`, "{}", `{"id":"x","other":1}`} {
		if _, err := Decode([]byte(out), []string{"pane", "list"}); err == nil {
			t.Errorf("Decode(%q) read an envelope with nothing in it as a success", out)
		}
	}
}

func TestWhichHerdrBinaryIsRun(t *testing.T) {
	// Herdr injects the path to itself, and everything in the suite sets it --
	// which is why the case a real run takes had never been run at all. If the
	// fallback were wrong, every invocation outside a test would fail on a
	// binary that could not be found, and the tests would go on passing.
	t.Setenv("HERDR_BIN_PATH", "/opt/herdr/bin/herdr")
	if got := Bin(); got != "/opt/herdr/bin/herdr" {
		t.Errorf("Bin() = %q, want the path Herdr injected", got)
	}

	// Unset, and set to nothing, are both "Herdr did not say": fall back to
	// the name and let the PATH answer.
	t.Setenv("HERDR_BIN_PATH", "")
	if got := Bin(); got != "herdr" {
		t.Errorf("with no path injected, Bin() = %q, want herdr", got)
	}
	os.Unsetenv("HERDR_BIN_PATH")
	if got := Bin(); got != "herdr" {
		t.Errorf("with the variable unset, Bin() = %q, want herdr", got)
	}
}

func TestDecodeFindsTheReplyWhateverSurroundsIt(t *testing.T) {
	// Herdr prints the occasional notice around its JSON -- an available
	// update, a banner -- which is why this reads line by line instead of
	// treating the output as one document.
	//
	// Each line is trimmed before it is looked at, and nothing held that.
	// Without the trim a line that starts with a space fails the test for a
	// leading brace and is skipped as though it were a notice, so the reply it
	// carried is never found and the command fails with "unreadable response"
	// -- against a Herdr that indents its output, or one that ends its lines
	// the other way, neither of which is this plugin's to decide.
	const reply = `{"id":"x","result":{"type":"pane_list","panes":[]}}`

	for _, tt := range []struct {
		what string
		out  string
	}{
		{"on its own", reply},
		{"indented", "   " + reply},
		{"after a notice", "herdr 0.9.0 is available\n" + reply},
		{"indented, after a notice, and ended the other way",
			"herdr 0.9.0 is available\r\n  " + reply + "  \r"},
	} {
		result, err := Decode([]byte(tt.out), []string{"pane", "list"})
		if err != nil {
			t.Errorf("%s: %v", tt.what, err)
			continue
		}
		if len(result) == 0 {
			t.Errorf("%s: the reply was found but came back empty", tt.what)
		}
	}
}

func TestWhatHerdrSaidOnStandardErrorReachesTheFailure(t *testing.T) {
	// RunError is tested thoroughly with bytes handed straight to it. Whether
	// Herdr's own standard error ever reaches it was not, and deleting the
	// line that captures it left every test in this package green: the command
	// still fails, and still reports, and what it reports is an exit status
	// with nothing Herdr said about it.
	//
	// A failure that names no reason is a shape this project keeps finding.
	// The same hole in the mirror left one real mirror.log holding a hundred
	// and forty-one failures and not one reason among them.
	fakeHerdr(t, "echo 'that space is not a thing' >&2\nexit 1\n")

	_, err := Run("pane", "list")
	if err == nil {
		t.Fatal("a herdr that exited non-zero was reported as success")
	}
	if !strings.Contains(err.Error(), "that space is not a thing") {
		t.Errorf("the failure reads %q, and herdr said why on standard error", err)
	}
}
