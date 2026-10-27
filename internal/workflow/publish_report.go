package workflow

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OpenPublication describes an unfinished change of a workflow, for `doctor`.
type OpenPublication struct {
	WorkflowID string
	Kind       string
	Origin     string
	State      string
	Age        time.Duration
	OwnerGone  bool
	Note       string
}

// OpenPublications lists the unfinished changes without touching them.
func (h *HybridWorkflowStore) OpenPublications(ctx context.Context) ([]OpenPublication, error) {
	rows, err := h.sql.db.QueryContext(ctx, `
		SELECT workflow_id, kind, origin, state, owner_pid, started_at, note
		FROM workflow_publications ORDER BY started_at`)
	if err != nil {
		if isNoTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []OpenPublication
	for rows.Next() {
		var o OpenPublication
		var pid int
		var started sqliteTime
		if err := rows.Scan(&o.WorkflowID, &o.Kind, &o.Origin, &o.State, &pid, &started, &o.Note); err != nil {
			return nil, err
		}
		in := intent{OwnerPID: pid, StartedAt: started.Time}
		o.Age, o.OwnerGone = time.Since(started.Time), !in.live()
		out = append(out, o)
	}
	return out, rows.Err()
}

// FileDrift compares the workflow files with what the database last reconciled them with:
// unreadable files do not parse; pending files parse and differ, so they await an import.
func (h *HybridWorkflowStore) FileDrift(ctx context.Context) (unreadable, pending []string, err error) {
	if h.files == nil {
		return nil, nil, nil
	}
	entries, err := os.ReadDir(h.files.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok || strings.HasSuffix(id, ".ai-backup") {
			continue
		}
		var stored int
		if qerr := h.sql.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE id = ?`, id).Scan(&stored); qerr == nil && stored == 0 {
			continue // a file with no stored copy is reported as adoptable, not read here
		}
		raw, rerr := readRegularFile(filepath.Join(h.files.dir, e.Name()))
		if rerr != nil {
			continue
		}
		if wf, perr := parseWorkflowFile(raw); perr != nil || wf == nil || wf.ID == "" || graphIDProblem(wf) != "" {
			unreadable = append(unreadable, id)
			continue
		}
		var known string
		switch qerr := h.sql.db.QueryRowContext(ctx, `SELECT file_sha FROM workflow_file_state WHERE workflow_id = ?`, id).Scan(&known); {
		case qerr == nil && known == shaHex(append([]byte{}, raw...)):
		case qerr == nil || errors.Is(qerr, sql.ErrNoRows):
			pending = append(pending, id)
		case isNoTable(qerr):
			return unreadable, nil, nil
		default:
			return nil, nil, qerr
		}
	}
	return unreadable, pending, nil
}

// SweepStaleTemp removes staged temp files a crashed writer left in the workflows folder (they can
// be up to the file size limit each). Only files older than twice the lease are touched, so a
// writer that is still working keeps its file. It returns how many it removed.
func (h *HybridWorkflowStore) SweepStaleTemp() int {
	if h.files == nil {
		return 0
	}
	entries, err := os.ReadDir(h.files.dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) < 2*publicationLease {
			continue
		}
		if os.Remove(filepath.Join(h.files.dir, name)) == nil {
			n++
		}
	}
	return n
}
