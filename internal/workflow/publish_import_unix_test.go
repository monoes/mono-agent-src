//go:build !windows

package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A workflow file that is a named pipe (or a link) is never opened by the importer, so it cannot
// hold the database's write lock: the import returns at once and writes by others still work.
func TestImportNeverBlocksOnAPipeOrLink(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	dir := h.files.dir
	if err := syscall.Mkfifo(filepath.Join(dir, "wf-pipe.json"), 0o600); err != nil {
		t.Skip("no named pipes here:", err)
	}
	if err := os.Symlink(filepath.Join(dir, "wf-g.json"), filepath.Join(dir, "wf-link.json")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for _, id := range []string{"wf-pipe", "wf-link"} {
			if res, err := h.ImportFile(ctx, id, nil); err != nil || res.Imported {
				done <- fmt.Errorf("%s: %+v %v", id, res, err)
				return
			}
		}
		_, err := s.db.Exec(`UPDATE workflows SET updated_at = updated_at WHERE id = 'wf-g'`)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the importer is stuck on a special file")
	}
	// Recovery's and the publication's own reads are bounded the same way.
	if _, _, err := h.files.readRaw("wf-pipe"); err == nil {
		t.Fatal("a pipe was read as a workflow file")
	}
}

// A pipe where the workflow's file was leaves an abandoned change that nobody can read the file of:
// recovery marks it unresolved (it does not fail every later save), and the operator can resolve a
// stuck change that is still marked pending but has no live owner.
func TestUnreadableFileUnderAnAbandonedChangeIsResolvable(t *testing.T) {
	ctx := context.Background()
	h, s, _, _ := guardedRig(t)
	path := filepath.Join(h.files.dir, "wf-g.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skip(err)
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
	// A pending change with no live owner that recovery has not touched yet is resolvable too.
	if _, err := s.db.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('wf-g', 'save', 'pending', 0, ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := h.ResolveUnresolved(ctx, "wf-g", KeepRows); err != nil {
		t.Fatalf("a stuck pending change: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES ('wf-g', 'save', 'pending', ?, ?)`, os.Getpid(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := h.ResolveUnresolved(ctx, "wf-g", KeepRows); err == nil {
		t.Fatal("a change that is still being made must not be resolved from under its owner")
	}
}

// The profile stamp never opens a pipe or follows a link: a pipe named like a workflow file must not
// block it (and with it every command that opens the workflows).
func TestStampSkipsPipesAndLinks(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "x.json"), 0o600); err != nil {
		t.Skip(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte(`{"id":"l"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "l.json")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		stamped, err := StampFileWorkflowProfiles(dir, "default", nil)
		if err == nil && len(stamped) != 0 {
			err = fmt.Errorf("stamped %v", stamped)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stamp is stuck on a pipe")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "real.txt")); string(b) != `{"id":"l"}` {
		t.Fatal("a link's target was rewritten")
	}
}

// A link is never read as a workflow file, by any reader, even when it points at a plain file.
func TestReadRawRefusesALink(t *testing.T) {
	files, err := NewWorkflowFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(files.dir, "real.txt")
	if err := os.WriteFile(target, []byte(`{"id":"l"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(files.dir, "l.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := files.readRaw("l"); err == nil {
		t.Fatal("a link was read as a workflow file")
	}
}
