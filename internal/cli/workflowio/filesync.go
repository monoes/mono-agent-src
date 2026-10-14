package workflowio

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// SyncMarkerName sits next to the file store directory (never inside it:
// every *.json there is read as a workflow).
const SyncMarkerName = "workflows-sqlite-sync.json"

// SyncFileWorkflowsToSQL mirrors file-store workflows into SQLite (workflow
// row, nodes, connections) when their SQLite copy is missing or older than
// the file. Readers that only look at SQLite — `workflow run --json` node
// types, executions — then see what the editor saved. A marker records each
// file's modtime+size, so a workflow is re-synced only after its file
// changes: the first call backfills everything, later calls are one stat
// per file. A workflow whose node or connection ids are owned by another
// workflow in SQLite is skipped (an upsert would move them) and retried on
// the next call. Returns the ids synced.
func SyncFileWorkflowsToSQL(ctx context.Context, db *sql.DB, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	markerPath := filepath.Join(filepath.Dir(dir), SyncMarkerName)
	marker := map[string]string{}
	if b, err := os.ReadFile(markerPath); err == nil {
		_ = json.Unmarshal(b, &marker)
	}
	files, err := workflow.NewWorkflowFileStore(dir)
	if err != nil {
		return nil, err
	}
	sqlStore := workflow.NewSQLiteWorkflowStore(db)
	var synced []string
	changed := false
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		stamp := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
		if marker[name] == stamp {
			continue
		}
		wf, err := files.GetWorkflow(ctx, name)
		if err != nil || wf == nil || wf.ID == "" {
			continue
		}
		if err := syncOneWorkflowToSQL(ctx, db, sqlStore, wf); err != nil {
			continue
		}
		marker[name] = stamp
		changed = true
		synced = append(synced, wf.ID)
	}
	if changed {
		if b, err := json.Marshal(marker); err == nil {
			tmp := markerPath + ".tmp"
			if os.WriteFile(tmp, b, 0o600) == nil {
				_ = os.Rename(tmp, markerPath)
			}
		}
	}
	return synced, nil
}

func syncOneWorkflowToSQL(ctx context.Context, db *sql.DB, s *workflow.SQLiteWorkflowStore, wf *workflow.Workflow) error {
	for _, q := range []struct {
		table string
		ids   []string
	}{
		{"workflow_nodes", nodeIDs(wf.Nodes)},
		{"workflow_connections", connIDs(wf.Connections)},
	} {
		if len(q.ids) == 0 {
			continue
		}
		args := []any{wf.ID}
		for _, id := range q.ids {
			args = append(args, id)
		}
		var n int
		stmt := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE workflow_id != ? AND id IN (%s)`, q.table,
			strings.TrimSuffix(strings.Repeat("?,", len(q.ids)), ","))
		if err := db.QueryRowContext(ctx, stmt, args...).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("workflow %s shares %d %s ids with another workflow", wf.ID, n, q.table)
		}
	}
	existing, err := s.GetWorkflow(ctx, wf.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		stub := *wf
		stub.Nodes, stub.Connections = nil, nil
		if err := s.CreateWorkflow(ctx, &stub); err != nil {
			return err
		}
	}
	nodes := append([]workflow.WorkflowNode(nil), wf.Nodes...)
	if err := s.SaveWorkflowNodes(ctx, wf.ID, nodes); err != nil {
		return err
	}
	return s.SaveWorkflowConnections(ctx, wf.ID, wf.Connections)
}

func nodeIDs(ns []workflow.WorkflowNode) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		if n.ID != "" {
			out = append(out, n.ID)
		}
	}
	return out
}

func connIDs(cs []workflow.WorkflowConnection) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if c.ID != "" {
			out = append(out, c.ID)
		}
	}
	return out
}
