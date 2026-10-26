package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
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
// recorded under the write lock by whichever process gets there first. The workflows created are
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
	// Without any profile nothing can be taken in (every workflow belongs to one): do not record the
	// claim, or the files that are really older versions' would become adoptable-only.
	var profiles int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiles`).Scan(&profiles); err != nil || profiles == 0 {
		return nil, err
	}
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
	// What the file holds, counted without quoting it: nodes, their configuration bytes, whether it
	// has workflow settings or an error workflow, and how many references to secrets or
	// variables ("secret." and "${") its configuration and settings contain. Cut is set when
	// anything above was shortened, so the operator must read the file itself.
	Nodes, ConfigBytes, Refs int
	HasSettings              bool
	ErrorWorkflow            bool
	Cut                      bool
}

const (
	maxAdoptableShown   = 20
	maxAdoptableEntries = 5000 // directory names looked at in one listing
	maxAdoptableReads   = 200  // files read in full and parsed in one listing
	maxHiddenNamed      = 20   // ids of files not shown that are named
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

// AdoptableListing is what AdoptableFiles found. Shown are the files that can be looked at;
// More counts every other file with no stored copy that was seen but not shown (not a plain id, too
// big, would not parse, beyond the listing); HiddenIDs names the first few of those whose names are
// plain ids, so a file that is not shown can still be adopted by naming it (read it, take its
// sha256, `doctor fix workflows.publications.adopt:<id>:<profile>:<sha256>`). Truncated says more
// directory entries existed than were looked at, so More is a lower bound.
type AdoptableListing struct {
	Shown     []AdoptableFile
	More      int
	HiddenIDs []string
	Truncated bool
}

// AdoptableFiles lists workflow files that have no rows and so wait to be adopted (see
// AdoptableListing for the numbers); more counts the files not shown.
func (h *HybridWorkflowStore) AdoptableFiles(ctx context.Context) (shown []AdoptableFile, more int, err error) {
	l, err := h.AdoptableListing(ctx)
	return l.Shown, l.More, err
}

// AdoptableListing does a bounded amount of work however many files there are: at most
// maxAdoptableEntries names are looked at (the directory is read in batches, not all at once),
// everything decidable from the name and the directory entry (a plain id, a regular file, small
// enough, no stored copy) is decided before a file is opened, candidates are taken newest first,
// and at most maxAdoptableReads files are opened and parsed in all.
func (h *HybridWorkflowStore) AdoptableListing(ctx context.Context) (AdoptableListing, error) {
	var out AdoptableListing
	if h.files == nil {
		return out, nil
	}
	dir, err := os.Open(h.files.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	defer dir.Close()
	stored, err := h.workflowIDs(ctx)
	if err != nil {
		return out, err
	}
	type candidate struct {
		id    string
		mtime time.Time
	}
	var cands []candidate
	seen := 0
	for !out.Truncated {
		names, rerr := dir.Readdirnames(256)
		for _, name := range names {
			if seen++; seen > maxAdoptableEntries {
				out.Truncated = true
				break
			}
			id, ok := strings.CutSuffix(name, ".json")
			if !ok || strings.HasSuffix(id, ".ai-backup") || stored[id] {
				continue
			}
			if !safeFileID.MatchString(id) {
				out.More++
				continue
			}
			info := regularFileInfo(h.files, id)
			if info == nil || info.Size() > maxAdoptableBytes {
				out.More++
				continue
			}
			cands = append(cands, candidate{id, info.ModTime()})
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return out, rerr
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].mtime.Equal(cands[j].mtime) {
			return cands[i].mtime.After(cands[j].mtime)
		}
		return cands[i].id < cands[j].id
	})
	reads := 0
	hide := func(id string) {
		out.More++
		if len(out.HiddenIDs) < maxHiddenNamed {
			out.HiddenIDs = append(out.HiddenIDs, id)
		}
	}
	for _, c := range cands {
		if len(out.Shown) >= maxAdoptableShown || reads >= maxAdoptableReads {
			hide(c.id)
			continue
		}
		// A glance at the start of the file first: a file that does not even begin like a JSON
		// object is counted without being read in full, so such files cost almost nothing and do not
		// use up the reads.
		path, _ := h.files.filePath(c.id)
		if head, herr := readRegularPrefix(path, 64); herr != nil || !strings.HasPrefix(strings.TrimSpace(string(head)), "{") {
			hide(c.id)
			continue
		}
		reads++
		raw, existed, rerr := h.files.readRaw(c.id)
		if rerr != nil || !existed {
			hide(c.id)
			continue
		}
		wf, perr := parseWorkflowFile(raw)
		if perr != nil || wf == nil || wf.ID != c.id || !safeFileID.MatchString(wf.ProfileID) || graphIDProblem(wf) != "" {
			hide(c.id)
			continue
		}
		out.Shown = append(out.Shown, describeAdoptable(h.files, c.id, wf, raw))
	}
	return out, nil
}

// describeAdoptable counts and cuts what a parsed file holds, for an operator to look at.
func describeAdoptable(files *WorkflowFileStore, id string, wf *Workflow, raw []byte) AdoptableFile {
	path, _ := files.filePath(id)
	a := AdoptableFile{ID: id, ProfileID: wf.ProfileID, Path: path, SHA: shaHex(append([]byte{}, raw...))}
	types, trig := map[string]bool{}, map[string]bool{}
	a.Nodes, a.HasSettings = len(wf.Nodes), len(wf.Settings) > 0
	_, a.ErrorWorkflow = wf.Settings[ErrorWorkflowSetting]
	a.Cut = len(wf.Nodes) > maxPreviewNodes
	if sj, err := json.Marshal(wf.Settings); err == nil {
		a.Refs += countRefs(string(sj))
	}
	for i, nd := range wf.Nodes {
		cj, _ := json.Marshal(nd.Config)
		a.ConfigBytes += len(cj)
		a.Refs += countRefs(string(cj))
		a.Cut = a.Cut || len(cj) > maxPreviewChars || len(nd.Name) > maxNameShown || len(nd.Type) > maxNameShown
		types[nd.Type] = true
		if strings.HasPrefix(nd.Type, "trigger.") {
			trig[nd.Type] = true
		}
		if i < maxPreviewNodes {
			a.Preview = append(a.Preview, cut(nd.Type, maxNameShown)+" "+cut(nd.Name, maxNameShown)+" "+cut(string(cj), maxPreviewChars))
		}
	}
	if len(wf.Nodes) > maxPreviewNodes {
		a.Preview = append(a.Preview, fmt.Sprintf("(+%d more nodes)", len(wf.Nodes)-maxPreviewNodes))
	}
	a.NodeTypes, a.Triggers = shortList(types), shortList(trig)
	a.Cut = a.Cut || len(types) > maxNamesShown || len(trig) > maxNamesShown
	return a
}

// countRefs counts the places a text refers to a secret or a variable by the spellings the engine
// uses. It is a floor, not a proof: it is shown as "at least".
func countRefs(s string) int {
	n := 0
	for _, marker := range []string{"secret.", "${", "@secret:", "{{", "$vars", "$env"} {
		n += strings.Count(s, marker)
	}
	return n
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
