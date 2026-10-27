package workflow

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// handEdit changes the workflow's file the way an editor does: the store is not involved.
func handEdit(t *testing.T, h *HybridWorkflowStore, id string, edit func(w *Workflow)) {
	t.Helper()
	w, err := h.files.GetWorkflow(context.Background(), id)
	if err != nil || w == nil {
		t.Fatalf("%v", err)
	}
	edit(w)
	if err := h.files.SaveWorkflow(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}

func versionCount(t *testing.T, s *SQLiteWorkflowStore, id string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM workflow_versions WHERE workflow_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportFollowsAnOutsideEditAndRecordsIt(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	if res, err := h.ImportFile(ctx, "wf-g", nil); err != nil || res.Imported {
		t.Fatalf("the first look at an unchanged file imported: %+v %v", res, err)
	}
	v0 := versionCount(t, s, "wf-g")
	handEdit(t, h, "wf-g", func(w *Workflow) { w.Nodes[1].Config = map[string]interface{}{"k": "by-hand"} })
	onDisk := fileBytes(t, h, "wf-g")
	res, err := h.ImportFile(ctx, "wf-g", nil)
	if err != nil || !res.Imported || res.Created {
		t.Fatalf("%+v %v", res, err)
	}
	if sqlConfig(t, s, "wf-g", "g-set") != "by-hand" {
		t.Fatal("the rows did not follow the file")
	}
	if versionCount(t, s, "wf-g") != v0+1 {
		t.Fatal("the edit was not recorded as a version")
	}
	var note string
	_ = s.db.QueryRow(`SELECT note FROM workflow_versions WHERE workflow_id = 'wf-g' ORDER BY version DESC LIMIT 1`).Scan(&note)
	if note != ImportNote {
		t.Fatalf("note %q", note)
	}
	if !bytes.Equal(onDisk, fileBytes(t, h, "wf-g")) {
		t.Fatal("the import rewrote the file")
	}
	// An outside edit cannot be attributed to a person: every earlier approval goes with it.
	cur, _ := h.GetWorkflow(ctx, "wf-g")
	hash, _ := definitionHash(cur)
	if ok, _ := h.DefinitionApproved(ctx, "wf-g", hash); ok || approvals(t, s, "wf-g") != 0 {
		t.Fatalf("approvals after an outside edit: approved=%v rows=%d", ok, approvals(t, s, "wf-g"))
	}
	// Seen again, nothing more happens.
	if res, _ := h.ImportFile(ctx, "wf-g", nil); res.Imported || versionCount(t, s, "wf-g") != v0+1 {
		t.Fatal("the same file was imported twice")
	}
}

func TestImportOfAReformattedFileChangesNothing(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	v0 := versionCount(t, s, "wf-g")
	handEdit(t, h, "wf-g", func(w *Workflow) { w.UpdatedAt = w.UpdatedAt.Add(time.Hour) })
	if res, err := h.ImportFile(ctx, "wf-g", nil); err != nil || res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	if versionCount(t, s, "wf-g") != v0 {
		t.Fatal("a version was recorded for an unchanged definition")
	}
	if n, _ := rowsCount(s, `SELECT COUNT(*) FROM workflow_file_state WHERE workflow_id = 'wf-g'`); n != 1 {
		t.Fatal("the new bytes were not remembered")
	}
}

func rowsCount(s *SQLiteWorkflowStore, q string) (int, error) {
	var n int
	err := s.db.QueryRow(q).Scan(&n)
	return n, err
}

// A file whose change is open is not imported: this is the importer reading a save that has not
// committed.
func TestImportSkipsAWorkflowWithAChangeOpen(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	handEdit(t, h, "wf-g", func(w *Workflow) { w.Nodes[1].Config = map[string]interface{}{"k": "pending"} })
	if _, err := s.db.Exec(`INSERT INTO workflow_publications
		(workflow_id, kind, state, owner_pid, started_at) VALUES ('wf-g', 'save', 'pending', ?, ?)`,
		os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if res, err := h.ImportFile(ctx, "wf-g", nil); err != nil || res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	if sqlConfig(t, s, "wf-g", "g-set") == "pending" {
		t.Fatal("an uncommitted file reached the rows")
	}
}

func TestImportIgnoresAFileThatDoesNotParse(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	if err := os.WriteFile(filepath.Join(h.files.dir, "wf-g.json"), []byte(`{"id":"wf-g","nodes":[{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := h.ImportFile(ctx, "wf-g", nil); err != nil || res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	if sqlConfig(t, s, "wf-g", "g-set") != "v" {
		t.Fatal("rows changed")
	}
}

func TestImportCreatesRowsForAFileThatHasNone(t *testing.T) {
	ctx := context.Background()
	h, s := newHybridForTest(t) // unused so far: the file counts as an older version's
	if err := h.files.SaveWorkflow(ctx, mirrorWF("wf-file-only", "f-trig", "f-set")); err != nil {
		t.Fatal(err)
	}
	// The first use of the store takes in the files older versions left.
	if _, err := h.ImportFile(ctx, "wf-file-only", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetWorkflow(ctx, "wf-file-only"); got == nil || len(got.Nodes) != 2 {
		t.Fatalf("rows: %+v", got)
	}
}

func TestAdoptRefusesIdsOwnedByAnotherWorkflow(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := guardedRig(t)
	clash := mirrorWF("wf-clash", "g-trig", "c-set")
	clash.Connections = nil
	if err := h.files.SaveWorkflow(ctx, clash); err != nil {
		t.Fatal(err)
	}
	shown, _, _ := h.AdoptableFiles(ctx)
	if len(shown) != 1 {
		t.Fatalf("%+v", shown)
	}
	if err := h.AdoptFile(ctx, "wf-clash", shown[0].ProfileID, shown[0].SHA); !errors.Is(err, ErrSharedIDs) {
		t.Fatalf("%v", err)
	}
}

func TestImportHonoursTheCallersRefusal(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	if err := h.files.SaveWorkflow(ctx, mirrorWF("wf-refused", "r-trig", "r-set")); err != nil {
		t.Fatal(err)
	}
	res, err := h.ImportFile(ctx, "wf-refused", func(*Workflow) bool { return false })
	if err != nil || res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	if got, _ := s.GetWorkflow(ctx, "wf-refused"); got != nil {
		t.Fatal("a refused file was imported")
	}
}

// The importer, run inside a save's window (file replaced, commit not yet made), imports nothing; the
// commit then fails and the rows never held the proposed content.
func TestImportInsideAFailingSavesWindowNeverSeesTheProposal(t *testing.T) {
	ctx := context.Background()
	h, s, hash, nodes := guardedRig(t)
	var inWindow ImportResult
	var importErr error
	guardedCommitFault = func() error {
		inWindow, importErr = h.ImportFile(ctx, "wf-g", nil)
		return errors.New("commit failed")
	}
	if err := h.SaveNodesGuarded(ctx, "wf-g", nodes, SaveGuard{ExpectHash: hash}); err == nil {
		t.Fatal("the save should have failed")
	}
	if importErr != nil || inWindow.Imported {
		t.Fatalf("the importer acted inside the window: %+v %v", inWindow, importErr)
	}
	if sqlConfig(t, s, "wf-g", "g-set") == "edited" {
		t.Fatal("the rows hold content that never committed")
	}
	if again, err := h.ImportFile(ctx, "wf-g", nil); err != nil || again.Imported {
		t.Fatalf("after the rollback: %+v %v", again, err)
	}
}

// Approve A, edit outside to B (imported), put A back outside (imported): A is not approved again
// by the history alone.
func TestImportDoesNotLetARestoredDefinitionRunAsApproved(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	handEdit(t, h, "wf-g", func(w *Workflow) { w.Nodes[1].Config = map[string]interface{}{"k": "B"} })
	if _, err := h.ImportFile(ctx, "wf-g", nil); err != nil {
		t.Fatal(err)
	}
	handEdit(t, h, "wf-g", func(w *Workflow) { w.Nodes[1].Config = map[string]interface{}{"k": "v"} }) // back to A
	if res, err := h.ImportFile(ctx, "wf-g", nil); err != nil || !res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	cur, _ := h.GetWorkflow(ctx, "wf-g")
	hash, _ := definitionHash(cur)
	if ok, _ := h.DefinitionApproved(ctx, "wf-g", hash); ok || approvals(t, s, "wf-g") != 0 {
		t.Fatal("the restored definition counts as approved")
	}
}

// A file may switch a workflow off but never on: an activation-only edit is ignored, a
// deactivation-only edit reaches the rows, and neither touches approvals.
func TestImportOnlyHonoursAFileSwitchingAWorkflowOff(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	handEdit(t, h, "wf-g", func(w *Workflow) { w.IsActive = true })
	if _, err := h.ImportFile(ctx, "wf-g", nil); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-g"); rows.IsActive {
		t.Fatal("a file switched a workflow on")
	}
	if got, _ := h.GetWorkflow(ctx, "wf-g"); got.IsActive {
		t.Fatal("a read served the file's active flag")
	}
	if err := h.SetWorkflowActive(ctx, "wf-g", true); err != nil {
		t.Fatal(err)
	}
	handEdit(t, h, "wf-g", func(w *Workflow) { w.IsActive = false })
	if _, err := h.ImportFile(ctx, "wf-g", nil); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-g"); rows.IsActive {
		t.Fatal("a file could not switch a workflow off")
	}
	if approvals(t, s, "wf-g") != 1 {
		t.Fatal("an activation change touched approvals")
	}
}

// The owner of a workflow is not the file's to change: a file naming another profile is declined
// and reads keep the rows' profile; a file that appears for a profile starts inactive.
func TestImportKeepsTheOwnerProfileAndANewFileWaitsToBeAdopted(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	if _, err := s.db.Exec(`INSERT INTO profiles (id, name) VALUES ('victim', 'Victim')`); err != nil {
		t.Fatal(err)
	}
	handEdit(t, h, "wf-g", func(w *Workflow) {
		w.ProfileID = "victim"
		w.Nodes[1].Config = map[string]interface{}{"k": "moved"} // a changed definition too
	})
	if res, err := h.ImportFile(ctx, "wf-g", nil); err != nil || res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	got, err := h.GetWorkflow(ctx, "wf-g")
	if err != nil || got.ProfileID != "default" {
		t.Fatalf("a read served profile %q (%v)", got.ProfileID, err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-g"); rows.ProfileID != "default" || sqlConfig(t, s, "wf-g", "g-set") == "moved" {
		t.Fatalf("rows moved to %q", rows.ProfileID)
	}
	fresh := mirrorWF("wf-new-file", "nf-trig", "nf-set")
	fresh.ProfileID, fresh.IsActive = "victim", true
	if err := h.files.SaveWorkflow(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	// It appeared after the legacy set was recorded: not taken in, not served, until adopted.
	if res, err := h.ImportFile(ctx, "wf-new-file", nil); err != nil || res.Imported {
		t.Fatalf("%+v %v", res, err)
	}
	if got, _ := h.GetWorkflow(ctx, "wf-new-file"); got != nil {
		t.Fatal("a read served a file nobody adopted")
	}
	shown, _, _ := h.AdoptableFiles(ctx)
	if len(shown) != 1 || shown[0].ProfileID != "victim" {
		t.Fatalf("adoptable: %+v", shown)
	}
	if err := h.AdoptFile(ctx, "wf-new-file", "victim", shown[0].SHA); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-new-file"); rows == nil || rows.IsActive || rows.ProfileID != "victim" {
		t.Fatalf("adopted rows: %+v", rows)
	}
}

// An unresolved change ends when the operator chooses: the file is rewritten from the rows, or the
// rows follow the file; either way the intent goes in the same transaction.
func TestResolveUnresolvedEitherWay(t *testing.T) {
	ctx := context.Background()
	for _, keep := range []string{KeepRows, KeepFile} {
		h, s, _, _ := guardedRig(t)
		handEdit(t, h, "wf-g", func(w *Workflow) { w.Nodes[1].Config = map[string]interface{}{"k": "by-hand"} })
		if _, err := s.db.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
			VALUES ('wf-g', 'save', 'unresolved', 0, ?)`, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if n, _ := h.RecoverPublications(ctx); n != 0 {
			t.Fatal("recovery touched an unresolved change")
		}
		if err := h.ResolveUnresolved(ctx, "wf-g", keep); err != nil {
			t.Fatalf("%s: %v", keep, err)
		}
		if openIntentCount(t, s) != 0 {
			t.Fatalf("%s: the intent is still there", keep)
		}
		f, _ := h.files.GetWorkflow(ctx, "wf-g")
		want := "v"
		if keep == KeepFile {
			want = "by-hand"
		}
		if f.Nodes[1].Config["k"] != want || sqlConfig(t, s, "wf-g", "g-set") != want {
			t.Fatalf("%s: file %v rows %v, want %s in both", keep, f.Nodes[1].Config["k"], sqlConfig(t, s, "wf-g", "g-set"), want)
		}
		if err := h.ResolveUnresolved(ctx, "wf-g", keep); err == nil {
			t.Fatalf("%s: resolving twice must be refused", keep)
		}
		// Saves work again.
		cur, _ := h.GetWorkflow(ctx, "wf-g")
		if err := h.SaveWorkflow(ctx, cur); err != nil {
			t.Fatalf("%s: a save after the resolution: %v", keep, err)
		}
	}
}

// Approve A, save B, put A back outside the store, then activate: the activation must not consume
// the outside file unseen. It is imported first, which removes the old approval of A, and the
// unapproved result is refused.
func TestPublishReconcilesAnOutsideEditBeforeBuildingOnIt(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	cur, _ := h.GetWorkflow(ctx, "wf-g")
	cur.Nodes[1].Config = map[string]interface{}{"k": "B"}
	if err := h.SaveWorkflow(ctx, cur); err != nil {
		t.Fatal(err)
	}
	handEdit(t, h, "wf-g", func(w *Workflow) { w.Nodes[1].Config = map[string]interface{}{"k": "v"} }) // A again
	// The outside file is imported before anything builds on it, which removes the old approval of
	// A: so the activation, which nobody approved, is refused (it used to go through unapproved).
	if err := h.SetWorkflowActive(ctx, "wf-g", true); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("activating the outside-edited definition: %v", err)
	}
	now, _ := h.GetWorkflow(ctx, "wf-g")
	hash, _ := definitionHash(now)
	if ok, _ := h.DefinitionApproved(ctx, "wf-g", hash); ok || approvals(t, s, "wf-g") != 0 {
		t.Fatal("the restored definition still counts as approved")
	}
	if now.IsActive {
		t.Fatal("a refused activation switched the workflow on")
	}
	if n := versionCount(t, s, "wf-g"); n < 3 {
		t.Fatalf("the outside edit was not recorded as a version (%d versions)", n)
	}
	if res, _ := h.ImportFile(ctx, "wf-g", nil); res.Imported {
		t.Fatal("the bytes were left unreconciled")
	}
}

// A file whose graph ids are missing or repeated is not imported (and so not recorded as
// reconciled): the rows would differ from the file for good.
func TestImportRefusesMissingAndRepeatedGraphIDs(t *testing.T) {
	ctx := context.Background()
	for name, edit := range map[string]func(w *Workflow){
		"connection without an id": func(w *Workflow) { w.Connections[0].ID = "" },
		"repeated node id":         func(w *Workflow) { w.Nodes[1].ID = w.Nodes[0].ID },
		"node without an id":       func(w *Workflow) { w.Nodes[1].ID = "" },
	} {
		h, s, _, _ := guardedRig(t)
		path := filepath.Join(h.files.dir, "wf-g.json")
		w, _ := h.files.GetWorkflow(ctx, "wf-g")
		w.Nodes[1].Config = map[string]interface{}{"k": "changed"}
		edit(w)
		data, err := h.files.renderWorkflow(w)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if res, err := h.ImportFile(ctx, "wf-g", nil); err != nil || res.Imported {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		if n, _ := rowsCount(s, `SELECT COUNT(*) FROM workflow_file_state WHERE workflow_id = 'wf-g' AND file_sha = '`+shaHex(data)+`'`); n != 0 {
			t.Fatalf("%s: the bytes were recorded as reconciled", name)
		}
		unreadable, _, _ := h.FileDrift(ctx)
		if len(unreadable) != 1 {
			t.Fatalf("%s: doctor does not list the file: %v", name, unreadable)
		}
	}
}

// Approve A, move the file away, change the rows without a file, then put A's exact bytes back: the
// remembered bytes must not make the import skip it. The file differs from the rows, so it is
// imported and the old approval goes.
func TestRowsOnlySaveForgetsTheReconciledBytes(t *testing.T) {
	ctx := context.Background()
	h, s, _, nodes := guardedRig(t)
	path := filepath.Join(h.files.dir, "wf-g.json")
	a := fileBytes(t, h, "wf-g")
	if err := os.Rename(path, path+".away"); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveWorkflowNodes(ctx, "wf-g", nodes); err != nil { // B, rows only
		t.Fatal(err)
	}
	if err := os.Rename(path+".away", path); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, fileBytes(t, h, "wf-g")) {
		t.Fatal("setup: the file is not A")
	}
	res, err := h.ImportFile(ctx, "wf-g", nil)
	if err != nil || !res.Imported {
		t.Fatalf("A's bytes were skipped: %+v %v", res, err)
	}
	if approvals(t, s, "wf-g") != 0 {
		t.Fatal("the old approval survived")
	}
}

// A link put where the workflow's file was (pointing at an approved definition) is declined by the
// importer and must not be built on by the next publication either: the activation builds from the
// committed rows and replaces the link with a plain file.
func TestPublicationDoesNotBuildOnAFileTheImporterDeclined(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	a := fileBytes(t, h, "wf-g") // approved A
	cur, _ := h.GetWorkflow(ctx, "wf-g")
	cur.Nodes[1].Config = map[string]interface{}{"k": "B"}
	if err := h.SaveWorkflow(ctx, cur); err != nil {
		t.Fatal(err)
	}
	approveForTest(t, h, "wf-g") // the rows' B is approved: the refusal below is about the file
	dir := h.files.dir
	target := filepath.Join(dir, "elsewhere.txt")
	if err := os.WriteFile(target, a, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wf-g.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	// The importer declined the link, and the publication refuses to replace it: it will not
	// build on its target and will not silently turn it into a plain file either.
	if err := h.SetWorkflowActive(ctx, "wf-g", true); !errors.Is(err, ErrFileSyncFailed) {
		t.Fatalf("%v", err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was touched: %v", err)
	}
	if sqlConfig(t, s, "wf-g", "g-set") != "B" || openIntentCount(t, s) != 0 {
		t.Fatal("the refused change left something behind")
	}
}

// A hand edit the importer cannot take in (here cut off mid-write) is not lost when the next save
// replaces the file: a copy is kept in the quarantine folder.
func TestSaveKeepsACopyOfADeclinedHandEdit(t *testing.T) {
	ctx := context.Background()
	h, s, _, nodes := guardedRig(t)
	half := []byte(`{"id":"wf-g","name":"half","nodes":[`)
	if err := os.WriteFile(filepath.Join(h.files.dir, "wf-g.json"), half, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveWorkflowNodes(ctx, "wf-g", nodes); err != nil {
		t.Fatal(err)
	}
	kept, _ := filepath.Glob(filepath.Join(filepath.Dir(h.files.dir), "workflow-quarantine", "wf-g.*.json"))
	if len(kept) != 1 {
		t.Fatalf("quarantined copies: %v", kept)
	}
	if b, _ := os.ReadFile(kept[0]); !bytes.Equal(b, half) {
		t.Fatal("the copy is not the edit")
	}
	if sqlConfig(t, s, "wf-g", "g-set") != "edited" {
		t.Fatal("the save did not land")
	}
}

// Whatever reads a workflow to run it sees an outside edit only after it has been taken in: approve
// A, save B, put A back by hand, read: the read imports A, which removes A's old approval.
func TestReadingAnOutsideEditedFileReconcilesItFirst(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	cur, _ := h.GetWorkflow(ctx, "wf-g")
	cur.Nodes[1].Config = map[string]interface{}{"k": "B"}
	if err := h.SaveWorkflow(ctx, cur); err != nil {
		t.Fatal(err)
	}
	handEdit(t, h, "wf-g", func(w *Workflow) { w.Nodes[1].Config = map[string]interface{}{"k": "v"} }) // A again
	got, err := h.GetWorkflow(ctx, "wf-g")
	if err != nil || got.Nodes[1].Config["k"] != "v" {
		t.Fatalf("%v %v", got, err)
	}
	hash, _ := definitionHash(got)
	if ok, _ := h.DefinitionApproved(ctx, "wf-g", hash); ok || approvals(t, s, "wf-g") != 0 {
		t.Fatal("A was served under its old approval")
	}
	if sqlConfig(t, s, "wf-g", "g-set") != "v" {
		t.Fatal("the rows were not brought up to the file")
	}
}

// Choosing the rows replaces the file, but first keeps a copy of whatever the file holds now.
func TestResolveKeepRowsKeepsACopyOfTheCurrentFile(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	late := []byte(`{"id":"wf-g","name":"edited after the flag"}`)
	if err := os.WriteFile(filepath.Join(h.files.dir, "wf-g.json"), late, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('wf-g', 'save', 'unresolved', 0, ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := h.ResolveUnresolved(ctx, "wf-g", KeepRows); err != nil {
		t.Fatal(err)
	}
	kept, _ := filepath.Glob(filepath.Join(filepath.Dir(h.files.dir), "workflow-quarantine", "wf-g.*.json"))
	if len(kept) != 1 {
		t.Fatalf("copies: %v", kept)
	}
	if b, _ := os.ReadFile(kept[0]); !bytes.Equal(b, late) {
		t.Fatal("the copy is not the file that was replaced")
	}
}

// A node edit never changes the active flag: a file that says is_active:true (imported with the
// same definition) must not be turned into an active workflow by the next unrelated edit.
func TestNodeEditDoesNotSwitchAWorkflowOn(t *testing.T) {
	ctx := context.Background()
	h, s, _, nodes := guardedRig(t)
	handEdit(t, h, "wf-g", func(w *Workflow) { w.IsActive = true })
	if _, err := h.ImportFile(ctx, "wf-g", nil); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveWorkflowNodes(ctx, "wf-g", nodes); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-g"); rows.IsActive {
		t.Fatal("the rows were switched on by a node edit")
	}
	if f, _ := h.files.GetWorkflow(ctx, "wf-g"); f.IsActive {
		t.Fatal("the file was switched on by a node edit")
	}
	hash, _ := ReviewHash(mustGet(t, h, "wf-g"))
	nodes[1].Config = map[string]interface{}{"k": "guarded"}
	if err := h.SaveNodesGuarded(ctx, "wf-g", nodes, SaveGuard{ExpectHash: hash}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-g"); rows.IsActive {
		t.Fatal("a guarded edit switched the workflow on")
	}
}

func mustGet(t *testing.T, h *HybridWorkflowStore, id string) *Workflow {
	t.Helper()
	w, err := h.GetWorkflow(context.Background(), id)
	if err != nil || w == nil {
		t.Fatalf("%v", err)
	}
	return w
}
