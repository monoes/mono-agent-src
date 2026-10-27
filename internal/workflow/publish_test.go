package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

func openIntentCount(t *testing.T, s *SQLiteWorkflowStore) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM workflow_publications`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A second change to a workflow whose first change is still open waits and then refuses.
func TestPublishRefusesWhileAnotherChangeIsOpen(t *testing.T) {
	ctx := context.Background()
	h, s, hash, nodes := guardedRig(t)
	if _, err := s.db.Exec(`INSERT INTO workflow_publications
		(workflow_id, kind, state, owner_pid, started_at) VALUES ('wf-g', 'save', 'pending', ?, ?)`,
		os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	old := publicationWait
	publicationWait = 150 * time.Millisecond
	t.Cleanup(func() { publicationWait = old })
	err := h.SaveNodesGuarded(ctx, "wf-g", nodes, SaveGuard{ExpectHash: hash})
	if !errors.Is(err, ErrPublicationBusy) {
		t.Fatalf("%v", err)
	}
	if got := sqlConfig(t, s, "wf-g", "g-set"); got == "edited" {
		t.Fatal("the refused change was written")
	}
}

// An abandoned intent is always rolled back, even when the rows' definition equals its target: an
// intent that survives proves the commit did not happen.
func TestRecoveryRollsBackEvenWhenTheRowsHoldTheTarget(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	cur, _ := h.GetWorkflow(ctx, "wf-g")
	def, _ := definitionHash(cur)
	prev := fileBytes(t, h, "wf-g")
	proposed := []byte("{}")
	if err := os.WriteFile(h.files.dir+"/wf-g.json", proposed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO workflow_publications
		(workflow_id, kind, state, target_hash, target_sha, prev_sha, prev_bytes, prev_existed, owner_pid, started_at)
		VALUES ('wf-g', 'save', 'pending', ?, ?, ?, ?, 1, 0, ?)`, def, shaHex(proposed), shaHex(prev), prev, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if n, err := h.RecoverPublications(ctx); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if !bytes.Equal(fileBytes(t, h, "wf-g"), prev) || openIntentCount(t, s) != 0 {
		t.Fatal("the proposed file was kept")
	}
}

// An intent whose owner is alive and recent is left alone by recovery.
func TestRecoveryLeavesALiveOwnersChangeAlone(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	if _, err := s.db.Exec(`INSERT INTO workflow_publications
		(workflow_id, kind, state, owner_pid, started_at) VALUES ('wf-g', 'save', 'pending', ?, ?)`,
		os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	before := fileBytes(t, h, "wf-g")
	_, _ = h.RecoverPublications(ctx)
	if openIntentCount(t, s) != 1 || !bytes.Equal(before, fileBytes(t, h, "wf-g")) {
		t.Fatal("recovery touched a change that is still being made")
	}
}

// A failed commit puts the file back and leaves no intent.
func TestPublishLeavesNoIntentAfterAFailure(t *testing.T) {
	ctx := context.Background()
	h, s, hash, nodes := guardedRig(t)
	guardedCommitFault = func() error { return errors.New("commit failed") }
	if err := h.SaveNodesGuarded(ctx, "wf-g", nodes, SaveGuard{ExpectHash: hash}); err == nil {
		t.Fatal("expected a failure")
	}
	if openIntentCount(t, s) != 0 {
		t.Fatal("an intent was left behind")
	}
}

// A save that revokes approvals does it in the commit that writes the rows: a hook that refuses
// the commit keeps the approvals, and a commit that goes through removes them.
func TestSaveWorkflowRevokingRemovesApprovalsInTheSameCommit(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	cur, _ := h.GetWorkflow(ctx, "wf-g")
	refuse := WithTxHook(ctx, func(context.Context, *sql.Tx) error { return errors.New("no") })
	if err := h.SaveWorkflowRevoking(refuse, cur, ""); err == nil {
		t.Fatal("the hook should have refused")
	}
	if approvals(t, s, "wf-g") != 1 {
		t.Fatal("a refused save revoked approvals")
	}
	if err := h.SaveWorkflowRevoking(ctx, cur, ""); err != nil {
		t.Fatal(err)
	}
	if approvals(t, s, "wf-g") != 0 {
		t.Fatal("approvals survived")
	}
}

// A node edit whose file cannot be written is an error, and the rows keep the old nodes: the
// file step used to be dropped silently after the rows had changed.
func TestNodeEditWithAFailingFileIsAnErrorAndChangesNothing(t *testing.T) {
	ctx := context.Background()
	h, s, _, nodes := guardedRig(t)
	guardedFileFault = func() error { return errors.New("disk full") }
	err := h.SaveWorkflowNodes(ctx, "wf-g", nodes)
	if !errors.Is(err, ErrFileSyncFailed) {
		t.Fatalf("%v", err)
	}
	if sqlConfig(t, s, "wf-g", "g-set") == "edited" {
		t.Fatal("the rows changed although the file did not")
	}
	err = h.SaveWorkflowConnections(ctx, "wf-g", nil)
	if !errors.Is(err, ErrFileSyncFailed) {
		t.Fatalf("%v", err)
	}
	if got, _ := s.GetWorkflow(ctx, "wf-g"); len(got.Connections) != 1 {
		t.Fatal("the connections changed although the file did not")
	}
}

// A node edit lands in the file and the rows, and records its version in the same commit.
func TestNodeEditPublishesFileRowsAndVersionTogether(t *testing.T) {
	ctx := context.Background()
	h, s, _, nodes := guardedRig(t)
	before, _ := h.GetWorkflow(ctx, "wf-g")
	if err := h.SaveWorkflowNodes(ctx, "wf-g", nodes); err != nil {
		t.Fatal(err)
	}
	got, _ := h.GetWorkflow(ctx, "wf-g")
	if got.Nodes[1].Config["k"] != "edited" || sqlConfig(t, s, "wf-g", "g-set") != "edited" {
		t.Fatal("file and rows disagree")
	}
	if got.Version != before.Version+1 {
		t.Fatalf("version %d -> %d", before.Version, got.Version)
	}
	if v, _ := h.GetWorkflowVersion(ctx, "wf-g", got.Version); v == nil {
		t.Fatal("no version recorded")
	}
}

// An operator edit records its approval in the commit that writes the rows; a refused commit
// records nothing.
func TestWithApprovalRecordsTheNewDefinitionInTheCommit(t *testing.T) {
	ctx := context.Background()
	h, s, _, nodes := guardedRig(t)
	refuse := WithApproval(WithTxHook(ctx, func(context.Context, *sql.Tx) error { return errors.New("no") }))
	if err := h.SaveWorkflowNodes(refuse, "wf-g", nodes); err == nil {
		t.Fatal("the hook should have refused")
	}
	if approvals(t, s, "wf-g") != 1 {
		t.Fatalf("a refused edit changed approvals: %d", approvals(t, s, "wf-g"))
	}
	if err := h.SaveWorkflowNodes(WithApproval(ctx), "wf-g", nodes); err != nil {
		t.Fatal(err)
	}
	cur, _ := h.GetWorkflow(ctx, "wf-g")
	hash, _ := definitionHash(cur)
	if ok, _ := h.DefinitionApproved(ctx, "wf-g", hash); !ok {
		t.Fatal("the edited definition was not approved")
	}
	// Without it the edit is left for a person to approve.
	nodes[1].Config = map[string]interface{}{"k": "again"}
	if err := h.SaveWorkflowNodes(ctx, "wf-g", nodes); err != nil {
		t.Fatal(err)
	}
	cur, _ = h.GetWorkflow(ctx, "wf-g")
	hash, _ = definitionHash(cur)
	if ok, _ := h.DefinitionApproved(ctx, "wf-g", hash); ok {
		t.Fatal("an edit without WithApproval was approved")
	}
}

// Removing a node takes its connections out of the file as well as the rows.
func TestRemovingANodeTakesItsConnectionsFromTheFileToo(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	cur, _ := h.GetWorkflow(ctx, "wf-g")
	if err := h.SaveWorkflowNodes(ctx, "wf-g", cur.Nodes[:1]); err != nil {
		t.Fatal(err)
	}
	f, _ := h.files.GetWorkflow(ctx, "wf-g")
	r, _ := s.GetWorkflow(ctx, "wf-g")
	if len(f.Connections) != 0 || len(r.Connections) != 0 {
		t.Fatalf("file %d rows %d connections left", len(f.Connections), len(r.Connections))
	}
}

// The transactions of a publication flush to disk before they return; the connection goes back to
// the pool as it was.
func TestPublicationTransactionsSyncToDisk(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	var before int
	_ = s.db.QueryRow(`PRAGMA synchronous`).Scan(&before)
	tx, done, err := h.beginDurable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var inside int
	if err := tx.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&inside); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	done()
	var after int
	_ = s.db.QueryRow(`PRAGMA synchronous`).Scan(&after)
	if inside != 2 || after != before {
		t.Fatalf("synchronous: inside %d (want 2 = FULL), before %d, after %d", inside, before, after)
	}
}

// BenchmarkPublish measures one guarded save (intent, file, commit) on a scratch database.
func BenchmarkPublish(b *testing.B) {
	ctx := context.Background()
	files, err := NewWorkflowFileStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	db, err := storage.NewDatabase(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		b.Fatal(err)
	}
	sqlStore := NewSQLiteWorkflowStore(db.DB)
	h := NewHybridWorkflowStore(files, sqlStore)
	if err := h.SaveWorkflow(ctx, mirrorWF("wf-b", "b-trig", "b-set")); err != nil {
		b.Fatal(err)
	}
	cur, _ := h.GetWorkflow(ctx, "wf-b")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nodes := append([]WorkflowNode(nil), cur.Nodes...)
		nodes[1].Config = map[string]interface{}{"k": i}
		if err := h.SaveWorkflowNodes(ctx, "wf-b", nodes); err != nil {
			b.Fatal(err)
		}
	}
}

// A change that starts while files are being read makes the read run again, so a file replaced
// before the read opened it is never trusted.
func TestStableFileReadRepeatsWhenAChangeStartsDuringIt(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	calls := 0
	var last map[string]bool
	err := h.stableFileRead(ctx, func(open map[string]bool) error {
		calls++
		last = open
		if calls == 1 {
			_, err := s.db.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
				VALUES ('wf-g', 'save', 'pending', ?, ?)`, os.Getpid(), time.Now().UTC())
			return err
		}
		return nil
	})
	if err != nil || calls != 2 || !last["wf-g"] {
		t.Fatalf("calls %d open %v err %v", calls, last, err)
	}
}

// A guarded save with no expected hash never matches: it is refused for an existing workflow, and
// a missing workflow is reported instead of crashing.
func TestGuardedSaveWithAnEmptyHashIsRefused(t *testing.T) {
	ctx := context.Background()
	h, s, _, nodes := guardedRig(t)
	if err := h.SaveNodesGuarded(ctx, "wf-g", nodes, SaveGuard{}); !errors.Is(err, ErrChangedSincePreview) {
		t.Fatalf("existing workflow: %v", err)
	}
	if sqlConfig(t, s, "wf-g", "g-set") == "edited" {
		t.Fatal("the refused edit was written")
	}
	if err := h.SaveNodesGuarded(ctx, "no-such", nodes, SaveGuard{}); err == nil {
		t.Fatal("a missing workflow must be an error")
	}
}

// Connections saved with empty handles are normalized before the version is worked out: saving the
// same workflow again is not a new version, and an operator save approves what was published.
func TestSaveNormalizesConnectionHandlesBeforeHashingAndVersioning(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	w := mirrorWF("wf-h", "h-trig", "h-set")
	w.Connections[0].SourceHandle, w.Connections[0].TargetHandle = "", ""
	if err := h.SaveWorkflow(WithApproval(ctx), w); err != nil {
		t.Fatal(err)
	}
	got, _ := h.GetWorkflow(ctx, "wf-h")
	hash, _ := definitionHash(got)
	if ok, _ := h.DefinitionApproved(ctx, "wf-h", hash); !ok {
		t.Fatal("the published definition is not the one that was approved")
	}
	again := mirrorWF("wf-h", "h-trig", "h-set")
	again.Connections[0].SourceHandle, again.Connections[0].TargetHandle = "", ""
	if err := h.SaveWorkflow(ctx, again); err != nil {
		t.Fatalf("saving the same workflow again: %v", err)
	}
	if versionCount(t, s, "wf-h") != 1 {
		t.Fatalf("%d versions for one definition", versionCount(t, s, "wf-h"))
	}
}

// The rows' definition is read in one transaction: while saves keep changing both the description
// and a node, a reader never sees one from before and the other from after.
func TestCommittedWorkflowIsNeverAMixOfTwoSaves(t *testing.T) {
	ctx := context.Background()
	h, _, _, _ := guardedRig(t)
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			cur, err := h.committedWorkflow(ctx, "wf-g")
			if err != nil || cur == nil {
				done <- err
				return
			}
			cur.Description = fmt.Sprint(i)
			cur.Nodes[1].Config = map[string]interface{}{"k": fmt.Sprint(i)}
			if err := h.SaveWorkflow(ctx, cur); err != nil {
				done <- err
				return
			}
		}
	}()
	for i := 0; i < 400; i++ {
		w, err := h.committedWorkflow(ctx, "wf-g")
		if err != nil || w == nil {
			t.Fatalf("%v", err)
		}
		if w.Description != "" && w.Nodes[1].Config["k"] != w.Description && w.Nodes[1].Config["k"] != "v" {
			t.Fatalf("a mix of two saves: description %q, node %v", w.Description, w.Nodes[1].Config["k"])
		}
		if w.Description != "" && w.Nodes[1].Config["k"] == "v" {
			t.Fatalf("description %q with the node of an earlier save", w.Description)
		}
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A staging failure leaves no temp file behind (a full disk would otherwise be filled by retries).
func TestStagingFailureLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	orig := stageWriteHook
	t.Cleanup(func() { stageWriteHook = orig })
	stageWriteHook = func(f *os.File) error { return errors.New("disk full") }
	if _, err := stageFile(filepath.Join(dir, "wf.json"), []byte("{}")); err == nil {
		t.Fatal("the failure was swallowed")
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

// Missing node ids are generated before the file and the rows are written (so both carry the same
// ones), and repeated ids are refused before anything is recorded.
func TestPublishNormalizesNodeIDsAndRefusesRepeats(t *testing.T) {
	ctx := context.Background()
	h, s, _, nodes := guardedRig(t)
	nodes = append(nodes, WorkflowNode{Type: "core.set", Name: "new", Config: map[string]interface{}{"k": "n"}})
	if err := h.SaveWorkflowNodes(ctx, "wf-g", nodes); err != nil {
		t.Fatal(err)
	}
	f, _ := h.files.GetWorkflow(ctx, "wf-g")
	r, _ := s.GetWorkflow(ctx, "wf-g")
	if len(f.Nodes) != 3 || len(r.Nodes) != 3 || f.Nodes[2].ID == "" {
		t.Fatalf("file %d rows %d nodes, id %q", len(f.Nodes), len(r.Nodes), f.Nodes[2].ID)
	}
	fh, _ := definitionHash(f)
	rh, _ := definitionHash(r)
	if fh != rh {
		t.Fatal("file and rows disagree after generating an id")
	}
	dup := append([]WorkflowNode(nil), r.Nodes...)
	dup[1].ID = dup[0].ID
	before := fileBytes(t, h, "wf-g")
	if err := h.SaveWorkflowNodes(ctx, "wf-g", dup); !errors.Is(err, ErrInvalidGraph) {
		t.Fatalf("%v", err)
	}
	if !bytes.Equal(before, fileBytes(t, h, "wf-g")) || openIntentCount(t, s) != 0 {
		t.Fatal("a refused definition touched the file or left an intent")
	}
}

// A definition whose file would be larger than every reader accepts is refused before an intent is
// recorded; an oversized file that is already there under an open change is left to the operator,
// who can replace it from the rows.
func TestOversizedFilesAreRefusedAndRecoverable(t *testing.T) {
	ctx := context.Background()
	h, s, _, nodes := guardedRig(t)
	huge := strings.Repeat("x", maxWorkflowFileBytes+1)
	big := append([]WorkflowNode(nil), nodes...)
	big[1].Config = map[string]interface{}{"k": huge}
	before := fileBytes(t, h, "wf-g")
	if err := h.SaveWorkflowNodes(ctx, "wf-g", big); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("%v", err)
	}
	if !bytes.Equal(before, fileBytes(t, h, "wf-g")) || openIntentCount(t, s) != 0 {
		t.Fatal("the refusal changed the file or left an intent")
	}
	// An oversized file under an abandoned change.
	if err := os.WriteFile(filepath.Join(h.files.dir, "wf-g.json"), []byte(huge), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('wf-g', 'save', 'pending', 0, ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.RecoverPublications(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.db.QueryRow(`SELECT state FROM workflow_publications`).Scan(&state); err != nil || state != "unresolved" {
		t.Fatalf("state %q %v", state, err)
	}
	if err := h.ResolveUnresolved(ctx, "wf-g", KeepRows); err != nil {
		t.Fatal(err)
	}
	if got, err := h.GetWorkflow(ctx, "wf-g"); err != nil || got == nil || openIntentCount(t, s) != 0 {
		t.Fatalf("after resolving: %v %v", got, err)
	}
}

// An operator's edit is approved only if the workflow is still what the operator read: a node added
// by someone else in between makes the edit fail, instead of approving that node with it.
func TestApprovedEditRefusesAWorkflowThatChangedSinceItWasRead(t *testing.T) {
	ctx := context.Background()
	h, s, readHash, nodes := guardedRig(t)
	// Someone else adds a node after the operator read the workflow.
	other := append([]WorkflowNode(nil), nodes...)
	other = append(other, WorkflowNode{ID: "g-sneaky", Type: "core.set", Name: "sneaky", Config: map[string]interface{}{"k": "x"}})
	if err := h.SaveWorkflowNodes(ctx, "wf-g", other); err != nil {
		t.Fatal(err)
	}
	err := h.SaveWorkflowConnections(WithExpectHash(WithApproval(ctx), readHash), "wf-g", nil)
	if !errors.Is(err, ErrChangedSincePreview) {
		t.Fatalf("%v", err)
	}
	cur, _ := h.GetWorkflow(ctx, "wf-g")
	hash, _ := definitionHash(cur)
	if ok, _ := h.DefinitionApproved(ctx, "wf-g", hash); ok {
		t.Fatal("the other change was approved")
	}
	fresh, _ := ReviewHash(cur)
	if err := h.SaveWorkflowConnections(WithExpectHash(WithApproval(ctx), fresh), "wf-g", nil); err != nil {
		t.Fatal(err)
	}
	cur, _ = h.GetWorkflow(ctx, "wf-g")
	hash, _ = definitionHash(cur)
	if ok, _ := h.DefinitionApproved(ctx, "wf-g", hash); !ok {
		t.Fatal("the edit the operator read was not approved")
	}
	_ = s
}

// Ids that are not safe file names on every platform are refused, not turned into odd files.
func TestFilePathRefusesPortableTroubleNames(t *testing.T) {
	files, err := NewWorkflowFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"nul", "CON", "com1", "Lpt9.txt", "a:b", "a/b", `a\b`, "..", ""} {
		if _, err := files.filePath(id); err == nil {
			t.Errorf("id %q was accepted", id)
		}
	}
	for _, id := range []string{"wf-1", "hello-v1", "5f3c1d7e-aaaa-bbbb-cccc-0123456789ab", "com10", "console"} {
		if _, err := files.filePath(id); err != nil {
			t.Errorf("id %q was refused: %v", id, err)
		}
	}
}

// holdWriteLock takes SQLite's write lock in another transaction until release is called.
func holdWriteLock(t *testing.T, s *SQLiteWorkflowStore) (release func()) {
	t.Helper()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE workflow_publications SET state = state`); err != nil {
		t.Fatal(err)
	}
	return func() { _ = tx.Rollback() }
}

func quickly(t *testing.T, what string, fn func()) {
	t.Helper()
	start := time.Now()
	fn()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("%s waited %v for the write lock", what, d)
	}
}

// Looking at a change that is still being made does not take the write lock, so readers do not queue
// behind a writer.
func TestRecoveryDoesNotLockForALiveChange(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	if _, err := s.db.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('wf-g', 'save', 'pending', ?, ?)`, os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	release := holdWriteLock(t, s)
	defer release()
	quickly(t, "recovery", func() { _, _ = h.RecoverPublications(ctx) })
}

// A file the importer keeps declining is not retried under the write lock on every read.
func TestADeclinedFileIsNotRetriedOnEveryRead(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	if err := os.WriteFile(filepath.Join(h.files.dir, "wf-g.json"), []byte(`{"id":"wf-g","profile_id":"default","nodes":[{"id":"a"},{"id":"a"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := h.GetWorkflow(ctx, "wf-g"); err != nil || got == nil {
		t.Fatalf("%v %v", got, err)
	}
	release := holdWriteLock(t, s)
	defer release()
	quickly(t, "the second read", func() { _, _ = h.GetWorkflow(ctx, "wf-g") })
}

// A caller that gives up (cancelled context) mid-change still has its file put back and its intent
// removed.
func TestACancelledCallerStillRollsBack(t *testing.T) {
	h, s, hash, nodes := guardedRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	guardedCommitFault = func() error { cancel(); return errors.New("commit failed") }
	before := fileBytes(t, h, "wf-g")
	if err := h.SaveNodesGuarded(ctx, "wf-g", nodes, SaveGuard{ExpectHash: hash}); err == nil {
		t.Fatal("expected a failure")
	}
	if openIntentCount(t, s) != 0 || !bytes.Equal(before, fileBytes(t, h, "wf-g")) {
		t.Fatal("the cancelled change left its intent or its file behind")
	}
}

// Temp files a crashed writer left behind are swept once they are old; a recent one (a writer that
// is still working) is left alone.
func TestSweepStaleTempFiles(t *testing.T) {
	h, _, _, _ := guardedRig(t)
	old := filepath.Join(h.files.dir, ".wf-g.json.111.tmp")
	fresh := filepath.Join(h.files.dir, ".wf-g.json.222.tmp")
	keep := filepath.Join(h.files.dir, "wf-g.json")
	for _, f := range []string{old, fresh} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if n := h.SweepStaleTemp(); n != 1 {
		t.Fatalf("swept %d", n)
	}
	for f, want := range map[string]bool{old: false, fresh: true, keep: true} {
		if _, err := os.Stat(f); (err == nil) != want {
			t.Errorf("%s present=%v, want %v", filepath.Base(f), err == nil, want)
		}
	}
}

// The quarantine keeps the newest copies of a workflow, not an unbounded number of them.
func TestQuarantineKeepsOnlyTheNewestCopies(t *testing.T) {
	h, _, _, _ := guardedRig(t)
	for i := 0; i < quarantineKeep+5; i++ {
		if _, err := h.files.quarantine("wf-g", []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	kept, _ := filepath.Glob(filepath.Join(h.files.quarantineDir(), "wf-g.*.json"))
	if len(kept) != quarantineKeep {
		t.Fatalf("%d copies kept", len(kept))
	}
}

// A database too busy to give the write lock within its own timeout is retried by Publish like a
// change in progress, instead of failing the caller at once.
func TestPublishRetriesWhenTheDatabaseIsBusy(t *testing.T) {
	ctx := context.Background()
	h, s, hash, nodes := guardedRig(t)
	calls := 0
	guardedCommitFault = func() error {
		calls++
		if calls == 1 {
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	}
	if err := h.SaveNodesGuarded(ctx, "wf-g", nodes, SaveGuard{ExpectHash: hash}); err != nil {
		t.Fatalf("a busy first attempt was not retried: %v", err)
	}
	if calls != 2 || sqlConfig(t, s, "wf-g", "g-set") != "edited" {
		t.Fatalf("calls %d", calls)
	}
}

// A workflow with an unresolved change refuses a new one at once and says where to go, instead of
// waiting out the retry window and reporting "busy".
func TestUnresolvedChangeIsReportedAtOnce(t *testing.T) {
	ctx := context.Background()
	h, s, hash, nodes := guardedRig(t)
	if _, err := s.db.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('wf-g', 'save', 'unresolved', 0, ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := h.SaveNodesGuarded(ctx, "wf-g", nodes, SaveGuard{ExpectHash: hash})
	if !errors.Is(err, ErrPublicationUnresolved) || !strings.Contains(err.Error(), "doctor") {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("an unresolved change was retried instead of reported")
	}
}

// After the retry window a busy answer is still recognizable as busy.
func TestExhaustedRetryStillSaysBusy(t *testing.T) {
	ctx := context.Background()
	h, s, hash, nodes := guardedRig(t)
	if _, err := s.db.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('wf-g', 'save', 'pending', ?, ?)`, os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	old := publicationWait
	publicationWait = 150 * time.Millisecond
	t.Cleanup(func() { publicationWait = old })
	if err := h.SaveNodesGuarded(ctx, "wf-g", nodes, SaveGuard{ExpectHash: hash}); !errors.Is(err, ErrPublicationBusy) {
		t.Fatalf("%v", err)
	}
}

// A save carries the active flag the caller read earlier; it must not undo a toggle made since.
func TestSaveDoesNotUndoAToggleMadeSinceItWasRead(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	stale := mustGet(t, h, "wf-g") // read while inactive
	if err := h.SetWorkflowActive(ctx, "wf-g", true); err != nil {
		t.Fatal(err)
	}
	stale.Description = "edited from a stale read"
	if err := h.SaveWorkflow(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-g"); !rows.IsActive {
		t.Fatal("a save from a stale read switched the workflow off")
	}
	stale = mustGet(t, h, "wf-g") // read while active
	if err := h.SetWorkflowActive(ctx, "wf-g", false); err != nil {
		t.Fatal(err)
	}
	stale.Description = "edited again"
	if err := h.SaveWorkflow(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-g"); rows.IsActive {
		t.Fatal("a save from a stale read switched the workflow on")
	}
}

// An approval with a guard that matches nothing leaves no trace: no approval row, the file as it
// was, and no open change.
func TestABogusGuardLeavesNoApprovalFileOrIntent(t *testing.T) {
	ctx := context.Background()
	h, s, _, nodes := guardedRig(t)
	before := fileBytes(t, h, "wf-g")
	approved := approvals(t, s, "wf-g")
	err := h.SaveWorkflowNodes(WithExpectHash(WithApproval(ctx), "bogus"), "wf-g", nodes)
	if !errors.Is(err, ErrChangedSincePreview) {
		t.Fatalf("%v", err)
	}
	if approvals(t, s, "wf-g") != approved || !bytes.Equal(before, fileBytes(t, h, "wf-g")) || openIntentCount(t, s) != 0 {
		t.Fatal("a refused edit left something behind")
	}
}

// A caller that cancels is told so, never that the file and the database disagree.
func TestACancelBeforeStartStaysACancel(t *testing.T) {
	h, s, hash, nodes := guardedRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := h.SaveNodesGuarded(ctx, "wf-g", nodes, SaveGuard{ExpectHash: hash})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrFileInconsistent) {
		t.Fatalf("%v", err)
	}
	if openIntentCount(t, s) != 0 || sqlConfig(t, s, "wf-g", "g-set") == "edited" {
		t.Fatal("a cancelled change left something behind")
	}
}

// A caller deadline shorter than the retry window still gets "busy" (and the deadline).
func TestACallerDeadlineShorterThanTheWaitStillSaysBusy(t *testing.T) {
	h, s, hash, nodes := guardedRig(t)
	if _, err := s.db.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('wf-g', 'save', 'pending', ?, ?)`, os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := h.SaveNodesGuarded(ctx, "wf-g", nodes, SaveGuard{ExpectHash: hash})
	if !errors.Is(err, ErrPublicationBusy) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
}

// The kill switch works under an unresolved change: the rows go inactive, the file and the open
// change are left alone, and nothing can be switched on under it.
func TestDeactivateWorksUnderAnUnresolvedChangeButActivateDoesNot(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	if err := h.SetWorkflowActive(ctx, "wf-g", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('wf-g', 'save', 'unresolved', 0, ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	before := fileBytes(t, h, "wf-g")
	if err := h.SetWorkflowActive(ctx, "wf-g", false); err != nil {
		t.Fatalf("the kill switch failed: %v", err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-g"); rows.IsActive {
		t.Fatal("the rows are still active")
	}
	if !bytes.Equal(before, fileBytes(t, h, "wf-g")) || openIntentCount(t, s) != 1 {
		t.Fatal("the file or the open change was touched")
	}
	if state, _ := h.UnfinishedChange(ctx, "wf-g"); state != "unresolved" {
		t.Fatalf("state %q", state)
	}
	if err := h.SetWorkflowActive(ctx, "wf-g", true); !errors.Is(err, ErrPublicationUnresolved) {
		t.Fatalf("activating under an unresolved change: %v", err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-g"); rows.IsActive {
		t.Fatal("a workflow was activated under an unresolved change")
	}
	// A change still being made is not a reason to bypass it.
	if _, err := s.db.Exec(`UPDATE workflow_publications SET state = 'pending', owner_pid = ?`, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if state, _ := h.UnfinishedChange(ctx, "wf-g"); state != "" {
		t.Fatalf("a live change reported as %q", state)
	}
}

// A new workflow saved without the operator's approval starts inactive whatever the caller said;
// the operator's own save (with approval) keeps the flag it names.
func TestANewWorkflowStartsInactiveUnlessTheOperatorCreatesIt(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	agent := mirrorWF("wf-by-agent", "a-trig", "a-set")
	agent.IsActive = true
	if err := h.SaveWorkflow(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-by-agent"); rows.IsActive {
		t.Fatal("a new workflow saved without approval is active")
	}
	if f, _ := h.files.GetWorkflow(ctx, "wf-by-agent"); f.IsActive {
		t.Fatal("its file says active")
	}
	op := mirrorWF("wf-by-operator", "o-trig", "o-set")
	op.IsActive = true
	if err := h.SaveWorkflow(WithApproval(ctx), op); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GetWorkflow(ctx, "wf-by-operator"); !rows.IsActive {
		t.Fatal("the operator's create lost its flag")
	}
}
