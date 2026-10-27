package workflowio

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// SyncMarkerName was the name of the file that remembered each workflow file's modtime and size.
// Nothing reads or writes it any more: what was last reconciled is recorded in the database
// (workflow_file_state), by content, so it cannot go stale between processes.
const SyncMarkerName = "workflows-sqlite-sync.json"

// SyncFileWorkflowsToSQL makes the database rows follow workflow files that were changed outside
// the store (an editor, a script, an older version): a file with no rows gets them, and a file
// whose definition differs from its rows is imported as a new version (workflow.ImportFile; the
// file itself is never rewritten). Readers that only look at SQLite — `workflow run --json` node
// types, executions — then see what the editor saved. What was last reconciled is remembered by
// the file's content, so a file that has not changed costs one read. Changes that another
// process left unfinished are resolved first, and a workflow with a change in progress is left
// for the next call, as is one whose node or connection ids are owned by another workflow in
// SQLite (an upsert would move them). Returns the ids imported.
func SyncFileWorkflowsToSQL(ctx context.Context, db *sql.DB, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files, err := workflow.NewWorkflowFileStore(dir)
	if err != nil {
		return nil, err
	}
	store := workflow.NewHybridWorkflowStore(files, workflow.NewSQLiteWorkflowStore(db))
	_, _ = store.RecoverPublications(ctx)
	store.SweepStaleTemp()
	// Unfinished changes are settled before any file is rewritten (the stamp rewrites legacy files).
	if err := StampFileWorkflows(ctx, db, dir); err != nil {
		return nil, err
	}
	// The files older versions left become workflows now, once; a file with no rows that appears
	// later is not imported by anything (an operator adopts it).
	claimed, err := store.ClaimLegacyFiles(ctx)
	if err != nil {
		return nil, err
	}
	var synced []string
	for _, w := range claimed {
		synced = append(synced, w.ID)
	}
	// A file of a profile that no longer exists (deleted, or copied from one) must not come back
	// as a row of a profile that is gone.
	profileExists := func(wf *workflow.Workflow) bool {
		var n int
		return wf.ProfileID == "" || (db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiles WHERE id = ?`, wf.ProfileID).Scan(&n) == nil && n > 0)
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok {
			continue
		}
		res, err := store.ImportFile(ctx, name, profileExists)
		if err != nil || !res.Imported {
			continue
		}
		// A workflow that was only this file when approvals arrived is
		// approved now that it has a row, if its definition is unchanged.
		if err := workflow.ApproveBackfilledFile(ctx, db, res.Workflow); err != nil {
			continue
		}
		synced = append(synced, res.Workflow.ID)
	}
	return synced, nil
}

// StampFileWorkflows gives workflow files that carry no profile_id the owner
// their SQLite mirror row records, else the owner migration 099 gives
// ownerless database rows: the profile 'default' if it exists, else the
// oldest. It runs ahead of every file-to-SQLite sync (the
// point the CLI opens its workflow store), is idempotent, and does nothing
// when there are no profiles or no files to fix.
func StampFileWorkflows(ctx context.Context, db *sql.DB, dir string) error {
	owner, err := (&storage.Database{DB: db}).LegacyOwnerProfile(ctx)
	if err != nil {
		return err
	}
	rowOwner := func(id string) string {
		var p sql.NullString
		if db.QueryRowContext(ctx, `SELECT profile_id FROM workflows WHERE id = ?`, id).Scan(&p) != nil {
			return ""
		}
		return p.String
	}
	// Before the files older versions left are taken in, all of them may need an owner. After it,
	// only the files of workflows that have rows do: a file with no stored copy is left unread.
	eligible := func(id string) bool {
		if !workflow.LegacyClaimDone(ctx, db) {
			return true
		}
		var n int
		return db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE id = ?`, id).Scan(&n) == nil && n > 0
	}
	stamped, err := workflow.StampFileWorkflowProfilesFor(dir, owner, rowOwner, eligible)
	if len(stamped) > 0 {
		log.Printf("workflow files: gave %d workflow(s) with no profile an owner (fallback %q)", len(stamped), owner)
	}
	return err
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
