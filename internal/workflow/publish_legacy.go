package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
)

const (
	// legacyDoneKey is the settings row that says the workflow files older versions left have been
	// taken in. Until it exists, and after it, a file with no rows is never imported or served by
	// anything but the claim itself and an operator's adoption.
	legacyDoneKey = "workflow_files.legacy_import_done"

	// ImportNewFileNote is the note of the first version of a workflow an operator adopted from a
	// file that appeared by itself: it starts inactive.
	ImportNewFileNote = "imported from an outside file, inactive"
)

// importMode says who is asking for a file to become a workflow.
type importMode int

const (
	// importEdit follows an outside edit of a workflow that already has rows. A file with no rows
	// is declined.
	importEdit importMode = iota
	// importClaim is the one-time take-in of the files older versions left (rows created as they
	// were: the file's active flag is kept).
	importClaim
	// importAdopt is an operator's explicit adoption: no rows yet, created inactive.
	importAdopt
)

// ClaimLegacyFiles takes in, once and atomically, the workflow files that exist when the workflow
// store is first used: those are the workflows older versions saved as files. They get rows exactly
// as the importer always gave them. From then on a file with no rows is just a file: it is not
// imported, not served and not listed until an operator adopts it, so a file dropped in later (by a
// person, a script or an agent) cannot become a workflow of any profile by itself. The claim is
// recorded under the write lock by whichever process gets there first, even when there is no profile
// yet (then nothing is taken in and every file is left for adoption). The workflows created are
// returned.
func (h *HybridWorkflowStore) ClaimLegacyFiles(ctx context.Context) ([]*Workflow, error) {
	if h.files == nil {
		return nil, nil
	}
	tx, err := h.sql.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// A write first: the claim and the directory listing happen under the write lock.
	if _, err := tx.ExecContext(ctx, `UPDATE settings SET value = value WHERE key = ?`, legacyDoneKey); err != nil {
		return nil, err
	}
	if done, err := legacyDone(ctx, tx); err != nil || done {
		return nil, err
	}
	// The claim is recorded at the first use whether or not a profile exists yet. Older versions
	// needed a profile for every workflow, so a machine that has legacy files has one; a machine with
	// none is a fresh install, where a file in this folder is not legacy at all, and it must not be
	// swept in later, when the first profile appears, as if it were. Files the claim cannot take in
	// (no profile exists, the file names none, ids clash) stay files: `doctor` offers them for
	// adoption and the log says which.
	entries, err := os.ReadDir(h.files.dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	profileExists := func(w *Workflow) bool {
		var n int
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiles WHERE id = ?`, w.ProfileID).Scan(&n) == nil && n > 0
	}
	var created []*Workflow
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok || strings.HasSuffix(id, ".ai-backup") {
			continue
		}
		var rows int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE id = ?`, id).Scan(&rows); err != nil {
			return nil, err
		}
		if rows > 0 {
			continue
		}
		// One file's failure must not leave half of it in the transaction.
		if _, err := tx.ExecContext(ctx, `SAVEPOINT claim_file`); err != nil {
			return nil, err
		}
		res, declined, err := h.importLocked(ctx, tx, id, profileExists, 0, false, regularFileInfo(h.files, id), importClaim, "")
		if err != nil || !res.Imported {
			why := declined
			if err != nil {
				why = err.Error()
			}
			// Left as a file: `doctor` offers it for adoption (the reason is not lost with the claim).
			log.Printf("workflow file %q was not taken in: %s", id, why)
			if _, rerr := tx.ExecContext(ctx, `ROLLBACK TO claim_file`); rerr != nil {
				return nil, rerr
			}
		}
		if _, err := tx.ExecContext(ctx, `RELEASE claim_file`); err != nil {
			return nil, err
		}
		if res.Imported {
			created = append(created, res.Workflow)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, '1')`, legacyDoneKey); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// A workflow that was only a file when approvals arrived is approved, if its definition is unchanged.
	for _, w := range created {
		_ = ApproveBackfilledFile(ctx, h.sql.db, w)
	}
	return created, nil
}

func legacyDone(ctx context.Context, q rowQuerier) (bool, error) {
	var v string
	switch err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, legacyDoneKey).Scan(&v); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		if isNoTable(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ensureClaimed makes sure the legacy files have been taken in the first time this store is used for
// files, in whatever process that is (a CLI command, the daemon, the MCP server, the API). A failed
// claim is tried again at the next use; until one succeeds no file with no rows is served at all.
func (h *HybridWorkflowStore) ensureClaimed(ctx context.Context) {
	if h.files == nil {
		return
	}
	h.claimMu.Lock()
	defer h.claimMu.Unlock()
	if h.claimed {
		return
	}
	if _, err := h.ClaimLegacyFiles(ctx); err == nil {
		h.claimed = true
	}
}

// safeFileID is what an id must look like to be offered for adoption or printed by `doctor`.
var safeFileID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// AdoptableFile describes a workflow file that appeared by itself, for an operator to look at.
type AdoptableFile struct {
	ID        string
	ProfileID string
	Path      string
	// SHA is the file's full sha256 (64 hex characters); adopting names it, so only the file that was
	// looked at can be adopted.
	SHA string
	// NodeTypes and Triggers are cut to a few short names. Preview is one bounded line per node:
	// type, name and the start of its configuration. All of it comes from the file and is escaped
	// where it is printed.
	NodeTypes []string
	Triggers  []string
	Preview   []string
}

const (
	maxAdoptableShown   = 20
	maxAdoptableExamine = 200
	maxAdoptableBytes   = 1 << 20
	maxNameShown        = 48
	maxNamesShown       = 10
	maxPreviewNodes     = 10
	maxPreviewChars     = 240
)

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func shortList(set map[string]bool) []string {
	var all []string
	for t := range set {
		all = append(all, t)
	}
	sort.Strings(all)
	var out []string
	for i, t := range all {
		if i == maxNamesShown {
			out = append(out, fmt.Sprintf("(+%d more)", len(all)-maxNamesShown))
			break
		}
		out = append(out, cut(t, maxNameShown))
	}
	return out
}

// AdoptableFiles lists workflow files that have no rows and so wait to be adopted, up to
// maxAdoptableShown; more counts the rest (files beyond the listing, with names that are not safe to
// print, unreadable, or larger than 1 MiB). At most maxAdoptableExamine directory entries are looked
// at and a file is read only if it is small.
func (h *HybridWorkflowStore) AdoptableFiles(ctx context.Context) (shown []AdoptableFile, more int, err error) {
	if h.files == nil {
		return nil, 0, nil
	}
	entries, err := os.ReadDir(h.files.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	examined := 0
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok || strings.HasSuffix(id, ".ai-backup") {
			continue
		}
		var n int
		if err := h.sql.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE id = ?`, id).Scan(&n); err != nil || n > 0 {
			continue
		}
		if examined++; examined > maxAdoptableExamine {
			more++
			continue
		}
		info := regularFileInfo(h.files, id)
		if len(shown) >= maxAdoptableShown || !safeFileID.MatchString(id) || info == nil || info.Size() > maxAdoptableBytes {
			more++
			continue
		}
		raw, existed, rerr := h.files.readRaw(id)
		if rerr != nil || !existed {
			more++
			continue
		}
		wf, perr := parseWorkflowFile(raw)
		if perr != nil || wf == nil || wf.ID != id || !safeFileID.MatchString(wf.ProfileID) || graphIDProblem(wf) != "" {
			more++
			continue
		}
		path, _ := h.files.filePath(id)
		a := AdoptableFile{ID: id, ProfileID: wf.ProfileID, Path: path, SHA: shaHex(append([]byte{}, raw...))}
		types, trig := map[string]bool{}, map[string]bool{}
		for i, nd := range wf.Nodes {
			types[nd.Type] = true
			if strings.HasPrefix(nd.Type, "trigger.") {
				trig[nd.Type] = true
			}
			if i < maxPreviewNodes {
				cfg, _ := json.Marshal(nd.Config)
				a.Preview = append(a.Preview, cut(nd.Type, maxNameShown)+" "+cut(nd.Name, maxNameShown)+" "+cut(string(cfg), maxPreviewChars))
			}
		}
		if len(wf.Nodes) > maxPreviewNodes {
			a.Preview = append(a.Preview, fmt.Sprintf("(+%d more nodes)", len(wf.Nodes)-maxPreviewNodes))
		}
		a.NodeTypes, a.Triggers = shortList(types), shortList(trig)
		shown = append(shown, a)
	}
	return shown, more, nil
}

// ImportedInactive lists workflows an operator adopted from files and that are still inactive.
func (h *HybridWorkflowStore) ImportedInactive(ctx context.Context) ([]string, error) {
	rows, err := h.sql.db.QueryContext(ctx, `
		SELECT DISTINCT v.workflow_id FROM workflow_versions v JOIN workflows w ON w.id = v.workflow_id
		WHERE v.note = ? AND w.is_active = 0 ORDER BY v.workflow_id`, ImportNewFileNote)
	if err != nil {
		if isNoTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AdoptFile creates the workflow of a file that has no rows. It is the operator's act: the file must
// be the one that was looked at (its sha256 is sha), must name profile (which must
// exist), and the workflow starts inactive and unapproved whatever the file says.
func (h *HybridWorkflowStore) AdoptFile(ctx context.Context, id, profile, sha string) error {
	if h.files == nil {
		return fmt.Errorf("no workflow files here")
	}
	if !safeFileID.MatchString(id) || profile == "" || len(sha) != 64 {
		return fmt.Errorf("adopting needs the workflow id, its profile and the file's full sha256 (64 hex characters, from `doctor`)")
	}
	pre := regularFileInfo(h.files, id)
	if pre == nil {
		return fmt.Errorf("workflow file %s is missing or is not a plain file", id)
	}
	tx, err := h.sql.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockPublication(ctx, tx, id); err != nil {
		return err
	}
	exists := func(w *Workflow) bool {
		var n int
		return w.ProfileID == profile && tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiles WHERE id = ?`, w.ProfileID).Scan(&n) == nil && n > 0
	}
	res, declined, err := h.importLocked(ctx, tx, id, exists, 0, true, pre, importAdopt, sha)
	if err != nil {
		return err
	}
	if !res.Imported {
		if declined == "" {
			declined = "it is not in a state that can be adopted"
		}
		return fmt.Errorf("workflow file %s was not adopted: %s", id, declined)
	}
	return tx.Commit()
}

// LegacyClaimDone reports whether the files older versions left have been taken in (see
// ClaimLegacyFiles).
func LegacyClaimDone(ctx context.Context, db *sql.DB) bool {
	done, err := legacyDone(ctx, db)
	return err == nil && done
}
