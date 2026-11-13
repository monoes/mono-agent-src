package chat

import (
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
	"github.com/monoes/mono-agent/internal/storage"
)

// delete_nodes reads the nodes it snapshots and then deletes them. A writer that commits between the two must not make
// it fail with SQLITE_BUSY_SNAPSHOT (517, which busy_timeout does not wait for): the write lock is taken first (#1460).
func TestDeleteNodesSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	backupDir := t.TempDir()
	old := aiBackupDir
	aiBackupDir = func() (string, error) { return backupDir, nil }
	t.Cleanup(func() { aiBackupDir = old })

	path := filepath.Join(t.TempDir(), "t.db")
	d, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO workflows (id, name, profile_id) VALUES ('wf-1', 'w', 'prof')`,
		`INSERT INTO workflow_nodes (id, workflow_id, node_type, name, config) VALUES ('n1', 'wf-1', 'ai.chat', 'Chat', '{}')`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()

	db, trip := dbconntest.OpenPath(t, path)
	competing := dbconntest.CompetingWriter(t, path)
	fired := false
	trip.BeforeExec = func(string) {
		if !fired {
			fired = true
			competing()
		}
	}
	ct := NewCanvasTools(db, "prof")
	if _, err := ct.Execute("delete_nodes", `{"workflow_id":"wf-1","node_ids":["n1"]}`); err != nil {
		t.Fatalf("delete_nodes failed when a writer committed between its read and its write: %v", err)
	}
	if !fired {
		t.Fatal("the hook never ran")
	}
}
