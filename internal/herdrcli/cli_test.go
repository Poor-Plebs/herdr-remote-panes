package herdrcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These run against a stand-in for the Herdr binary, so what this package does
// with an answer -- and with a refusal -- is exercised rather than described.

// fakeHerdr puts a herdr on PATH that behaves as the script says.
func fakeHerdr(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "herdr")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", path)
}

// refuses is the envelope Herdr prints when it will not do something.
func refuses(code, message string) string {
	return `echo '{"error":{"code":"` + code + `","message":"` + message +
		`"},"id":"cli:test"}'` + "\nexit 1\n"
}

func TestClosingSomethingAlreadyGoneIsNotAFailure(t *testing.T) {
	// The daemon closes panes it saw in a listing a moment earlier, and a pane
	// can go in between -- somebody closes it, or its own command exits. That
	// is the outcome being asked for, so reporting it as an error would fill
	// the log with failures that are nothing of the kind.
	t.Run("a pane that has gone", func(t *testing.T) {
		fakeHerdr(t, refuses("pane_not_found", "pane w1:p2 not found"))
		if err := ClosePaneByID("w1:p2"); err != nil {
			t.Errorf("closing a pane that was already gone = %v, want nil", err)
		}
	})

	t.Run("a space that has gone", func(t *testing.T) {
		// Focus is asked for after connecting, and a machine whose space closed
		// in between is not a failure worth reporting over the connection
		// having worked.
		fakeHerdr(t, refuses("workspace_not_found", "workspace w1 not found"))
		if err := FocusWorkspace("w1"); err != nil {
			t.Errorf("focusing a space that was already gone = %v, want nil", err)
		}
	})

	t.Run("but a real refusal is still a refusal", func(t *testing.T) {
		// Only "gone" is forgiven. Swallowing everything would turn a Herdr
		// that is refusing for some other reason into silence.
		fakeHerdr(t, refuses("permission_denied", "not allowed"))
		if err := ClosePaneByID("w1:p2"); err == nil {
			t.Error("a refusal that was not about the pane being gone was swallowed")
		}
		fakeHerdr(t, refuses("server_unavailable", "server is shutting down"))
		if err := FocusWorkspace("w1"); err == nil {
			t.Error("a server that is going away was read as a space that had gone")
		}
	})
}

func TestNotFoundIsAboutTheCodeNotTheWording(t *testing.T) {
	// Matched on the code Herdr sets, not on the message, which is prose and
	// free to change.
	fakeHerdr(t, refuses("tab_not_found", "no such tab"))
	_, err := Run("tab", "list")
	if !IsNotFound(err) {
		t.Errorf("IsNotFound on %v = false, want true: the code ends in _not_found", err)
	}

	fakeHerdr(t, refuses("busy", "pane not found in the cache, try again"))
	_, err = Run("pane", "list")
	if IsNotFound(err) {
		t.Errorf("IsNotFound on %v = true, but the code is not a not-found", err)
	}
}

func TestWorkspaceLabelFindsTheRightSpace(t *testing.T) {
	fakeHerdr(t, `echo '{"result":{"workspaces":[`+
		`{"workspace_id":"w1","label":"one"},`+
		`{"workspace_id":"w2","label":"☁  bot"}`+
		`]},"id":"cli:test"}'`)

	if got := WorkspaceLabel("w2"); got != "☁  bot" {
		t.Errorf("WorkspaceLabel(w2) = %q", got)
	}
	// A space that is not there is not an error to the caller: it decides what
	// an unknown space means.
	if got := WorkspaceLabel("w9"); got != "" {
		t.Errorf("WorkspaceLabel(w9) = %q, want empty", got)
	}
}

func TestWorkspaceLabelSaysNothingRatherThanGuessing(t *testing.T) {
	// The caller uses the label to work out which machine a pane belongs to.
	// Anything other than the real label would put a terminal on the wrong
	// machine, so a failure has to read as "do not know".
	for _, name := range []string{"herdr refuses", "herdr says nothing", "unreadable output"} {
		t.Run(name, func(t *testing.T) {
			switch name {
			case "herdr refuses":
				fakeHerdr(t, refuses("server_unavailable", "server is shutting down"))
			case "herdr says nothing":
				fakeHerdr(t, "exit 0\n")
			case "unreadable output":
				fakeHerdr(t, `echo 'not json at all'`)
			}
			if got := WorkspaceLabel("w1"); got != "" {
				t.Errorf("WorkspaceLabel = %q, want empty", got)
			}
		})
	}
}

func TestACallThatNeverAnswersIsGivenUpOn(t *testing.T) {
	// These go to a socket on this machine and answer in milliseconds. The
	// bound is there because the reconcile loop holds the daemon's lock while
	// it runs, so one call that never returns takes the status listing, the
	// menu and every machine down with it.
	restore := commandTimeout
	commandTimeout = 100 * time.Millisecond
	defer func() { commandTimeout = restore }()

	fakeHerdr(t, "sleep 30\n")

	done := make(chan error, 1)
	go func() {
		_, err := Run("pane", "list")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a call that never answered was treated as success")
		}
		// Said in terms of what was being waited for, since the caller has
		// nothing else to go on.
		if !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "pane list") {
			t.Errorf("the error is %q, which does not say which call gave up", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call was not given up on")
	}
}

func TestANoticeAroundTheAnswerDoesNotBreakIt(t *testing.T) {
	// Herdr prints the occasional line of its own -- an available update, a
	// banner -- around its JSON. Reading the output as one document made any
	// such line fail every command with "unreadable response".
	fakeHerdr(t, `echo 'A new version of herdr is available.'
echo '{"result":{"panes":[{"pane_id":"w1:p1"}]},"id":"cli:test"}'
echo 'Run herdr upgrade to install it.'`)

	panes, err := PaneList()
	if err != nil {
		t.Fatalf("PaneList: %v", err)
	}
	if len(panes) != 1 || panes[0].PaneID != "w1:p1" {
		t.Errorf("panes = %+v, want the one in the answer", panes)
	}
}

func TestWhichCallsTreatAMissingThingAsDone(t *testing.T) {
	// The daemon reconciles against a listing that is a moment old, and Herdr
	// removes things too. So the two race over which of them closes a pane
	// first, and the loser is told it is not there — which is the outcome it
	// asked for, not a failure. Reporting it as one filled the log with the
	// daemon complaining that something it wanted gone was gone.
	//
	// Which calls forgive it is a decision, not a property of the error: a
	// rename that finds nothing is a pane that went while being named, and
	// worth hearing about. Nothing held the split.
	fakeHerdr(t, refuses("pane_not_found", "pane w1:p2 not found"))

	for _, tt := range []struct {
		what    string
		run     func() error
		forgive bool
	}{
		{"closing a pane", func() error { return ClosePane("w1:p2") }, true},
		{"closing one by id", func() error { return ClosePaneByID("w1:p2") }, true},
		{"releasing an agent", func() error {
			return ReleaseAgent("w1:p2", "poorplebs.remote-panes", "claude")
		}, true},
		{"focusing a space", func() error { return FocusWorkspace("w1") }, true},

		// Not asking for a thing to be gone, so its being gone is news.
		{"renaming a pane", func() error { return RenamePane("w1:p2", "shell@bot") }, false},
		{"reporting an agent", func() error {
			return ReportAgent("w1:p2", "poorplebs.remote-panes", "claude", AgentState("working"))
		}, false},
	} {
		t.Run(tt.what, func(t *testing.T) {
			err := tt.run()
			if tt.forgive && err != nil {
				t.Errorf("%s reported a failure for a thing already gone: %v", tt.what, err)
			}
			if !tt.forgive && err == nil {
				t.Errorf("%s said nothing about a pane that was not there", tt.what)
			}
		})
	}
}
