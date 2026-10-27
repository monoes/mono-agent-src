//go:build wfcrash && !windows

package workflow

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

// These tests run the real publication paths in a second process and stop it at a named point.
// They need the failpoint build: go test -tags wfcrash ./internal/workflow -run Crash

type crashRig struct {
	root string
	h    *HybridWorkflowStore
	s    *SQLiteWorkflowStore
	hash string
	// pauseAction is the writer pauseHelper runs; apply-fix when empty.
	pauseAction string
}

func openRigStore(t testing.TB, root string, migrate bool) (*HybridWorkflowStore, *SQLiteWorkflowStore, func()) {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(root, "rig.db"))
	if err != nil {
		t.Fatal(err)
	}
	if migrate {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatal(err)
		}
	}
	files, err := NewWorkflowFileStore(filepath.Join(root, "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	s := NewSQLiteWorkflowStore(db.DB)
	return NewHybridWorkflowStore(files, s), s, func() { db.Close() }
}

func newCrashRig(t *testing.T) *crashRig {
	t.Helper()
	root := t.TempDir()
	h, s, closeDB := openRigStore(t, root, true)
	t.Cleanup(closeDB)
	ctx := context.Background()
	if err := h.SaveWorkflow(ctx, mirrorWF("wf-c", "c-trig", "c-set")); err != nil {
		t.Fatal(err)
	}
	if err := h.ApproveDefinition(ctx, "wf-c"); err != nil {
		t.Fatal(err)
	}
	cur, err := h.GetWorkflow(ctx, "wf-c")
	if err != nil || cur == nil {
		t.Fatalf("%v", err)
	}
	hash, err := ReviewHash(cur)
	if err != nil {
		t.Fatal(err)
	}
	return &crashRig{root: root, h: h, s: s, hash: hash}
}

// startHelper runs TestHelperPublish in a child process with the extra environment.
func (r *crashRig) startHelper(t *testing.T, action string, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperPublish$", "-test.count=1")
	cmd.Env = append(os.Environ(), append([]string{"WF_HELPER_ROOT=" + r.root, "WF_HELPER_ACTION=" + action, "WF_HELPER_HASH=" + r.hash}, env...)...)
	cmd.Stdout, cmd.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func waitKilled(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	err := cmd.Wait()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("the helper was expected to be killed, got %v", err)
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("the helper was not killed by SIGKILL: %v", err)
	}
}

// assertFileMatchesRows: what readers get from the file is what the rows say.
func (r *crashRig) assertFileMatchesRows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	got, err := r.h.GetWorkflow(ctx, "wf-c")
	if err != nil || got == nil {
		t.Fatalf("reading the workflow: %v", err)
	}
	rows, err := r.s.GetWorkflow(ctx, "wf-c")
	if err != nil || rows == nil {
		t.Fatalf("reading the rows: %v", err)
	}
	a, _ := definitionHash(got)
	b, _ := definitionHash(rows)
	if a != b {
		t.Fatalf("readers see a definition (%.8s) the committed rows do not have (%.8s)", a, b)
	}
}

func (r *crashRig) fileOnDisk(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.root, "workflows", "wf-c.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestHelperPublish is not a test: it is the body of the child process.
func TestHelperPublish(t *testing.T) {
	root := os.Getenv("WF_HELPER_ROOT")
	if root == "" {
		t.Skip("child process body")
	}
	h, _, closeDB := openRigStore(t, root, false) // the parent migrated it
	defer closeDB()
	ctx := context.Background()
	cur, err := h.GetWorkflow(ctx, "wf-c")
	if err == nil && cur == nil {
		os.Exit(3) // another writer deleted it first: a refusal, not a failure
	}
	if err != nil {
		t.Fatalf("%v", err)
	}
	if b := os.Getenv("WF_HELPER_BARRIER"); b != "" {
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
			if _, err := os.Stat(b); err == nil {
				break
			}
		}
	}
	act, ok := helperActions[os.Getenv("WF_HELPER_ACTION")]
	if !ok {
		t.Fatalf("unknown action")
	}
	if err := act(ctx, h, cur); err != nil {
		fmt.Fprintln(os.Stderr, "REFUSED:", err)
		os.Exit(3)
	}
}

// C1: a process killed after the file was renamed and before the commit leaves the rows alone, and
// the next process to read the workflow must not be served the file that never committed.
func TestCrashKilledAfterRenameIsRolledBack(t *testing.T) {
	r := newCrashRig(t)
	before := r.fileOnDisk(t)
	cmd := r.startHelper(t, "apply-fix", "MONOAGENT_WF_FAILPOINT=after-rename")
	waitKilled(t, cmd)
	r.assertFileMatchesRows(t)
	if !bytes.Equal(r.fileOnDisk(t), before) {
		t.Fatal("the file still holds the definition that never committed")
	}
	if n := approvals(t, r.s, "wf-c"); n != 1 {
		t.Fatalf("approvals touched: %d", n)
	}
}

// C3 (control): killed after the commit, both stores hold the new definition.
func TestCrashKilledAfterCommitKeepsTheSave(t *testing.T) {
	r := newCrashRig(t)
	cmd := r.startHelper(t, "apply-fix", "MONOAGENT_WF_FAILPOINT=after-commit")
	waitKilled(t, cmd)
	r.assertFileMatchesRows(t)
	if got := sqlConfig(t, r.s, "wf-c", "c-set"); got != "from-helper" {
		t.Fatalf("the committed save is missing: %v", got)
	}
}

// C5: a reader in another process, while a save sits between its file step and its commit, is
// not served the uncommitted definition.
func TestCrashReaderInsideTheWindowSeesCommittedRows(t *testing.T) {
	r := newCrashRig(t)
	fifo := filepath.Join(r.root, "pause")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := r.startHelper(t, "apply-fix", "MONOAGENT_WF_FAILPOINT_PAUSE=after-rename:"+fifo)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(fifo + ".reached"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper never reached the window")
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := r.h.GetWorkflow(context.Background(), "wf-c")
	if err != nil || got == nil {
		t.Fatalf("%v", err)
	}
	if got.Nodes[1].Config["k"] == "from-helper" {
		t.Error("a reader was served the definition of a save that has not committed")
	}
	f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("x"))
	f.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the helper failed: %v", err)
	}
	r.assertFileMatchesRows(t)
	if got := sqlConfig(t, r.s, "wf-c", "c-set"); got != "from-helper" {
		t.Fatalf("the save did not land after release: %v", got)
	}
}

func (r *crashRig) pauseHelper(t *testing.T, point string) (cmd *exec.Cmd, release func()) {
	t.Helper()
	fifo := filepath.Join(r.root, "pause")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	act := r.pauseAction
	if act == "" {
		act = "apply-fix"
	}
	cmd = r.startHelper(t, act, "MONOAGENT_WF_FAILPOINT_PAUSE="+point+":"+fifo)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(fifo + ".reached"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper never reached the window")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cmd, func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte("x"))
		f.Close()
	}
}

func (r *crashRig) openIntents(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.s.db.QueryRow(`SELECT COUNT(*) FROM workflow_publications`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// C2: killed after the intent and before the file changed: nothing to put back, the intent goes.
func TestCrashKilledBeforeTheFileChangedLeavesNothingBehind(t *testing.T) {
	r := newCrashRig(t)
	before := r.fileOnDisk(t)
	waitKilled(t, r.startHelper(t, "apply-fix", "MONOAGENT_WF_FAILPOINT=after-intent"))
	if r.openIntents(t) != 1 {
		t.Fatal("the dead process should have left its intent")
	}
	r.assertFileMatchesRows(t)
	if r.openIntents(t) != 0 || !bytes.Equal(r.fileOnDisk(t), before) {
		t.Fatal("recovery left an intent behind or changed the file")
	}
}

// C4: an edit from outside lands in the window and the process dies: the file is not touched, a
// copy is kept, the intent is unresolved, and readers keep serving the committed rows.
func TestCrashExternalEditInTheWindowIsKeptNotOverwritten(t *testing.T) {
	r := newCrashRig(t)
	cmd, _ := r.pauseHelper(t, "after-rename")
	edited := []byte(`{"id":"wf-c","name":"edited by hand","nodes":[],"connections":[]}`)
	if err := os.WriteFile(filepath.Join(r.root, "workflows", "wf-c.json"), edited, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	r.assertFileMatchesRows(t)
	if !bytes.Equal(r.fileOnDisk(t), edited) {
		t.Fatal("the outside edit was overwritten")
	}
	var state string
	if err := r.s.db.QueryRow(`SELECT state FROM workflow_publications`).Scan(&state); err != nil || state != "unresolved" {
		t.Fatalf("state %q err %v", state, err)
	}
	kept, _ := filepath.Glob(filepath.Join(r.root, "workflow-quarantine", "wf-c.*.json"))
	if len(kept) != 1 {
		t.Fatalf("quarantined copies: %v", kept)
	}
}

// A change that another process already rolled back must not commit afterwards.
func TestCrashARolledBackChangeDoesNotCommit(t *testing.T) {
	r := newCrashRig(t)
	before := r.fileOnDisk(t)
	cmd, release := r.pauseHelper(t, "after-rename")
	var id int64
	if err := r.s.db.QueryRow(`SELECT id FROM workflow_publications`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := r.h.rollbackIntent(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	release()
	err := cmd.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
		t.Fatalf("the helper should report the change as lost: %v", err)
	}
	r.assertFileMatchesRows(t)
	if !bytes.Equal(r.fileOnDisk(t), before) {
		t.Fatal("the file does not hold the committed definition")
	}
	if got := sqlConfig(t, r.s, "wf-c", "c-set"); got == "from-helper" {
		t.Fatal("the rolled-back change committed")
	}
}
