// Package n8nimport converts n8n workflow JSON into mono-agent workflows.
//
// Convert maps every n8n node through a registered Mapper (or a generic
// AppMapper for declarative API packages), translates n8n expressions into
// mono's template language, rebuilds the connection graph (multi-output
// nodes, error outputs, loops) and returns a Report that says, node by
// node, what was mapped fully, partially or not at all.
package n8nimport

import (
	"encoding/json"
	"fmt"
	"strings"
)

// flexString accepts a JSON string or number (older n8n versions used
// numeric ids for workflows and credentials).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexString(n.String())
		return nil
	}
	*f = ""
	return nil
}

// n8nWorkflow is the subset of an n8n workflow export the importer reads.
type n8nWorkflow struct {
	ID          flexString                        `json:"id"`
	Name        string                            `json:"name"`
	Active      bool                              `json:"active"`
	Nodes       []*n8nNode                        `json:"nodes"`
	Connections map[string]map[string][][]*n8nEnd `json:"connections"`
	Settings    map[string]interface{}            `json:"settings"`
	PinData     map[string]interface{}            `json:"pinData"`
	StaticData  interface{}                       `json:"staticData"`
}

// n8nNode is one node of an n8n workflow.
type n8nNode struct {
	ID               flexString                  `json:"id"`
	Name             string                      `json:"name"`
	Type             string                      `json:"type"`
	TypeVersion      float64                     `json:"typeVersion"`
	Position         []float64                   `json:"position"`
	Parameters       map[string]interface{}      `json:"parameters"`
	Credentials      map[string]n8nCredentialRef `json:"credentials"`
	Disabled         bool                        `json:"disabled"`
	Notes            string                      `json:"notes"`
	OnError          string                      `json:"onError"`
	ContinueOnFail   bool                        `json:"continueOnFail"`
	AlwaysOutputData bool                        `json:"alwaysOutputData"`
	ExecuteOnce      bool                        `json:"executeOnce"`
	WebhookID        string                      `json:"webhookId"`
	RetryOnFail      bool                        `json:"retryOnFail"`
	MaxTries         float64                     `json:"maxTries"`
	WaitBetweenTries float64                     `json:"waitBetweenTries"`
}

// n8nCredentialRef is how a node names the credential it uses. Exports
// carry only the id and name, never the secret.
type n8nCredentialRef struct {
	ID   flexString `json:"id"`
	Name string     `json:"name"`
}

// n8nEnd is the target of one n8n connection.
type n8nEnd struct {
	Node  string `json:"node"`
	Type  string `json:"type"`
	Index int    `json:"index"`
}

// parseN8n reads an n8n export: a workflow object, a one-element array of
// them (n8n export:workflow), or an object wrapping it in "data" (the
// public API and some copy/paste formats).
func parseN8n(raw []byte) (*n8nWorkflow, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("parse n8n JSON: %w", err)
		}
		if len(list) != 1 {
			return nil, fmt.Errorf("the file holds %d workflows; export or convert one workflow at a time", len(list))
		}
		raw = list[0]
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("parse n8n JSON: %w", err)
	}
	if _, hasNodes := probe["nodes"]; !hasNodes {
		if data, ok := probe["data"]; ok {
			return parseN8n(data)
		}
		return nil, fmt.Errorf("not an n8n workflow: no \"nodes\" array")
	}
	var wf n8nWorkflow
	if err := json.Unmarshal(raw, &wf); err != nil {
		return nil, fmt.Errorf("parse n8n JSON: %w", err)
	}
	if len(wf.Nodes) == 0 {
		return nil, fmt.Errorf("not an n8n workflow: \"nodes\" is empty")
	}
	seen := map[string]bool{}
	for i, n := range wf.Nodes {
		if n == nil || n.Name == "" || n.Type == "" {
			return nil, fmt.Errorf("n8n node #%d has no name or type", i+1)
		}
		if seen[n.Name] {
			return nil, fmt.Errorf("two n8n nodes are named %q", n.Name)
		}
		seen[n.Name] = true
		if n.Parameters == nil {
			n.Parameters = map[string]interface{}{}
		}
	}
	return &wf, nil
}

// LooksLikeN8n reports whether raw parses as an n8n workflow export: nodes
// whose "type" carries an n8n package prefix and a connections object keyed
// by node name.
func LooksLikeN8n(raw []byte) bool {
	wf, err := parseN8n(raw)
	if err != nil {
		return false
	}
	for _, n := range wf.Nodes {
		if strings.HasPrefix(n.Type, "n8n-nodes-base.") || strings.HasPrefix(n.Type, "@n8n/") {
			return true
		}
	}
	return false
}

// position returns the node's canvas position.
func (n *n8nNode) position() (float64, float64) {
	if len(n.Position) >= 2 {
		return n.Position[0], n.Position[1]
	}
	return 0, 0
}

// shortType strips the package prefix: "n8n-nodes-base.slack" → "slack".
func shortType(t string) string {
	if i := strings.LastIndex(t, "."); i >= 0 {
		return t[i+1:]
	}
	return t
}
