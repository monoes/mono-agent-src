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
	maxAdoptableNames   = 100000   // directory names passed over in one listing
	maxAdoptableEntries = 5000     // candidate files (.json, no stored copy) looked at in one listing
	maxAdoptableBudget  = 32 << 20 // bytes read in full in one listing
	maxAdoptableReads   = 200      // files read in full and parsed in one listing
	maxHiddenNamed      = 20       // ids of files not shown that are named
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
	// Reads is how many files were read in full to produce Shown and More.
	Reads int
	// Incomplete says the listing may have missed or left unnamed files that have no stored copy:
	// it stopped at the entry cap, or more files were held back than could be named. One file can
	// be looked at by its id with AdoptableFile.
	Incomplete bool
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
		size  int64
		mtime time.Time
	}
	var cands []candidate
	seen, counted := 0, 0
	for !out.Truncated {
		names, rerr := dir.Readdirnames(256)
		for _, name := range names {
			// Every name costs a little, so there is a cap on all of them; only the files that could be
			// adopted (a .json name with no stored copy) count toward the smaller cap on candidates.
			if seen++; seen > maxAdoptableNames {
				out.Truncated = true
				break
			}
			id, ok := strings.CutSuffix(name, ".json")
			if !ok || strings.HasSuffix(id, ".ai-backup") || stored[id] {
				continue
			}
			if counted++; counted > maxAdoptableEntries {
				out.Truncated = true
				break
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
			cands = append(cands, candidate{id, info.Size(), info.ModTime()})
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
	// File times are the writer's to set, so neither end of the order is trusted: the files are
	// taken alternately from the newest and the oldest end, and both ends of what is held back are
	// named.
	order := make([]candidate, 0, len(cands))
	for lo, hi := 0, len(cands)-1; lo <= hi; lo, hi = lo+1, hi-1 {
		order = append(order, cands[lo])
		if lo != hi {
			order = append(order, cands[hi])
		}
	}
	var held []string
	hide := func(id string) {
		out.More++
		held = append(held, id)
	}
	spent := int64(0)
	for _, c := range order {
		if len(out.Shown) >= maxAdoptableShown || out.Reads >= maxAdoptableReads || spent+c.size > maxAdoptableBudget {
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
		// The bytes are paid for before they are read, and what is read is cut at the largest file
		// that may be shown, whatever the file has grown to since it was measured.
		out.Reads++
		spent += c.size
		raw, rerr := readRegularPrefix(path, maxAdoptableBytes+1)
		if rerr != nil || len(raw) > maxAdoptableBytes {
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
	// Name some of what was held back from each end of the order it was taken in.
	if len(held) <= maxHiddenNamed {
		out.HiddenIDs = held
	} else {
		half := maxHiddenNamed / 2
		out.HiddenIDs = append(append([]string{}, held[:half]...), held[len(held)-half:]...)
	}
	out.Incomplete = out.Truncated || len(held) > len(out.HiddenIDs)
	return out, nil
}

// AdoptableFile looks at the one file named id, which must be a plain id with no stored copy: the
// bounded way to reach a file the listing did not show. When the file cannot be shown, why says so.
func (h *HybridWorkflowStore) AdoptableFile(ctx context.Context, id string) (a *AdoptableFile, why string, err error) {
	if h.files == nil {
		return nil, "there are no workflow files here", nil
	}
	if !safeFileID.MatchString(id) {
		return nil, "the name is not a plain workflow id", nil
	}
	var n int
	if err := h.sql.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE id = ?`, id).Scan(&n); err != nil {
		return nil, "", err
	}
	if n > 0 {
		return nil, "the workflow has a stored copy; this file is not waiting to be adopted", nil
	}
	info := regularFileInfo(h.files, id)
	if info == nil || info.Size() > maxAdoptableBytes {
		return nil, "there is no such plain file, or it is larger than 1 MiB", nil
	}
	raw, existed, rerr := h.files.readRaw(id)
	if rerr != nil || !existed {
		return nil, "the file cannot be read", nil
	}
	wf, perr := parseWorkflowFile(raw)
	if perr != nil || wf == nil || wf.ID != id || !safeFileID.MatchString(wf.ProfileID) || graphIDProblem(wf) != "" {
		return nil, "the file is not a workflow that can be adopted (it does not parse, names another id, has no safe profile id or has bad node ids)", nil
	}
	d := describeAdoptable(h.files, id, wf, raw)
	return &d, "", nil
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

var (
	refTemplate = regexp.MustCompile(`\$\{[^}]*\}|\{\{.*?\}\}|@secret:[A-Za-z0-9_.-]+`)
	refSecret   = regexp.MustCompile(`secret\.[A-Za-z0-9_]+`)
)

// countRefs counts the template expressions (${...}, {{...}}), @secret: references and bare secret.x
// mentions in a text, each place once. It counts syntax, not references: an expression with no
// variable in it counts too, and a spelling it does not know is not counted, so it is shown as a
// count of markers and never as proof either way.
func countRefs(s string) int {
	n := len(refTemplate.FindAllString(s, -1))
	return n + len(refSecret.FindAllString(refTemplate.ReplaceAllString(s, " "), -1))
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
