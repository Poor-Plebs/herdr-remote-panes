package syncd

import (
	"testing"

	"github.com/Poor-Plebs/herdr-remote-panes/internal/herdrcli"
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
	for _, label := range []string{"bot", "☁ bot", "☁  bot", "🟢 bot"} {
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
