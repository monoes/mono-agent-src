package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// ── node tools ───────────────────────────────────────────────────────────────

// nodeCategory mirrors the CLI's node list categorization.
func nodeCategory(t string) string {
	legacy := map[string]string{
		"mysql": "database", "postgres": "database", "mongodb": "database", "redis": "database",
		"github": "service", "notion": "service", "airtable": "service", "jira": "service",
		"linear": "service", "asana": "service", "stripe": "service", "shopify": "service",
		"salesforce": "service", "hubspot": "service", "google_sheets": "service",
		"gmail": "service", "google_drive": "service",
		"datetime": "data", "crypto": "data", "html": "data", "xml": "data",
		"markdown": "data", "spreadsheet": "data", "compression": "data", "write_binary_file": "data",
	}
	switch {
	case strings.HasPrefix(t, "trigger."):
		return "trigger"
	case strings.HasPrefix(t, "core."), strings.HasPrefix(t, "control."):
		return "control"
	case strings.HasPrefix(t, "http."):
		return "http"
	case strings.HasPrefix(t, "system."):
		return "system"
	case strings.HasPrefix(t, "comm."):
		return "communication"
	case strings.HasPrefix(t, "ai."), strings.HasPrefix(t, "agent."), strings.HasPrefix(t, "browser."):
		return "ai"
	case strings.HasPrefix(t, "instagram."), strings.HasPrefix(t, "linkedin."),
		strings.HasPrefix(t, "x."), strings.HasPrefix(t, "tiktok."),
		strings.HasPrefix(t, "hackernews."), strings.HasPrefix(t, "producthunt."),
		strings.HasPrefix(t, "gemini."):
		return "browser/social"
	case strings.HasPrefix(t, "people."):
		return "people"
	case strings.HasPrefix(t, "db."), strings.HasPrefix(t, "mysql."), strings.HasPrefix(t, "postgres."):
		return "database"
	case strings.HasPrefix(t, "service."):
		return "service"
	case strings.HasPrefix(t, "data."):
		return "data"
	case legacy[t] != "":
		return legacy[t]
	default:
		return "other"
	}
}

// nodeTitle derives a human-friendly title from a node type string
// ("service.google_sheets" -> "Google Sheets").
func nodeTitle(t string) string {
	name := t
	if i := strings.Index(t, "."); i >= 0 {
		name = t[i+1:]
	}
	words := strings.Split(name, "_")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func toolNodeList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Category string `json:"category"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	types := rt.registryOrDefault().Types()
	// Triggers run through the trigger manager, not the registry: list them
	// from their embedded schemas so an agent can find one to start with.
	for _, t := range workflow.ListEmbeddedSchemas() {
		if strings.HasPrefix(t, "trigger.") {
			types = append(types, t)
		}
	}
	sort.Strings(types)
	types = slices.Compact(types)
	out := []map[string]interface{}{}
	for _, t := range types {
		cat := nodeCategory(t)
		if a.Category != "" && cat != a.Category {
			continue
		}
		out = append(out, map[string]interface{}{
			"type":     t,
			"category": cat,
			"title":    nodeTitle(t),
		})
	}
	return out, nil
}

func toolNodeSchema(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Type string `json:"type"`
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
	// Trigger types (trigger.schedule, trigger.webhook, ...) have embedded
	// schemas but no executor in the registry; accept either source.
	registry := rt.registryOrDefault()
	hasSchema := false
	for _, name := range workflow.ListEmbeddedSchemas() {
		if name == a.Type {
			hasSchema = true
			break
		}
	}
	if !registry.Has(a.Type) && !hasSchema {
		return nil, fmt.Errorf("unknown node type %q — use node_list to see all types", a.Type)
	}
	schema, err := workflow.LoadDefaultSchema(a.Type)
	if err != nil {
		return nil, fmt.Errorf("load schema: %w", err)
	}
	return map[string]interface{}{
		"type":   a.Type,
		"schema": schema,
	}, nil
}

// ── Human-in-Loop tools ──────────────────────────────────────────────────────

type hilItem struct {
	ID           string                 `json:"id"`
	ExecutionID  string                 `json:"execution_id"`
	WorkflowID   string                 `json:"workflow_id"`
	WorkflowName string                 `json:"workflow_name"`
	NodeID       string                 `json:"node_id"`
	NodeName     string                 `json:"node_name"`
	Status       string                 `json:"status"`
	ReadonlyData map[string]interface{} `json:"readonly_data"`
	EditableData map[string]interface{} `json:"editable_data"`
	CreatedAt    string                 `json:"created_at"`
}

func toolHilList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	rows, err := rt.db.DB.QueryContext(ctx,
		`SELECT h.id, h.execution_id, h.workflow_id, h.node_id, h.node_name, h.status,
		        h.readonly_data, h.editable_data, h.created_at, COALESCE(w.name,'')
		 FROM hil_pending h
		 LEFT JOIN workflows w ON w.id = h.workflow_id
		 WHERE h.status = 'pending' AND h.profile_id = ?
		 ORDER BY h.created_at ASC`,
		rt.profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("query HIL items: %w", err)
	}
	defer rows.Close()

	items := []hilItem{}
	for rows.Next() {
		var it hilItem
		var roRaw, edRaw string
		if err := rows.Scan(&it.ID, &it.ExecutionID, &it.WorkflowID, &it.NodeID, &it.NodeName,
			&it.Status, &roRaw, &edRaw, &it.CreatedAt, &it.WorkflowName); err != nil {
			return nil, fmt.Errorf("scan HIL item: %w", err)
		}
		_ = json.Unmarshal([]byte(roRaw), &it.ReadonlyData)
		_ = json.Unmarshal([]byte(edRaw), &it.EditableData)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate HIL items: %w", err)
	}
	return items, nil
}

func toolHilApprove(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID   string                 `json:"id"`
		Data map[string]interface{} `json:"data"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if a.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	if a.Data == nil {
		a.Data = map[string]interface{}{}
	}
	edited, err := json.Marshal(a.Data)
	if err != nil {
		return nil, fmt.Errorf("render data: %w", err)
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	res, err := rt.db.DB.ExecContext(ctx,
		`UPDATE hil_pending SET status='approved', edited_data=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='pending' AND profile_id = ?`,
		string(edited), a.ID, rt.profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("approve HIL item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("HIL item %q not found or already resolved", a.ID)
	}
	return map[string]string{"id": a.ID, "status": "approved"}, nil
}

func toolHilReject(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if a.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	res, err := rt.db.DB.ExecContext(ctx,
		`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='pending' AND profile_id = ?`,
		a.ID, rt.profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("reject HIL item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("HIL item %q not found or already resolved", a.ID)
	}
	return map[string]string{"id": a.ID, "status": "rejected"}, nil
}

// ── docs tool ────────────────────────────────────────────────────────────────

func toolDocs(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Topic string `json:"topic"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if a.Topic == "" {
		topics := make([]string, 0, len(docsTopics))
		for t := range docsTopics {
			topics = append(topics, t)
		}
		sort.Strings(topics)
		return map[string]interface{}{
			"topics": topics,
			"hint":   "pass a topic to the docs tool for its content; `monoagentcli ref <topic>` covers the same ground in the CLI",
		}, nil
	}
	content, ok := docsTopics[a.Topic]
	if !ok {
		return nil, fmt.Errorf("unknown topic %q", a.Topic)
	}
	return map[string]string{"topic": a.Topic, "content": content}, nil
}
