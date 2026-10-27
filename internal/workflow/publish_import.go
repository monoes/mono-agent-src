package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// ImportNote is the note of the version an outside edit is recorded as.
const ImportNote = "edited outside the app"

// ImportResult says what ImportFile did.
type ImportResult struct {
	// Imported: the rows now follow the file's definition.
	Imported bool
	// Created: the workflow had no rows before.
	Created bool
	// Workflow is the imported definition.
	Workflow *Workflow
}

// ImportFile makes the committed rows follow the workflow's file when the file was changed outside
// the store (an editor, a script). It is one transaction: the write lock is held from the look at
// the open changes to the commit, so it cannot interleave with a save (a save records its intent
// before it touches the file, and the importer refuses to read a file whose change is open). The
// file is never rewritten. An edit that does not change the definition only refreshes the file's
// recorded bytes (and the active flag, which the definition does not cover). An edit that changes
// the definition of an existing workflow is recorded as a version and removes the workflow's
// approvals in the same commit: a file cannot be attributed to a person, and approvals are keyed by
// definition hash, so a later edit back to an old approved definition must not run as approved.
//
// Nothing is imported, and nil is returned, when the file is absent, does not parse, was changed
// while it was read, has no profile, accept (when given) refuses it, or its workflow has a change
// open. A definition that shares node or connection ids with another workflow returns
// ErrSharedIDs.
func (h *HybridWorkflowStore) ImportFile(ctx context.Context, id string, accept func(*Workflow) bool) (ImportResult, error) {
	var none ImportResult
	if h.files == nil {
		return none, nil
	}
	h.ensureClaimed(ctx)
	// Look before the write lock is taken: a workflow with no stored copy is not imported by an edit,
	// and only a plain, bounded file is worth the lock.
	var stored int
	if err := h.sql.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE id = ?`, id).Scan(&stored); err == nil && stored == 0 {
		return none, nil
	}
	pre := regularFileInfo(h.files, id)
	if pre == nil {
		return none, nil
	}
	tx, err := h.sql.db.BeginTx(ctx, nil)
	if err != nil {
		return none, err
	}
	defer tx.Rollback()
	if err := lockPublication(ctx, tx, id); err != nil {
		return none, err
	}
	res, _, err := h.importLocked(ctx, tx, id, accept, 0, false, pre, importEdit, "")
	if err != nil {
		return none, err
	}
	if err := tx.Commit(); err != nil {
		return none, fmt.Errorf("importing workflow %s: %w", id, err)
	}
	return res, nil
}

// importLocked is ImportFile inside a transaction that already holds the write lock. An open change
// other than skipIntent makes it do nothing; force imports even bytes already reconciled. declined
// says why nothing was read from the file ("" when it was).
func (h *HybridWorkflowStore) importLocked(ctx context.Context, tx *sql.Tx, id string, accept func(*Workflow) bool, skipIntent int64, force bool, pre os.FileInfo, mode importMode, wantSHA string) (res ImportResult, declined string, err error) {
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_publications WHERE workflow_id = ? AND id != ?`, id, skipIntent).Scan(&open); err != nil {
		if isNoTable(err) {
			return res, "no publication table", nil
		}
		return res, "", err
	}
	if open > 0 {
		return res, "a change is in progress", nil
	}
	if mode == importEdit {
		var stored int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE id = ?`, id).Scan(&stored); err != nil {
			return res, "", err
		}
		if stored == 0 {
			// Checked before the file is opened: a file with no stored copy is never imported by an
			// edit, so there is nothing to read.
			return res, "a file with no stored copy waits to be adopted", nil
		}
	}
	before := regularFileInfo(h.files, id)
	if before == nil || (pre != nil && !os.SameFile(pre, before)) {
		return res, "the file is not the plain file that was looked at", nil
	}
	raw, existed, err := h.files.readRaw(id)
	if err != nil || !existed {
		return res, "no file", nil
	}
	if after := regularFileInfo(h.files, id); after == nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return res, "the file changed while it was read", nil
	}
	sha := shaHex(append([]byte{}, raw...))
	if wantSHA != "" && sha != wantSHA {
		return res, "the file is not the one that was looked at", nil
	}
	var known string
	switch err := tx.QueryRowContext(ctx, `SELECT file_sha FROM workflow_file_state WHERE workflow_id = ?`, id).Scan(&known); {
	case err == nil && known == sha && !force:
		return res, "", nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return res, "", err
	}
	wf, err := parseWorkflowFile(raw)
	if err != nil || wf == nil || wf.ID != id || wf.ProfileID == "" {
		return res, "the file does not parse as this workflow", nil
	}
	if accept != nil && !accept(wf) {
		return res, "refused", nil
	}
	if why := graphIDProblem(wf); why != "" {
		return res, why, nil
	}
	fileDef, err := definitionHash(wf)
	if err != nil {
		return res, "the file does not parse as this workflow", nil
	}
	rows, err := getWorkflowFrom(ctx, tx, id)
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		rows = nil
	default:
		return res, "", err
	}
	if wf.Version < 0 || wf.Version > 1_000_000 {
		wf.Version = 0 // a file cannot push the version counter out of range
	}
	res.Workflow = wf
	if rows != nil && wf.ProfileID != rows.ProfileID {
		// The owner of a workflow is not the file's to change: its runs use the owner's vault.
		return res, "the file names a different profile than the workflow's owner", nil
	}
	if rows != nil {
		rowsDef, err := definitionHash(rows)
		if err != nil {
			return res, "", err
		}
		if rowsDef == fileDef {
			// Same definition; the active flag is not part of it. A file may switch a workflow
			// off, never on: starting it is an operator's act.
			if rows.IsActive && !wf.IsActive {
				if _, err := tx.ExecContext(ctx, `UPDATE workflows SET is_active = ?, updated_at = ? WHERE id = ?`,
					boolToInt(wf.IsActive), time.Now().UTC(), id); err != nil {
					return res, "", err
				}
			}
			return res, "", h.recordFileState(ctx, tx, id, sha, fileDef)
		}
	}
	if err := checkOwnIDsOn(ctx, tx, wf); err != nil {
		return res, "", err
	}
	note := ImportNote
	if rows == nil {
		note = ""
		switch mode {
		case importClaim:
			// older versions' file-only workflow: as it always was (its active flag is kept)
		case importAdopt:
			wf.IsActive, note = false, ImportNewFileNote
		default:
			return res, "a file with no stored copy waits to be adopted", nil
		}
	} else {
		if mode != importEdit {
			return res, "the workflow already has a stored copy", nil
		}
		wf.IsActive = wf.IsActive && rows.IsActive
	}
	if _, err := writeDefinitionRows(ctx, tx, wf, true, wf.Version, note); err != nil {
		return res, "", err
	}
	if rows != nil {
		if _, err := tx.ExecContext(ctx, revokeApprovalsSQL, id); err != nil && !isNoTable(err) {
			return res, "", fmt.Errorf("revoking approvals: %w", err)
		}
	}
	if err := h.recordFileState(ctx, tx, id, sha, fileDef); err != nil {
		return res, "", err
	}
	res.Imported, res.Created = true, rows == nil
	return res, "", nil
}

const revokeApprovalsSQL = `DELETE FROM workflow_approvals WHERE workflow_id = ?`

// Resolutions of an unresolved change.
const (
	KeepRows = "keep-rows" // the file is rewritten from the committed rows
	KeepFile = "keep-file" // the rows follow the file, as an outside edit
)

// ResolveUnresolved ends a change that recovery could not settle (the file was edited by someone
// else while the change was open): it publishes the chosen state and removes the intent in one
// transaction. KeepRows rewrites the file from the committed rows (or removes it when there are
// none); KeepFile imports the file as an outside edit. The quarantined copy is kept.
func (h *HybridWorkflowStore) ResolveUnresolved(ctx context.Context, id, keep string) error {
	if h.files == nil {
		return fmt.Errorf("no workflow files here")
	}
	if keep != KeepRows && keep != KeepFile {
		return fmt.Errorf("choose %s or %s", KeepRows, KeepFile)
	}
	tx, done, err := h.beginDurable(ctx)
	if err != nil {
		return err
	}
	defer done()
	defer tx.Rollback()
	if err := lockPublication(ctx, tx, id); err != nil {
		return err
	}
	in, err := scanIntent(tx.QueryRowContext(ctx, `SELECT `+intentColumns+` FROM workflow_publications WHERE workflow_id = ?`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("workflow %s has no unfinished change", id)
		}
		return err
	}
	// An unresolved change, or one whose owner is gone but that recovery could not settle.
	if in.State == "pending" && in.live() {
		return fmt.Errorf("workflow %s: the change is still being made by process %d", id, in.OwnerPID)
	}
	intentID := in.ID
	switch keep {
	case KeepFile:
		res, declined, err := h.importLocked(ctx, tx, id, nil, intentID, true, nil, importEdit, "")
		if err != nil {
			return err
		}
		if res.Workflow == nil {
			return fmt.Errorf("the file cannot be kept: %s", declined)
		}
	case KeepRows:
		rows, err := getWorkflowFrom(ctx, tx, id)
		// Whatever is in the file now is about to be replaced: keep a copy of it (it may be an edit
		// that arrived after the copy taken when the change was flagged).
		if cur, there, rerr := h.files.readRaw(id); rerr == nil && there {
			if _, qerr := h.files.quarantine(id, cur); qerr != nil {
				return fmt.Errorf("keeping a copy of the file first: %w", qerr)
			}
		}
		switch {
		case err == nil:
			data, rerr := h.files.renderWorkflow(rows)
			if rerr != nil {
				return rerr
			}
			if err := h.files.writeRaw(id, data); err != nil {
				return err
			}
			def, _ := definitionHash(rows)
			if err := h.recordFileState(ctx, tx, id, shaHex(data), def); err != nil {
				return err
			}
		case errors.Is(err, sql.ErrNoRows):
			if err := h.files.removeFile(id); err != nil {
				return err
			}
		default:
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, dropIntentSQL, intentID); err != nil {
		return err
	}
	return tx.Commit()
}

// recordFileState remembers, in tx, the bytes the rows were last reconciled with.
func (h *HybridWorkflowStore) recordFileState(ctx context.Context, tx *sql.Tx, id, sha, def string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO workflow_file_state (workflow_id, file_sha, definition_hash, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(workflow_id) DO UPDATE SET file_sha = excluded.file_sha,
			definition_hash = excluded.definition_hash, updated_at = excluded.updated_at`,
		id, sha, def, time.Now().UTC())
	return err
}

// regularFileInfo is the workflow file's info when it is a plain file (not a link, pipe or device)
// within the size bound, else nil. It never opens the file.
func regularFileInfo(s *WorkflowFileStore, id string) os.FileInfo {
	path, err := s.filePath(id)
	if err != nil {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxWorkflowFileBytes {
		return nil
	}
	return info
}

// graphIDProblem says what is wrong with the node and connection ids of a workflow read from a
// file ("" when nothing is): the rows are keyed by them, so a missing or repeated id would give
// rows that differ from the file, and the file's bytes would still be recorded as reconciled.
func graphIDProblem(wf *Workflow) string {
	seen := map[string]bool{}
	for _, n := range wf.Nodes {
		if n.ID == "" {
			return "a node has no id"
		}
		if seen[n.ID] {
			return "two nodes share the id " + n.ID
		}
		seen[n.ID] = true
	}
	seen = map[string]bool{}
	for _, c := range wf.Connections {
		if c.ID == "" {
			return "a connection has no id"
		}
		if seen[c.ID] {
			return "two connections share the id " + c.ID
		}
		seen[c.ID] = true
	}
	return ""
}
