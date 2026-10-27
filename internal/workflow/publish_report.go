package workflow

import (
	"context"
	"os"
	"path/filepath"
	"sort"
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

// Bounds of one FileDrift scan: how many files are read, how many bytes in all, how many names are
// listed per kind.
const (
	maxDriftListed = 200
	maxDriftFiles  = 2000
	maxDriftBytes  = 64 << 20
)

// DriftReport is what FileDriftScan found. Incomplete says a limit stopped the scan (files, bytes
// or cancellation) so there may be more.
type DriftReport struct {
	Unreadable, Pending []string
	Incomplete          bool
}

// FileDrift compares the workflow files with what the database last reconciled them with:
// unreadable files do not parse; pending files parse and differ, so they await an import.
func (h *HybridWorkflowStore) FileDrift(ctx context.Context) (unreadable, pending []string, err error) {
	r, err := h.FileDriftScan(ctx)
	return r.Unreadable, r.Pending, err
}

// FileDriftScan is FileDrift with limits. A file whose bytes hash to what the database last
// reconciled is not parsed at all.
func (h *HybridWorkflowStore) FileDriftScan(ctx context.Context) (DriftReport, error) {
	var out DriftReport
	if h.files == nil {
		return out, nil
	}
	if _, err := os.Stat(h.files.dir); err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	// Only the files of workflows that have rows are looked at, found by their ids and not by
	// listing the folder, so files without rows (and names that are not plain ids) cost nothing.
	stored, err := h.workflowIDs(ctx)
	if err != nil {
		return out, err
	}
	known := map[string]string{}
	switch rs, qerr := h.sql.db.QueryContext(ctx, `SELECT workflow_id, file_sha FROM workflow_file_state`); {
	case qerr == nil:
		for rs.Next() {
			var id, sha string
			if err := rs.Scan(&id, &sha); err != nil {
				rs.Close()
				return out, err
			}
			known[id] = sha
		}
		rs.Close()
	case isNoTable(qerr):
		return out, nil
	default:
		return out, qerr
	}
	ids := make([]string, 0, len(stored))
	for id := range stored {
		if safeFileID.MatchString(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	files, bytesRead := 0, 0
	for _, id := range ids {
		if ctx.Err() != nil || files >= maxDriftFiles || bytesRead >= maxDriftBytes {
			out.Incomplete = true
			break
		}
		files++
		raw, rerr := readRegularFile(filepath.Join(h.files.dir, id+".json"))
		if rerr != nil {
			continue
		}
		bytesRead += len(raw)
		sha := shaHex(append([]byte{}, raw...))
		if known[id] == sha {
			continue // what the database last reconciled: nothing to parse
		}
		if wf, perr := parseWorkflowFile(raw); perr != nil || wf == nil || wf.ID == "" || graphIDProblem(wf) != "" {
			if len(out.Unreadable) < maxDriftListed {
				out.Unreadable = append(out.Unreadable, id)
			}
			continue
		}
		if len(out.Pending) < maxDriftListed {
			out.Pending = append(out.Pending, id)
		}
	}
	return out, nil
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
