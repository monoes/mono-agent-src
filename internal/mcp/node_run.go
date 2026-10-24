package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

func nodeRunTool() tool {
	return tool{
		name: "node_run",
		description: "Execute one node type directly with a config and input items, as `monoagentcli node run`, and return its outputs by handle (credential fields redacted). " +
			"Credentials resolve from the profile's connections (credential, else config.credential_id, else the node type's platform). " +
			"Browser and social nodes need a browser session, which this server does not open: they return that error, and run through the CLI or the daemon instead.",
		schema: objSchema(map[string]interface{}{
			"type":       strParam("Node type, e.g. http.request (node_list lists them)"),
			"config":     map[string]interface{}{"type": "object", "description": "Node config (node_schema describes it)"},
			"input":      map[string]interface{}{"description": "Input items: an array of {\"json\":{...}} objects, or one plain object (default: one empty item)"},
			"credential": strParam("Connection ID or platform name to take credentials from (optional)"),
		}, "type"),
		annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": true},
		mutating:    true,
		handler:     toolNodeRun,
	}
}

// toolNodeRun is `node run` (cmd/monoagentcli/node.go) without the browser:
// the same guard first (an outbound send an agent starts is queued for
// approval, internal/hilsend), the same credential resolution, the vault in
// the context, and publication tracking after.
func toolNodeRun(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Type       string                 `json:"type"`
		Config     map[string]interface{} `json:"config"`
		Input      json.RawMessage        `json:"input"`
		Credential string                 `json:"credential"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if a.Type == "" {
		return nil, fmt.Errorf("type is required")
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	// The engine's setup is what makes a node runnable here: the registry
	// with the DB (AI nodes, the send guard) and the global credential and
	// config stores.
	if _, err := rt.ensureEngine(ctx, profileOf(ctx)); err != nil {
		return nil, err
	}
	factory, ok := rt.registryOrDefault().Get(a.Type)
	if !ok {
		return nil, unknownNodeType(rt, a.Type)
	}
	config := a.Config
	if config == nil {
		config = map[string]interface{}{}
	}
	cred, err := resolveNodeCredential(ctx, rt, a.Type, a.Credential, config)
	if err != nil {
		return nil, err
	}
	// An AI wrote the config: the connection's secret goes only to the
	// hosts the connection names (workflow.CheckCredentialDestinations).
	if err := workflow.CheckCredentialDestinations(factory(), cred, false, config, config); err != nil {
		return nil, fmt.Errorf("node %s: %w", a.Type, err)
	}
	items, err := parseInputItems(a.Input)
	if err != nil {
		return nil, err
	}

	ctx = vault.ContextWithProfileID(vault.ContextWithDB(ctx, rt.db.DB), profileOf(ctx))
	// An AI wrote this config, as it writes a workflow the user never
	// approved: the node runs as in such a workflow (no programs on this
	// computer, no hidden files in the user's home outside the project
	// folder, approvals for what needs the user's).
	ctx = workflow.WithUnapprovedStep(ctx, profiledir.Root(rt.db.DB, profileOf(ctx)), "node_run")
	if queued, handled, gerr := workflow.RunExecGuard(ctx, a.Type, workflow.NodeInput{Items: items}, config); handled {
		if gerr != nil {
			return nil, fmt.Errorf("node %s failed: %w", a.Type, gerr)
		}
		return renderOutputs(queued, workflow.NewReplyRedactor(ctx, rt.db.DB)), nil
	}
	input := workflow.NodeInput{
		Items:       items,
		NodeOutputs: map[string][]workflow.Item{},
		WorkflowID:  "mcp",
		ExecutionID: "mcp-" + uuid.NewString(),
		NodeID:      "mcp-node",
		NodeName:    a.Type,
	}
	ctx = secrets.WithAccessNodeType(secrets.WithAccessNode(ctx, input.NodeID, input.NodeName), a.Type)
	outputs, err := factory().Execute(ctx, input, config)
	outputs = workflow.MaskAgentNodeOutputs(ctx, outputs)
	err = workflow.MaskAgentNodeError(ctx, err)
	recordPublications(ctx, rt, a.Type, config, input, outputs)
	if errors.Is(err, nodes.ErrUserOnlyBrowserRun) {
		return nil, fmt.Errorf("operator_only: %w", err)
	}
	if errors.Is(err, workflow.ErrUnapprovedProgram) {
		return nil, fmt.Errorf("node %s runs a program on this computer, which node_run never does for an AI: "+
			"put the step in a workflow and have the user save or activate it in the app or their own terminal", a.Type)
	}
	// An agent reaches this server: reply tokens in the node's outputs or
	// error are hidden from it (workflow.ReplyRedactor).
	rr := workflow.NewReplyRedactor(ctx, rt.db.DB, input.ExecutionID)
	if err != nil {
		if msg := rr.String(err.Error()); msg != err.Error() {
			return nil, fmt.Errorf("node %s failed: %s", a.Type, msg)
		}
		return nil, fmt.Errorf("node %s failed: %w", a.Type, err)
	}
	if a.Type == "vault.secret_get" {
		// This node explicitly returns plaintext under the profile's Reveal
		// grant; key-name credential masking would hide the requested value.
		name, _ := config["name"].(string)
		allowed, err := secrets.CanAgentReveal(ctx, rt.db.DB, profileOf(ctx), name)
		if err != nil || !allowed {
			return nil, fmt.Errorf("vault reveal access is no longer available")
		}
		out := map[string]interface{}{}
		for _, o := range outputs {
			out[o.Handle] = rr.Items(workflow.TruncateItems(o.Items))
		}
		return map[string]interface{}{"outputs": out}, nil
	}
	return renderOutputs(outputs, rr), nil
}

func unknownNodeType(rt *runtime, nodeType string) error {
	all := rt.registryOrDefault().Types()
	sort.Strings(all)
	var near []string
	for _, t := range all {
		if strings.Contains(t, nodeType) || strings.Contains(nodeType, t) {
			near = append(near, t)
		}
	}
	if len(near) > 0 {
		return fmt.Errorf("unknown node type %q; did you mean one of: %s", nodeType, strings.Join(near, ", "))
	}
	return fmt.Errorf("unknown node type %q: node_list lists them", nodeType)
}

// parseInputItems takes an array of items or one plain object; nothing is
// one empty item, which most nodes need.
func parseInputItems(raw json.RawMessage) ([]workflow.Item, error) {
	var items []workflow.Item
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &items); err != nil {
			var single map[string]interface{}
			if err2 := json.Unmarshal(raw, &single); err2 != nil {
				return nil, fmt.Errorf("invalid input (expected an array of items or one object): %w", err)
			}
			items = []workflow.Item{{JSON: single}}
		}
	}
	if len(items) == 0 {
		items = []workflow.Item{{JSON: map[string]interface{}{}}}
	}
	return items, nil
}

// renderOutputs maps each handle to its items, redacted and size-capped as
// every node output this server returns is, with rr's reply tokens hidden.
func renderOutputs(outputs []workflow.NodeOutput, rr *workflow.ReplyRedactor) map[string]interface{} {
	out := map[string]interface{}{}
	for _, o := range outputs {
		items := rr.Items(workflow.RedactAndTruncateItems(o.Items))
		if items == nil {
			items = []workflow.Item{}
		}
		out[o.Handle] = items
	}
	return map[string]interface{}{"outputs": out}
}

func recordPublications(ctx context.Context, rt *runtime, nodeType string, config map[string]interface{}, input workflow.NodeInput, outputs []workflow.NodeOutput) {
	pub := make([]publication.Output, 0, len(outputs))
	for _, o := range outputs {
		items := make([]map[string]interface{}, 0, len(o.Items))
		for _, it := range o.Items {
			items = append(items, it.JSON)
		}
		pub = append(pub, publication.Output{Handle: o.Handle, Items: items})
	}
	entries := publication.Normalize(publication.CaptureInput{NodeType: nodeType, Config: config, Outputs: pub,
		Source: workflow.PublicationSource(ctx, input)})
	hilsend.Attribute(ctx, rt.db.DB, profileOf(ctx), entries)
	publication.Record(ctx, publication.NewStore(rt.db.DB, profileOf(ctx)), entries, func(msg string) { fmt.Fprintln(os.Stderr, "mcp: warning:", msg) })
}

// platformOverrides maps the node types whose platform is not their suffix.
var platformOverrides = map[string]string{
	"db.postgres": "postgresql", "db.mysql": "mysql", "db.mongodb": "mongodb", "db.redis": "redis",
}

// nodeTypePlatform is the connections platform of a node type
// ("service.google_sheets" → "google_sheets").
func nodeTypePlatform(nodeType string) string {
	if p, ok := platformOverrides[nodeType]; ok {
		return p
	}
	for _, prefix := range []string{"service.", "comm.", "db."} {
		if strings.HasPrefix(nodeType, prefix) {
			return strings.TrimPrefix(nodeType, prefix)
		}
	}
	return nodeType
}

// resolveNodeCredential merges a connection's data into config, as `node
// run` does: an explicit credential that cannot be resolved is an error, a
// derived one that cannot is not (the config may carry its own).
func resolveNodeCredential(ctx context.Context, rt *runtime, nodeType, explicit string, config map[string]interface{}) (*workflow.CredentialUse, error) {
	key := explicit
	if key == "" {
		if cid, ok := config["credential_id"].(string); ok && cid != "" {
			key = cid
		}
	}
	if key == "" {
		key = nodeTypePlatform(nodeType)
	}
	conn, err := credentialData(ctx, connections.NewStore(rt.db.DB), key, profileOf(ctx))
	if err != nil || conn == nil {
		if explicit != "" {
			return nil, fmt.Errorf("credential resolution failed: %w", err)
		}
		return nil, nil
	}
	for k, v := range conn.Data {
		if _, exists := config[k]; !exists {
			config[k] = v
		}
	}
	config["credential"] = conn.Data
	// A send's card keeps no credentials: pinned by id, its approval sends
	// through this connection, not whichever the name picks then.
	config["credential_id"] = conn.ID
	name := conn.Label
	if name == "" {
		name = key
	}
	return &workflow.CredentialUse{Name: name, Platform: conn.Platform, Data: conn.Data}, nil
}

// credentialData is the connection of profileID found by id, else by
// platform (an active one first), its OAuth token refreshed when it is
// about to expire.
func credentialData(ctx context.Context, store *connections.Store, key, profileID string) (*connections.Connection, error) {
	conn, err := store.Get(ctx, key)
	if err == nil && conn != nil && conn.ProfileID != "" && conn.ProfileID != profileID {
		conn = nil
	}
	if conn == nil {
		if conns, lerr := store.ListByPlatform(ctx, key, profileID); lerr == nil && len(conns) > 0 {
			conn = &conns[0]
			for i := range conns {
				if conns[i].Status == "active" {
					conn = &conns[i]
					break
				}
			}
		}
	}
	if conn == nil {
		return nil, fmt.Errorf("no connection found for %q: connect it with `monoagentcli connect %s`", key, key)
	}
	if exp, _ := conn.Data["expires_at"].(string); exp != "" {
		if at, perr := time.Parse(time.RFC3339, exp); perr == nil && time.Now().UTC().After(at.Add(-60*time.Second)) {
			if rerr := store.RefreshToken(ctx, conn); rerr != nil {
				fmt.Fprintf(os.Stderr, "mcp: warning: token refresh failed, using the existing token: %v\n", rerr)
			}
		}
	}
	return conn, nil
}
