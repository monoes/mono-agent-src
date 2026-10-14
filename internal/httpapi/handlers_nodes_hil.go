package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// ── nodes ─────────────────────────────────────────────────────────────────

// nodeCategory mirrors internal/mcp/tools.go's categorization (itself
// mirroring the CLI's node list).
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
	case strings.HasPrefix(t, "ai."), strings.HasPrefix(t, "agent."):
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

// GET /nodes
func (s *Server) handleNodeList(w http.ResponseWriter, r *http.Request) {
	types := s.rt.registryOrDefault().Types()
	sort.Strings(types)
	out := []map[string]interface{}{}
	for _, t := range types {
		out = append(out, map[string]interface{}{
			"type":     t,
			"category": nodeCategory(t),
			"title":    nodeTitle(t),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /nodes/{type}/schema
func (s *Server) handleNodeSchema(w http.ResponseWriter, r *http.Request) {
	nodeType := r.PathValue("type")
	registry := s.rt.registryOrDefault()
	hasSchema := false
	for _, name := range workflow.ListEmbeddedSchemas() {
		if name == nodeType {
			hasSchema = true
			break
		}
	}
	if !registry.Has(nodeType) && !hasSchema {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown node type %q — GET /nodes for all types", nodeType))
		return
	}
	schema, err := workflow.LoadDefaultSchema(nodeType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load schema: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"type": nodeType, "schema": schema})
}

// ── HIL ───────────────────────────────────────────────────────────────────

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

// GET /hil
func (s *Server) handleHilList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rt.db.DB.QueryContext(r.Context(),
		`SELECT h.id, h.execution_id, h.workflow_id, h.node_id, h.node_name, h.status,
		        h.readonly_data, h.editable_data, h.created_at, COALESCE(w.name,'')
		 FROM hil_pending h
		 LEFT JOIN workflows w ON w.id = h.workflow_id
		 WHERE h.status = 'pending' AND h.profile_id = ?
		 ORDER BY h.created_at ASC`,
		s.rt.profileID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query HIL items: "+err.Error())
		return
	}
	defer rows.Close()

	items := []hilItem{}
	for rows.Next() {
		var it hilItem
		var roRaw, edRaw string
		if err := rows.Scan(&it.ID, &it.ExecutionID, &it.WorkflowID, &it.NodeID, &it.NodeName,
			&it.Status, &roRaw, &edRaw, &it.CreatedAt, &it.WorkflowName); err != nil {
			writeError(w, http.StatusInternalServerError, "scan HIL item: "+err.Error())
			return
		}
		_ = json.Unmarshal([]byte(roRaw), &it.ReadonlyData)
		_ = json.Unmarshal([]byte(edRaw), &it.EditableData)
		items = append(items, it)
	}
	writeJSON(w, http.StatusOK, items)
}

// POST /hil/{id}/approve  (mutating)
func (s *Server) handleHilApprove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Data map[string]interface{} `json:"data"`
	}
	raw, err := readLimitedBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	}
	if body.Data == nil {
		body.Data = map[string]interface{}{}
	}
	edited, err := json.Marshal(body.Data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "render data: "+err.Error())
		return
	}
	res, err := s.rt.db.DB.ExecContext(r.Context(),
		`UPDATE hil_pending SET status='approved', edited_data=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='pending' AND profile_id = ?`,
		string(edited), id, s.rt.profileID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "approve HIL item: "+err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, fmt.Sprintf("HIL item %q not found or already resolved", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "approved"})
}

// POST /hil/{id}/reject  (mutating)
func (s *Server) handleHilReject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, err := s.rt.db.DB.ExecContext(r.Context(),
		`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='pending' AND profile_id = ?`,
		id, s.rt.profileID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reject HIL item: "+err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, fmt.Sprintf("HIL item %q not found or already resolved", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "rejected"})
}
