package syncd

import (
	"errors"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/remote"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"encoding/json"
	"fmt"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/config"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
	"github.com/Poor-Plebs/herdr-remote-panes/internal/text"
)

// Each test here stands for a bug that reached a real machine. The comments say
// what went wrong, so the reason a rule exists survives the next refactor.

func TestPlanPaneTarget(t *testing.T) {
	tests := []struct {
		name            string
		placement       string
		workspace       string
		paneInWorkspace string
		want            paneTarget
	}{
		{
			// Sending workspace_id with a split was rejected as invalid_params,
			// so no split mirror ever opened. A split targets a pane only.
			name:      "split targets a pane and never a workspace",
			placement: placementSplit, workspace: "w1", paneInWorkspace: "w1:p1",
			want: paneTarget{Placement: placementSplit, TargetPane: "w1:p1"},
		},
		{
			name:      "zoomed behaves like split",
			placement: placementZoomed, workspace: "w1", paneInWorkspace: "w1:p1",
			want: paneTarget{Placement: placementZoomed, TargetPane: "w1:p1"},
		},
		{
			// Herdr splits relative to an existing pane. A workspace with none
			// cannot be split, so a tab is the only thing that will open.
			name:      "split with nothing to split from becomes a tab",
			placement: placementSplit, workspace: "w1", paneInWorkspace: "",
			want: paneTarget{Placement: placementTab, Workspace: "w1"},
		},
		{
			// A tab rejects target_pane_id, the mirror image of the split rule.
			name:      "tab targets a workspace and never a pane",
			placement: placementTab, workspace: "w1", paneInWorkspace: "w1:p1",
			want: paneTarget{Placement: placementTab, Workspace: "w1"},
		},
		{
			name:      "overlay targets nothing",
			placement: placementOverlay, workspace: "w1", paneInWorkspace: "w1:p1",
			want: paneTarget{Placement: placementOverlay},
		},
		{
			name:      "popup targets nothing",
			placement: placementPopup, workspace: "w1", paneInWorkspace: "w1:p1",
			want: paneTarget{Placement: placementPopup},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planPaneTarget(tt.placement, tt.workspace, tt.paneInWorkspace)
			if got != tt.want {
				t.Errorf("planPaneTarget(%q, %q, %q) = %+v, want %+v",
					tt.placement, tt.workspace, tt.paneInWorkspace, got, tt.want)
			}
			// The rule that actually broke things: never both at once.
			if got.Workspace != "" && got.TargetPane != "" {
				t.Errorf("%+v sets both workspace and target pane; Herdr rejects that", got)
			}
		})
	}
}

func TestPlanTrackedMirror(t *testing.T) {
	tests := []struct {
		name          string
		adopted       bool
		paneAlive     bool
		mirrorRunning bool
		want          mirrorAction

		failed bool
	}{
		{
			// A daemon restart used to open a second mirror for every terminal,
			// and the duplicates then fought over the exclusive remote attach.
			name:    "a live mirror is adopted, not reopened",
			adopted: false, paneAlive: true, mirrorRunning: true, want: mirrorKeep,
		},
		{
			// Restarting Herdr restores a plugin pane as a plain shell without
			// re-running its command: the pane and its name survive, the mirror
			// does not. Adopting that husk stranded the terminal forever.
			name:    "a pane whose mirror is not running is replaced",
			adopted: false, paneAlive: true, mirrorRunning: false, want: mirrorReplace,
		},
		{
			// Panes from a previous session are gone after a restart. Treating
			// that as a deliberate close meant nothing was ever mirrored again.
			name:    "a pane missing on the first pass is stale bookkeeping",
			adopted: false, paneAlive: false, mirrorRunning: false, want: mirrorForget,
		},
		{
			// Once running, a pane that disappears was closed by the user, and
			// reopening it would fight them every couple of seconds.
			name:    "a pane that disappears later was closed by hand",
			adopted: true, paneAlive: false, mirrorRunning: false, want: mirrorDismiss,
		},
		{
			// Unless its bridge said why it went. A pane that dropped is not a
			// pane somebody shut, and treating it as one closes the terminal on
			// the machine: a moment of trouble reaching a machine would destroy
			// the work on it.
			name:    "a pane whose bridge failed dropped, it was not closed",
			adopted: true, paneAlive: false, mirrorRunning: false, failed: true,
			want: mirrorForget,
		},
		{
			name:    "a healthy mirror is left alone",
			adopted: true, paneAlive: true, mirrorRunning: true, want: mirrorKeep,
		},
		{
			// After adoption the liveness mark is not consulted: a mirror can
			// legitimately be between processes without being a husk.
			name:    "liveness is only checked before adoption",
			adopted: true, paneAlive: true, mirrorRunning: false, want: mirrorKeep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Empty terminal ids: these cases are about the pane, not about
			// which terminal is in it, and that is checked separately below.
			got := planTrackedMirrorFor(tt.adopted, tt.paneAlive, tt.mirrorRunning, tt.failed, "", "")
			if got != tt.want {
				t.Errorf("planTrackedMirrorFor(adopted=%v, alive=%v, running=%v, failed=%v) = %v, want %v",
					tt.adopted, tt.paneAlive, tt.mirrorRunning, tt.failed, got, tt.want)
			}
		})
	}
}

func TestPlanLabels(t *testing.T) {
	t.Run("distinct names are left alone", func(t *testing.T) {
		labels := planLabels([]herdrcli.Pane{
			{TerminalID: "t1", PaneID: "w1:p1", Label: "build"},
			{TerminalID: "t2", PaneID: "w1:p2", Label: "tests"},
		})
		if labels["t1"] != "build" || labels["t2"] != "tests" {
			t.Errorf("labels = %v, want build/tests unchanged", labels)
		}
	})

	t.Run("repeated names get the short pane id", func(t *testing.T) {
		// Unnamed panes fall back to their directory, so several shells in one
		// directory all read "deploy" and became indistinguishable.
		labels := planLabels([]herdrcli.Pane{
			{TerminalID: "t1", PaneID: "w1:p1", Cwd: "/home/deploy"},
			{TerminalID: "t2", PaneID: "w1:p2", Cwd: "/home/deploy"},
		})
		if labels["t1"] != "deploy p1" || labels["t2"] != "deploy p2" {
			t.Errorf("labels = %v, want deploy p1 / deploy p2", labels)
		}
	})

	t.Run("full pane id when the short one repeats too", func(t *testing.T) {
		// Panes from different remote workspaces share short ids: w2:p1 and
		// w3:p1 both shorten to p1, so both mirrors read "deploy p1".
		labels := planLabels([]herdrcli.Pane{
			{TerminalID: "t1", PaneID: "w2:p1", Cwd: "/home/deploy"},
			{TerminalID: "t2", PaneID: "w3:p1", Cwd: "/home/deploy"},
		})
		if labels["t1"] != "deploy w2:p1" || labels["t2"] != "deploy w3:p1" {
			t.Errorf("labels = %v, want full pane ids", labels)
		}
	})
}

func TestSameWorkspace(t *testing.T) {
	// Changing workspace_format used to orphan the space a machine's panes
	// already lived in, because lookup matched the label exactly.
	// Renaming the space as reachability changes must not orphan it, so the
	// lookup ignores whichever marker it currently carries.
	for _, label := range []string{"bot", "☁ bot", "☁  bot", "⚠  bot", "🟢 bot"} {
		if !sameWorkspace(label, "bot") {
			t.Errorf("sameWorkspace(%q, \"bot\") = false, want true", label)
		}
	}
	for _, label := range []string{"bots", "prod", "~"} {
		if sameWorkspace(label, "bot") {
			t.Errorf("sameWorkspace(%q, \"bot\") = true, want false", label)
		}
	}
}

func TestShortPaneID(t *testing.T) {
	for in, want := range map[string]string{"w2:p1": "p1", "w10:pAB": "pAB", "p1": "p1"} {
		if got := shortPaneID(in); got != want {
			t.Errorf("shortPaneID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlanStrayPane(t *testing.T) {
	tests := []struct {
		name       string
		capture    bool
		isMirror   bool
		seenBefore bool
		want       bool
	}{
		{
			// Herdr's plus icon and new-tab key always open a local shell, and
			// a plugin cannot intercept either, so a local pane appearing in a
			// machine's space is corrected afterwards.
			name:    "a new local pane in a machine's space is captured",
			capture: true, isMirror: false, seenBefore: false, want: true,
		},
		{
			// The plugin's own mirrors live there; capturing them would close
			// the very panes being shown.
			name:    "a mirror is never captured",
			capture: true, isMirror: true, seenBefore: false, want: false,
		},
		{
			// Acting once means a pane deliberately kept there is not closed
			// again on the next pass, two seconds later, forever.
			name:    "a pane already left alone stays left alone",
			capture: true, isMirror: false, seenBefore: true, want: false,
		},
		{
			name:    "capture can be switched off",
			capture: false, isMirror: false, seenBefore: false, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planStrayPane(tt.capture, tt.isMirror, tt.seenBefore)
			if got != tt.want {
				t.Errorf("planStrayPane(capture=%v, mirror=%v, seen=%v) = %v, want %v",
					tt.capture, tt.isMirror, tt.seenBefore, got, tt.want)
			}
		})
	}
}

func TestPlanStrayPlacement(t *testing.T) {
	// A pane alone in its tab was made by the plus icon or the new-tab key, so
	// its replacement is a tab. Replacing it with a split would rearrange the
	// layout under someone who asked for a tab.
	if got := planStrayPlacement(1); got != placementTab {
		t.Errorf("a pane alone in its tab = %q, want %q", got, placementTab)
	}
	if got := planStrayPlacement(0); got != placementTab {
		t.Errorf("an unknown tab should default to %q, got %q", placementTab, got)
	}
	// Sharing a tab means it came from a split.
	if got := planStrayPlacement(2); got != placementSplit {
		t.Errorf("a pane sharing a tab = %q, want %q", got, placementSplit)
	}
}

func TestPlanSharedPanes(t *testing.T) {
	// A machine's panes, deliberately out of order and spread across two of its
	// spaces: "wC" is the space this plugin owns, "w7" is the machine's own.
	panes := []herdrcli.Pane{
		{PaneID: "wC:p3", TabID: "wC:t3", WorkspaceID: "wC"},
		{PaneID: "w7:p1", TabID: "w7:t1", WorkspaceID: "w7"},
		{PaneID: "wC:p1", TabID: "wC:t1", WorkspaceID: "wC"},
		{PaneID: "wC:p2", TabID: "wC:t2", WorkspaceID: "wC"},
	}
	order := map[string]int{"wC:t1": 1, "wC:t2": 2, "wC:t3": 3, "w7:t1": 1}

	t.Run("shared scope mirrors only this machine's space, in tab order", func(t *testing.T) {
		// Mirroring the machine's other spaces made the two ends differ by a
		// constant, which reads as being permanently out of sync.
		got := planSharedPanes(panes, "wC", order, true)
		want := []string{"wC:p1", "wC:p2", "wC:p3"}
		if len(got) != len(want) {
			t.Fatalf("got %d panes, want %d: %+v", len(got), len(want), got)
		}
		for i := range want {
			if got[i].PaneID != want[i] {
				t.Errorf("pane %d = %s, want %s", i, got[i].PaneID, want[i])
			}
		}
	})

	t.Run("all scope keeps every pane but still orders them", func(t *testing.T) {
		got := planSharedPanes(panes, "wC", order, false)
		if len(got) != 4 {
			t.Fatalf("got %d panes, want 4", len(got))
		}
		// Tab numbers repeat across spaces, so the pane id breaks the tie
		// deterministically rather than leaving the order to chance.
		for i := 1; i < len(got); i++ {
			a, b := got[i-1], got[i]
			na, nb := order[a.TabID], order[b.TabID]
			if na > nb || (na == nb && a.PaneID > b.PaneID) {
				t.Errorf("panes out of order at %d: %s then %s", i, a.PaneID, b.PaneID)
			}
		}
	})

	t.Run("an unknown shared space mirrors nothing", func(t *testing.T) {
		// Before the space exists there is nothing shared yet; mirroring the
		// machine's own work instead would be a surprise.
		if got := planSharedPanes(panes, "", order, true); len(got) != 0 {
			t.Errorf("got %d panes, want none: %+v", len(got), got)
		}
	})
}

func TestPlanLostPane(t *testing.T) {
	// A dropped connection took the machine's whole space with it and nothing
	// brought it back, so the machine looked disconnected until it was
	// reconnected by hand.
	if !planLostPane(true) {
		t.Error("a terminal whose bridge failed should be reopened")
	}
	// Reopening a terminal someone just closed is worse than leaving it shut.
	if planLostPane(false) {
		t.Error("a terminal closed by hand should stay closed")
	}
}

func TestPlanShellsToRestore(t *testing.T) {
	// What a machine is left with when the way it is reached changes under it
	// -- pressing m on a connected machine, most of the time. Its mirrors
	// cannot be kept up in SSH mode and have just been closed, so without this
	// its space is emptied and the machine looks disconnected.
	for _, tt := range []struct {
		what     string
		hadPanes bool
		saved    int
		want     int
	}{
		{"a machine whose panes were just closed gets one back", true, 0, 1},
		{"a count saved before a restart is what it had", true, 3, 3},
		{"a machine with nothing open gains nothing", false, 0, 0},
		{"and a saved count survives with no panes to close", false, 2, 2},
	} {
		if got := planShellsToRestore(tt.hadPanes, tt.saved); got != tt.want {
			t.Errorf("%s: had panes %v with %d saved gives %d, want %d",
				tt.what, tt.hadPanes, tt.saved, got, tt.want)
		}
	}
}

func TestPlanRestoreShell(t *testing.T) {
	// A plain SSH machine has nothing to discover, so after a Herdr restart it
	// was simply missing from the sidebar with no way back but reconnecting.
	if !planRestoreShell(1, 0) {
		t.Error("a machine that had a terminal should get one back")
	}
	// Its terminals survived, so nothing is needed.
	if planRestoreShell(1, 1) {
		t.Error("a machine whose terminal is alive should be left alone")
	}
	// Connecting is not implied: a machine nobody connected to stays absent.
	if planRestoreShell(0, 0) {
		t.Error("a machine with no terminals should not gain one")
	}
}

func TestPlanRestoreShellBringsBackAllOfThem(t *testing.T) {
	// The count is what the snapshot records. The reconcile loop had its own
	// copy of this decision that read it as "there were some", so three
	// terminals came back as one -- and this function, which reads the count,
	// was written and tested and never called.
	if !planRestoreShell(3, 0) {
		t.Error("three were open and none are back: should open one")
	}
	if !planRestoreShell(3, 2) {
		t.Error("three were open and two are back: should open the third")
	}
	if planRestoreShell(3, 3) {
		t.Error("all three are back: should stop")
	}
	if planRestoreShell(3, 4) {
		t.Error("more than were open: should not keep going")
	}
}

func TestPlanNeedsTerminal(t *testing.T) {
	// Counting terminals ever opened, rather than still open, left a machine
	// reporting "connected" with nothing to show after its last terminal was
	// closed, and no way to reopen it from the menu.
	if !planNeedsTerminal(0) {
		t.Error("a machine with no terminals should get one")
	}
	if planNeedsTerminal(1) {
		t.Error("a machine that already has a terminal should not gain another")
	}
	if planNeedsTerminal(5) {
		t.Error("a busy machine should not gain another terminal")
	}
}

func TestPlanShellName(t *testing.T) {
	// Numbering from a running total repeats the moment one in the middle is
	// closed: with three open, closing the second gave the next terminal the
	// third's name, and two panes on the machine were called "shell 3".
	at := func(name string) string { return name + "@bot" }

	if got := planShellName(nil, at); got != "shell" {
		t.Errorf("first terminal = %q, want %q", got, "shell")
	}

	one := map[string]bool{"shell@bot": true}
	if got := planShellName(one, at); got != "shell 2" {
		t.Errorf("second terminal = %q, want %q", got, "shell 2")
	}

	three := map[string]bool{"shell@bot": true, "shell 2@bot": true, "shell 3@bot": true}
	if got := planShellName(three, at); got != "shell 4" {
		t.Errorf("fourth terminal = %q, want %q", got, "shell 4")
	}

	// The case that was wrong: the second of three closed, so its name is free
	// again and the third's is not.
	gap := map[string]bool{"shell@bot": true, "shell 3@bot": true}
	if got := planShellName(gap, at); got != "shell 2" {
		t.Errorf("terminal after a gap = %q, want the free name %q", got, "shell 2")
	}

	// The first name is reusable too, once nothing is called it.
	if got := planShellName(map[string]bool{"shell 2@bot": true}, at); got != "shell" {
		t.Errorf("terminal = %q, want %q", got, "shell")
	}

	// Names are compared as they are drawn, so a format that hides the number
	// does not make every terminal collide forever.
	sameForAll := func(string) string { return "terminal@bot" }
	if got := planShellName(map[string]bool{}, sameForAll); got != "shell" {
		t.Errorf("terminal = %q, want %q", got, "shell")
	}
}

func TestPlanShellNameNeverRepeatsWhatIsOpen(t *testing.T) {
	// Opening and closing in any order must never produce two terminals with
	// the same name on one machine.
	at := func(name string) string { return name + "@bot" }
	open := map[string]bool{}

	add := func() string {
		name := planShellName(open, at)
		if open[at(name)] {
			t.Fatalf("%q is already on screen", at(name))
		}
		open[at(name)] = true
		return name
	}

	first, second, third := add(), add(), add()
	if first == second || second == third || first == third {
		t.Fatalf("names repeat: %q %q %q", first, second, third)
	}

	delete(open, at(second))
	if got := add(); got != second {
		t.Errorf("the freed name %q was not reused, got %q", second, got)
	}
	// And with everything closed, numbering starts over.
	open = map[string]bool{}
	if got := add(); got != "shell" {
		t.Errorf("with nothing open the first terminal is %q, want %q", got, "shell")
	}
}

func TestPlanGiveUp(t *testing.T) {
	// A blip: the sort of failure that can come good by itself, so it is
	// counted rather than acted on.
	blip := errors.New("ssh: connect to host bot port 22: Connection refused")

	// A machine that answers is never given up on.
	if planGiveUp(0, nil) {
		t.Error("a healthy machine should keep being polled")
	}
	// One failure can be a blip, so it is tried again.
	if planGiveUp(1, blip) {
		t.Error("a single failure should be retried")
	}
	// Retrying every couple of seconds burns SSH connections and fills the log.
	if !planGiveUp(2, blip) {
		t.Error("a machine that failed twice should be left alone")
	}
	if !planGiveUp(20, blip) {
		t.Error("a long-failing machine should stay left alone")
	}
}

func TestAFailureNeedingAPersonIsNotRetried(t *testing.T) {
	// The second attempt at a changed host key cannot come out differently: it
	// fails identically until somebody looks at known_hosts. Paying for it
	// costs another connection and another fifteen-line banner in the log, and
	// delays the one line worth reading.
	settled := []string{
		"REMOTE HOST IDENTIFICATION HAS CHANGED",
		"Host key verification failed.",
		"prod: Permission denied (publickey).",
		"ssh: Could not resolve hostname prd: Name or service not known",
		"Received disconnect: Too many authentication failures",
		remote.ErrNoHerdr.Error(),
	}
	for _, message := range settled {
		err := errors.New(message)
		if !planGiveUp(0, err) {
			t.Errorf("%q should not be retried; it needs a person", message)
		}
		if summarizeError(err) == "" {
			t.Errorf("%q should still be named in the listing", message)
		}
	}

	// These can come good on their own, and giving up on the first one would
	// turn a machine still booting into a machine you have to reconnect to.
	transient := []string{
		"ssh: connect to host bot port 22: Connection refused",
		"ssh: connect to host bot port 22: Connection timed out",
		"ssh: connect to host bot port 22: Network is unreachable",
		"kex_exchange_identification: Connection closed by remote host",
		"Connection reset by peer",
		"ssh: connect to host bot port 22: No route to host",
	}
	for _, message := range transient {
		if planGiveUp(0, errors.New(message)) {
			t.Errorf("%q should be retried; it can come good on its own", message)
		}
	}

	// A failure nobody has classified is retried rather than written off: the
	// cautious reading of an unknown cause is that it might pass.
	if planGiveUp(0, errors.New("something nobody has seen before")) {
		t.Error("an unrecognised failure should get its retries")
	}
}

func TestSummarizeError(t *testing.T) {
	// SSH prints fifteen lines of banner for a changed host key, which turned a
	// status listing into a wall of text and buried the machine it belonged to.
	hostKey := errors.New(`prod is not reachable over ssh: exit status 255: @@@@@@@@@@@@@@@@
@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @
@@@@@@@@@@@@@@@@
IT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!
Offending ECDSA key in /home/ounos/.ssh/known_hosts:113
Host key verification failed.`)

	got := summarizeError(hostKey)
	if strings.Contains(got, "\n") {
		t.Errorf("summary spans lines: %q", got)
	}
	if !strings.Contains(got, "host key changed") {
		t.Errorf("summary = %q, want it to name the cause", got)
	}

	for in, want := range map[string]string{
		"ssh: Permission denied (publickey).":  "ssh permission denied — check your key",
		"connect: Connection refused":          "connection refused",
		"ssh: Could not resolve hostname nope": "host name does not resolve",

		// macOS words a timeout differently from Linux, so this used to fall
		// through and print the raw ssh line.
		"ssh: connect to host bot port 22: Operation timed out":  "connection timed out",
		"ssh: connect to host bot port 22: Connection timed out": "connection timed out",

		// The rest are everyday ssh failures that used to arrive raw.
		"connect: Network is unreachable":                       "no network — check you are online",
		"Connection closed by 10.0.0.4 port 22":                 "the machine closed the connection",
		"read: Connection reset by peer":                        "the machine reset the connection",
		"Received disconnect: Too many authentication failures": "too many keys offered — set IdentitiesOnly=yes for this host",
		"kex_exchange_identification: read: Connection reset":   "the machine dropped the connection before login — it may be busy or rate-limiting",
		// The real message carries both causes; the specific one should win.
		"kex_exchange_identification: Connection closed by remote host": "the machine dropped the connection before login — it may be busy or rate-limiting",
	} {
		if got := summarizeError(errors.New(in)); got != want {
			t.Errorf("summarizeError(%q) = %q, want %q", in, got, want)
		}
	}

	// This plugin's own error, wrapped the way a caller wraps it, rather than
	// spelled out as text: the wording belongs to the error and copying it here
	// is how the two came to be written down twice.
	if got := summarizeError(fmt.Errorf("bot: %w", remote.ErrNoHerdr)); got != "herdr not found on the machine" {
		t.Errorf("summarizeError on ErrNoHerdr = %q, want the sentence about falling back", got)
	}

	t.Run("an unrecognised failure keeps its first line", func(t *testing.T) {
		if got := summarizeError(errors.New("something odd\nmore detail below")); got != "something odd" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("a very long line is trimmed", func(t *testing.T) {
		got := summarizeError(errors.New(strings.Repeat("x", 500)))
		if n := len([]rune(got)); n > 90 {
			t.Errorf("summary is %d characters, too long for a list", n)
		}
	})

	t.Run("trimming does not split a character", func(t *testing.T) {
		// Cutting by bytes would leave half a rune behind, which renders as a
		// replacement character in the sidebar.
		got := summarizeError(errors.New(strings.Repeat("☁", 500)))
		if !utf8.ValidString(got) {
			t.Errorf("summary is not valid UTF-8: %q", got)
		}
		if n := len([]rune(got)); n > 90 {
			t.Errorf("summary is %d characters, too long", n)
		}
	})

	if summarizeError(nil) != "" {
		t.Error("no error should summarize to nothing")
	}
}

func TestPlanMirrors(t *testing.T) {
	pane := func(term, id string) herdrcli.Pane {
		return herdrcli.Pane{TerminalID: term, PaneID: id, WorkspaceID: "wA"}
	}
	remote := []herdrcli.Pane{pane("t1", "wA:p1"), pane("t2", "wA:p2"), pane("t3", "wA:p3")}

	t.Run("nothing mirrored yet opens everything, in order", func(t *testing.T) {
		// Order is the machine's tab order, and mirrors must line up with it.
		plan := planMirrors(remote, mirrorState{Mirrored: map[string]string{}})
		if len(plan.Open) != 3 {
			t.Fatalf("opening %d, want 3", len(plan.Open))
		}
		for i, want := range []string{"t1", "t2", "t3"} {
			if plan.Open[i].TerminalID != want {
				t.Errorf("open[%d] = %s, want %s", i, plan.Open[i].TerminalID, want)
			}
		}
	})

	t.Run("terminals already mirrored are refreshed, not reopened", func(t *testing.T) {
		// Reopening one would give the machine a second pane for the same
		// terminal, and the two would fight over the exclusive attach.
		plan := planMirrors(remote, mirrorState{
			Mirrored: map[string]string{"t1": "w1:p1", "t2": "w1:p2"},
		})
		if len(plan.Existing) != 2 || len(plan.Open) != 1 || plan.Open[0].TerminalID != "t3" {
			t.Errorf("existing=%d open=%v", len(plan.Existing), plan.Open)
		}
	})

	t.Run("a terminal closed by hand is left closed", func(t *testing.T) {
		// Reopening it would fight the user every couple of seconds.
		plan := planMirrors(remote, mirrorState{
			Mirrored:  map[string]string{},
			Dismissed: map[string]bool{"t2": true},
		})
		for _, p := range plan.Open {
			if p.TerminalID == "t2" {
				t.Error("a dismissed terminal was reopened")
			}
		}
		if len(plan.Open) != 2 {
			t.Errorf("opening %d, want 2", len(plan.Open))
		}
	})

	t.Run("a terminal waiting out a failure is skipped", func(t *testing.T) {
		plan := planMirrors(remote, mirrorState{
			Mirrored:  map[string]string{},
			BackedOff: map[string]bool{"t1": true},
		})
		if len(plan.Open) != 2 || plan.Open[0].TerminalID != "t2" {
			t.Errorf("open = %v, want t2 and t3", plan.Open)
		}
	})

	t.Run("a terminal that has gone is closed here", func(t *testing.T) {
		plan := planMirrors([]herdrcli.Pane{pane("t1", "wA:p1")}, mirrorState{
			Mirrored: map[string]string{"t1": "w1:p1", "t9": "w1:p9"},
		})
		if len(plan.Gone) != 1 || plan.Gone[0] != "t9" {
			t.Errorf("gone = %v, want [t9]", plan.Gone)
		}
	})

	t.Run("the per-machine limit stops the rest", func(t *testing.T) {
		// A machine with a runaway number of terminals must not flood the
		// session, and the limit counts what is already mirrored.
		plan := planMirrors(remote, mirrorState{Mirrored: map[string]string{}, Max: 2})
		if len(plan.Open) != 2 || !plan.AtCapacity {
			t.Errorf("open=%d atCapacity=%v, want 2 and true", len(plan.Open), plan.AtCapacity)
		}

		plan = planMirrors(remote, mirrorState{
			Mirrored: map[string]string{"t1": "w1:p1", "t2": "w1:p2"},
			Max:      2,
		})
		if len(plan.Open) != 0 || !plan.AtCapacity {
			t.Errorf("open=%d atCapacity=%v, want 0 and true", len(plan.Open), plan.AtCapacity)
		}
	})

	t.Run("no limit means no cap", func(t *testing.T) {
		plan := planMirrors(remote, mirrorState{Mirrored: map[string]string{}, Max: 0})
		if len(plan.Open) != 3 || plan.AtCapacity {
			t.Errorf("open=%d atCapacity=%v", len(plan.Open), plan.AtCapacity)
		}
	})

	t.Run("a pane with no terminal is ignored", func(t *testing.T) {
		// A pane without a terminal id cannot be bridged to anything.
		plan := planMirrors([]herdrcli.Pane{{PaneID: "wA:p1"}}, mirrorState{
			Mirrored: map[string]string{},
		})
		if len(plan.Open) != 0 {
			t.Errorf("open = %v, want nothing", plan.Open)
		}
	})

	t.Run("a machine with nothing running closes every mirror", func(t *testing.T) {
		plan := planMirrors(nil, mirrorState{
			Mirrored: map[string]string{"t1": "w1:p1", "t2": "w1:p2"},
		})
		if len(plan.Gone) != 2 {
			t.Errorf("gone = %v, want both", plan.Gone)
		}
		if len(plan.Open) != 0 {
			t.Errorf("open = %v, want nothing", plan.Open)
		}
	})
}

func TestPlanOrphanedPane(t *testing.T) {
	const suffix = "@bot"

	tests := []struct {
		name          string
		label         string
		tracked       bool
		mirrorRunning bool
		want          bool
	}{
		{
			// Herdr restores a plugin pane after a restart as a plain shell
			// without re-running its command, so what is left wears a remote
			// terminal's name while being a local shell. Untracked ones would
			// sit in the space forever.
			name:  "a name from this machine with nothing behind it is closed",
			label: "build@bot", want: true,
		},
		{
			// A pane the plugin is looking after is not an orphan, whatever
			// its process is doing at this instant.
			name:  "a tracked pane is left alone",
			label: "build@bot", tracked: true, want: false,
		},
		{
			name:  "a working mirror is left alone",
			label: "build@bot", mirrorRunning: true, want: false,
		},
		{
			// A terminal someone opened in the space is moved onto the machine
			// rather than closed, so it must not be taken for an orphan.
			name:  "an unnamed pane is not an orphan",
			label: "", want: false,
		},
		{
			name:  "a name from another machine is left alone",
			label: "build@other", want: false,
		},
		{
			name:  "a name that merely contains the machine is left alone",
			label: "bot-notes", want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planOrphanedPane(tt.label, suffix, tt.tracked, tt.mirrorRunning)
			if got != tt.want {
				t.Errorf("planOrphanedPane(%q, %q, tracked=%v, running=%v) = %v, want %v",
					tt.label, suffix, tt.tracked, tt.mirrorRunning, got, tt.want)
			}
		})
	}

	t.Run("no machine name means nothing is an orphan", func(t *testing.T) {
		if planOrphanedPane("build@bot", "", false, false) {
			t.Error("without a machine name nothing should be closed")
		}
	})
}

func TestPlanTrackedMirrorChecksIdentity(t *testing.T) {
	// Herdr reuses pane ids. A record saying "terminal t1 is mirrored at
	// w1:p2" can survive to meet a pane that is now mirroring something else,
	// and adopting it would leave t1 unmirrored and the other terminal
	// mirrored twice — two mirrors then fight over the exclusive attach.
	if got := planTrackedMirrorFor(false, true, true, false, "t1", "t5"); got != mirrorReplace {
		t.Errorf("a pane mirroring another terminal should be replaced, got %v", got)
	}

	// The same terminal is the mirror the record meant.
	if got := planTrackedMirrorFor(false, true, true, false, "t1", "t1"); got != mirrorKeep {
		t.Errorf("a matching mirror should be kept, got %v", got)
	}

	// A mark written before the terminal was recorded is taken at its word,
	// or upgrading would replace every working mirror.
	if got := planTrackedMirrorFor(false, true, true, false, "t1", ""); got != mirrorKeep {
		t.Errorf("an older mark should be kept, got %v", got)
	}

	// Once running, the identity is not rechecked: a mirror can be between
	// processes without being the wrong one.
	if got := planTrackedMirrorFor(true, true, false, false, "t1", "t5"); got != mirrorKeep {
		t.Errorf("after adoption the pane should be left alone, got %v", got)
	}
}

func TestLabelIsSafeToDraw(t *testing.T) {
	// A terminal's name comes from whatever runs on the far machine, and ends
	// up in the sidebar here. Anything that moves the cursor or changes how the
	// rest is drawn has to go, and a long name has to be cut.
	d := withConfig(&Daemon{}, config.Defaults())
	host := config.Host{Target: "bot"}

	got := d.label(host, herdrcli.Pane{}, "build\nfake")
	if strings.Contains(got, "\n") {
		t.Errorf("label %q still spans lines", got)
	}

	got = d.label(host, herdrcli.Pane{}, "build\x1b[31m")
	if strings.Contains(got, "\x1b") {
		t.Errorf("label %q still carries an escape", got)
	}

	got = d.label(host, herdrcli.Pane{}, strings.Repeat("x", 200))
	if text.Width(got) > maxLabelWidth+len("@bot")+1 {
		t.Errorf("label %q is %d cells, too wide for the sidebar", got, text.Width(got))
	}

	// An ordinary name is left as it is.
	if got := d.label(host, herdrcli.Pane{}, "build"); got != "build@bot" {
		t.Errorf("label = %q, want build@bot", got)
	}
}

func TestStatusOrderIsStable(t *testing.T) {
	// Ranging over the map of machines reshuffled the list between runs, so the
	// same machines came back in a different order each time. Config order is
	// what someone wrote down, so it is the order they expect to read back.
	d := withConfig(&Daemon{
		hosts: map[string]*hostSync{
			"staging": {host: config.Host{Target: "staging"}},
			"bot":     {host: config.Host{Target: "bot"}},
			"prod":    {host: config.Host{Target: "prod"}},
			// Reached from ~/.ssh/config rather than named in the plugin config.
			"zeta":  {host: config.Host{Target: "zeta"}},
			"alpha": {host: config.Host{Target: "alpha"}},
		}}, config.Config{Hosts: []config.Host{
		{Target: "bot"}, {Target: "prod"}, {Target: "staging"},
	}})

	want := []string{"bot", "prod", "staging", "alpha", "zeta"}
	// Repeat: map order is randomised per range, so one pass proves nothing.
	for attempt := 0; attempt < 50; attempt++ {
		var got []string
		for _, state := range d.orderedHosts() {
			got = append(got, state.host.Target)
		}
		if len(got) != len(want) {
			t.Fatalf("got %d machines, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("order = %v, want %v", got, want)
			}
		}
	}
}

func TestStatusSkipsMachinesThatAreNotConnected(t *testing.T) {
	// A machine listed in the config but never connected has no state, and
	// including it would report on something that is not being tracked.
	d := withConfig(&Daemon{
		hosts: map[string]*hostSync{
			"bot":  {host: config.Host{Target: "bot"}},
			"prod": {host: config.Host{Target: "prod"}},
		}}, config.Config{Hosts: []config.Host{
		{Target: "bot"}, {Target: "never-connected"}, {Target: "prod"},
	}})

	var got []string
	for _, state := range d.orderedHosts() {
		got = append(got, state.host.Target)
	}
	if len(got) != 2 || got[0] != "bot" || got[1] != "prod" {
		t.Errorf("order = %v, want [bot prod]", got)
	}
}

func TestStatusToleratesADuplicateInTheConfig(t *testing.T) {
	// The config can be edited by hand, and a machine listed twice should not
	// be reported twice.
	d := withConfig(&Daemon{
		hosts: map[string]*hostSync{"bot": {host: config.Host{Target: "bot"}}}}, config.Config{Hosts: []config.Host{
		{Target: "bot"}, {Target: "bot"},
	}})

	if got := d.orderedHosts(); len(got) != 1 {
		t.Errorf("got %d entries, want 1", len(got))
	}
}

func TestStatusIsSafeWhileMachinesChange(t *testing.T) {
	// status walks the map of machines while the reconcile loop is adding to
	// it, removing from it and swapping the config. Reading that map without
	// holding the lock is the kind of fault that shows up as a crash on a busy
	// machine and never in a quiet test, so the race detector is pointed at it
	// deliberately here.
	d := withConfig(&Daemon{
		hosts: map[string]*hostSync{}}, config.Config{Hosts: []config.Host{{Target: "bot"}, {Target: "prod"}}})

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers: whatever the menu and the status command do.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					for _, info := range d.status() {
						_ = info.Target
					}
				}
			}
		}()
	}

	// Writer: machines connecting and dropping, and the config being replaced
	// when a mode is toggled from the menu. It is waited on separately from the
	// readers, which only stop when told to.
	written := make(chan struct{})
	go func() {
		defer close(written)
		targets := []string{"bot", "prod", "staging", "ci"}
		for i := 0; i < 400; i++ {
			target := targets[i%len(targets)]
			d.mu.Lock()
			if _, ok := d.hosts[target]; ok {
				delete(d.hosts, target)
			} else {
				d.hosts[target] = &hostSync{host: config.Host{Target: target}}
			}
			d.setConfig(config.Config{Hosts: []config.Host{
				{Target: "bot"}, {Target: target},
			}})
			d.mu.Unlock()
		}
	}()

	// Let the writer finish, then release the readers.
	<-written
	close(stop)
	wg.Wait()
}

func TestMarksArePrunedMoreThanOnce(t *testing.T) {
	// Pruning used to happen once, on the first pane listing after startup.
	// Every mark dropped later in the session then stayed until the daemon was
	// restarted -- and Herdr reuses pane ids, so a stale ".failed" is
	// eventually read as belonging to whatever lands on that id next, and a
	// pane someone deliberately closed gets reopened.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SESSION", "default")

	marks := filepath.Join(dir, "panes", "default")
	if err := os.MkdirAll(marks, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(marks, name), []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(marks, name))
		return err == nil
	}

	d := &Daemon{}

	// First pass: a mark for a pane that is gone goes, one for a live pane stays.
	write("w1-p1.failed")
	write("w1-p2.pid")
	d.maybePrune(map[string]bool{"w1:p2": true})
	if exists("w1-p1.failed") {
		t.Error("a mark for a vanished pane survived the first prune")
	}
	if !exists("w1-p2.pid") {
		t.Error("a mark for a live pane was removed")
	}

	// A mark dropped later in the same session. Pruning again this soon would
	// be wasted work, so it is left for now.
	write("w1-p3.failed")
	d.maybePrune(map[string]bool{"w1:p2": true})
	if !exists("w1-p3.failed") {
		t.Error("pruned again immediately; the interval is not being respected")
	}

	// Once the interval has passed it is cleared, rather than waiting for the
	// daemon to be restarted.
	d.lastPrune.Store(time.Now().Add(-2 * pruneInterval).UnixNano())
	d.maybePrune(map[string]bool{"w1:p2": true})
	if exists("w1-p3.failed") {
		t.Error("a mark dropped after the first prune was never cleared")
	}
	if !exists("w1-p2.pid") {
		t.Error("a mark for a live pane was removed")
	}
}

// index builds a pane listing the way Herdr would report it.
func index(panes ...herdrcli.Pane) *paneIndex {
	return newPaneIndex(panes)
}

func pane(id, workspace, tab string) herdrcli.Pane {
	return herdrcli.Pane{PaneID: id, WorkspaceID: workspace, TabID: tab}
}

func TestStrayCaptureMovesALocalPaneOntoTheMachine(t *testing.T) {
	// Opening a terminal while looking at a machine's space should put it on
	// that machine. Herdr opens it locally, so the pane is spotted after the
	// fact and moved.
	d := withConfig(&Daemon{}, config.Defaults())
	state := &hostSync{
		host:        config.Host{Target: "bot"},
		workspaceID: "w1",
		mirrors:     map[string]string{},
	}

	strays := d.planStrayCapture(state, index(
		pane("w1:p1", "w1", "w1:t1"),
		pane("w9:p1", "w9", "w9:t1"), // another space; not this machine's business
	))

	if len(strays) != 1 || strays[0].PaneID != "w1:p1" {
		t.Fatalf("strays = %+v, want just w1:p1", strays)
	}
}

func TestStrayCaptureLeavesItsOwnMirrorsAlone(t *testing.T) {
	// A pane this plugin opened is already on the machine. Treating it as a
	// stray would move it onto the machine again, and again, forever.
	d := withConfig(&Daemon{}, config.Defaults())
	state := &hostSync{
		host:        config.Host{Target: "bot"},
		workspaceID: "w1",
		mirrors:     map[string]string{"term_1": "w1:p1"},
	}

	if strays := d.planStrayCapture(state, index(pane("w1:p1", "w1", "w1:t1"))); len(strays) != 0 {
		t.Errorf("strays = %+v, want none: that pane is already a mirror", strays)
	}
}

func TestStrayCaptureActsOnAPaneOnlyOnce(t *testing.T) {
	// Reconciling runs every couple of seconds. Without remembering what it has
	// already acted on, one stray pane would be moved on every pass.
	d := withConfig(&Daemon{}, config.Defaults())
	state := &hostSync{
		host:        config.Host{Target: "bot"},
		workspaceID: "w1",
		mirrors:     map[string]string{},
	}
	listing := index(pane("w1:p1", "w1", "w1:t1"))

	if strays := d.planStrayCapture(state, listing); len(strays) != 1 {
		t.Fatalf("first pass found %d strays, want 1", len(strays))
	}
	if strays := d.planStrayCapture(state, listing); len(strays) != 0 {
		t.Errorf("second pass found %+v, want none", strays)
	}
}

func TestStrayCaptureJudgesAReusedPaneIDAfresh(t *testing.T) {
	// Herdr reuses pane ids. Remembering "already handled w1:p1" forever would
	// mean the next pane to land on that id is never moved onto the machine.
	d := withConfig(&Daemon{}, config.Defaults())
	state := &hostSync{
		host:        config.Host{Target: "bot"},
		workspaceID: "w1",
		mirrors:     map[string]string{},
	}

	d.planStrayCapture(state, index(pane("w1:p1", "w1", "w1:t1")))
	// The pane goes away.
	d.planStrayCapture(state, index())
	if d.seenStray["w1:p1"] {
		t.Fatal("a pane that no longer exists is still remembered")
	}
	// A different pane arrives on the same id.
	if strays := d.planStrayCapture(state, index(pane("w1:p1", "w1", "w1:t1"))); len(strays) != 1 {
		t.Errorf("a reused pane id was not judged afresh: %+v", strays)
	}
}

func TestStrayCaptureDoesNothingForAPlainSSHMachine(t *testing.T) {
	// Without mirroring there is no machine-side space to move anything onto,
	// and the terminals in that space are plain SSH sessions already.
	d := withConfig(&Daemon{}, config.Defaults())
	state := &hostSync{
		host:        config.Host{Target: "bot"},
		workspaceID: "w1",
		sshOnly:     true,
		mirrors:     map[string]string{},
	}

	if strays := d.planStrayCapture(state, index(pane("w1:p1", "w1", "w1:t1"))); len(strays) != 0 {
		t.Errorf("strays = %+v, want none for a plain SSH machine", strays)
	}
}

func TestStrayCaptureRespectsTheSetting(t *testing.T) {
	// capture_new_panes exists for people who would rather a local pane stayed
	// local, and turning it off must actually stop the moving.
	cfg := config.Defaults()
	off := false
	cfg.CaptureNewPanes = &off
	d := withConfig(&Daemon{}, cfg)
	state := &hostSync{
		host:        config.Host{Target: "bot"},
		workspaceID: "w1",
		mirrors:     map[string]string{},
	}

	if strays := d.planStrayCapture(state, index(pane("w1:p1", "w1", "w1:t1"))); len(strays) != 0 {
		t.Errorf("strays = %+v, want none when capture_new_panes is off", strays)
	}
}

func TestHostConfigFindsAMachineByEitherName(t *testing.T) {
	d := withConfig(&Daemon{}, config.Config{Hosts: []config.Host{
		{Target: "bot.example.com", Label: "bot"},
		{Target: "ci"},
	}})

	// Both the thing typed after ssh and the name shown here should work: the
	// menu offers the label, the config and the command line use the target.
	for _, name := range []string{"bot.example.com", "bot"} {
		got, err := d.hostConfig(name)
		if err != nil || got.Target != "bot.example.com" {
			t.Errorf("hostConfig(%q) = %+v, %v; want the configured machine", name, got, err)
		}
	}
}

func TestHostConfigAcceptsAnUnconfiguredMachine(t *testing.T) {
	// Everything in ~/.ssh/config is offered in the menu, so connecting must
	// work for a machine that was never written into the plugin's own config.
	d := withConfig(&Daemon{}, config.Defaults())

	got, err := d.hostConfig("some-laptop")
	if err != nil || got.Target != "some-laptop" {
		t.Errorf("hostConfig = %+v, %v; want an ad-hoc machine", got, err)
	}

	// Nothing at all is still nothing.
	if _, err := d.hostConfig(""); err == nil {
		t.Error("an empty name was accepted as a machine")
	}
}

func TestConfigWarningSaysWhichProblemItIs(t *testing.T) {
	// A config that cannot be read at all and a config with a typo in it need
	// different things done about them, so they should not read alike.
	unreadable := &Daemon{configErr: errors.New("unexpected end of JSON input")}
	got := unreadable.configWarning()
	if !strings.Contains(got, "could not be read") || !strings.Contains(got, "JSON") {
		t.Errorf("warning = %q, want it to say the file could not be read and why", got)
	}

	typo := withConfig(&Daemon{}, config.Config{Mode: "shh", Hosts: []config.Host{{Target: "bot"}}})
	got = typo.configWarning()
	if !strings.Contains(got, "shh") {
		t.Errorf("warning = %q, want it to name the bad value", got)
	}
	if strings.Contains(got, "could not be read") {
		t.Errorf("warning = %q, a readable file should not be reported as unreadable", got)
	}

	if got := (withConfig(&Daemon{}, config.Defaults())).configWarning(); got != "" {
		t.Errorf("a good config warned: %q", got)
	}

	// Several at once. The menu wraps this to two lines and turns the rest
	// into an ellipsis, so without a count somebody reads one problem and
	// cannot tell there are others waiting behind it.
	// From the defaults rather than a bare literal, which would leave every
	// other setting empty and complaining about that instead.
	cfg := config.Defaults()
	cfg.Mode = "shh"
	cfg.PollInterval = "30"
	cfg.Hosts = []config.Host{{Target: "bot", Placement: "sideways"}}
	several := withConfig(&Daemon{}, cfg)
	got = several.configWarning()
	if !strings.Contains(got, "3 problems") {
		t.Errorf("warning = %q, want it to say how many there are", got)
	}
	if !strings.Contains(got, "status") {
		t.Errorf("warning = %q, want it to say where the rest can be read", got)
	}
	// The first one is still there in full: a count with nothing to act on is
	// worse than the one problem it replaced.
	if !strings.Contains(got, "shh") {
		t.Errorf("warning = %q, want the first problem still spelled out", got)
	}

	// And one problem is not counted at all. "1 problems" is wrong twice over
	// -- it does not read, and there is nothing to go and list.
	one := config.Defaults()
	one.Mode = "shh"
	one.Hosts = []config.Host{{Target: "bot"}}
	got = withConfig(&Daemon{}, one).configWarning()
	if !strings.Contains(got, "shh") {
		t.Fatalf("warning = %q, want the one problem named", got)
	}
	if strings.Contains(got, "problems") {
		t.Errorf("warning = %q, which counts a single problem", got)
	}
	if strings.Contains(got, "status") {
		t.Errorf("warning = %q, which offers to list one problem", got)
	}
}

// withConfig sets a daemon's configuration. It is held atomically rather than
// as a plain field, so that a command changing it and a command reading it can
// run at the same time without one seeing half of the other's write.
func withConfig(d *Daemon, cfg config.Config) *Daemon {
	d.setConfig(cfg)
	// Filled in here so a test does not have to remember to: the real
	// constructor does it, and forgetting produces a nil map panic rather than
	// a readable failure.
	if d.seenStray == nil {
		d.seenStray = map[string]bool{}
	}
	if d.markedWorkspaces == nil {
		d.markedWorkspaces = map[string]workspaceMark{}
	}
	return d
}

func TestConfigCanBeReadWhileItIsBeingReplaced(t *testing.T) {
	// Toggling mirroring from the menu replaces the whole configuration, and
	// every control connection is handled in its own goroutine, so that write
	// runs alongside commands that read it. It used to be a plain field read
	// without the lock: the race detector caught a command changing the
	// configuration while another was walking the list of machines in it.
	//
	// Guarding it with the lock would have meant auditing three dozen call
	// sites for which ones already held it, and getting one wrong is a
	// deadlock, so it is held atomically instead and safe to read anywhere.
	d := withConfig(&Daemon{hosts: map[string]*hostSync{}}, config.Defaults())

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = d.hostConfig("bot")
					_ = d.configWarning()
					_ = d.status()
				}
			}
		}()
	}

	written := make(chan struct{})
	go func() {
		defer close(written)
		for i := 0; i < 500; i++ {
			d.setConfig(config.Config{Hosts: []config.Host{
				{Target: "bot"}, {Target: "prod"},
			}})
		}
	}()

	<-written
	close(stop)
	wg.Wait()
}

func TestPaneTargetNeverCarriesBothKindsOfTarget(t *testing.T) {
	// Herdr rejects the combination outright: a split takes a pane and no
	// workspace, a tab takes a workspace and no pane, an overlay takes neither.
	// Sending both came back as invalid_params, which is how no split mirror
	// ever opened. The table above checks the placements that exist; this holds
	// the rule for whatever gets added next, including a placement nobody here
	// has heard of.
	placements := []string{
		placementSplit, placementZoomed, placementTab,
		placementOverlay, placementPopup,
		"", "TAB", "float", "tiled", "  split  ", "split;rm",
	}
	workspaces := []string{"", "w1"}
	panes := []string{"", "w1:p1"}

	for _, placement := range placements {
		for _, workspace := range workspaces {
			for _, paneInWorkspace := range panes {
				got := planPaneTarget(placement, workspace, paneInWorkspace)
				if got.Workspace != "" && got.TargetPane != "" {
					t.Errorf("planPaneTarget(%q, %q, %q) = %+v, which Herdr rejects",
						placement, workspace, paneInWorkspace, got)
				}
				if got.Placement == "" {
					t.Errorf("planPaneTarget(%q, %q, %q) chose no placement",
						placement, workspace, paneInWorkspace)
				}
				// A split with nothing to split from cannot open, so it must
				// have become something that can.
				if got.Placement == placementSplit && got.TargetPane == "" {
					t.Errorf("planPaneTarget(%q, %q, %q) = %+v: a split needs a pane",
						placement, workspace, paneInWorkspace, got)
				}
			}
		}
	}
}

func TestSnapshotIsOnlyWrittenWhenItChanges(t *testing.T) {
	// Reconciling happens every couple of seconds whether anything changed or
	// not, and persist used to write the file every time: the same bytes, tens
	// of thousands of times a day, keeping a laptop's disk awake for nothing.
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SESSION", "default")

	d := withConfig(&Daemon{hosts: map[string]*hostSync{
		"bot": {
			host:       config.Host{Target: "bot"},
			mirrors:    map[string]string{"term_1": "w1:p1"},
			dismissed:  map[string]bool{},
			shellPanes: map[string]bool{"w1:p2": true},
		},
	}}, config.Defaults())

	d.persist()
	path, err := snapshotPath()
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatalf("nothing was written: %v", err)
	}

	// Nothing changed, so nothing should be written.
	d.persist()
	d.persist()
	again, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModTime().Equal(first.ModTime()) {
		t.Error("an unchanged snapshot was written again")
	}

	// Something changed, so it must be written.
	d.mu.Lock()
	d.hosts["bot"].mirrors["term_2"] = "w1:p3"
	d.mu.Unlock()
	d.persist()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "term_2") {
		t.Errorf("a changed snapshot was not written: %s", raw)
	}
}

func TestDismissedTerminalsSurviveARestart(t *testing.T) {
	// A terminal closed by hand was written to the snapshot and then never read
	// back, so a restart forgot it and mirrored it again -- reopening, on the
	// machine's next reconcile, exactly the terminal someone had shut.
	d := withConfig(&Daemon{
		hosts: map[string]*hostSync{},
		snapshot: snapshot{Hosts: map[string]hostSnapshot{
			"bot": {
				Mirrors:   map[string]string{"term_1": "w1:p1"},
				Dismissed: []string{"term_9", "term_8"},
				Shells:    2,
			},
		}},
	}, config.Defaults())

	state := &hostSync{
		host:      config.Host{Target: "bot"},
		mirrors:   map[string]string{},
		dismissed: map[string]bool{},
	}
	restoreFromSnapshot(state, d.snapshot.Hosts["bot"])

	for _, terminalID := range []string{"term_8", "term_9"} {
		if !state.dismissed[terminalID] {
			t.Errorf("%s was closed by hand but is not remembered as such", terminalID)
		}
	}
	if state.mirrors["term_1"] != "w1:p1" {
		t.Error("a live mirror was not restored")
	}
	if state.restoreShells != 2 {
		t.Errorf("restoreShells = %d, want 2", state.restoreShells)
	}
}

func TestGivingUpOnAMirrorIsNotRememberedAcrossARestart(t *testing.T) {
	// Both a pane someone closed and a mirror that failed too often stop the
	// terminal being mirrored again, and they used to share one set. Only the
	// first is worth remembering across a restart: restarting is exactly when a
	// mirror that kept failing deserves another go.
	d := withConfig(&Daemon{hosts: map[string]*hostSync{
		"bot": {
			host:      config.Host{Target: "bot"},
			mirrors:   map[string]string{},
			dismissed: map[string]bool{"closed-by-hand": true},
			abandoned: map[string]bool{"kept-failing": true},
		},
	}}, config.Defaults())

	d.mu.Lock()
	state := d.hosts["bot"]
	saved := hostSnapshot{}
	for terminalID := range state.dismissed {
		saved.Dismissed = append(saved.Dismissed, terminalID)
	}
	d.mu.Unlock()

	if len(saved.Dismissed) != 1 || saved.Dismissed[0] != "closed-by-hand" {
		t.Fatalf("snapshot holds %v, want only the pane closed by hand", saved.Dismissed)
	}

	// After a restart the one that was closed stays closed, and the one that
	// kept failing is tried again.
	fresh := &hostSync{
		mirrors:   map[string]string{},
		dismissed: map[string]bool{},
		abandoned: map[string]bool{},
	}
	restoreFromSnapshot(fresh, saved)

	if !fresh.dismissed["closed-by-hand"] {
		t.Error("a pane closed by hand came back after a restart")
	}
	if fresh.dismissed["kept-failing"] || fresh.abandoned["kept-failing"] {
		t.Error("a mirror that kept failing is still given up on after a restart")
	}
}

func TestBothSetsStopATerminalBeingMirrored(t *testing.T) {
	// Separating them must not let either through.
	panes := []herdrcli.Pane{
		{TerminalID: "closed-by-hand"},
		{TerminalID: "kept-failing"},
		{TerminalID: "fine"},
	}
	plan := planMirrors(panes, mirrorState{
		Mirrored:  map[string]string{},
		Dismissed: map[string]bool{"closed-by-hand": true},
		Abandoned: map[string]bool{"kept-failing": true},
		Max:       32,
	})

	if len(plan.Open) != 1 || plan.Open[0].TerminalID != "fine" {
		t.Errorf("plan opens %+v, want only the untouched terminal", plan.Open)
	}
}

func TestAFailedSnapshotWriteIsTriedAgain(t *testing.T) {
	// The snapshot is only written when it differs from what was last written.
	// Recording it as written before the write actually succeeded meant a
	// failed one -- a full disk, a state directory that went away -- left this
	// believing the file held content it never received. Every later pass then
	// saw nothing to do, and the snapshot silently stopped being saved for as
	// long as the daemon ran.
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(blocked, "state"))
	t.Setenv("HERDR_SESSION", "default")

	d := withConfig(&Daemon{hosts: map[string]*hostSync{
		"bot": {
			host:       config.Host{Target: "bot"},
			mirrors:    map[string]string{"term_1": "w1:p1"},
			dismissed:  map[string]bool{},
			abandoned:  map[string]bool{},
			shellPanes: map[string]bool{},
		},
	}}, config.Defaults())

	d.persist()
	d.mu.Lock()
	recorded := len(d.lastSaved)
	d.mu.Unlock()
	if recorded != 0 {
		t.Error("a write that failed was recorded as having succeeded")
	}

	// Once the state directory works, the next pass must write it rather than
	// deciding there is nothing to do.
	good := filepath.Join(dir, "state")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", good)
	d.persist()

	path, err := snapshotPath()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the snapshot was never written: %v", err)
	}
	if !strings.Contains(string(raw), "term_1") {
		t.Errorf("snapshot = %s, want the mirror in it", raw)
	}
}

func TestEveryPartOfALabelIsSafeToDraw(t *testing.T) {
	// A terminal's name was made safe several passes ago; the agent name comes
	// from the same place by the same route and was not. Whatever a format
	// refers to ends up in the sidebar, so all of it has to survive being
	// drawn rather than obeyed.
	d := withConfig(&Daemon{}, config.Config{
		LabelFormat: "{name}|{agent}|{pane}|{host}",
	})

	got := d.label(
		config.Host{Target: "bot", Label: "b\x1bot"},
		herdrcli.Pane{
			Agent:  "claude\x1b[31m\nfake",
			PaneID: "w1:p1\x1b[0m",
		},
		"build\nstep",
	)

	for _, bad := range []string{"\x1b", "\n", "\r", "\x00"} {
		if strings.Contains(got, bad) {
			t.Errorf("label %q still carries %q", got, bad)
		}
	}
	// The readable parts survive.
	for _, want := range []string{"build", "claude", "w1", "bot"} {
		if !strings.Contains(got, want) {
			t.Errorf("label %q lost %q", got, want)
		}
	}
}

func TestALongAgentNameCannotCrowdOutTheLabel(t *testing.T) {
	d := withConfig(&Daemon{}, config.Config{LabelFormat: "{agent}@{host}"})

	got := d.label(config.Host{Target: "bot"},
		herdrcli.Pane{Agent: strings.Repeat("x", 400)}, "")

	if len([]rune(got)) > maxLabelWidth+len("@bot")+1 {
		t.Errorf("label is %d runes: %q", len([]rune(got)), got)
	}
	// closeOrphans matches on the host suffix, so it has to survive the cut.
	if !strings.HasSuffix(got, "@bot") {
		t.Errorf("label %q no longer ends with the machine", got)
	}
}

func TestControlSocketIsPrivateToItsOwner(t *testing.T) {
	// The socket's permissions were left to the umask. Connecting to a Unix
	// socket needs write permission on it, so with the usual umask nobody else
	// could reach it -- but that is a property of the umask rather than of this
	// code, and what the socket accepts is instructions to open SSH connections
	// to other machines.
	socket := testSocket(t)

	listener, err := listenControl(socket)
	if err != nil {
		t.Fatalf("listenControl: %v", err)
	}
	defer listener.Close()

	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket permissions are %o, want 600", perm)
	}
}

func TestListenControlClearsASocketLeftBehind(t *testing.T) {
	// A daemon killed rather than stopped leaves the file there, and binding
	// over it fails. The next one has to be able to take it.
	socket := testSocket(t)

	first, err := listenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	// Go unlinks the socket when the listener closes, which a killed process
	// never gets to do. Turning that off leaves exactly what a kill leaves.
	if unix, ok := first.(*net.UnixListener); ok {
		unix.SetUnlinkOnClose(false)
	} else {
		t.Fatalf("expected a unix listener, got %T", first)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("the socket should still be there: %v", err)
	}

	second, err := listenControl(socket)
	if err != nil {
		t.Fatalf("could not take over a socket left behind: %v", err)
	}
	defer second.Close()

	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket permissions are %o after taking over, want 600", perm)
	}
}

func TestListenControlRefusesToStealALiveSocket(t *testing.T) {
	// Two daemons on one socket would each answer half the commands.
	socket := testSocket(t)

	first, err := listenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	// Shortened, or this waits out the whole handover window -- ninety
	// seconds of a test suite, for a socket nobody is going to let go of.
	restore := handoverWait
	handoverWait = 300 * time.Millisecond
	defer func() { handoverWait = restore }()

	_, err = listenControl(socket)
	if err == nil {
		t.Fatal("a second daemon took a socket that was already being served")
	}
	// And says which of the two things went wrong. Refusing with the bind
	// error underneath -- "address already in use" -- sends somebody looking
	// for a port conflict, when what is there is the other half of this same
	// plugin, already running and answering.
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("refused with %q, which does not say a daemon is already there", err)
	}
}

// testSocket places a control socket the way the daemon does. A Unix socket
// path is bounded by the sockaddr struct, and a macOS temp directory is nearly
// at that limit on its own -- binding a path under one directly fails with
// "invalid argument", which is the very thing socketPathFor exists to avoid.
func testSocket(t *testing.T) string {
	t.Helper()
	socket := socketPathFor(t.TempDir(), "test", os.TempDir())
	t.Cleanup(func() { _ = os.Remove(socket) })
	return socket
}

func TestAPlainSSHPaneIsNamedAfterItsMachine(t *testing.T) {
	// When one of these fails it announces itself by the name it was given.
	// A mirrored pane is told its full label; a plain SSH pane was told only
	// the bare part, so a failure read as "shell: exit status 255" with no
	// mention of which machine had gone -- ninety-five such lines in one log.
	d := withConfig(&Daemon{}, config.Defaults())

	render := func(n string) string { return d.label(config.Host{Target: "bot"}, herdrcli.Pane{}, n) }

	for _, taken := range []map[string]bool{
		nil,
		{"shell@bot": true},
		{"shell@bot": true, "shell 2@bot": true},
	} {
		name := planShellName(taken, render)
		label := render(name)

		if !strings.Contains(label, "bot") {
			t.Errorf("label %q does not say which machine it is", label)
		}
		if !strings.Contains(label, "shell") {
			t.Errorf("label %q lost the name", label)
		}
	}
}

func TestCoalescerRunsOneAtATime(t *testing.T) {
	// Herdr fires a pane.created event for every pane, and this plugin creates
	// panes, so opening a few at once started that many full reconciles on top
	// of one another -- six inside six hundred milliseconds on one startup,
	// each with its own pane listing and its own round trips to every machine.
	var c coalescer
	var mu sync.Mutex
	inFlight, peak, runs := 0, 0, 0

	job := func() {
		mu.Lock()
		inFlight++
		runs++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()

		time.Sleep(time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.run(job)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak != 1 {
		t.Errorf("%d jobs ran at once, want 1", peak)
	}
	if runs < 1 {
		t.Error("the job never ran")
	}
	if runs > 20 {
		t.Errorf("the job ran %d times for 20 requests", runs)
	}
}

func TestCoalescerDoesNotLoseARequest(t *testing.T) {
	// A request arriving while one is running must still be acted on, or the
	// pane that prompted it goes unnoticed until the next tick of the clock.
	var c coalescer
	var runs atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})

	go c.run(func() {
		if runs.Add(1) == 1 {
			close(started)
			<-release
		}
	})

	<-started
	// Arrives mid-run: folded into one further pass rather than starting a
	// second one.
	done := make(chan struct{})
	go func() { defer close(done); c.run(func() { t.Error("a second job ran alongside the first") }) }()
	<-done

	close(release)
	// The folded request runs as a second pass of the original job.
	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := runs.Load(); got != 2 {
		t.Errorf("the job ran %d times, want a second pass for the folded request", got)
	}
}

func TestCoalescerRunsAgainAfterItFinishes(t *testing.T) {
	// Folding is only for requests that overlap; a later one is its own run.
	var c coalescer
	runs := 0
	for i := 0; i < 3; i++ {
		c.run(func() { runs++ })
	}
	if runs != 3 {
		t.Errorf("the job ran %d times, want 3", runs)
	}
}

func TestAWorkspaceThatIsGoneIsForgotten(t *testing.T) {
	// A machine's space disappears when its last pane closes, and the id was
	// kept regardless: every pass then renamed and marked a space that no
	// longer existed, two failing calls each time, for as long as the daemon
	// ran. This machine's log had them every two seconds.
	d := withConfig(&Daemon{
		markedWorkspaces: map[string]workspaceMark{"w37": {token: "remote_up"}},
		rootPanes:        map[string]string{"w37": "w37:p1"},
	}, config.Defaults())
	state := &hostSync{host: config.Host{Target: "prod"}, workspaceID: "w37"}

	// Everything the daemon keys by a space, so the guard's list is a list of
	// things something actually calls forgetWorkspace with rather than a list
	// of claims about them.
	value := reflect.ValueOf(d).Elem()
	for _, name := range perWorkspaceFields {
		field := value.FieldByName(name)
		if !field.IsValid() || field.Kind() != reflect.Map {
			t.Fatalf("perWorkspaceFields names %s, which is not a map on Daemon", name)
		}
		if field.Len() == 0 {
			t.Fatalf("the fixture never puts a space in %s, so forgetWorkspace is "+
				"not asked about it -- put w37 in it", name)
		}
	}

	d.forgetWorkspace(state, "w37")

	if state.workspaceID != "" {
		t.Errorf("workspaceID = %q, want it forgotten", state.workspaceID)
	}
	for _, name := range perWorkspaceFields {
		if value.FieldByName(name).Len() != 0 {
			t.Errorf("%s outlived the space it was about", name)
		}
	}
}

func TestAWorkspaceThatIsGoneLeavesNoPlaceholderBehind(t *testing.T) {
	// The placeholder is the shell Herdr opens with a space this daemon
	// created, closed once a mirror is there to hold the space open. If the
	// space goes before that happens -- the machine drops while its first
	// mirror is still being made -- nothing was closing the record of it.
	//
	// Herdr reuses both space and pane ids. The stale entry is read by
	// retireRootPane the next time a space takes that id, and the id it holds
	// may by then belong to a live pane somebody is working in, which it
	// closes. The aliveness check it makes is not that check: it asks whether
	// the pane exists, which a recycled id does.
	d := withConfig(&Daemon{
		markedWorkspaces: map[string]workspaceMark{"w37": {token: "remote_up"}},
		rootPanes:        map[string]string{"w37": "w37:p1", "w99": "w99:p1"},
	}, config.Defaults())
	state := &hostSync{host: config.Host{Target: "prod"}, workspaceID: "w37"}

	d.forgetWorkspace(state, "w37")

	if root, ok := d.rootPanes["w37"]; ok {
		t.Errorf("placeholder %q outlived the space it was opened with", root)
	}
	if d.rootPanes["w99"] != "w99:p1" {
		t.Error("forgetting one space took another space's placeholder with it")
	}
}

func TestForgettingLeavesADifferentSpaceAlone(t *testing.T) {
	// The machine may already have moved on to another space by the time a
	// stale call comes back saying the old one is gone.
	d := withConfig(&Daemon{markedWorkspaces: map[string]workspaceMark{}}, config.Defaults())
	state := &hostSync{host: config.Host{Target: "prod"}, workspaceID: "w99"}

	d.forgetWorkspace(state, "w37")

	if state.workspaceID != "w99" {
		t.Errorf("workspaceID = %q, want the current space kept", state.workspaceID)
	}
}

func TestAnUnreachablePlainSSHMachineKeepsSayingSo(t *testing.T) {
	// Reconciling a plain SSH machine never contacts it: the ssh runs inside
	// the pane. A pass that went fine therefore says nothing about whether the
	// machine can be reached, and clearing the connect failure on that basis
	// made an unreachable machine report "ok" -- until one of its panes had
	// dropped twice, which is a different mechanism arriving later.
	failure := errors.New("prod is not reachable over ssh: host key changed")

	sshOnly := &hostSync{host: config.Host{Target: "prod"}, sshOnly: true, lastErr: failure}
	if recordReconcile(sshOnly, nil); sshOnly.lastErr == nil {
		t.Error("a plain SSH machine forgot it could not be reached")
	}

	// A mirroring machine is contacted while reconciling, so a pass that went
	// fine is evidence, and the failure should go.
	mirroring := &hostSync{host: config.Host{Target: "ci"}, lastErr: failure, failCount: 1}
	recordReconcile(mirroring, nil)
	if mirroring.lastErr != nil {
		t.Error("a mirroring machine kept an error a successful pass disproved")
	}
	if mirroring.failCount != 0 {
		t.Errorf("failCount = %d, want it reset", mirroring.failCount)
	}
}

func TestRecordReconcileGivesUpOnceAndSaysSoOnce(t *testing.T) {
	// The message is logged by the caller, so it must be reported exactly on
	// the pass that gives up -- not on every pass after it.
	failure := errors.New("cannot reach it")
	state := &hostSync{host: config.Host{Target: "ci"}}

	var announced int
	for i := 0; i < 5; i++ {
		if recordReconcile(state, failure) {
			announced++
		}
	}

	if !state.gaveUp {
		t.Error("it never gave up")
	}
	if announced != 1 {
		t.Errorf("giving up was announced %d times, want once", announced)
	}
	if state.lastErr == nil {
		t.Error("the reason was lost")
	}
}

func TestFocusDoesNothingUntilThereIsSomewhereToGo(t *testing.T) {
	// Picking a machine from the menu is a request to go and work on it. If
	// its space does not exist yet there is nothing to focus, and asking Herdr
	// to focus an empty id would be a failure logged for no reason.
	held := withFakeHerdr(t)
	// A space for the machine that has one, so the call at the end is a real
	// request rather than one Herdr would refuse.
	if _, err := herdrcli.Run("workspace", "create", "--label", "somewhere"); err != nil {
		t.Fatal(err)
	}
	d := withConfig(&Daemon{hosts: map[string]*hostSync{
		"bot":     {host: config.Host{Target: "bot"}},
		"pending": {host: config.Host{Target: "pending"}, workspaceID: ""},
	}}, config.Defaults())

	// Neither of these should reach Herdr: one has no space, the other is not
	// a machine this knows about. That this test said so and checked nothing is
	// why it is written this way now — it called both and asserted nothing, so
	// it passed whether or not either reached Herdr at all.
	d.focusHost("pending")
	d.focusHost("never-connected")

	if n := held().Calls["workspace focus"]; n != 0 {
		t.Errorf("Herdr was asked to focus %d times for machines with nowhere to go", n)
	}

	// And the case that does: a machine whose space is known is focused, or
	// picking it from the menu does nothing at all.
	d.mu.Lock()
	d.hosts["bot"].workspaceID = firstWorkspace(t, held())
	d.mu.Unlock()
	listed := held().Calls["workspace list"]
	d.focusHost("bot")

	if n := held().Calls["workspace focus"]; n != 1 {
		t.Errorf("focusing a machine with a space made %d calls, want 1", n)
	}
	// Straight there. Looking the space up is the insurance for a machine that
	// arrived here without one -- reconciling learns the id, and every path
	// that reaches this reconciles first. Asking Herdr again on every pick is
	// a process spawned for an answer already held, and turning that guard
	// inside out is invisible while the lookup happens to succeed.
	if n := held().Calls["workspace list"] - listed; n != 0 {
		t.Errorf("focusing a machine whose space is known asked Herdr for the list %d times", n)
	}
}

// firstWorkspace is a space that exists in the stand-in, so focusing it is a
// request Herdr can actually answer.
func firstWorkspace(t *testing.T, held fakeHerdr) string {
	t.Helper()
	for id := range held.Workspaces {
		return id
	}
	t.Fatal("the stand-in has no spaces")
	return ""
}

func TestDisconnectingClosesEveryKindOfPane(t *testing.T) {
	// Disconnecting used to close a machine's mirrors only. A plain SSH machine
	// has no mirrors -- its panes are the sessions themselves -- so
	// disconnecting one stopped tracking it and left its terminals open, each
	// still holding an SSH connection, with nothing watching them. That is the
	// default mode, so it was the usual case rather than an unusual one.
	sshOnly := &hostSync{
		sshOnly:    true,
		mirrors:    map[string]string{},
		shellPanes: map[string]bool{"w1:p2": true, "w1:p3": true},
	}
	got := panesToClose(sshOnly)
	if len(got) != 2 || got[0] != "w1:p2" || got[1] != "w1:p3" {
		t.Errorf("panesToClose = %v, want both terminals", got)
	}

	mirroring := &hostSync{
		mirrors:    map[string]string{"term_1": "w2:p1"},
		shellPanes: map[string]bool{},
	}
	if got := panesToClose(mirroring); len(got) != 1 || got[0] != "w2:p1" {
		t.Errorf("panesToClose = %v, want the mirror", got)
	}

	// A machine that fell back to plain SSH after being mirrored can have both.
	both := &hostSync{
		mirrors:    map[string]string{"term_1": "w3:p1"},
		shellPanes: map[string]bool{"w3:p2": true},
	}
	if got := panesToClose(both); len(got) != 2 {
		t.Errorf("panesToClose = %v, want both kinds", got)
	}

	// Nothing open is nothing to close, not a nil surprise for the caller.
	if got := panesToClose(&hostSync{mirrors: map[string]string{}, shellPanes: map[string]bool{}}); len(got) != 0 {
		t.Errorf("panesToClose = %v, want none", got)
	}
}

func TestChangingAModeIsNotUndoneByAnUnreachableMachine(t *testing.T) {
	// Toggling mirroring writes the setting first and then reconnects under it.
	// A machine that will not answer used to make the whole thing report
	// failure, so the menu said the change had not worked while the file on
	// disk said it had -- and put fifteen lines of ssh banner on the screen to
	// explain it.
	unreachable := errors.New("staging is not reachable over ssh: exit status 255: " +
		"@@@@@@@@@@\nWARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\n" +
		"Host key verification failed.")

	summary := summarizeError(unreachable)
	if strings.Contains(summary, "\n") {
		t.Errorf("the reply would span lines: %q", summary)
	}
	if !strings.Contains(summary, "host key changed") {
		t.Errorf("summary = %q, want it to name the cause", summary)
	}

	// What the reply reads as: the change, then the machine's state.
	reply := "mirroring off for staging" + ", but it is not reachable: " + summary
	if !strings.Contains(reply, "mirroring off for staging") {
		t.Errorf("reply = %q, want it to say the change happened", reply)
	}
	if len(reply) > 200 {
		t.Errorf("reply is %d characters, too long for a menu screen", len(reply))
	}
}

func TestMachinesSharingASpaceDoNotStealEachOther(t *testing.T) {
	// "workspace" puts every machine in one space instead of giving each its
	// own. Stray capture asked only "is this pane mine", so every other
	// machine's terminal in that space looked like a local pane to be moved
	// onto this machine and closed here -- each of them doing it to the
	// others, in turn.
	shared := config.Defaults()
	shared.Workspace = "remote"

	bot := &hostSync{
		host:        config.Host{Target: "bot"},
		workspaceID: "w1",
		mirrors:     map[string]string{"term_b": "w1:p1"},
		shellPanes:  map[string]bool{},
	}
	prod := &hostSync{
		host:        config.Host{Target: "prod"},
		workspaceID: "w1",
		mirrors:     map[string]string{"term_p": "w1:p2"},
		shellPanes:  map[string]bool{},
	}
	d := withConfig(&Daemon{hosts: map[string]*hostSync{"bot": bot, "prod": prod}}, shared)

	listing := index(
		pane("w1:p1", "w1", "w1:t1"), // bot's
		pane("w1:p2", "w1", "w1:t2"), // prod's
		pane("w1:p3", "w1", "w1:t3"), // genuinely nobody's
	)

	strays := d.planStrayCapture(bot, listing)
	if len(strays) != 1 || strays[0].PaneID != "w1:p3" {
		t.Errorf("bot claimed %+v, want only the pane nobody owns", strays)
	}

	// And prod does not claim it a second time, having been marked as seen by
	// whichever machine looked first.
	if got := d.planStrayCapture(prod, listing); len(got) != 0 {
		t.Errorf("prod also claimed %+v", got)
	}
}

func TestClaimedPanesCoversEveryMachineAndBothKinds(t *testing.T) {
	d := withConfig(&Daemon{hosts: map[string]*hostSync{
		"bot": {
			mirrors:    map[string]string{"term_1": "w1:p1"},
			shellPanes: map[string]bool{"w1:p2": true},
		},
		"ci": {
			mirrors:    map[string]string{"term_2": "w2:p1"},
			shellPanes: map[string]bool{},
		},
	}}, config.Defaults())

	claimed := d.claimedPanes(nil)
	for _, want := range []string{"w1:p1", "w1:p2", "w2:p1"} {
		if !claimed[want] {
			t.Errorf("%s is a machine's pane but is not claimed", want)
		}
	}
	if claimed["w9:p9"] {
		t.Error("a pane nobody has was claimed")
	}
}

func TestWhatTheMenuShowsAfterAFailedConnectIsReadable(t *testing.T) {
	// The status line and the log both summarise a machine that will not
	// answer. The reply to pressing enter did not, so the one screen somebody
	// is certainly looking at was the one showing fifteen lines of ssh banner.
	banner := errors.New(`prod is not reachable over ssh: exit status 255: @@@@@@@@@@@@@@@
@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @
@@@@@@@@@@@@@@@
IT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!
Someone could be eavesdropping on you right now (man-in-the-middle attack)!
The fingerprint for the ED25519 key sent by the remote host is
SHA256:ekdqq1VzTZUkbfV4hKMQa2T+6OB7kJ2bDyKFsEDJYV0.
Offending ECDSA key in /home/ounos/.ssh/known_hosts:113
Host key verification failed.`)

	got := summarizeError(banner)
	if strings.Contains(got, "\n") {
		t.Errorf("the reply spans lines: %q", got)
	}
	if strings.Contains(got, "@@@") {
		t.Errorf("the reply still carries the banner: %q", got)
	}
	if !strings.Contains(got, "host key changed") {
		t.Errorf("reply = %q, want it to name the cause", got)
	}
	if !strings.Contains(got, "known_hosts") {
		t.Errorf("reply = %q, want it to say what to do", got)
	}

	// Short enough for the screen it lands on, which wraps to eight lines.
	if len([]rune(got)) > 120 {
		t.Errorf("reply is %d characters, too long for a popup", len([]rune(got)))
	}
}

func TestARememberedRemoteSpaceIsCheckedBeforeItIsTrusted(t *testing.T) {
	// The id is remembered from when the space was found or made, and a space
	// goes when its last terminal does. A remembered id that matches nothing
	// filters every pane out, so the machine looks as though it has no
	// terminals -- with nothing said. The local side had the same fault and at
	// least complained about it twice a second; this one was silent.
	panes := []herdrcli.Pane{
		{PaneID: "wA:p1", WorkspaceID: "wA", TerminalID: "t1"},
		{PaneID: "wB:p1", WorkspaceID: "wB", TerminalID: "t2"},
	}

	if planRemoteWorkspaceIsStale("wA", panes) {
		t.Error("a space with panes in it was treated as gone")
	}
	if !planRemoteWorkspaceIsStale("wZ", panes) {
		t.Error("a space nothing is in should be looked up again")
	}
	if !planRemoteWorkspaceIsStale("", panes) {
		t.Error("nothing remembered means it has to be looked up")
	}

	// A machine with nothing open at all: asked again, which costs one call
	// and is the only way to tell an empty space from one that has gone.
	if !planRemoteWorkspaceIsStale("wA", nil) {
		t.Error("with no panes at all it should be looked up rather than assumed")
	}
}

func TestSharedPanesFilterOutEverythingWhenTheSpaceIsWrong(t *testing.T) {
	// Why the above matters: this is what a stale id does to the listing.
	panes := []herdrcli.Pane{
		{PaneID: "wA:p1", WorkspaceID: "wA", TerminalID: "t1"},
		{PaneID: "wA:p2", WorkspaceID: "wA", TerminalID: "t2"},
	}
	order := map[string]int{}

	kept := planSharedPanes(panes, "wA", order, true)
	if len(kept) != 2 {
		t.Errorf("with the right space, kept %d of 2", len(kept))
	}

	gone := planSharedPanes(panes, "wZ", order, true)
	if len(gone) != 0 {
		t.Errorf("with a space that is gone, kept %+v", gone)
	}
}

// perTerminalFields names what hostSync remembers per terminal, which
// forgetTerminals has to clear when that terminal goes. One list, read by the
// test that checks each one is cleared and by the guard that checks a newly
// added field is in some list at all.
//
// It was two lists. The guard's named seven and the fixture beside it built
// six: placement was added to the guard, which was then satisfied, and never
// added to the fixture -- so forgetTerminals was never once called with a
// placement in it. Inverting its loop, to keep what is gone and drop what is
// still there, passed the entire suite.
var perTerminalFields = []string{
	"dismissed", "abandoned", "failures", "retryAt",
	"pendingPlacement", "pendingFocus", "placement",
}

// perPaneFields is the same for what is remembered per pane, which forgetPane
// clears. A slice is in here with the maps: where the order was needed one of
// these was moved from a map to a slice, and the shape a thing is remembered
// in has nothing to do with whether it has to be forgotten.
var perPaneFields = []string{
	"labels", "reportedAgents", "shellPanes", "shellPlacement",
}

// perWorkspaceFields is what the daemon itself remembers per space, cleared by
// forgetWorkspace. The daemon's own state had no list of this kind, and
// rootPanes was cleared only where the placeholder was deliberately retired --
// never on the path that says the space is gone.
var perWorkspaceFields = []string{
	"markedWorkspaces", "rootPanes",
}

func TestNothingIsRememberedAboutATerminalThatIsGone(t *testing.T) {
	// Each of these was cleared where it was most obviously needed, which left
	// the ones nobody had thought about growing for as long as the daemon ran.
	// The count of failed attempts was only cleared alongside a pending retry,
	// and giving up on a terminal deletes the retry first -- so the count that
	// had just reached the limit stayed for good, one entry per terminal that
	// ever failed that often.
	state := &hostSync{
		dismissed:        map[string]bool{"gone": true, "here": true},
		abandoned:        map[string]bool{"gone": true, "here": true},
		failures:         map[string]int{"gone": 5, "here": 2},
		retryAt:          map[string]time.Time{"gone": time.Now(), "here": time.Now()},
		pendingPlacement: map[string]string{"gone": "tab", "here": "tab"},
		pendingFocus:     map[string]bool{"gone": true, "here": true},
		placement:        map[string]string{"gone": "tab", "here": "tab"},
	}

	// The fixture is written out above because reflection cannot fill an
	// unexported field, so this asks the list whether anything it names was
	// left out of it. That is the failure that hid placement: a map nobody
	// puts a terminal into is a map forgetTerminals is never asked about.
	value := reflect.ValueOf(state).Elem()
	for _, name := range perTerminalFields {
		field := value.FieldByName(name)
		if !field.IsValid() || field.Kind() != reflect.Map {
			t.Fatalf("perTerminalFields names %s, which is not a map on hostSync", name)
		}
		if field.Len() != 2 {
			t.Fatalf("the fixture never puts a terminal in %s, so forgetTerminals "+
				"is not asked about it -- give it a \"gone\" and a \"here\"", name)
		}
	}

	forgetTerminals(state, map[string]bool{"here": true})

	gone, here := reflect.ValueOf("gone"), reflect.ValueOf("here")
	for _, name := range perTerminalFields {
		field := value.FieldByName(name)
		if field.MapIndex(gone).IsValid() {
			t.Errorf("%s still remembers a terminal that is gone", name)
		}
		if !field.MapIndex(here).IsValid() {
			t.Errorf("%s forgot a terminal that is still there", name)
		}
	}

	// Presence is what the loops above decide; that they leave the value alone
	// is separate, and cheaper to read written out.
	if state.failures["here"] != 2 || state.pendingPlacement["here"] != "tab" ||
		state.placement["here"] != "tab" {
		t.Error("forgetting one terminal changed what is known about another")
	}
}

func TestGivingUpDoesNotStrandTheCountThatGaveUp(t *testing.T) {
	// The specific path: backOff deletes the retry when it gives up, so the
	// count was never reached by the loop that cleared it alongside one.
	state := &hostSync{
		host:      config.Host{Target: "bot"},
		dismissed: map[string]bool{},
		abandoned: map[string]bool{},
		failures:  map[string]int{},
		retryAt:   map[string]time.Time{},
	}
	d := withConfig(&Daemon{}, config.Defaults())

	for i := 0; i < maxMirrorAttempts; i++ {
		d.backOff(state, "term_1", errors.New("no"))
	}
	if !state.abandoned["term_1"] {
		t.Fatal("it never gave up")
	}
	if _, ok := state.retryAt["term_1"]; ok {
		t.Fatal("a retry is still scheduled for something given up on")
	}
	if state.failures["term_1"] == 0 {
		t.Fatal("the count was not kept while the terminal was alive")
	}

	// Once the terminal is gone, so is the count.
	forgetTerminals(state, map[string]bool{})
	if _, ok := state.failures["term_1"]; ok {
		t.Error("the count survived the terminal it counted")
	}
}

func TestANewDaemonHasEveryMapItWillWriteTo(t *testing.T) {
	// Writing to a nil map panics, and the panic is nowhere near the map that
	// was forgotten. This is the constructor's whole job beyond holding the
	// config, and adding a map without adding it here is a mistake nothing
	// else would catch until the daemon ran and something wrote to it.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	d := New(config.Defaults())

	// Each of these is written to during an ordinary pass.
	d.hosts["bot"] = &hostSync{}
	d.rootPanes["w1"] = "w1:p1"
	d.markedWorkspaces["w1"] = workspaceMark{token: "remote_up"}
	d.seenStray["w1:p2"] = true

	if d.config().PollInterval == "" {
		t.Error("the config was not carried")
	}
	if d.snapshot.Hosts == nil {
		t.Error("the snapshot was not loaded, so restoring would panic")
	}
	if err := d.configWarning(); err != "" {
		t.Errorf("a good config warned: %q", err)
	}
}

func TestADaemonToldItsConfigIsUnreadableSaysSo(t *testing.T) {
	// Built this way when the file could not be read at all: the daemon still
	// runs, because the menu and every action reach it over its socket and
	// exiting would leave them all failing with no visible reason.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	d := NewWithConfigError(config.Defaults(), errors.New("unexpected end of JSON input"))

	warning := d.configWarning()
	if !strings.Contains(warning, "could not be read") {
		t.Errorf("warning = %q, want it to say the file was unreadable", warning)
	}
	if !strings.Contains(warning, "JSON") {
		t.Errorf("warning = %q, want it to say why", warning)
	}
	// And it is still usable: the maps are there and the defaults apply.
	d.hosts["bot"] = &hostSync{}
	if !d.config().ShouldAutoStart() {
		t.Error("the defaults did not take effect")
	}
}

func TestLabelsForNamesEveryTerminal(t *testing.T) {
	// What ends up in the sidebar for a machine's terminals, including the
	// disambiguation when two of them would otherwise read the same.
	d := withConfig(&Daemon{}, config.Defaults())
	host := config.Host{Target: "bot"}

	labels := d.labelsFor(host, []herdrcli.Pane{
		{TerminalID: "t1", PaneID: "w1:p1", Title: "vim notes.md"},
		{TerminalID: "t2", PaneID: "w1:p2", Title: "deploy@box: ~"},
		{TerminalID: "t3", PaneID: "w1:p3", Title: "deploy@box: ~"},
	})

	if len(labels) != 3 {
		t.Fatalf("named %d of 3 terminals", len(labels))
	}
	for id, label := range labels {
		if !strings.HasSuffix(label, "@bot") {
			t.Errorf("%s is named %q, which does not say which machine it is on", id, label)
		}
	}
	if !strings.Contains(labels["t1"], "vim notes.md") {
		t.Errorf("t1 = %q, want the command it is running", labels["t1"])
	}
	// The two prompt banners would read alike, so they are told apart rather
	// than both being shown the same.
	if labels["t2"] == labels["t3"] {
		t.Errorf("two terminals are both called %q", labels["t2"])
	}
}

func TestASnapshotCarriesTheTerminalCount(t *testing.T) {
	// End to end through the two functions that write and read it, since the
	// count being right on disk was never the problem -- it was read as a
	// boolean at the far end.
	state := &hostSync{
		mirrors:    map[string]string{},
		dismissed:  map[string]bool{},
		abandoned:  map[string]bool{},
		shellPanes: map[string]bool{"w1:p1": true, "w1:p2": true, "w1:p3": true},
	}
	saved := hostSnapshot{Shells: len(state.shellPanes)}

	fresh := &hostSync{mirrors: map[string]string{}, dismissed: map[string]bool{}}
	restoreFromSnapshot(fresh, saved)

	if fresh.restoreShells != 3 {
		t.Errorf("restoreShells = %d, want the three that were open", fresh.restoreShells)
	}
}

func TestAMachineOwnsItsSpaceEvenWhenItCannotBeReached(t *testing.T) {
	// A machine's space is named "☁  bot" while it can be reached and "⚠  bot"
	// while it cannot. Matching against the reachable form alone meant a
	// machine stopped being recognised as the owner of its own space the moment
	// it went down -- so opening a tab there made an ordinary local shell,
	// inside that machine's space, with nothing to say why.
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Target: "bot"}}
	d := withConfig(&Daemon{hosts: map[string]*hostSync{}}, cfg)

	for _, label := range []string{"☁  bot", "⚠  bot"} {
		got, ok := d.hostForWorkspaceLabel(label)
		if !ok {
			t.Errorf("%q was not recognised as a machine's space", label)
			continue
		}
		if got.Target != "bot" {
			t.Errorf("%q resolved to %q, want bot", label, got.Target)
		}
	}

	// Something that is nobody's space stays nobody's, so the keybinding still
	// makes an ordinary tab everywhere else.
	if _, ok := d.hostForWorkspaceLabel("~"); ok {
		t.Error("the local space was claimed by a machine")
	}
	if _, ok := d.hostForWorkspaceLabel("☁  somewhere-else"); ok {
		t.Error("another machine's space was claimed")
	}
}

func TestAMachineConnectedAdHocOwnsItsSpaceToo(t *testing.T) {
	// Machines picked from ~/.ssh/config are never written into the plugin's
	// config, so they are only found through what is connected.
	d := withConfig(&Daemon{hosts: map[string]*hostSync{
		"laptop": {host: config.Host{Target: "laptop"}},
	}}, config.Defaults())

	for _, label := range []string{"☁  laptop", "⚠  laptop"} {
		got, ok := d.hostForWorkspaceLabel(label)
		if !ok || got.Target != "laptop" {
			t.Errorf("%q resolved to %+v, %v; want laptop", label, got, ok)
		}
	}
}

func TestAMachineWithItsOwnNameOwnsItsSpace(t *testing.T) {
	// A machine can be given a name to show here, and its space is named after
	// that rather than after the ssh destination.
	cfg := config.Defaults()
	cfg.Hosts = []config.Host{{Target: "bot.example.com", Label: "bot"}}
	d := withConfig(&Daemon{hosts: map[string]*hostSync{}}, cfg)

	for _, label := range []string{"☁  bot", "⚠  bot"} {
		got, ok := d.hostForWorkspaceLabel(label)
		if !ok || got.Target != "bot.example.com" {
			t.Errorf("%q resolved to %+v, %v; want the machine behind the name", label, got, ok)
		}
	}
}

func TestTheSpaceOnAMachineIsFoundAfterTheFormatChanges(t *testing.T) {
	// The local lookup ignores the marker so that changing workspace_format
	// does not orphan the space a machine's panes are already in. The far side
	// has its own format and its own space, and was matching the name exactly
	// -- so changing remote_workspace_format would leave the terminals where
	// they were and quietly make a second space beside them.
	hub := config.HubName()

	for _, existing := range []string{
		"☁  " + hub, // what this plugin makes by default
		"⚠  " + hub, // some other marker
		hub,         // a format with no marker at all
	} {
		if !sameWorkspace(existing, hub) {
			t.Errorf("a space called %q was not recognised as this machine's", existing)
		}
	}

	// Somebody else's space is still somebody else's.
	if sameWorkspace("☁  someone-else", hub) {
		t.Errorf("a space named for another machine was claimed")
	}
	if sameWorkspace("~", hub) {
		t.Error("the machine's own local space was claimed")
	}
}

func TestMachinesNotWrittenDownAreStillBroughtBack(t *testing.T) {
	// A machine picked from ~/.ssh/config is never written to the config file,
	// so the snapshot is the only record that it was connected. Starting up
	// walked the config alone, which made "restarting brings your machines
	// back" true for the ones written down and quietly false for the rest --
	// including the sentence to that effect I put in the README.
	remembered := []string{"laptop", "bot", "retired", "ci"}
	connected := map[string]bool{"bot": true, "ci": true} // already done from the config
	disabled := map[string]bool{"retired": true}

	got := planSnapshotRestore(remembered, connected, disabled)

	if len(got) != 1 || got[0] != "laptop" {
		t.Errorf("restoring %v, want just the machine that is not written down", got)
	}
}

func TestATurnedOffMachineIsNotBroughtBackByASnapshot(t *testing.T) {
	// It is not connected at startup for the same reason it is not offered in
	// the menu, and a snapshot from before it was turned off should not undo
	// that.
	got := planSnapshotRestore([]string{"retired"}, map[string]bool{}, map[string]bool{"retired": true})
	if len(got) != 0 {
		t.Errorf("restoring %v, want nothing: that machine is turned off", got)
	}
}

func TestRestoringFromASnapshotIsInAStableOrder(t *testing.T) {
	// Two machines coming back in a different order each start is the sort of
	// thing that makes a log impossible to compare with the one before it.
	remembered := []string{"zeta", "alpha", "mid"}
	for i := 0; i < 20; i++ {
		got := planSnapshotRestore(remembered, map[string]bool{}, map[string]bool{})
		if len(got) != 3 || got[0] != "alpha" || got[1] != "mid" || got[2] != "zeta" {
			t.Fatalf("order = %v, want it sorted", got)
		}
	}
}

func TestAnExactlyNamedSpaceWinsOverAGuess(t *testing.T) {
	// The tolerant match exists so that changing workspace_format does not
	// orphan the space a machine's terminals are already in, which means it
	// also accepts a space called just "bot". Taking the first match found made
	// that a matter of what order Herdr happened to list them in: somebody with
	// a space of their own called "bot", and a machine called bot, could have
	// either adopted -- renamed, and given terminals from the machine.
	mine := herdrcli.Workspace{WorkspaceID: "w1", Label: "bot"}    // somebody's own
	ours := herdrcli.Workspace{WorkspaceID: "w2", Label: "☁  bot"} // this plugin's

	for _, order := range [][]herdrcli.Workspace{{mine, ours}, {ours, mine}} {
		got, ok := pickWorkspace(order, "☁  bot", "bot")
		if !ok || got != "w2" {
			t.Errorf("listed as %v, picked %q; want the one named exactly", labelsOf(order), got)
		}
	}
}

func TestAGuessIsStillBetterThanMakingASecondSpace(t *testing.T) {
	// With no exact match, the tolerant one is what keeps a machine's terminals
	// where they are after its format changes -- which is the whole reason it
	// is there.
	for _, existing := range []string{"bot", "⚠  bot", "🔴 bot"} {
		list := []herdrcli.Workspace{{WorkspaceID: "w9", Label: existing}}
		got, ok := pickWorkspace(list, "☁  bot", "bot")
		if !ok || got != "w9" {
			t.Errorf("a space called %q was not recognised as the machine's", existing)
		}
	}

	// And nothing that is not the machine's.
	none := []herdrcli.Workspace{{WorkspaceID: "w1", Label: "~"}, {WorkspaceID: "w2", Label: "☁  other"}}
	if id, ok := pickWorkspace(none, "☁  bot", "bot"); ok {
		t.Errorf("claimed %q, which is not this machine's space", id)
	}
}

func labelsOf(list []herdrcli.Workspace) []string {
	out := make([]string, 0, len(list))
	for _, ws := range list {
		out = append(out, ws.Label)
	}
	return out
}

func TestHowATerminalWasAskedForSurvivesAFailedOpen(t *testing.T) {
	// Opening a pane can fail, and the mirror is retried afterwards. The
	// placement and focus somebody asked for were spent on the attempt rather
	// than on the pane, so a "new tab on this machine" that failed once came
	// back as whatever that machine's usual placement is, unfocused.
	state := &hostSync{
		pendingPlacement: map[string]string{"term_1": "tab"},
		pendingFocus:     map[string]bool{"term_1": true},
	}

	// An attempt that fails: the request is read and not spent.
	placement, focus, _ := takeRequest(state, "term_1", "split")
	if placement != "tab" || !focus {
		t.Fatalf("first attempt got %q focus=%v, want the tab that was asked for", placement, focus)
	}

	// The retry still knows what was asked for.
	placement, focus, done := takeRequest(state, "term_1", "split")
	if placement != "tab" || !focus {
		t.Errorf("the retry got %q focus=%v, want the tab that was asked for", placement, focus)
	}
	done()

	// Spent now, so a later terminal is not opened as somebody else's request
	// -- which matters because Herdr reuses ids.
	placement, focus, _ = takeRequest(state, "term_1", "split")
	if placement != "split" || focus {
		t.Errorf("a later terminal got %q focus=%v, want the machine's usual placement", placement, focus)
	}
}

func TestATerminalNobodyAskedForGetsTheMachinesUsualPlacement(t *testing.T) {
	// Most terminals are discovered rather than requested: they appeared on the
	// machine and are being mirrored here.
	state := &hostSync{
		pendingPlacement: map[string]string{},
		pendingFocus:     map[string]bool{},
	}
	placement, focus, done := takeRequest(state, "term_9", "zoomed")
	if placement != "zoomed" || focus {
		t.Errorf("got %q focus=%v, want the machine's placement and no focus", placement, focus)
	}
	done() // forgetting nothing must not panic or disturb anything
	if len(state.pendingPlacement) != 0 || len(state.pendingFocus) != 0 {
		t.Error("forgetting a request nobody made left something behind")
	}
}

func TestPlanLostPaneAction(t *testing.T) {
	// The real thing, as ssh prints it, banner and all -- this is what the
	// bridge records and what the daemon has to recognise.
	hostKey := `bot is not reachable over ssh: exit status 255: @@@@@@@@@@@@@@@@
@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @
@@@@@@@@@@@@@@@@
IT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!
Host key verification failed.`
	dropped := "bot is not reachable over ssh: exit status 255: Connection reset by peer"

	t.Run("a dropped link is reopened", func(t *testing.T) {
		if got := planLostPaneAction(0, dropped); got != reopenPane {
			t.Errorf("got %v, want the terminal reopened", got)
		}
	})

	t.Run("a failure that needs a person costs no second terminal", func(t *testing.T) {
		// Reopening was the only way to find out why a pane went, so a machine
		// whose host key had changed opened a second terminal to fail in
		// exactly the same way, and put a second banner in the log doing it.
		if got := planLostPaneAction(0, hostKey); got != stopUntilFixed {
			t.Errorf("got %v, want the machine left alone until it is fixed", got)
		}
	})

	t.Run("enough dropped links still stop", func(t *testing.T) {
		if got := planLostPaneAction(maxHostAttempts, dropped); got != stopForNow {
			t.Errorf("got %v, want the machine left alone for now", got)
		}
	})

	t.Run("an unrecorded reason goes by the count, as it always did", func(t *testing.T) {
		// A pane marked by an older build wrote no reason, and one killed
		// rather than failed leaves nothing to read.
		if got := planLostPaneAction(0, ""); got != reopenPane {
			t.Errorf("got %v, want the terminal reopened", got)
		}
		if got := planLostPaneAction(maxHostAttempts, ""); got != stopForNow {
			t.Errorf("got %v, want the machine left alone for now", got)
		}
	})
}

func TestALocalFailureIsNotMistakenForAnSSHOne(t *testing.T) {
	// The classifier matches ssh's own wording, and reconcile errors are not
	// all from ssh -- a call to the local Herdr can fail too. Writing a machine
	// off for good over a local hiccup would be the worst kind of wrong answer,
	// since the cause is not on that machine at all and nothing on it can be
	// fixed to clear it.
	//
	// What keeps the two apart is that ssh capitalises and Go's syscall errors
	// do not. That is a thin distinction to be relying on, so it is pinned here
	// rather than left to be discovered.
	local := []string{
		"dial unix /run/herdr.sock: connect: permission denied",
		"open /home/u/.config/herdr/plugins/p/config.json: permission denied",
		"herdr pane list: fork/exec /usr/bin/herdr: permission denied",
	}
	for _, message := range local {
		if planGiveUp(0, errors.New(message)) {
			t.Errorf("%q stopped the machine for good; it is not an ssh refusal", message)
		}
	}

	// The ssh one, which does need a person, still does.
	if !planGiveUp(0, errors.New("bot: Permission denied (publickey).")) {
		t.Error("ssh refusing a key should still stop the machine")
	}
}

func TestEveryNamedFailureIsReachableAndDecided(t *testing.T) {
	// Mutation testing found two rows of this table that no test could reach.
	// Both were "covered" by a case whose message matched an earlier row first:
	// "Could not resolve hostname" sits behind "Name or service not known",
	// which Linux prints in the same sentence, and "Connection closed by" sits
	// behind kex_exchange_identification for the same reason. Flipping either
	// row's retry decision changed nothing any test noticed.
	//
	// The first attempt at this test read the expected decision out of the same
	// table, which meant flipping a row flipped both sides of the comparison
	// and it agreed with whatever it was given. So the decisions are written
	// out here instead, and the two lists are held to each other below: adding
	// a cause to the table without deciding here is a failure, not a silence.
	settled := map[string]bool{
		"REMOTE HOST IDENTIFICATION HAS CHANGED": true,
		"Host key verification failed":           true,
		// Two spellings of one refusal, matched on ssh's own wording so that a
		// file mode reported by the machine is not read as a key it would not
		// take. Both are settled for the same reason: the next attempt offers
		// the same key and is refused the same way.
		"Permission denied (":                 true,
		"Permission denied, please try again": true,
		"Too many authentication failures":    true,
		"Name or service not known":           true,
		"Could not resolve hostname":          true,
		remote.ErrNoHerdr.Error():             true,

		"Connection refused":          false,
		"Connection timed out":        false,
		"Operation timed out":         false,
		"Network is unreachable":      false,
		"kex_exchange_identification": false,
		"Connection closed by":        false,
		"Connection reset by peer":    false,
		"No route to host":            false,
	}

	for _, known := range knownFailures {
		t.Run(known.needle, func(t *testing.T) {
			want, decided := settled[known.needle]
			if !decided {
				t.Fatalf("%q was added to the table without a decision here: "+
					"does trying again stand any chance of a different answer?", known.needle)
			}
			// Each row on its own, rather than through a message that a
			// different row might answer first.
			err := errors.New(known.needle)
			if got := summarizeError(err); got != known.summary {
				t.Errorf("summarizeError(%q) = %q, want %q", known.needle, got, known.summary)
			}
			if got := planGiveUp(0, err); got != want {
				t.Errorf("planGiveUp on %q = %v, want %v", known.needle, got, want)
			}
		})
	}

	if len(settled) != len(knownFailures) {
		t.Errorf("%d causes decided here but %d in the table", len(settled), len(knownFailures))
	}
	inTable := map[string]bool{}
	for _, known := range knownFailures {
		inTable[known.needle] = true
	}
	for needle := range settled {
		if !inTable[needle] {
			t.Errorf("%q is decided here but is no longer a cause the code knows", needle)
		}
	}
}

func TestNoNamedFailureIsHiddenBehindAnEarlierOne(t *testing.T) {
	// The table is read in order and the first match wins, so a row whose
	// phrase contains an earlier row's phrase can never be reached. It would
	// look like a case that is handled and behave like one that is not.
	for i, later := range knownFailures {
		for _, earlier := range knownFailures[:i] {
			if strings.Contains(later.needle, earlier.needle) {
				t.Errorf("%q can never match: %q comes first and is inside it",
					later.needle, earlier.needle)
			}
		}
	}
}

func TestANameThatDoesNotResolveIsRecognisedOnBothPlatforms(t *testing.T) {
	// The two rows exist because the two systems word it differently, and the
	// Linux wording carries both phrases -- which is exactly what hid the macOS
	// row from every test that thought it was covering this.
	messages := map[string]string{
		"linux": "prd is not reachable over ssh: exit status 255: ssh: Could not resolve hostname prd: Name or service not known",
		"macos": "prd is not reachable over ssh: exit status 255: ssh: Could not resolve hostname prd: nodename nor servname provided, or not known",
	}
	for platform, message := range messages {
		t.Run(platform, func(t *testing.T) {
			err := errors.New(message)
			if got := summarizeError(err); got != "host name does not resolve" {
				t.Errorf("summarizeError = %q, want %q", got, "host name does not resolve")
			}
			// A name that does not resolve does not start resolving because
			// this tried again.
			if !planGiveUp(0, err) {
				t.Error("a name that does not resolve should not be retried")
			}
		})
	}
}

func TestAConnectionClosedAfterTheBannerIsStillWorthRetrying(t *testing.T) {
	// The row for this sits behind kex_exchange_identification, which is
	// deliberate -- when both appear the specific cause is the better line --
	// but it does happen alone, when the link drops after the banner exchange.
	err := errors.New("bot is not reachable over ssh: exit status 255: Connection closed by 10.0.0.1 port 22")
	if got := summarizeError(err); got != "the machine closed the connection" {
		t.Errorf("summarizeError = %q", got)
	}
	if planGiveUp(0, err) {
		t.Error("a connection that closed can come good; it should be retried")
	}

	// And with both, the more specific one is what gets said.
	both := errors.New("kex_exchange_identification: Connection closed by remote host")
	if got := summarizeError(both); !strings.Contains(got, "before login") {
		t.Errorf("summarizeError = %q, want the kex wording", got)
	}
}

func TestMirroredTerminalsKeepTheOrderTheyHaveOnTheMachine(t *testing.T) {
	// The whole promise of "shared" scope is that both ends show the same
	// terminals in the same order: the first tab here is the first tab there.
	// Herdr does not promise an order in a pane listing, so without this the
	// two sides drift apart as panes come and go -- which reads as tabs
	// shuffling themselves for no reason.
	//
	// Every comparison in the sort could be reversed without a test noticing.
	// The pane ids deliberately run against the tab order. Sorted by id alone
	// these come out p1, p2, p5, p9 -- which is what a comparator that ignores
	// the tab produces, so data where the two agree cannot tell the difference.
	panes := []herdrcli.Pane{
		{PaneID: "p2", TabID: "t3", WorkspaceID: "shared"},
		{PaneID: "p9", TabID: "t1", WorkspaceID: "shared"},
		{PaneID: "p5", TabID: "t2", WorkspaceID: "shared"},
		{PaneID: "p1", TabID: "t3", WorkspaceID: "shared"},
		{PaneID: "p7", TabID: "t2", WorkspaceID: "elsewhere"},
	}
	// What the machine's own tab bar looks like, left to right.
	tabOrder := map[string]int{"t1": 0, "t2": 1, "t3": 2}

	t.Run("tabs come in the machine's order, and splits within a tab in theirs", func(t *testing.T) {
		got := planSharedPanes(panes, "shared", tabOrder, true)
		var ids []string
		for _, pane := range got {
			ids = append(ids, pane.PaneID)
		}
		// t1 first (p9), then t2 (p5), then t3 (p1 before p2 by id).
		want := []string{"p9", "p5", "p1", "p2"}
		if len(ids) != len(want) {
			t.Fatalf("got %v, want %v", ids, want)
		}
		for i := range want {
			if ids[i] != want[i] {
				t.Fatalf("got %v, want %v", ids, want)
			}
		}
	})

	t.Run("a pane outside the shared space is left where it is", func(t *testing.T) {
		for _, pane := range planSharedPanes(panes, "shared", tabOrder, true) {
			if pane.PaneID == "p7" {
				t.Error("a pane from the machine's own work was mirrored")
			}
		}
	})

	t.Run("scope all takes everything, still in order", func(t *testing.T) {
		got := planSharedPanes(panes, "shared", tabOrder, false)
		if len(got) != len(panes) {
			t.Fatalf("got %d panes, want all %d", len(got), len(panes))
		}
		// p5 and p7 share tab t2, so the pane id decides between them.
		if got[1].PaneID != "p5" || got[2].PaneID != "p7" {
			t.Errorf("within a tab the order is by pane id; got %s then %s",
				got[1].PaneID, got[2].PaneID)
		}
	})

	t.Run("a tab the machine did not list sorts first, not randomly", func(t *testing.T) {
		// An unknown tab reads as position zero. It has to land somewhere
		// definite, or its pane moves about between passes.
		withUnknown := append([]herdrcli.Pane{{PaneID: "p0", TabID: "t9", WorkspaceID: "shared"}}, panes...)
		first := planSharedPanes(withUnknown, "shared", tabOrder, true)
		second := planSharedPanes(withUnknown, "shared", tabOrder, true)
		for i := range first {
			if first[i].PaneID != second[i].PaneID {
				t.Fatalf("the order is not stable: %v then %v", first, second)
			}
		}
		if first[0].PaneID != "p0" {
			t.Errorf("an unlisted tab landed at %s, not first", first[0].PaneID)
		}
	})
}

func TestTruncateRunesKeepsAsMuchAsFits(t *testing.T) {
	// This is what stops an ssh error filling the sidebar, and it cuts by runes
	// so a non-ASCII host name is not split mid-character. Both boundaries
	// could move by one unnoticed.
	for _, tt := range []struct {
		in   string
		max  int
		want string
	}{
		{"abcdef", 6, "abcdef"},
		{"abcdef", 5, "abcd…"},
		{"abcdef", 1, "…"},
		{"abcdef", 0, ""},
		{"abcdef", -1, ""},
		{"", 0, ""},
		// By runes, not bytes: six characters that are eighteen bytes.
		{"日本語です。ね", 7, "日本語です。ね"},
		{"日本語です。ね", 6, "日本語です…"},
	} {
		if got := truncateRunes(tt.in, tt.max); got != tt.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}

func TestRefusingAMachineSaysWhyRatherThanBlamingTheConfig(t *testing.T) {
	// The answer used to be "%s is not in the plugin config", which was the
	// wrong thing to be told twice over. A name that is fine needs no entry in
	// that file -- anything in ~/.ssh/config works, and so does anything that
	// looks like a machine -- and a name that is not fine would still be
	// refused after adding one. It sent people to edit a file that had nothing
	// to do with the problem.
	//
	// The names that reach here are not always typed, either: connect falls
	// back to whatever text is selected in the terminal.
	d := withConfig(&Daemon{}, config.Defaults())

	cases := []struct {
		name string
		want string
	}{
		{"-oProxyCommand=touch /tmp/x", "dash"},
		{"error: could not reach the database", "space"},
		{"", "no target"},
		{"bot\x00", "control character"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := d.hostConfig(tt.name)
			if err == nil {
				t.Fatalf("%q was accepted as a machine", tt.name)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("refusing %q said %q, which does not mention %q",
					tt.name, err, tt.want)
			}
			if strings.Contains(err.Error(), "plugin config") {
				t.Errorf("refusing %q blamed the plugin config: %q", tt.name, err)
			}
		})
	}
}

func TestASlowCommandStillGetsItsAnswer(t *testing.T) {
	// The connection used to carry one deadline for the whole exchange, which
	// therefore had to cover the work in between. The work is a connect:
	// several ssh calls, each with its own timeout, and for a connect that
	// names no machine, that again for every machine in the config one after
	// another. Enough unreachable ones and the daemon cut off the answer it
	// had just finished working out, and the client -- still waiting on a
	// longer deadline of its own -- reported a broken connection rather than
	// the result.
	//
	// So the halves are bounded separately, and this holds the property that
	// falls out of it: however long the work takes, the answer arrives.
	// Shortened so the work can outlast it without the test being slow. What
	// matters is the order of events, not the size of the numbers.
	restore := exchangeTimeout
	exchangeTimeout = 50 * time.Millisecond
	defer func() { exchangeTimeout = restore }()
	// Read once, here: the dispatch below runs in its own goroutine and must
	// not read the package variable that the deferred restore writes.
	slow := 10 * exchangeTimeout

	server, client := net.Pipe()
	defer client.Close()

	go serveExchange(server, func(Command) Reply {
		time.Sleep(slow)
		return Reply{OK: true, Message: "worked it out eventually"}
	})

	if err := json.NewEncoder(client).Encode(Command{Cmd: "status"}); err != nil {
		t.Fatal(err)
	}

	var reply Reply
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewDecoder(client).Decode(&reply); err != nil {
		t.Fatalf("the answer never arrived: %v", err)
	}
	if !reply.OK || reply.Message != "worked it out eventually" {
		t.Errorf("reply = %+v", reply)
	}
}

func TestARequestThatCannotBeReadSaysWhy(t *testing.T) {
	// It is the plugin talking to itself, so a malformed command means a bug,
	// and the shape of it is the only clue there will be. "bad request" was
	// not one.
	server, client := net.Pipe()
	defer client.Close()

	go serveExchange(server, func(Command) Reply { return Reply{OK: true} })

	if _, err := client.Write([]byte("this is not json\n")); err != nil {
		t.Fatal(err)
	}

	var reply Reply
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewDecoder(client).Decode(&reply); err != nil {
		t.Fatalf("no answer to a malformed request: %v", err)
	}
	if reply.OK {
		t.Error("a malformed request was accepted")
	}
	if !strings.Contains(reply.Message, "could not read the request") {
		t.Errorf("reply = %q, want it to say what went wrong", reply.Message)
	}
	if reply.Message == "bad request" {
		t.Error("the reason was thrown away")
	}
}

func TestAnEndlessRequestIsCutOff(t *testing.T) {
	// A client that opens a JSON object and never closes it should not grow
	// the daemon a buffer at a time. The read is bounded well above anything a
	// real command can be -- five short strings -- so hitting the bound means
	// the request was never going to end.
	server, client := net.Pipe()
	defer client.Close()

	go serveExchange(server, func(Command) Reply {
		t.Error("a request that never ended was dispatched")
		return Reply{}
	})

	go func() {
		_, _ = client.Write([]byte(`{"cmd":"status","host":"`))
		chunk := []byte(strings.Repeat("x", 4096))
		for {
			// Stops when the daemon closes its end.
			if _, err := client.Write(chunk); err != nil {
				return
			}
		}
	}()

	// The answer arriving is the proof: it can only come after the read gave
	// up, and the read can only give up at the bound.
	_ = client.SetReadDeadline(time.Now().Add(20 * time.Second))
	var reply Reply
	if err := json.NewDecoder(client).Decode(&reply); err != nil {
		t.Fatalf("the daemon kept reading a request that never ends: %v", err)
	}
	if reply.OK {
		t.Error("a request that never ended was accepted")
	}
}

func TestConnectAllReachesTheMachinesTogether(t *testing.T) {
	// One at a time, an unreachable machine costs its whole connect timeout
	// and the next one has not started yet, so the total is the sum. Three of
	// them was enough to outlast the deadline on the connection carrying the
	// answer back, and the caller was told the connection broke rather than
	// what happened.
	//
	// Checked by watching when the ssh processes overlap rather than by timing
	// the whole thing, so the result does not depend on how loaded the machine
	// running the test is.
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho start >> " + log + "\nsleep 0.4\necho end >> " + log + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	const machines = 4
	cfg := config.Defaults()
	for i := 0; i < machines; i++ {
		cfg.Hosts = append(cfg.Hosts, config.Host{Target: fmt.Sprintf("machine%d", i)})
	}
	d := withConfig(&Daemon{hosts: map[string]*hostSync{}}, cfg)

	connected, err := d.connectAll()
	if err != nil {
		t.Fatalf("connectAll: %v", err)
	}
	if len(connected) != machines {
		t.Fatalf("connected %v, want all %d", connected, machines)
	}
	// Reported in the order they are written in the config, however the
	// connections finished.
	for i, target := range connected {
		if want := fmt.Sprintf("machine%d", i); target != want {
			t.Errorf("connected %v, want them in config order", connected)
			break
		}
	}

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(raw))
	started := 0
	for _, line := range lines {
		if line == "end" {
			break
		}
		started++
	}
	if started < machines {
		t.Errorf("only %d of %d connections had started before the first finished: %v",
			started, machines, lines)
	}
}

func TestConnectEachReportsFailuresAgainstTheRightMachine(t *testing.T) {
	// The machines are reached at the same time, so they finish in whatever
	// order they finish in. The answers have to come back against the machine
	// they belong to regardless, or a reconnect reports the wrong ones as
	// having worked.
	dir := t.TempDir()
	// Fails for anything named "broken", and the slow one finishes last, so
	// the order the connections complete is not the order they were asked for.
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in broken*) exit 255;; slow*) sleep 0.3;; esac; done\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SESSION", "hub")

	hosts := []config.Host{
		{Target: "slow-and-fine"},
		{Target: "broken-one"},
		{Target: "fine"},
		{Target: "broken-two"},
	}
	d := withConfig(&Daemon{hosts: map[string]*hostSync{}}, config.Defaults())

	errs := d.connectEach(hosts)
	if len(errs) != len(hosts) {
		t.Fatalf("got %d answers for %d machines", len(errs), len(hosts))
	}
	for i, host := range hosts {
		broken := strings.HasPrefix(host.Target, "broken")
		if broken && errs[i] == nil {
			t.Errorf("%s failed but was reported as connected", host.Target)
		}
		if !broken && errs[i] != nil {
			t.Errorf("%s connected but was reported as failed: %v", host.Target, errs[i])
		}
	}
}

func TestPlanWorkspaceMark(t *testing.T) {
	// This used to run on every pass: a space whose name and marker had not
	// changed was renamed to the name it already had and marked with the marker
	// it already carried, a couple of seconds later, for as long as Herdr was
	// open. Two processes per machine per pass, for ever, to change nothing.
	now := time.Now()
	settled := workspaceMark{label: "☁  bot", token: tokenRemoteUp, at: now}

	t.Run("nothing changed and it was recent", func(t *testing.T) {
		if planWorkspaceMark(settled, "☁  bot", tokenRemoteUp, now.Add(time.Second)) {
			t.Error("a space that already reads correctly was written again")
		}
	})

	t.Run("the machine stopped answering", func(t *testing.T) {
		// The name and the marker both change, and this is the moment somebody
		// is looking at the sidebar to find out why something is not working.
		if !planWorkspaceMark(settled, "⚠  bot", tokenRemoteDown, now.Add(time.Second)) {
			t.Error("a space did not get its unreachable marker")
		}
	})

	t.Run("only the marker changed", func(t *testing.T) {
		if !planWorkspaceMark(settled, "☁  bot", tokenRemoteDown, now.Add(time.Second)) {
			t.Error("a changed marker was not written")
		}
	})

	t.Run("nothing changed but it has been a while", func(t *testing.T) {
		// The repair: the marker comes back if anything else cleared it, and
		// Herdr reuses space ids, so an id this remembers can turn out to
		// belong to a different space.
		if !planWorkspaceMark(settled, "☁  bot", tokenRemoteUp, now.Add(workspaceRepairInterval)) {
			t.Error("a space was never written again, so nothing would repair it")
		}
	})

	t.Run("the last attempt failed", func(t *testing.T) {
		// Nothing was put there to keep, so there is nothing to skip.
		failed := settled
		failed.failed = true
		if !planWorkspaceMark(failed, "☁  bot", tokenRemoteUp, now.Add(time.Second)) {
			t.Error("a failed attempt was not retried")
		}
	})

	t.Run("a space nothing is known about", func(t *testing.T) {
		if !planWorkspaceMark(workspaceMark{}, "☁  bot", tokenRemoteUp, now) {
			t.Error("a space never written to was left unnamed")
		}
	})
}

// TestEveryPaneClosedInAPassLeavesTheListing encodes the rule four bugs broke.
//
// A reconcile pass works from one pane listing, taken at the start. Closing a
// pane during that pass makes the listing untrue, and everything after it goes
// on reading it: a closed pane that is still in there is alive as far as the
// rest of the pass can tell, and belongs to nobody -- which is the description
// of a pane somebody opened by hand in a machine's space. Those get moved onto
// the machine. So a pane this closed came back as a new terminal on the far
// end, three separate times, by three different routes.
//
// The fourth was the same mistake made with the fix for the first three: the
// pane was struck from the list of live panes and left named as the pane to
// split from, so the replacement mirror was opened beside the pane it was
// replacing, a moment after that pane had been closed. Taking a pane out of the
// listing means taking it out of all of it, which is what index.remove is for.
//
// Checked in the source because it cannot be checked anywhere else: the mistake
// is a line that is not there, in a function that compiles and runs perfectly
// well without it.
func TestEveryPaneClosedInAPassLeavesTheListing(t *testing.T) {
	source, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(source), "\n")

	// Which functions have the pass's listing in hand. Outside those, closing a
	// pane has no listing to correct.
	inPass := false
	closes := 0
	for i, line := range lines {
		if strings.HasPrefix(line, "func ") {
			inPass = strings.Contains(line, "index *paneIndex")
		}
		if !inPass {
			continue
		}
		if !strings.Contains(line, "herdrcli.ClosePane(") && !strings.Contains(line, "herdrcli.ClosePaneByID(") {
			continue
		}
		closes++

		// The correction, within the few lines that handle this close.
		found := false
		for j := i; j < len(lines) && j < i+12; j++ {
			if strings.Contains(lines[j], "index.remove(") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("daemon.go:%d closes a pane and leaves it in the listing:\n  %s\n"+
				"Add index.remove(...) beside it -- not delete(index.alive, ...), which "+
				"leaves the pane named as somewhere to split from. Everything later in the "+
				"pass reads that listing: a closed pane still in it looks like somebody "+
				"else's pane sitting in a machine's space, and one still named as a target "+
				"gets split from after it is gone.",
				i+1, strings.TrimSpace(line))
		}
	}

	if closes < 4 {
		t.Fatalf("only %d closes found inside a pass; this is not reaching the code", closes)
	}
}

func TestSummarisingAFailureAlwaysSaysSomething(t *testing.T) {
	// The summary is what the listing and the menu show for a machine that is
	// not working, so an empty one is the worst answer available: it reads as
	// a machine with nothing wrong.
	for _, tt := range []struct {
		name string
		err  string
		want string
	}{
		{"an ordinary one line failure", "no route to that machine", "no route to that machine"},
		{"the first line of several", "cannot reach it\nand here is why", "cannot reach it"},
		{"one that opens with a blank line", "\n\ncannot reach it\nmore", "cannot reach it"},
		{"one that is only blank lines", "\n\n  \n", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeError(errors.New(tt.err))
			if got != tt.want {
				t.Errorf("summarizeError(%q) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}

	// And nothing at all is still nothing, rather than a panic or a stray line.
	if got := summarizeError(nil); got != "" {
		t.Errorf("summarizeError(nil) = %q", got)
	}
}

func TestATerminalIsNeverLeftWithoutAName(t *testing.T) {
	// A terminal's name comes from whatever is running on the far machine, and
	// is made safe to draw before it reaches the sidebar. One made only of
	// things that cannot be drawn is left with nothing at all, and the terminal
	// arrives called "@bot" -- which says which machine it is on and nothing
	// else, so two of them cannot be told apart.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	d := New(machineConfig("bot"))

	for _, tt := range []struct{ what, name string }{
		{"a name of control characters", "\x01\x02\x03"},
		{"a bell and nothing else", "\a"},
		{"nothing at all", ""},
		{"only whitespace", "   \t "},
	} {
		t.Run(tt.what, func(t *testing.T) {
			label := d.label(config.Host{Target: "bot"}, herdrcli.Pane{PaneID: "w4A:p2"}, tt.name)
			if label == "@bot" || !strings.Contains(label, "@bot") {
				t.Fatalf("label is %q, which names no terminal", label)
			}
			if !strings.Contains(label, "w4A:p2") {
				t.Errorf("label is %q; with no name to use it should fall back to the pane", label)
			}
		})
	}

	// A name that survives being made safe is used as it is.
	if got := d.label(config.Host{Target: "bot"}, herdrcli.Pane{PaneID: "w4A:p2"}, "build"); got != "build@bot" {
		t.Errorf("an ordinary name came out %q, want build@bot", got)
	}
}

func TestAPermissionRefusalIsSshsOwnAndNotTheMachinesOutput(t *testing.T) {
	// What gets classified is the whole failure text, and that carries whatever
	// the command on the machine printed. "Permission denied" on its own is a
	// phrase any Unix prints -- a file mode, a directory somebody cannot read --
	// and reading one of those as a refused key is the worst mistake available
	// here: it is settled, so the machine is given up on for good, and the
	// advice sends somebody to look at an ssh key that was never the problem.
	//
	// ssh says it with the methods it tried in brackets, or asks again.
	for _, ssh := range []string{
		"deploy@bot: Permission denied (publickey).",
		"Permission denied (publickey,keyboard-interactive).",
		"Permission denied, please try again.",
	} {
		err := errors.New(ssh)
		known, ok := classify(err)
		if !ok || !known.settled {
			t.Errorf("%q is ssh refusing a key and is not read as one", ssh)
		}
		if !planGiveUp(0, err) {
			t.Errorf("%q would be tried again, and the next attempt fails the same way", ssh)
		}
	}

	// Anything else wearing those words is the machine talking, and the machine
	// talking is not a reason to stop trying it.
	for _, theirs := range []string{
		"bot: bash: /opt/herdr/bin/herdr: Permission denied",
		"bot: herdr: open /home/deploy/.local/state/herdr: Permission denied",
		"bot: cat: /etc/shadow: Permission denied",
	} {
		if planGiveUp(0, errors.New(theirs)) {
			t.Errorf("%q is the machine reporting a file it cannot read, and this "+
				"gives up on the machine for good over it", theirs)
		}
	}
}

func TestAMachineWithoutHerdrIsRecognisedFromTheErrorItself(t *testing.T) {
	// The one cause in the table this plugin writes rather than reads off ssh.
	// It used to be spelled out twice -- once where the error is made, once as
	// the text to look for -- and a needle that stops matching does not fail,
	// it just quietly stops recognising the thing it was for.
	//
	// What that costs: a machine without Herdr is asked again, which cannot
	// come good, and the answer it gives is the raw error instead of the
	// sentence that says mirroring has fallen back to plain SSH.
	err := fmt.Errorf("bot: %w", remote.ErrNoHerdr)

	known, ok := classify(err)
	if !ok {
		t.Fatalf("%v is not recognised, though it is this plugin's own error", err)
	}
	if !known.settled {
		t.Error("a machine without Herdr would be asked again, and the answer cannot change")
	}
	if got := summarizeError(err); got != "herdr not found on the machine" {
		t.Errorf("summarizeError(%v) = %q, want the sentence about falling back", err, got)
	}
}

func TestLeftoverPanesAreClosedWithoutAskingWhereTheSpaceIs(t *testing.T) {
	// Panes wearing a machine's name with no mirror behind them are husks a
	// Herdr restart left in its space. Nothing had ever called the thing that
	// clears them, so the lookup at the top of it -- insurance for a machine
	// that got here without a workspace id -- could be turned inside out: the
	// space is known, the lookup runs anyway, and if it comes back empty every
	// husk is left where it is.
	held := withFakeHerdr(t)
	d := withConfig(&Daemon{}, config.Defaults())

	state := newTestHost()
	state.host = config.Host{Target: "bot"}
	state.workspaceID = "w1"
	// One husk, and one pane this machine is still tracking. Only the husk
	// should go.
	state.mirrors["term-1"] = "w1:p2"
	index := newPaneIndex([]herdrcli.Pane{
		{PaneID: "w1:p1", WorkspaceID: "w1", Label: "shell@bot"},
		{PaneID: "w1:p2", WorkspaceID: "w1", Label: "work@bot"},
	})

	d.closeOrphans(state, index)

	if index.alive["w1:p1"] {
		t.Error("a husk wearing the machine's name was left in its space")
	}
	if !index.alive["w1:p2"] {
		t.Error("a pane the machine is still tracking was closed as a husk")
	}
	if n := held().Calls["workspace list"]; n != 0 {
		t.Errorf("clearing a known space asked Herdr where it was %d times", n)
	}
}

func TestAStartingDaemonWaitsForTheOneItIsReplacing(t *testing.T) {
	// Restarting Herdr starts the new daemon before the old one has finished.
	// They overlap, and for that moment the socket belongs to the old one.
	//
	// Refusing there is fatal rather than cautious: Herdr does not retry a
	// startup command that exited, so the new daemon is gone for good, and
	// when the old one stops a moment later there is nothing left serving.
	// Every action then says there is no daemon — which is true, and cannot be
	// fixed without restarting Herdr again.
	socket := testSocket(t)

	old, err := listenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	// The old daemon goes shortly after the new one starts, which is the whole
	// of what this is about.
	go func() {
		time.Sleep(300 * time.Millisecond)
		old.Close()
	}()

	started := time.Now()
	listener, err := listenControl(socket)
	if err != nil {
		t.Fatalf("the replacing daemon gave up instead of waiting: %v", err)
	}
	defer listener.Close()

	if waited := time.Since(started); waited < 200*time.Millisecond {
		t.Errorf("it took the socket after %s, which is sooner than the old daemon "+
			"let go of it — two daemons would each answer half the commands", waited)
	}
}

func TestADaemonThatIsReallyAlreadyRunningIsStillRefused(t *testing.T) {
	// The other side of it. Something is serving and stays serving, so this
	// one has nothing to do and says so rather than sitting there for ever.
	socket := testSocket(t)

	serving, err := listenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer serving.Close()

	// Shortened, or this test waits out the real handover window.
	restore := handoverWait
	handoverWait = 300 * time.Millisecond
	defer func() { handoverWait = restore }()

	if _, err := listenControl(socket); err == nil {
		t.Error("a second daemon took a socket that was being served")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Errorf("refused with %q, which does not say a daemon is already there", err)
	}

	// And the daemon that was serving still is. Refusing is not enough on its
	// own: the way this went wrong was asking repeatedly whether anybody was
	// there, filling the backlog of a daemon that was not accepting fast
	// enough, reading the refused connection as a daemon that had gone, and
	// taking the socket from under it.
	accepted := make(chan error, 1)
	go func() {
		conn, err := serving.Accept()
		if err == nil {
			conn.Close()
		}
		accepted <- err
	}()
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		t.Fatalf("the daemon that was serving can no longer be reached: %v", err)
	}
	conn.Close()
	select {
	case err := <-accepted:
		if err != nil {
			t.Errorf("the serving daemon could not accept afterwards: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("the connection went somewhere other than the daemon that was serving")
	}
}

func TestWhetherAnyoneIsServingIsAskedOnce(t *testing.T) {
	// The bug this is for: a daemon that is busy is not accepting, so every
	// connection made to ask whether it is there stays queued in its backlog.
	// Ask on a timer and the backlog fills — 128 of them, at four a second, is
	// half a minute — and the next ask is refused, which reads exactly like a
	// daemon that has gone. The socket is then taken from a daemon that was
	// answering a moment before.
	//
	// It does not reproduce on every platform: Linux dequeues differently and
	// only the macOS runner ever showed it. So what is held is the shape of
	// the thing rather than its symptom — asked once, whatever the answer.
	socket := testSocket(t)

	serving, err := listenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer serving.Close()

	asked := 0
	restoreDial := dialControl
	dialControl = func(s string) (net.Conn, error) {
		asked++
		return restoreDial(s)
	}
	defer func() { dialControl = restoreDial }()

	restoreWait := handoverWait
	handoverWait = 700 * time.Millisecond
	defer func() { handoverWait = restoreWait }()

	if _, err := listenControl(socket); err == nil {
		t.Fatal("a second daemon took a socket that was being served")
	}
	if asked != 1 {
		t.Errorf("asked whether anyone was serving %d times over the wait; once is "+
			"the answer, and asking again is what fills the backlog", asked)
	}
}

func TestStoppingLeavesNoSocketBehind(t *testing.T) {
	// The daemon has to let go of the socket when it stops, or the next one
	// finds a file with nothing behind it. Closing the listener is what does
	// that -- a Unix listener unlinks the path it bound -- and it is the only
	// thing that should, because removing the file separately can delete a
	// socket that a replacing daemon has already created at the same path.
	socket := testSocket(t)

	listener, err := listenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("the socket was never created: %v", err)
	}

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket is still there after closing: %v", err)
	}
}

func TestAReplacingDaemonKeepsItsOwnSocket(t *testing.T) {
	// The hazard the removal was: the old daemon lets go, the new one binds,
	// and then the old one's cleanup deletes the file the new one just made.
	// What is left is a daemon listening on a path nothing can reach, which
	// from every action looks exactly like no daemon at all.
	socket := testSocket(t)

	old, err := listenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	replacing, err := listenControl(socket)
	if err != nil {
		t.Fatalf("the replacing daemon could not bind: %v", err)
	}
	defer replacing.Close()

	// Whatever the old one does on its way out, the new one is still reachable.
	old.Close()
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("the replacing daemon's socket is gone: %v", err)
	}
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		t.Fatalf("nothing can reach the replacing daemon: %v", err)
	}
	conn.Close()
}

func TestEverythingRememberedAboutAMachineIsCleanedBySomething(t *testing.T) {
	// forgetTerminals exists because each of these was cleared where it was
	// most obviously needed, which left the ones nobody had thought about
	// growing for as long as the daemon ran. Its comment says gathering them
	// in one place "is the only way this stays true when the next one is
	// added" -- and the test beside it names all six by hand, so a seventh
	// would be added, never cleaned, and never noticed.
	//
	// This one asks the type instead. A new one has to be put in one of these
	// lists, which is the moment somebody decides how it gets cleaned.
	//
	// Maps and slices alike. It asked about maps only, and a placement kept
	// per pane was moved from a map to a slice the same day -- the order was
	// needed -- which took it out of here without anything saying so. What is
	// remembered about a machine has to be forgotten about it; the shape it is
	// remembered in has nothing to do with that.
	perPane := map[string]bool{} // by forgetPane, when its pane goes
	for _, name := range perPaneFields {
		perPane[name] = true
	}
	// The same list the test above fills and checks, so a map named here is a
	// map something actually calls forgetTerminals with. A second copy of the
	// list is what let placement be named in one and missing from the other.
	perTerminal := map[string]bool{} // by forgetTerminals, when its terminal goes
	for _, name := range perTerminalFields {
		perTerminal[name] = true
	}
	// mirrors is the record of what is mirrored rather than something
	// remembered about it: an entry is removed as the mirror is.
	theRecord := map[string]bool{"mirrors": true}
	// Filled and emptied inside one piece of work rather than outliving it:
	// strays are gathered in a pass and drained at the end of it, and the
	// placements to restore are spent as the terminals come back and dropped
	// with the count they belong to. Nothing else has to clean these, but
	// something has to say that -- which is the point of naming them here.
	spentAsItGoes := map[string]bool{"strays": true, "restoreShellsAs": true}

	shape := reflect.TypeOf(hostSync{})
	found := 0
	for i := 0; i < shape.NumField(); i++ {
		field := shape.Field(i)
		if field.Type.Kind() != reflect.Map && field.Type.Kind() != reflect.Slice {
			continue
		}
		found++
		if !perPane[field.Name] && !perTerminal[field.Name] &&
			!theRecord[field.Name] && !spentAsItGoes[field.Name] {
			t.Errorf("hostSync.%s is remembered about a machine and nothing here says "+
				"what clears it; add it to forgetTerminals or forgetPane, and to the "+
				"list above", field.Name)
		}
	}
	if found < 14 {
		t.Fatalf("found %d maps on hostSync, which is fewer than there are -- this "+
			"test has stopped looking at the type", found)
	}

	// That each of the per-terminal ones is actually cleared is the test above,
	// which populates them and calls forgetTerminals. This is only about a map
	// that appears later and is in neither place -- the failure that one
	// cannot see, because a test naming six fields is happy with a seventh it
	// has never heard of.
}

func TestFollowingOnlySplitsBesideAPaneThatIsStillThere(t *testing.T) {
	// The mirrors map holds what was opened, which is not the same as what is
	// there now: a machine's space here is remade in several circumstances and
	// a mirror left in the old one stays in the map. Splitting beside that puts
	// the terminal in a space the machine no longer uses, on its own, where
	// nothing will ever join it.
	mirrors := map[string]string{
		"term_gone":  "w1:p1", // its pane went
		"term_stale": "w2:p2", // in the space this machine used to use
		"term_here":  "w3:p3",
	}
	remoteTabOf := map[string]string{
		"term_gone": "t1", "term_stale": "t1", "term_here": "t1",
	}
	usable := func(paneID string) bool { return paneID == "w3:p3" }

	if got := planFollowSibling(mirrors, remoteTabOf, "t1", usable); got != "w3:p3" {
		t.Errorf("split beside %q, want the one that is alive and in this space", got)
	}

	// None of them usable is the same as none of them existing: a tab of its
	// own, which is where the machine has it.
	none := func(string) bool { return false }
	if got := planFollowSibling(mirrors, remoteTabOf, "t1", none); got != "" {
		t.Errorf("split beside %q when nothing was usable, want a tab of its own", got)
	}
}

func TestFollowingIsSteadyBetweenPasses(t *testing.T) {
	// Any pane in the tab lands the terminal in the same tab, so which one is
	// chosen decides only where the divider goes -- but map order is not order,
	// and a layout that comes out differently on each pass would be its own
	// report.
	mirrors := map[string]string{
		"a": "w1:p9", "b": "w1:p3", "c": "w1:p7", "d": "w1:p5",
	}
	remoteTabOf := map[string]string{"a": "t1", "b": "t1", "c": "t1", "d": "t1"}
	always := func(string) bool { return true }

	first := planFollowSibling(mirrors, remoteTabOf, "t1", always)
	for i := 0; i < 50; i++ {
		if got := planFollowSibling(mirrors, remoteTabOf, "t1", always); got != first {
			t.Fatalf("two passes over the same panes chose %q and %q", first, got)
		}
	}
	if first != "w1:p3" {
		t.Errorf("chose %q, want the lowest so it is the same every time", first)
	}
}

func TestFollowingATabTheMachineDoesNotName(t *testing.T) {
	// A terminal the machine reports without a tab has nothing to be beside.
	// Reading that as "matches every terminal whose tab is also unknown" would
	// gather unrelated terminals into one tab.
	mirrors := map[string]string{"other": "w1:p1"}
	remoteTabOf := map[string]string{"other": ""}
	always := func(string) bool { return true }

	if got := planFollowSibling(mirrors, remoteTabOf, "", always); got != "" {
		t.Errorf("a terminal with no tab was put beside %q", got)
	}
}

func TestASiblingMustBeInTheSpaceTheMachineIsUsing(t *testing.T) {
	// The decision the daemon actually makes, rather than the function it
	// hands it to. Taking the space out of this left every test in the package
	// passing -- the pure part was held, and what the pure part was asked was
	// not.
	state := newTestHost()
	state.mirrors = map[string]string{
		"term_old":  "wOld:p1", // a mirror left in the space this machine used to use
		"term_here": "wNow:p2",
	}
	remoteTabOf := map[string]string{"term_old": "t1", "term_here": "t1"}

	index := newPaneIndex([]herdrcli.Pane{
		{PaneID: "wOld:p1", WorkspaceID: "wOld"},
		{PaneID: "wNow:p2", WorkspaceID: "wNow"},
	})

	if got := followSibling(state, remoteTabOf, "t1", "wNow", index); got != "wNow:p2" {
		t.Errorf("opened beside %q, want the mirror in the space being used", got)
	}

	// And with only the stale one to go on, a tab of its own beats joining a
	// space the machine has left.
	state.mirrors = map[string]string{"term_old": "wOld:p1"}
	if got := followSibling(state, remoteTabOf, "t1", "wNow", index); got != "" {
		t.Errorf("opened beside %q, which is in a space this machine no longer "+
			"uses; want a tab of its own", got)
	}
}

func TestNothingNewTalksToHerdrHoldingTheDaemonsLock(t *testing.T) {
	// The daemon answers the menu, the status listing and every command on
	// d.mu. A call to Herdr is a subprocess and a call to a machine is an SSH
	// round trip, so one made with the lock in hand stops the daemon answering
	// for as long as it takes -- which is how a machine having trouble becomes
	// a menu that will not open. Measured at 5.7s behind one slow machine.
	//
	// Three do it deliberately and say so where they are. This is here so that
	// a fourth is a decision somebody makes rather than one that arrives.
	//
	// Direct calls only. A locked function calling something that reaches
	// Herdr two frames down is not found by reading one function at a time,
	// and saying so is better than implying a guarantee this does not give.
	deliberate := map[string]string{
		// Taking the lock only long enough to find the machine and then
		// working on it unlocked raced the reconcile pass, which reads and
		// writes the same bookkeeping.
		"openRemotePane":       "held across the work rather than the lookup",
		"ensureRemotePresence": "as openRemotePane: held across the work rather than the lookup",
		"focusHost":            "reads the space the machine is using while deciding to focus it",
	}

	source, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(source), "\n")

	talks := regexp.MustCompile(`herdrcli\.\w+\(|\.client\.Run\(`)
	fn, held, found := "", 0, 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "func ") {
			fn, held = functionName(line), 0
		}
		switch {
		case strings.Contains(trimmed, "d.mu.Lock()"), strings.Contains(trimmed, "d.mu.RLock()"):
			held++
		// A deferred unlock does not unlock here: it unlocks when the function
		// returns, so everything below it still holds the lock. Counting it
		// here reported no such calls anywhere, which read as a clean answer
		// rather than as a broken question -- and the floor below is what
		// turns that back into a failure.
		case strings.HasPrefix(trimmed, "d.mu.Unlock()"), strings.HasPrefix(trimmed, "d.mu.RUnlock()"):
			if held > 0 {
				held--
			}
		}
		if held == 0 || !talks.MatchString(trimmed) {
			continue
		}
		found++
		if _, ok := deliberate[fn]; !ok {
			t.Errorf("daemon.go:%d talks to Herdr or a machine with d.mu held, in %s:\n  %s\n"+
				"The daemon answers the menu on that lock. Do the call without it, or "+
				"name %s above with why it has to be this way.", i+1, fn, trimmed, fn)
		}
	}

	if found < 3 {
		t.Fatalf("found %d calls under the lock, which is fewer than there are -- "+
			"this has stopped matching", found)
	}
}

// functionName is the name out of a top-level func line, method or not.
func functionName(line string) string {
	name := strings.TrimPrefix(line, "func ")
	if strings.HasPrefix(name, "(") {
		if i := strings.Index(name, ") "); i >= 0 {
			name = name[i+2:]
		}
	}
	if i := strings.Index(name, "("); i >= 0 {
		name = name[:i]
	}
	return strings.TrimSpace(name)
}

func TestEverythingTheDaemonRemembersIsCleanedBySomething(t *testing.T) {
	// The same question as the one about a machine, asked of the daemon. Three
	// of these are keyed by a pane or a space, which come and go for as long as
	// the daemon runs: one nobody cleans grows until Herdr does, and nothing
	// says so because a leak is not a failure.
	//
	// Asking the type rather than naming them, because a list written here is
	// a list that stops being complete -- which is how the one about a machine
	// came to miss a field the same day it was added.
	perPane := map[string]bool{ // keyed by a pane, and cleaned when it goes
		"seenStray": true,
	}
	// The list the test on forgetWorkspace fills and checks. It said both of
	// these were "cleaned when it goes" while rootPanes was cleaned only where
	// a placeholder was deliberately retired -- a claim in a list nothing was
	// holding to.
	perWorkspace := map[string]bool{} // keyed by a space, and cleaned when it goes
	for _, name := range perWorkspaceFields {
		perWorkspace[name] = true
	}
	// hosts is the record of what is connected rather than something
	// remembered about it, and an entry goes when the machine is disconnected.
	theRecord := map[string]bool{"hosts": true}
	// touched is every machine this daemon has connected. Never cleaned, and
	// it does not need to be: it is keyed by a machine, and there are as many
	// of those as somebody has written down. Persisting reads it to tell a
	// machine it has dealt with from one it has not reached.
	bounded := map[string]bool{"touched": true}
	// lastSaved is the bytes last written, held to avoid writing them again.
	// One value, replaced rather than added to.
	notACollection := map[string]bool{"lastSaved": true}
	// unclosed is drained by retryUnclosed, which is the only thing that reads
	// it: an entry goes when the pane is closed or has left the listing. Keyed
	// by a pane and still not perPane above, because forgetPane cannot be what
	// clears it -- every site that records a refusal calls forgetPane straight
	// afterwards, having concluded the pane is gone, which is the conclusion
	// the entry exists to contradict.
	drainedByItsOwnWork := map[string]bool{"unclosed": true}

	shape := reflect.TypeOf(Daemon{})
	found := 0
	for i := 0; i < shape.NumField(); i++ {
		field := shape.Field(i)
		if field.Type.Kind() != reflect.Map && field.Type.Kind() != reflect.Slice {
			continue
		}
		found++
		if !perPane[field.Name] && !perWorkspace[field.Name] && !theRecord[field.Name] &&
			!bounded[field.Name] && !notACollection[field.Name] &&
			!drainedByItsOwnWork[field.Name] {
			t.Errorf("Daemon.%s is remembered for as long as the daemon runs and "+
				"nothing here says what clears it, or why it needs no clearing; "+
				"add it to one of the lists above and to whatever forgets it",
				field.Name)
		}
	}
	if found < 7 {
		t.Fatalf("found %d things the daemon remembers, which is fewer than there "+
			"are -- this is checking nothing", found)
	}
}
