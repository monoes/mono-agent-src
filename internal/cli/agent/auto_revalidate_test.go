package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/storage"
)

// A manual `agent validate` and the automatic one share one lock.
func TestValidationLockIsExclusive(t *testing.T) {
	release, err := lockValidation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockValidation(); !errors.Is(err, errValidationRunning) {
		t.Fatalf("second lock = %v, want errValidationRunning", err)
	}
	release()
	again, err := lockValidation()
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func openAutoTestDB(t *testing.T) *storage.Database {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	return db
}

// An unreadable state doesn't break status, on or off; on resets it.
func TestAutoRevalidateUnreadableState(t *testing.T) {
	t.Parallel()
	db := openAutoTestDB(t)
	ctx := context.Background()
	corrupt := func() {
		if _, err := db.DB.Exec(`INSERT INTO settings (key, value) VALUES ('agent_roster.auto_revalidate.state', '{broken')
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`); err != nil {
			t.Fatal(err)
		}
	}
	corrupt()
	st, err := autoRevalidateSnapshot(ctx, db.DB, false)
	if err != nil || st.StateError == "" {
		t.Fatalf("status: err=%v state_error=%q; want no error and the state error reported", err, st.StateError)
	}
	off := func(c *agentroster.AutoConfig) { c.Enabled = false }
	on := func(c *agentroster.AutoConfig) { c.Enabled = true }
	if err := applyAutoRevalidate(ctx, db.DB, off); err != nil {
		t.Fatalf("off: %v", err)
	}
	if _, err := agentroster.LoadAutoState(ctx, db.DB, time.Now()); err == nil {
		t.Fatal("off repaired the state; only on should")
	}
	if err := applyAutoRevalidate(ctx, db.DB, on); err != nil {
		t.Fatalf("on: %v", err)
	}
	if s, err := agentroster.LoadAutoState(ctx, db.DB, time.Now()); err != nil || s.RuntimesToday != 0 {
		t.Fatalf("after on: %+v, %v", s, err)
	}
	if st, err := autoRevalidateSnapshot(ctx, db.DB, false); err != nil || st.StateError != "" || !st.Enabled {
		t.Fatalf("status after on: %+v, %v", st, err)
	}
}

// Without a scan, status estimates the next run but names no runtime.
func TestAutoRevalidateNoScanNamesNoRuntime(t *testing.T) {
	t.Parallel()
	db := openAutoTestDB(t)
	ctx := context.Background()
	old := time.Now().Add(-10 * 24 * time.Hour)
	if err := agentroster.Save(ctx, db.DB, agentroster.Result{Runtime: "gone", Model: "m", Status: agentroster.StatusOK, HasCost: true, CostUSD: 0.01, ValidatedAt: old}); err != nil {
		t.Fatal(err)
	}
	st, err := autoRevalidateSnapshot(ctx, db.DB, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Next == nil || !st.NextUnchecked || st.Next.Runtime != "" || st.Next.Targets[0].Runtime != "" {
		t.Fatalf("next = %+v unchecked=%v", st.Next, st.NextUnchecked)
	}
}
