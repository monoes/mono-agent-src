package application

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/applypark"
)

// A resume that is slow but alive renews its hold: a sweep that runs while the submit is still going, long after
// the hold's last write, finds it fresh and leaves the tab, so the resume completes instead of cancelling its own
// task.
func TestFillResumeKeepsItsHoldWhileTheSubmitIsSlow(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 1)
	b := &browser{}
	b.install(t, dbPath)
	if out, err := run(t, dbPath, true, "fill", "queue", ids[0]); err != nil {
		t.Fatalf("queue: %v (%s)", err, out)
	}
	oldBeat, realSubmit := holdBeat, submitTask
	holdBeat = 20 * time.Millisecond
	t.Cleanup(func() { holdBeat, submitTask = oldBeat, realSubmit })
	stale := time.Now().UTC().Add(-2 * applypark.ResumeHoldTTL).Format(time.RFC3339)
	submitTask = func(ctx context.Context, args []string) ([]byte, error) {
		withDB(t, dbPath, func(db *sql.DB) {
			// The submit has been running longer than the hold's life: its last write is old.
			if _, err := db.Exec(`UPDATE application_tabs SET updated_at = ? WHERE application_id = ?`, stale, ids[0]); err != nil {
				t.Fatal(err)
			}
		})
		time.Sleep(200 * time.Millisecond) // the beat renews the hold meanwhile
		withDB(t, dbPath, func(db *sql.DB) {
			st := applypark.Store{DB: db, Profile: "default"}
			tab, err := st.Get(ctx, ids[0])
			if err != nil || tab == nil {
				t.Fatalf("tab: %v %v", tab, err)
			}
			live := []applypark.LiveTab{{TabID: tab.TabID}}
			if _, err := applypark.Sweep(ctx, st, applications.NewStore(db), live); err != nil {
				t.Fatal(err)
			}
		})
		return realSubmit(ctx, args)
	}
	b.status = aitask.StatusRunning
	if out, err := run(t, dbPath, true, "fill", "resume", ids[0]); err != nil {
		t.Fatalf("a slow resume that kept its hold failed: %v (%s)", err, out)
	}
}
