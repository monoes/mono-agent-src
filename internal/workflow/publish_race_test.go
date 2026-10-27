//go:build wfcrash && !windows

package workflow

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// helperActions are the writers a child process can run against the rig's workflow.
var helperActions = map[string]func(ctx context.Context, h *HybridWorkflowStore, cur *Workflow) error{
	"apply-fix": func(ctx context.Context, h *HybridWorkflowStore, cur *Workflow) error {
		nodes := append([]WorkflowNode(nil), cur.Nodes...)
		nodes[1].Config = map[string]interface{}{"k": "from-helper"}
		return h.SaveNodesGuarded(ctx, "wf-c", nodes, SaveGuard{ExpectHash: os.Getenv("WF_HELPER_HASH"), RevokeApprovals: true})
	},
	"save": func(ctx context.Context, h *HybridWorkflowStore, cur *Workflow) error {
		cur.Nodes[1].Config = map[string]interface{}{"k": "from-save-" + os.Getenv("WF_HELPER_VALUE")}
		return h.SaveWorkflow(ctx, cur)
	},
	"node-set": func(ctx context.Context, h *HybridWorkflowStore, cur *Workflow) error {
		nodes := append([]WorkflowNode(nil), cur.Nodes...)
		nodes[1].Config = map[string]interface{}{"k": "from-node-set-" + os.Getenv("WF_HELPER_VALUE")}
		return h.SaveWorkflowNodes(ctx, "wf-c", nodes)
	},
	"connections": func(ctx context.Context, h *HybridWorkflowStore, cur *Workflow) error {
		return h.SaveWorkflowConnections(ctx, "wf-c", nil)
	},
	"toggle": func(ctx context.Context, h *HybridWorkflowStore, cur *Workflow) error {
		round, _ := strconv.Atoi(os.Getenv("WF_HELPER_VALUE"))
		return h.SetWorkflowActive(ctx, "wf-c", round%2 == 0)
	},
	"delete": func(ctx context.Context, h *HybridWorkflowStore, cur *Workflow) error {
		return h.DeleteWorkflow(ctx, "wf-c")
	},
	"update": func(ctx context.Context, h *HybridWorkflowStore, cur *Workflow) error {
		cur.Description = "updated-" + os.Getenv("WF_HELPER_VALUE")
		return h.UpdateWorkflow(ctx, cur)
	},
	"save-shared": func(ctx context.Context, h *HybridWorkflowStore, cur *Workflow) error {
		return h.SaveWorkflow(ctx, mirrorWF("wf-x", "shared-n", "x-set"))
	},
	"save-revoking": func(ctx context.Context, h *HybridWorkflowStore, cur *Workflow) error {
		cur.Nodes[1].Config = map[string]interface{}{"k": "from-revoking"}
		return h.SaveWorkflowRevoking(ctx, cur, "")
	},
}

// race starts two writers together (a barrier file releases both) and waits for them. A writer
// either wins or reports a refusal (exit 3); anything else is a failure.
func (r *crashRig) race(t *testing.T, round int, a, b string) (winners int) {
	t.Helper()
	barrier := filepath.Join(r.root, fmt.Sprintf("go-%d", round))
	env := []string{"WF_HELPER_BARRIER=" + barrier, fmt.Sprintf("WF_HELPER_VALUE=%d", round)}
	ca, cb := r.startHelper(t, a, env...), r.startHelper(t, b, env...)
	if err := os.WriteFile(barrier, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*exec.Cmd{ca, cb} {
		switch err := c.Wait(); {
		case err == nil:
			winners++
		default:
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 3 {
				t.Logf("refused: %s", tailOf(c.Stdout.(*bytes.Buffer).String()+c.Stderr.(*bytes.Buffer).String()))
			}
			if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
				t.Fatalf("a writer failed: %v\n%s", err, tailOf(c.Stdout.(*bytes.Buffer).String()))
			}
		}
	}
	return winners
}

// assertConsistent: the file, the rows and the version history tell one story.
func (r *crashRig) assertConsistent(t *testing.T) {
	t.Helper()
	r.assertFileMatchesRows(t)
	if n := r.openIntents(t); n != 0 {
		t.Fatalf("%d intents left open", n)
	}
	if left, _ := filepath.Glob(filepath.Join(r.root, "workflows", "*.tmp")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
	rows, _ := r.s.GetWorkflow(context.Background(), "wf-c")
	file, _ := r.h.files.GetWorkflow(context.Background(), "wf-c")
	var latest, count, distinct int
	if err := r.s.db.QueryRow(`SELECT COALESCE(MAX(version), 0), COUNT(*), COUNT(DISTINCT version) FROM workflow_versions WHERE workflow_id = 'wf-c'`).
		Scan(&latest, &count, &distinct); err != nil {
		t.Fatal(err)
	}
	if count != distinct || rows.Version != latest || file.Version != latest {
		t.Fatalf("versions disagree: rows %d file %d latest %d (%d records, %d distinct)", rows.Version, file.Version, latest, count, distinct)
	}
}

func (r *crashRig) raceRounds(t *testing.T, rounds int, writer string) {
	t.Helper()
	for i := 0; i < rounds; i++ {
		cur, _ := r.h.GetWorkflow(context.Background(), "wf-c")
		r.hash, _ = ReviewHash(cur)
		if w := r.race(t, i, "apply-fix", writer); w < 1 {
			t.Fatalf("round %d: nobody won", i)
		}
		r.assertConsistent(t)
	}
}

// R-series: each definition writer raced against apply-fix across two processes.
func TestRaceSaveAgainstApplyFix(t *testing.T) { newCrashRig(t).raceRounds(t, 8, "save") }

// A save that revokes approvals does so in the commit that writes the rows.
func TestRaceSaveRevokingAgainstApplyFix(t *testing.T) {
	r := newCrashRig(t)
	r.raceRounds(t, 4, "save-revoking")
	if n := approvals(t, r.s, "wf-c"); n != 0 {
		t.Fatalf("approvals survived a revoking save: %d", n)
	}
}

// A plain save killed between its file and its commit is rolled back like a guarded one.
func TestCrashKilledSaveAfterRenameIsRolledBack(t *testing.T) {
	r := newCrashRig(t)
	before := r.fileOnDisk(t)
	waitKilled(t, r.startHelper(t, "save", "MONOAGENT_WF_FAILPOINT=after-rename"))
	r.assertConsistent(t)
	if string(r.fileOnDisk(t)) != string(before) {
		t.Fatal("the file still holds the save that never committed")
	}
}

func TestRaceNodeSetAgainstApplyFix(t *testing.T)     { newCrashRig(t).raceRounds(t, 8, "node-set") }
func TestRaceConnectionsAgainstApplyFix(t *testing.T) { newCrashRig(t).raceRounds(t, 4, "connections") }

func TestCrashKilledNodeSetAfterRenameIsRolledBack(t *testing.T) {
	r := newCrashRig(t)
	before := r.fileOnDisk(t)
	waitKilled(t, r.startHelper(t, "node-set", "MONOAGENT_WF_FAILPOINT=after-rename"))
	r.assertConsistent(t)
	if string(r.fileOnDisk(t)) != string(before) {
		t.Fatal("the file still holds the edit that never committed")
	}
}

// A toggle and a node edit from two processes both land: neither reads the file outside the lock
// and writes it back over the other.
func TestRaceToggleAgainstNodeSet(t *testing.T) {
	r := newCrashRig(t)
	for i := 0; i < 8; i++ {
		if w := r.race(t, i, "node-set", "toggle"); w != 2 {
			t.Fatalf("round %d: %d writers won", i, w)
		}
		r.assertConsistent(t)
		got, _ := r.h.GetWorkflow(context.Background(), "wf-c")
		if got.IsActive != (i%2 == 0) {
			t.Fatalf("round %d: the toggle was lost", i)
		}
		if want := fmt.Sprintf("from-node-set-%d", i); got.Nodes[1].Config["k"] != want {
			t.Fatalf("round %d: the node edit was lost: %v", i, got.Nodes[1].Config["k"])
		}
	}
}

func TestRaceUpdateAgainstApplyFix(t *testing.T) { newCrashRig(t).raceRounds(t, 4, "update") }

func TestCrashKilledDeleteAfterTheFileIsGoneKeepsTheWorkflow(t *testing.T) {
	r := newCrashRig(t)
	waitKilled(t, r.startHelper(t, "delete", "MONOAGENT_WF_FAILPOINT=after-rename"))
	r.assertConsistent(t)
	if got, _ := r.h.GetWorkflow(context.Background(), "wf-c"); got == nil {
		t.Fatal("the workflow vanished")
	}
	if len(r.fileOnDisk(t)) == 0 {
		t.Fatal("the file was not put back")
	}
}

func TestDeleteRemovesFileRowsApprovalsAndVersions(t *testing.T) {
	r := newCrashRig(t)
	if err := r.startHelper(t, "delete").Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "workflows", "wf-c.json")); !os.IsNotExist(err) {
		t.Fatalf("the file is still there: %v", err)
	}
	var rows, vers int
	_ = r.s.db.QueryRow(`SELECT COUNT(*) FROM workflows WHERE id = 'wf-c'`).Scan(&rows)
	_ = r.s.db.QueryRow(`SELECT COUNT(*) FROM workflow_versions WHERE workflow_id = 'wf-c'`).Scan(&vers)
	if rows != 0 || vers != 0 || approvals(t, r.s, "wf-c") != 0 || r.openIntents(t) != 0 {
		t.Fatalf("rows %d versions %d approvals %d intents %d", rows, vers, approvals(t, r.s, "wf-c"), r.openIntents(t))
	}
}

// A delete raced with a guarded save ends as one or the other, never a file without rows or rows
// without a file.
func TestRaceDeleteAgainstApplyFix(t *testing.T) {
	r := newCrashRig(t)
	r.race(t, 0, "apply-fix", "delete")
	_, err := os.Stat(filepath.Join(r.root, "workflows", "wf-c.json"))
	var rows int
	_ = r.s.db.QueryRow(`SELECT COUNT(*) FROM workflows WHERE id = 'wf-c'`).Scan(&rows)
	if (err == nil) != (rows == 1) || r.openIntents(t) != 0 {
		t.Fatalf("file present %v, rows %d, intents %d", err == nil, rows, r.openIntents(t))
	}
}

// An activation killed between its file and its commit leaves the workflow inactive: the rows'
// definition hash does not cover the active flag, so it cannot tell the commit happened.
func TestCrashKilledActivationAfterRenameIsRolledBack(t *testing.T) {
	r := newCrashRig(t)
	waitKilled(t, r.startHelper(t, "toggle", "MONOAGENT_WF_FAILPOINT=after-rename"))
	r.assertConsistent(t)
	if got, _ := r.h.GetWorkflow(context.Background(), "wf-c"); got.IsActive {
		t.Fatal("the uncommitted activation survived")
	}
	if f, _ := r.h.files.GetWorkflow(context.Background(), "wf-c"); f.IsActive {
		t.Fatal("the file still says active")
	}
}

// A writer that stalled before its file step (so its lease ran out) is fenced out: recovery removes
// its intent, another save commits, and when the writer wakes it neither writes its file nor
// commits. The file keeps matching the rows.
func TestCrashAnExpiredWriterCannotWriteItsFileAfterRecovery(t *testing.T) {
	r := newCrashRig(t)
	cmd, release := r.pauseHelper(t, "before-fence")
	if _, err := r.s.db.Exec(`UPDATE workflow_publications SET started_at = ?`, time.Now().Add(-time.Hour).UTC()); err != nil {
		t.Fatal(err)
	}
	if n, err := r.h.RecoverPublications(context.Background()); err != nil || n != 1 {
		t.Fatalf("recover: %d %v", n, err)
	}
	cur, _ := r.h.GetWorkflow(context.Background(), "wf-c")
	cur.Nodes[1].Config = map[string]interface{}{"k": "from-parent"}
	if err := r.h.SaveWorkflow(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.root, "workflows", "wf-c.json")
	before, _ := os.Stat(path)
	release()
	err := cmd.Wait()
	if after, _ := os.Stat(path); !os.SameFile(before, after) {
		t.Fatal("the fenced-out writer replaced the file (even if it was repaired afterwards)")
	}
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
		t.Fatalf("the stalled writer should be told its change was lost: %v", err)
	}
	r.assertConsistent(t)
	if got := sqlConfig(t, r.s, "wf-c", "c-set"); got != "from-parent" {
		t.Fatalf("rows: %v", got)
	}
}

// Listing, counting and copying go through the same gate as GetWorkflow: after a writer died with
// its file replaced, no listing returns the proposed definition, even though the listing itself is
// what recovers the abandoned change.
func TestCrashListingAfterADeadWritersRenameDoesNotReturnTheProposal(t *testing.T) {
	r := newCrashRig(t)
	waitKilled(t, r.startHelper(t, "apply-fix", "MONOAGENT_WF_FAILPOINT=after-rename"))
	list, err := r.h.ListWorkflows(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range list {
		if w.ID == "wf-c" && w.Nodes[1].Config["k"] == "from-helper" {
			t.Fatal("the listing returned a definition that never committed")
		}
	}
	r.assertConsistent(t)
}

func tailOf(s string) string {
	if len(s) > 1500 {
		return s[len(s)-1500:]
	}
	return s
}

// Two workflows that both name a node id nobody owns yet cannot both publish it: the second to
// commit is refused and its file goes back, instead of the node being moved out from under the first.
func TestCrashNodeIdClaimedWhileAChangeWasOpenIsRefusedAtTheCommit(t *testing.T) {
	r := newCrashRig(t)
	r.pauseAction = "save-shared"
	cmd, release := r.pauseHelper(t, "after-rename")
	other := mirrorWF("wf-y", "shared-n", "y-set")
	if err := r.h.SaveWorkflow(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	release()
	err := cmd.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
		t.Fatalf("the second claim should be refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "workflows", "wf-x.json")); !os.IsNotExist(err) {
		t.Fatalf("the refused workflow kept its file: %v", err)
	}
	got, _ := r.s.GetWorkflow(context.Background(), "wf-y")
	if got == nil || len(got.Nodes) != 2 || r.openIntents(t) != 0 {
		t.Fatalf("the first owner lost its node: %+v", got)
	}
}

// After an outside edit made a crash unresolved, choosing the rows puts the file back and frees the
// workflow for the next save.
func TestCrashUnresolvedChangeCanBeResolvedByTheOperator(t *testing.T) {
	r := newCrashRig(t)
	cmd, _ := r.pauseHelper(t, "after-rename")
	if err := os.WriteFile(filepath.Join(r.root, "workflows", "wf-c.json"), []byte(`{"id":"wf-c","name":"by hand"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if _, err := r.h.GetWorkflow(context.Background(), "wf-c"); err != nil {
		t.Fatal(err)
	}
	if err := r.h.ResolveUnresolved(context.Background(), "wf-c", KeepRows); err != nil {
		t.Fatal(err)
	}
	r.assertConsistent(t)
	cur, _ := r.h.GetWorkflow(context.Background(), "wf-c")
	if err := r.h.SaveWorkflow(context.Background(), cur); err != nil {
		t.Fatalf("a save after the resolution: %v", err)
	}
}

// When the coordination tables cannot be read (here: the context is already cancelled), a file left
// by a dead writer is not served; the read fails instead of trusting the file.
func TestCrashFailedCoordinationReadDoesNotTrustTheFile(t *testing.T) {
	r := newCrashRig(t)
	waitKilled(t, r.startHelper(t, "apply-fix", "MONOAGENT_WF_FAILPOINT=after-rename"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := r.h.GetWorkflow(ctx, "wf-c")
	if err == nil && got != nil && got.Nodes[1].Config["k"] == "from-helper" {
		t.Fatal("an uncommitted file was served although the intent table could not be read")
	}
	if err == nil {
		t.Fatal("expected the read to fail")
	}
}

// A backfill that runs while a new workflow's creation is between its file and its commit must not
// insert rows from that uncommitted file: when the creator dies, nothing of it may remain.
func TestCrashBackfillDuringACreationLeavesNoStub(t *testing.T) {
	r := newCrashRig(t)
	r.pauseAction = "save-shared"
	cmd, _ := r.pauseHelper(t, "after-rename")
	r.h.ensureSQLWorkflow(context.Background(), "wf-x")
	if got, _ := r.s.GetWorkflow(context.Background(), "wf-x"); got != nil {
		t.Fatal("a stub was created from the uncommitted file")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if got, err := r.h.GetWorkflow(context.Background(), "wf-x"); err != nil || got != nil {
		t.Fatalf("the abandoned creation is still visible: %v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "workflows", "wf-x.json")); !os.IsNotExist(err) {
		t.Fatalf("the abandoned file survived: %v", err)
	}
	if got, _ := r.s.GetWorkflow(context.Background(), "wf-x"); got != nil {
		t.Fatal("rows of the abandoned creation survived")
	}
}

// An edit that lands after a change was planned and before its rename is not overwritten blindly:
// the change leaves no intent behind, plans again against the file as it is (an edit it cannot take
// in is copied to the quarantine first) and then goes through.
func TestCrashAnEditBeforeTheRenameIsKeptNotOverwritten(t *testing.T) {
	r := newCrashRig(t)
	cmd, release := r.pauseHelper(t, "before-fence")
	edited := []byte(`{"id":"wf-c","name":"by hand"}`)
	if err := os.WriteFile(filepath.Join(r.root, "workflows", "wf-c.json"), edited, 0o600); err != nil {
		t.Fatal(err)
	}
	release()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the change should have gone through on its second plan: %v", err)
	}
	kept, _ := filepath.Glob(filepath.Join(r.root, "workflow-quarantine", "wf-c.*.json"))
	if len(kept) != 1 {
		t.Fatalf("quarantined copies: %v", kept)
	}
	if b, _ := os.ReadFile(kept[0]); !bytes.Equal(b, edited) {
		t.Fatal("the copy is not the edit")
	}
	r.assertConsistent(t)
	if got := sqlConfig(t, r.s, "wf-c", "c-set"); got != "from-helper" {
		t.Fatalf("the change did not land: %v", got)
	}
}
