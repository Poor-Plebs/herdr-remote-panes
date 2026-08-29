package herdrcli

import (
	"encoding/json"
	"fmt"
)

// The shapes Herdr returns, parsed in one place. They were repeated at every
// call site, which is how a response read from the wrong level went unnoticed:
// plugin.pane.open nests its pane under "plugin_pane", and reading "pane"
// instead yielded an empty id rather than an error.

// Workspace is the subset of Herdr's workspace_info shape this plugin needs.
type Workspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

// ParseWorkspaceList decodes a workspace_list result.
func ParseWorkspaceList(result json.RawMessage) ([]Workspace, error) {
	var body struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return nil, fmt.Errorf("parse workspace list: %w", err)
	}
	if body.Workspaces == nil {
		// The same distinction the pane list draws, for the same reason. No
		// workspaces means this machine's space is not there, and not being
		// there is what makes the plugin create one -- with a terminal in it,
		// on somebody's machine. Herdr sends "workspaces":[] when it has none,
		// so an absent field is a reply this cannot read.
		return nil, fmt.Errorf("parse workspace list: no workspaces field in the reply")
	}
	return body.Workspaces, nil
}

// Created is what Herdr reports after creating a workspace or a tab: the thing
// it made, and the pane that came with it.
type Created struct {
	WorkspaceID string
	TabID       string
	RootPane    Pane
}

// ParseCreated decodes a workspace_created or tab_created result.
func ParseCreated(result json.RawMessage) (Created, error) {
	var body struct {
		Workspace struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspace"`
		Tab struct {
			TabID string `json:"tab_id"`
		} `json:"tab"`
		RootPane Pane `json:"root_pane"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return Created{}, fmt.Errorf("parse create response: %w", err)
	}
	return Created{
		WorkspaceID: body.Workspace.WorkspaceID,
		TabID:       body.Tab.TabID,
		RootPane:    body.RootPane,
	}, nil
}

// ParseTabOrder decodes a tab_list result into tab id to display number, which
// is the order the tabs appear in.
// ParseTabOrder is deliberately not strict about a missing tabs field, unlike
// the two lists above.
//
// What an empty order costs is the order: terminals sort by pane id instead of
// by the tab they are in over there. Refusing would cost the whole pass, since
// a reconcile that returns an error counts against the machine and enough of
// those give up on it. An ordering is worth less than that, so this takes what
// it is given.
func ParseTabOrder(result json.RawMessage) (map[string]int, error) {
	var body struct {
		Tabs []struct {
			TabID  string `json:"tab_id"`
			Number int    `json:"number"`
		} `json:"tabs"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return nil, fmt.Errorf("parse tab list: %w", err)
	}
	order := make(map[string]int, len(body.Tabs))
	for _, tab := range body.Tabs {
		order[tab.TabID] = tab.Number
	}
	return order, nil
}
