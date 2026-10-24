package workflowaitask

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/storage"
)

// A file that could not be put back is recorded for the operator: task, workflow and event, no value.
func TestAnInconsistentFileIsWrittenToTheAuditTable(t *testing.T) {
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	d := Deps{ProfileID: func() string { return "p1" }}
	cause := errors.New("restore failed")
	if got := d.auditInconsistent(db, aitask.Task{ID: "at-1"}, "wf-1", cause); !errors.Is(got, cause) {
		t.Fatalf("the cause was lost: %v", got)
	}
	var task, profile, wf, event, at string
	if err := db.DB.QueryRow(`SELECT task_id, profile_id, workflow_id, event, at FROM ai_task_apply_audit`).Scan(&task, &profile, &wf, &event, &at); err != nil {
		t.Fatalf("no audit row: %v", err)
	}
	if task != "at-1" || profile != "p1" || wf != "wf-1" || event != "file_inconsistent" || at == "" {
		t.Fatalf("%s %s %s %s %s", task, profile, wf, event, at)
	}
	// A table that cannot be written does not hide the cause or the failure to record it.
	if _, err := db.DB.Exec(`ALTER TABLE ai_task_apply_audit RENAME TO gone`); err != nil {
		t.Fatal(err)
	}
	got := d.auditInconsistent(db, aitask.Task{ID: "at-2"}, "wf-1", cause)
	if !errors.Is(got, cause) || got.Error() == cause.Error() {
		t.Fatalf("a failed audit write was swallowed: %v", got)
	}
}
