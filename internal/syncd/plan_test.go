package syncd

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

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
			got := planTrackedMirror(tt.adopted, tt.paneAlive, tt.mirrorRunning)
			if got != tt.want {
				t.Errorf("planTrackedMirror(adopted=%v, alive=%v, running=%v) = %v, want %v",
					tt.adopted, tt.paneAlive, tt.mirrorRunning, got, tt.want)
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
	// Numbering from a running total drifts: close the only terminal and the
	// next one is "shell 2" with no "shell 1" anywhere.
	if got := planShellName(0); got != "shell" {
		t.Errorf("first terminal = %q, want %q", got, "shell")
	}
	if got := planShellName(1); got != "shell 2" {
		t.Errorf("second terminal = %q, want %q", got, "shell 2")
	}
	if got := planShellName(3); got != "shell 4" {
		t.Errorf("fourth terminal = %q, want %q", got, "shell 4")
	}
}

func TestPlanGiveUp(t *testing.T) {
	// A machine that answers is never given up on.
	if planGiveUp(0) {
		t.Error("a healthy machine should keep being polled")
	}
	// One failure can be a blip, so it is tried again.
	if planGiveUp(1) {
		t.Error("a single failure should be retried")
	}
	// Some failures never resolve on their own — a changed host key needs
	// someone to look at it — and retrying every couple of seconds burns SSH
	// connections and fills the log.
	if !planGiveUp(2) {
		t.Error("a machine that failed twice should be left alone")
	}
	if !planGiveUp(20) {
		t.Error("a long-failing machine should stay left alone")
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
		"bot: no herdr on the remote host":     "herdr not found on the machine",

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
	if got := planTrackedMirrorFor(false, true, true, "t1", "t5"); got != mirrorReplace {
		t.Errorf("a pane mirroring another terminal should be replaced, got %v", got)
	}

	// The same terminal is the mirror the record meant.
	if got := planTrackedMirrorFor(false, true, true, "t1", "t1"); got != mirrorKeep {
		t.Errorf("a matching mirror should be kept, got %v", got)
	}

	// A mark written before the terminal was recorded is taken at its word,
	// or upgrading would replace every working mirror.
	if got := planTrackedMirrorFor(false, true, true, "t1", ""); got != mirrorKeep {
		t.Errorf("an older mark should be kept, got %v", got)
	}

	// Once running, the identity is not rechecked: a mirror can be between
	// processes without being the wrong one.
	if got := planTrackedMirrorFor(true, true, false, "t1", "t5"); got != mirrorKeep {
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
		seenStray:   map[string]bool{},
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
		seenStray:   map[string]bool{},
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
		seenStray:   map[string]bool{},
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
		seenStray:   map[string]bool{},
	}

	d.planStrayCapture(state, index(pane("w1:p1", "w1", "w1:t1")))
	// The pane goes away.
	d.planStrayCapture(state, index())
	if state.seenStray["w1:p1"] {
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
		seenStray:   map[string]bool{},
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
		seenStray:   map[string]bool{},
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
		got, ok := d.hostConfig(name)
		if !ok || got.Target != "bot.example.com" {
			t.Errorf("hostConfig(%q) = %+v, %v; want the configured machine", name, got, ok)
		}
	}
}

func TestHostConfigAcceptsAnUnconfiguredMachine(t *testing.T) {
	// Everything in ~/.ssh/config is offered in the menu, so connecting must
	// work for a machine that was never written into the plugin's own config.
	d := withConfig(&Daemon{}, config.Defaults())

	got, ok := d.hostConfig("some-laptop")
	if !ok || got.Target != "some-laptop" {
		t.Errorf("hostConfig = %+v, %v; want an ad-hoc machine", got, ok)
	}

	// Nothing at all is still nothing.
	if _, ok := d.hostConfig(""); ok {
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
}

// withConfig sets a daemon's configuration. It is held atomically rather than
// as a plain field, so that a command changing it and a command reading it can
// run at the same time without one seeing half of the other's write.
func withConfig(d *Daemon, cfg config.Config) *Daemon {
	d.setConfig(cfg)
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

	if _, err := listenControl(socket); err == nil {
		t.Error("a second daemon took a socket that was already being served")
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

	for _, count := range []int{0, 1, 5} {
		name := planShellName(count)
		label := d.label(config.Host{Target: "bot"}, herdrcli.Pane{}, name)

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
