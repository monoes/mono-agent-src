package workflow

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

func dropFile(t *testing.T, h *HybridWorkflowStore, id string, active bool) {
	t.Helper()
	w := mirrorWF(id, id+"-trig", id+"-set")
	w.IsActive = active
	if err := h.files.SaveWorkflow(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}

func markerCount(t *testing.T, s *SQLiteWorkflowStore) int {
	t.Helper()
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, legacyDoneKey).Scan(&n)
	return n
}

// The workflow files older versions left become workflows when the store is first used, as they
// always did (the active flag is kept), and the claim is recorded once.
func TestFilesOlderVersionsLeftAreTakenInOnceAndKeepTheirFlag(t *testing.T) {
	ctx := context.Background()
	h, s := newHybridForTest(t) // nothing has used the store yet
	dropFile(t, h, "wf-old", true)
	if got, err := h.GetWorkflow(ctx, "wf-old"); err != nil || got == nil || !got.IsActive {
		t.Fatalf("a read of an older version's file: %+v %v", got, err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-old"); rows == nil || !rows.IsActive {
		t.Fatalf("rows: %+v", rows)
	}
	if markerCount(t, s) != 1 {
		t.Fatal("the claim was not recorded")
	}
}

// A file with no rows that appears after the claim is not imported, not served and not listed; an
// operator adopts it by naming the file they looked at, and it starts inactive.
func TestFileThatAppearsAfterTheClaimWaitsToBeAdopted(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t) // used already: the claim is recorded
	dropFile(t, h, "wf-dropped", true)
	if res, err := h.ImportFile(ctx, "wf-dropped", nil); err != nil || res.Imported {
		t.Fatalf("the importer took in a dropped file: %+v %v", res, err)
	}
	if got, err := h.GetWorkflow(ctx, "wf-dropped"); err != nil || got != nil {
		t.Fatalf("a read served a dropped file: %+v %v", got, err)
	}
	for _, w := range mustList(t, h) {
		if w.ID == "wf-dropped" {
			t.Fatal("a listing shows a dropped file")
		}
	}
	shown, more, err := h.AdoptableFiles(ctx)
	if err != nil || len(shown) != 1 || more != 0 || shown[0].ID != "wf-dropped" || shown[0].ProfileID != "default" || len(shown[0].SHA) != 64 {
		t.Fatalf("adoptable: %+v %d %v", shown, more, err)
	}
	a := shown[0]
	if len(a.Triggers) != 1 || a.Triggers[0] != "trigger.manual" {
		t.Fatalf("triggers: %v", a.Triggers)
	}
	if err := h.AdoptFile(ctx, "wf-dropped", a.ProfileID, strings.Repeat("0", 64)); err == nil {
		t.Fatal("adopted a file other than the one that was looked at")
	}
	if err := h.AdoptFile(ctx, "wf-dropped", "some-other-profile", a.SHA); err == nil {
		t.Fatal("adopted into a profile the file does not name")
	}
	if err := h.AdoptFile(ctx, "wf-dropped", a.ProfileID, a.SHA); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.GetWorkflow(ctx, "wf-dropped")
	if rows == nil || rows.IsActive {
		t.Fatalf("adopted workflow: %+v", rows)
	}
	var note string
	_ = s.db.QueryRow(`SELECT note FROM workflow_versions WHERE workflow_id = 'wf-dropped'`).Scan(&note)
	if note != ImportNewFileNote || approvals(t, s, "wf-dropped") != 0 {
		t.Fatalf("note %q, approvals %d", note, approvals(t, s, "wf-dropped"))
	}
	if got, _ := h.ImportedInactive(ctx); len(got) != 1 || got[0] != "wf-dropped" {
		t.Fatalf("doctor list: %v", got)
	}
}

// Adopting never replaces a stored workflow and never starts one: a workflow that has rows is
// refused (it would be an outside-edit import with revoked approvals), and a file that says
// is_active:true is adopted inactive.
func TestAdoptRefusesAStoredWorkflowAndAlwaysStartsInactive(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	handEdit(t, h, "wf-g", func(w *Workflow) { w.Nodes[1].Config = map[string]interface{}{"k": "changed"} })
	sha := shaHex(fileBytes(t, h, "wf-g"))
	if err := h.AdoptFile(ctx, "wf-g", "default", sha); err == nil {
		t.Fatal("a workflow that has rows was adopted")
	}
	if sqlConfig(t, s, "wf-g", "g-set") == "changed" || approvals(t, s, "wf-g") != 1 {
		t.Fatal("adopting acted as an outside-edit import")
	}
	dropFile(t, h, "wf-active-file", true)
	shown, _, _ := h.AdoptableFiles(ctx)
	if len(shown) != 1 {
		t.Fatalf("%+v", shown)
	}
	if err := h.AdoptFile(ctx, "wf-active-file", shown[0].ProfileID, shown[0].SHA); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-active-file"); rows.IsActive {
		t.Fatal("an adopted workflow started active")
	}
}

// An id that once had a workflow gets no special standing: after its rows are gone (deleted, or its
// profile deleted) a file recreated under it is just a file.
func TestARecreatedFileForAGoneWorkflowIsNotServed(t *testing.T) {
	ctx := context.Background()
	h, s := newHybridForTest(t)
	dropFile(t, h, "wf-gone", true)
	if got, _ := h.GetWorkflow(ctx, "wf-gone"); got == nil {
		t.Fatal("setup: the older version's file should be taken in")
	}
	if err := h.DeleteWorkflow(ctx, "wf-gone"); err != nil {
		t.Fatal(err)
	}
	dropFile(t, h, "wf-gone", true)
	if got, _ := h.GetWorkflow(ctx, "wf-gone"); got != nil {
		t.Fatalf("a recreated file was served: %+v", got)
	}
	// The same when the rows go some other way (a profile delete removes them directly).
	dropFile(t, h, "wf-gone2", true)
	if got, _ := h.GetWorkflow(ctx, "wf-gone2"); got != nil {
		t.Fatalf("a file that appeared after the claim was served: %+v", got)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-gone"); rows != nil {
		t.Fatal("rows came back")
	}
	for _, w := range mustList(t, h) {
		if w.ID == "wf-gone" || w.ID == "wf-gone2" {
			t.Fatalf("a listing shows %s", w.ID)
		}
	}
}

// Nothing is served from a file with no rows even when the claim never got recorded (a busy or
// broken database): the store fails closed.
func TestAFailedClaimServesNothingFromAFileWithNoRows(t *testing.T) {
	ctx := context.Background()
	h, _ := newHybridForTest(t)
	h.claimMu.Lock()
	h.claimed = true // pretend the claim ran: it did not, and there is no marker
	h.claimMu.Unlock()
	dropFile(t, h, "wf-x", true)
	if got, err := h.GetWorkflow(ctx, "wf-x"); err != nil || got != nil {
		t.Fatalf("served %+v (%v)", got, err)
	}
	for _, w := range mustList(t, h) {
		if w.ID == "wf-x" {
			t.Fatal("listed")
		}
	}
}

// An edit-import of a workflow with no rows returns before the write lock or the file is touched:
// the sync calls it for every file on every command.
func TestImportOfAFileWithNoRowsDoesNotTakeTheWriteLock(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	dropFile(t, h, "wf-nolock", true)
	release := holdWriteLock(t, s)
	defer release()
	quickly(t, "the import", func() {
		if res, err := h.ImportFile(ctx, "wf-nolock", nil); err != nil || res.Imported {
			t.Errorf("%+v %v", res, err)
		}
	})
}

// Many processes claim at once: the files are taken in once, the claim is recorded once, and a file
// that appears afterwards is not taken in by a later claim.
func TestLegacyClaimIsRecordedOnceByWhoeverGetsThereFirst(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	first, err := storage.NewDatabase(filepath.Join(root, "claim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := first.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	files, err := NewWorkflowFileStore(filepath.Join(root, "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	seed := NewHybridWorkflowStore(files, NewSQLiteWorkflowStore(first.DB))
	dropFile(t, seed, "wf-a", true)
	dropFile(t, seed, "wf-b", false)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := storage.NewDatabase(filepath.Join(root, "claim.db"))
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			_, err = NewHybridWorkflowStore(files, NewSQLiteWorkflowStore(db.DB)).ClaimLegacyFiles(ctx)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	dropFile(t, seed, "wf-c", true)
	if _, err := seed.ClaimLegacyFiles(ctx); err != nil { // a later claim does nothing
		t.Fatal(err)
	}
	var n, active int
	_ = first.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, legacyDoneKey).Scan(&n)
	if n != 1 {
		t.Fatalf("%d markers", n)
	}
	for id, want := range map[string]int{"wf-a": 1, "wf-b": 0} {
		if err := first.DB.QueryRow(`SELECT is_active FROM workflows WHERE id = ?`, id).Scan(&active); err != nil || active != want {
			t.Fatalf("%s: active %d (%v), want %d", id, active, err, want)
		}
	}
	if err := first.DB.QueryRow(`SELECT COUNT(*) FROM workflows WHERE id = 'wf-c'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a file that appeared after the claim got rows (%d, %v)", n, err)
	}
}

func mustList(t *testing.T, h *HybridWorkflowStore) []*Workflow {
	t.Helper()
	all, err := h.ListAllProfilesWorkflows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]*Workflow, len(all))
	for i := range all {
		out[i] = &all[i]
	}
	return out
}

// Listings say what the rows say: a file cannot move a workflow to another profile or switch it on,
// and a file whose content names another id than its name is ignored.
func TestListingsTakeOwnerAndActiveFlagFromTheRows(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	if _, err := s.db.Exec(`INSERT INTO profiles (id, name) VALUES ('victim', 'Victim')`); err != nil {
		t.Fatal(err)
	}
	handEdit(t, h, "wf-g", func(w *Workflow) { w.IsActive = true; w.ProfileID = "victim" })
	data, _ := os.ReadFile(filepath.Join(h.files.dir, "wf-g.json"))
	if err := os.WriteFile(filepath.Join(h.files.dir, "other-name.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	all, err := h.ListAllProfilesWorkflows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, w := range all {
		if w.ID != "wf-g" {
			continue
		}
		seen++
		if w.IsActive || w.ProfileID != "default" {
			t.Fatalf("the listing took the file's word: active=%v profile=%q", w.IsActive, w.ProfileID)
		}
	}
	if seen != 1 {
		t.Fatalf("wf-g listed %d times", seen)
	}
	if mine, _ := h.ListWorkflows(ctx, "victim"); len(mine) != 0 {
		t.Fatalf("the victim profile lists %d workflows", len(mine))
	}
}

// The pin is the whole sha256: a short prefix is refused, so a file cannot be swapped for one that
// shares the start of its hash.
func TestAdoptNeedsTheFullSha(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	dropFile(t, h, "wf-pin", false)
	shown, _, _ := h.AdoptableFiles(ctx)
	if len(shown) != 1 {
		t.Fatalf("%+v", shown)
	}
	a := shown[0]
	for _, short := range []string{a.SHA[:8], a.SHA[:12], a.SHA[:32], a.SHA[:63], ""} {
		if err := h.AdoptFile(ctx, "wf-pin", a.ProfileID, short); err == nil {
			t.Fatalf("adopted with a %d-character sha", len(short))
		}
	}
	if got, _ := s.GetWorkflow(ctx, "wf-pin"); got != nil {
		t.Fatal("a refused adoption created the workflow")
	}
	if err := h.AdoptFile(ctx, "wf-pin", a.ProfileID, a.SHA); err != nil {
		t.Fatal(err)
	}
}

// What the doctor shows of a file is bounded whatever the file contains: names are cut, lists are
// short, the preview is a few short lines, large files are not read, and only so many entries are
// looked at.
func TestAdoptableListingIsBounded(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := guardedRig(t)
	w := mirrorWF("wf-wide", "w-trig")
	long := strings.Repeat("T", 5000)
	w.Nodes = nil
	for i := 0; i < 40; i++ {
		w.Nodes = append(w.Nodes, WorkflowNode{ID: fmt.Sprintf("n%d", i), WorkflowID: "wf-wide", Type: fmt.Sprintf("trigger.%s%d", long, i), Name: long,
			Config: map[string]interface{}{"k": long}})
	}
	if err := h.files.SaveWorkflow(ctx, w); err != nil {
		t.Fatal(err)
	}
	// A big file and many junk files crowding the listing.
	if err := os.WriteFile(filepath.Join(h.files.dir, "wf-big.json"), []byte(`{"id":"wf-big","pad":"`+strings.Repeat("x", 2<<20)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		if err := os.WriteFile(filepath.Join(h.files.dir, fmt.Sprintf("z-junk-%03d.json", i)), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	shown, more, err := h.AdoptableFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if more < 200 {
		t.Fatalf("junk and big files were not counted as more: %d", more)
	}
	var wide *AdoptableFile
	for i := range shown {
		if shown[i].ID == "wf-wide" {
			wide = &shown[i]
		}
	}
	if wide == nil {
		t.Fatal("the wide file was not listed")
	}
	if len(wide.NodeTypes) > maxNamesShown+1 || len(wide.Triggers) > maxNamesShown+1 || len(wide.Preview) > maxPreviewNodes+1 {
		t.Fatalf("lists are not short: %d %d %d", len(wide.NodeTypes), len(wide.Triggers), len(wide.Preview))
	}
	for _, name := range append(append([]string{}, wide.NodeTypes...), wide.Triggers...) {
		if len(name) > maxNameShown+8 {
			t.Fatalf("a name of %d bytes", len(name))
		}
	}
	for _, line := range wide.Preview {
		if len(line) > maxPreviewChars+4*maxNameShown {
			t.Fatalf("a preview line of %d bytes", len(line))
		}
	}
}

// A fresh install has no profile yet. An agent drops a file in the workflows folder before the first
// profile exists: the claim is recorded at the first use anyway, takes nothing in, and the file is
// still not imported when the profile appears later; `doctor` lists it for an operator to adopt.
func TestAFileDroppedBeforeTheFirstProfileIsNotSweptInLater(t *testing.T) {
	ctx := context.Background()
	h, s := newHybridForTest(t)
	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(profilesWipeSQL); err != nil {
		t.Skip("cannot empty the profiles table here:", err)
	}
	dropFile(t, h, "wf-early", true) // an agent's file: active, in the folder before any profile exists
	if created, err := h.ClaimLegacyFiles(ctx); err != nil || len(created) != 0 {
		t.Fatalf("%v %v", created, err)
	}
	if markerCount(t, s) != 1 {
		t.Fatal("the claim of a store with no profile was not recorded: the window stays open until the first profile")
	}
	if _, err := s.db.Exec(`INSERT INTO profiles (id, name) VALUES ('default', 'default')`); err != nil {
		t.Fatal(err)
	}
	if created, err := h.ClaimLegacyFiles(ctx); err != nil || len(created) != 0 {
		t.Fatalf("a claim after the first profile took in %v (%v)", created, err)
	}
	if res, err := h.ImportFile(ctx, "wf-early", nil); err != nil || res.Imported {
		t.Fatalf("the file was imported after the profile appeared: %+v %v", res, err)
	}
	if wf, _ := h.GetWorkflow(ctx, "wf-early"); wf != nil {
		t.Fatal("the file is served as a workflow")
	}
	files, _, err := h.AdoptableFiles(ctx)
	if err != nil || len(files) != 1 || files[0].ID != "wf-early" {
		t.Fatalf("doctor does not offer the file for adoption: %+v %v", files, err)
	}
}

// An existing install has a profile: the files older versions left are still taken in at the first
// claim, active flag kept, as before. Recording the claim without a profile changed nothing here.
func TestAnExistingInstallStillTakesInItsFilesAtTheFirstClaim(t *testing.T) {
	ctx := context.Background()
	h, s := newHybridForTest(t) // the test schema has the profile 'default'
	dropFile(t, h, "wf-old-on", true)
	dropFile(t, h, "wf-old-off", false)
	created, err := h.ClaimLegacyFiles(ctx)
	if err != nil || len(created) != 2 || markerCount(t, s) != 1 {
		t.Fatalf("%d taken in, %v, markers=%d", len(created), err, markerCount(t, s))
	}
	on, _ := h.GetWorkflow(ctx, "wf-old-on")
	off, _ := h.GetWorkflow(ctx, "wf-old-off")
	if on == nil || !on.IsActive || off == nil || off.IsActive {
		t.Fatalf("the legacy files were not taken in with their flags: %+v %+v", on, off)
	}
}

// A listing opens only the files of workflows that have rows: a valid-looking file with no stored
// copy is never parsed (the file store caches what it parses, which makes that visible).
func TestListingDoesNotParseFilesWithNoRows(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := guardedRig(t)
	dropFile(t, h, "junk-no-rows", true)
	if _, err := h.ListAllProfilesWorkflows(ctx); err != nil {
		t.Fatal(err)
	}
	h.files.mu.Lock()
	defer h.files.mu.Unlock()
	for path := range h.files.cache {
		if strings.HasSuffix(path, "junk-no-rows.json") {
			t.Fatal("the listing parsed a file with no stored copy")
		}
	}
	if len(h.files.cache) == 0 {
		t.Fatal("the listing parsed nothing at all, not even the stored workflow")
	}
}

// A read-only store (the polled summary) writes nothing when it reads: it does not claim the
// legacy files and does not import an outside edit.
func TestAReadOnlyStoreWritesNothingWhenItReads(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	versions := versionCount(t, s, "wf-g")
	h.SetReadOnly()
	handEdit(t, h, "wf-g", func(w *Workflow) { w.Nodes[1].Config = map[string]interface{}{"k": "outside"} })
	got, err := h.GetWorkflow(ctx, "wf-g")
	if err != nil || got == nil || got.Nodes[1].Config["k"] == "outside" {
		t.Fatalf("a read-only read served the unreconciled file: %+v %v", got, err)
	}
	if versionCount(t, s, "wf-g") != versions || sqlConfig(t, s, "wf-g", "g-set") == "outside" {
		t.Fatal("a read wrote rows")
	}
	fresh, s2 := newHybridForTest(t)
	fresh.SetReadOnly()
	dropFile(t, fresh, "wf-old", true)
	if got, _ := fresh.GetWorkflow(ctx, "wf-old"); got != nil {
		t.Fatalf("a read-only store took in a legacy file: %+v", got)
	}
	if markerCount(t, s2) != 0 {
		t.Fatal("a read-only store recorded the claim")
	}
}

// sparseJunk writes a file of the largest size that may be shown, beginning like a JSON object and
// not a workflow, with the given modification time. It takes no disk space to speak of.
func sparseJunk(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxAdoptableBytes); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// Files that cannot be adopted do not use up the listing: invalid files of the largest allowed
// size that look like workflows at a glance, however many, however dated, do not hide the real one
// and cost a bounded number of full reads.
func TestManyInvalidFilesDoNotHideTheRealOne(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := guardedRig(t)
	dir := h.files.dir
	future := time.Now().Add(24 * time.Hour)
	for i := 0; i < 230; i++ {
		sparseJunk(t, filepath.Join(dir, fmt.Sprintf("000-big-%03d.json", i)), future.Add(time.Duration(i)*time.Second))
	}
	dropFile(t, h, "zzz-real", false)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "zzz-real.json"), old, old); err != nil {
		t.Fatal(err)
	}
	l, err := h.AdoptableListing(ctx)
	if err != nil || len(l.Shown) != 1 || l.Shown[0].ID != "zzz-real" || l.More != 230 {
		t.Fatalf("shown %d, more %d, %v", len(l.Shown), l.More, err)
	}
	if max := int(maxAdoptableBudget/maxAdoptableBytes) + 1; l.Reads > max {
		t.Fatalf("%d full reads of 1 MiB files, want at most %d", l.Reads, max)
	}
}

// Valid-looking files that fill the listing hide nobody silently, whatever times they carry: files
// are taken from both ends of the order, and both ends of what is held back are named.
func TestFilesBeyondTheListingAreNamed(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := guardedRig(t)
	dir := h.files.dir
	future := time.Now().Add(24 * time.Hour)
	for i := 0; i < 60; i++ {
		dropFile(t, h, fmt.Sprintf("decoy-%02d", i), false)
		ts := future.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(filepath.Join(dir, fmt.Sprintf("decoy-%02d.json", i)), ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	dropFile(t, h, "real-oldest", false)
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "real-oldest.json"), old, old); err != nil {
		t.Fatal(err)
	}
	l, err := h.AdoptableListing(ctx)
	if err != nil || len(l.Shown) != maxAdoptableShown || l.More != 41 || !l.Incomplete {
		t.Fatalf("shown %d, more %d, incomplete %v, %v", len(l.Shown), l.More, l.Incomplete, err)
	}
	found := false
	for _, a := range l.Shown {
		found = found || a.ID == "real-oldest"
	}
	if !found {
		t.Fatal("the oldest file, dated far back, was not shown")
	}
	if len(l.HiddenIDs) != maxHiddenNamed {
		t.Fatalf("named %d, want %d", len(l.HiddenIDs), maxHiddenNamed)
	}
	// Both ends of what was held back are named: the first held back and the innermost, which a
	// listing that only named the first few would leave out.
	inner := false
	for _, id := range l.HiddenIDs {
		for _, mid := range []string{"decoy-28", "decoy-29", "decoy-30", "decoy-31", "decoy-32"} {
			inner = inner || id == mid
		}
	}
	if !inner {
		t.Fatalf("only the first files held back are named: %v", l.HiddenIDs)
	}
	// A file in the middle is held back and not named: the listing says it is incomplete, and the
	// file can still be reached by its id.
	dropFile(t, h, "real-middle", false)
	mid := future.Add(30*time.Second + 500*time.Millisecond)
	if err := os.Chtimes(filepath.Join(dir, "real-middle.json"), mid, mid); err != nil {
		t.Fatal(err)
	}
	if l, _ = h.AdoptableListing(ctx); !l.Incomplete {
		t.Fatal("a listing that holds files back without naming them says it is complete")
	}
	if a, why, err := h.AdoptableFile(ctx, "real-middle"); err != nil || a == nil {
		t.Fatalf("the file cannot be reached by its id: %q %v", why, err)
	}
}

// A folder with a huge number of entries costs a bounded amount of work: names beyond the cap are
// not looked at, and the listing says it stopped.
func TestAHugeFolderIsListedWithBoundedWork(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := guardedRig(t)
	for i := 0; i < maxAdoptableEntries+500; i++ {
		if err := os.WriteFile(filepath.Join(h.files.dir, fmt.Sprintf("m-%05d.json", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	l, err := h.AdoptableListing(ctx)
	if err != nil || !l.Truncated || len(l.Shown) != 0 || time.Since(start) > 20*time.Second {
		t.Fatalf("truncated=%v shown=%d after %v: %v", l.Truncated, len(l.Shown), time.Since(start), err)
	}
}

// The listing counts what a file holds without quoting it: decoy keys cannot push the part that
// matters out of view, and anything shortened is flagged.
func TestAdoptableFileCountsWhatItHoldsAndFlagsCuts(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := guardedRig(t)
	cfg := map[string]interface{}{"url": "${secret.token}", "command": "run"}
	for i := 0; i < 30; i++ {
		cfg[fmt.Sprintf("a%02d", i)] = strings.Repeat("x", 20)
	}
	w := mirrorWF("wf-big", "wf-big-trig", "wf-big-set")
	w.Settings = map[string]interface{}{ErrorWorkflowSetting: "wf-err"}
	w.Nodes[1].Config = cfg
	for i := 0; i < maxPreviewNodes+2; i++ {
		w.Nodes = append(w.Nodes, WorkflowNode{ID: fmt.Sprintf("extra-%d", i), WorkflowID: "wf-big", Type: "core.set", Name: "e", Config: map[string]interface{}{}})
	}
	if err := h.files.SaveWorkflow(ctx, w); err != nil {
		t.Fatal(err)
	}
	shown, _, err := h.AdoptableFiles(ctx)
	if err != nil || len(shown) != 1 {
		t.Fatalf("%+v %v", shown, err)
	}
	a := shown[0]
	if a.Nodes != len(w.Nodes) || !a.Cut || !a.HasSettings || !a.ErrorWorkflow || a.Refs != 1 || a.ConfigBytes < 600 {
		t.Fatalf("counts: %+v", a)
	}
}

// Template markers are counted once each, whatever they contain; this is a count of syntax.
func TestCountRefsCountsEachMarkerOnce(t *testing.T) {
	for text, want := range map[string]int{
		"":                          0,
		"${secret.token}":           1,
		"{{ $vars.x }}":             1,
		"{{ 1 + 1 }}":               1,
		"@secret:api-key":           1,
		"uses secret.password here": 1,
		"${a} and {{b}} and @secret:c and secret.d": 4,
		"plain text": 0,
	} {
		if got := countRefs(text); got != want {
			t.Errorf("countRefs(%q) = %d, want %d", text, got, want)
		}
	}
}

// The folder is never created by opening it for reading.
func TestOpenWorkflowFileStoreDoesNotCreateTheFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workflows")
	if _, err := OpenWorkflowFileStore(dir); !os.IsNotExist(err) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the folder was created")
	}
}

// The drift scan stops when it is cancelled and says so, and does not parse a file whose bytes are
// what the database last reconciled.
func TestFileDriftScanIsBoundedAndSkipsReconciledFiles(t *testing.T) {
	h, s, _, _ := guardedRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := h.FileDriftScan(ctx); err == nil && !r.Incomplete {
		t.Fatalf("a cancelled scan ran to the end: %+v", r)
	}
	// The file no longer parses, yet its bytes are what the database recorded: it is not read as a workflow.
	junk := []byte("not json at all")
	if err := os.WriteFile(h.files.dir+"/wf-g.json", junk, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE workflow_file_state SET file_sha = ? WHERE workflow_id = 'wf-g'`, shaHex(junk)); err != nil {
		t.Fatal(err)
	}
	r, err := h.FileDriftScan(context.Background())
	if err != nil || len(r.Unreadable) != 0 || len(r.Pending) != 0 || r.Incomplete {
		t.Fatalf("%+v %v", r, err)
	}
}

// A file that grows between being measured and being read is not read past the size that was paid
// for: the bytes read stay within the budget, and the grown file is held back.
func TestAFileThatGrowsAfterItWasMeasuredIsNotReadInFull(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := guardedRig(t)
	path := filepath.Join(h.files.dir, "grower.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	adoptableAfterStat = func(id string) {
		if id == "grower" {
			_ = os.Truncate(path, maxAdoptableBytes)
		}
	}
	t.Cleanup(func() { adoptableAfterStat = nil })
	l, err := h.AdoptableListing(ctx)
	if err != nil || len(l.Shown) != 0 || l.More != 1 {
		t.Fatalf("shown %d, more %d, %v", len(l.Shown), l.More, err)
	}
	if l.BytesRead > 2 {
		t.Fatalf("%d bytes read of a file that was measured at 1 byte", l.BytesRead)
	}
}

// Names that are not candidates (not .json, backups, workflows with a stored copy) do not count
// toward the cap: a folder full of them, with real files among them, is listed completely, and some
// of the real files lie beyond the cap in directory order.
func TestNamesThatAreNotCandidatesDoNotUseUpTheCap(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := guardedRig(t)
	dir := h.files.dir
	var names []string
	for i := 0; i < maxAdoptableEntries+300; i++ {
		names = append(names, fmt.Sprintf("n-%05d.txt", i), fmt.Sprintf("b-%05d.ai-backup.json", i))
	}
	// All real files come after the candidate cap in the fixed stream. Only
	// these sixteen files exist on disk; no filesystem ordering can skip coverage.
	want := map[string]bool{}
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("real-%02d", i)
		dropFile(t, h, id, false)
		names = append(names, id+".json")
		want[id] = true
	}
	oldRead, oldStat := adoptableReaddirnames, adoptableAfterStat
	t.Cleanup(func() { adoptableReaddirnames = oldRead; adoptableAfterStat = oldStat })
	cursor, batches, reads := 0, 0, 0
	adoptableReaddirnames = func(f *os.File, n int) ([]string, error) {
		if f.Name() != dir || n != 256 {
			t.Fatalf("readdir %s batch %d", f.Name(), n)
		}
		batches++
		if cursor == len(names) {
			return nil, io.EOF
		}
		end := cursor + n
		if end > len(names) {
			end = len(names)
		}
		out := names[cursor:end]
		cursor = end
		return out, nil
	}
	adoptableAfterStat = func(id string) {
		if !want[id] {
			t.Fatalf("read an unexpected candidate %q", id)
		}
		reads++
	}
	l, err := h.AdoptableListing(ctx)
	if err != nil || l.Truncated || l.Incomplete || len(l.Shown) != len(want) || reads != len(want) || cursor != len(names) || batches < 2 {
		t.Fatalf("truncated=%v incomplete=%v shown=%d reads=%d cursor=%d/%d batches=%d: %v", l.Truncated, l.Incomplete, len(l.Shown), reads, cursor, len(names), batches, err)
	}
	for _, file := range l.Shown {
		if !want[file.ID] {
			t.Fatalf("unexpected or duplicate shown id %q", file.ID)
		}
		delete(want, file.ID)
	}
	if len(want) != 0 {
		t.Fatalf("not shown: %v", want)
	}
}
