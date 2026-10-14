package connections

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// newManagerDB returns a Manager over a migrated database (see newTestDB).
func newManagerDB(t *testing.T) (*Manager, *sql.DB) {
	t.Helper()
	db := newTestDB(t)
	mgr, err := NewManager(db)
	if err != nil {
		t.Fatalf("newManagerDB: NewManager: %v", err)
	}
	return mgr, db
}

// TestManagerListEmpty verifies that List on an empty DB returns an empty (non-nil) slice.
func TestManagerListEmpty(t *testing.T) {
	mgr, _ := newManagerDB(t)
	ctx := context.Background()

	conns, err := mgr.List(ctx, "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if conns == nil {
		t.Fatal("List returned nil, expected empty slice")
	}
	if len(conns) != 0 {
		t.Errorf("List returned %d connections, expected 0", len(conns))
	}
}

// TestManagerRemoveNotFound verifies that Remove returns an error for a non-existent ID.
func TestManagerRemoveNotFound(t *testing.T) {
	mgr, _ := newManagerDB(t)
	ctx := context.Background()

	err := mgr.Remove(ctx, "nonexistent-id", "")
	if err == nil {
		t.Fatal("expected error when removing non-existent ID, got nil")
	}
}

// TestManagerConnectUnknownPlatform verifies that Connect returns an error for an unknown platform.
func TestManagerConnectUnknownPlatform(t *testing.T) {
	mgr, _ := newManagerDB(t)
	ctx := context.Background()

	_, err := mgr.Connect(ctx, "notaplatform", ConnectOptions{})
	if err == nil {
		t.Fatal("expected error when connecting to unknown platform, got nil")
	}
}

// TestManagerConnectSavesProfileID is a regression test: Connect previously never
// set ProfileID on the created Connection, so every connection made via
// `monoes connect` (regardless of the active profile) was silently saved under
// "default" and became invisible to other profiles.
func TestManagerConnectSavesProfileID(t *testing.T) {
	mgr, _ := newManagerDB(t)
	ctx := context.Background()

	conn, err := mgr.Connect(ctx, "postgresql", ConnectOptions{
		Method:      MethodConnStr,
		ProfileID:   "work",
		FieldValues: map[string]string{"connection_string": "postgres://u:p@localhost:5432/db"},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn.ProfileID != "work" {
		t.Fatalf("ProfileID = %q, want %q", conn.ProfileID, "work")
	}

	saved, err := mgr.Get(ctx, conn.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if saved.ProfileID != "work" {
		t.Fatalf("saved ProfileID = %q, want %q", saved.ProfileID, "work")
	}
}

// TestManagerConnectBrowserPointsToLogin: a browser-login platform can't be
// connected here, and the error must name the command that does it (it used
// to say `connect <p>`, the command that had just failed).
func TestManagerConnectBrowserPointsToLogin(t *testing.T) {
	mgr, _ := newManagerDB(t)
	_, err := mgr.Connect(context.Background(), "instagram", ConnectOptions{Method: MethodBrowser})
	if err == nil {
		t.Fatal("expected an error for the browser method")
	}
	if !strings.Contains(err.Error(), "monoagentcli login instagram") {
		t.Fatalf("error should point to `login instagram`, got: %v", err)
	}
}

// SaveFields saves a prompt-free connection under the given profile, and
// Test re-validates it without writing anything to stdout.
func TestManagerSaveFieldsThenTest(t *testing.T) {
	mgr, _ := newManagerDB(t)
	ctx := context.Background()

	conn, err := mgr.SaveFields(ctx, "postgresql", MethodConnStr,
		map[string]interface{}{"connection_string": "postgres://u:p@localhost:5432/db"}, "work")
	if err != nil {
		t.Fatalf("SaveFields: %v", err)
	}
	if conn.ID == "" || conn.ProfileID != "work" || conn.Status != "active" || !strings.HasPrefix(conn.Label, "PostgreSQL") {
		t.Fatalf("saved = %+v", conn)
	}
	if list, _ := mgr.List(ctx, "postgresql", "work"); len(list) != 1 || list[0].ID != conn.ID {
		t.Fatalf("List(work) = %+v", list)
	}
	if list, _ := mgr.List(ctx, "postgresql", "default"); len(list) != 0 {
		t.Fatalf("List(default) = %+v, want none", list)
	}

	if _, err := mgr.Test(ctx, conn.ID); err != nil {
		t.Fatalf("Test: %v", err)
	}
	got, _ := mgr.Get(ctx, conn.ID)
	if got.LastTested == "" || got.Status != "active" {
		t.Fatalf("after Test: %+v", got)
	}

	if _, err := mgr.SaveFields(ctx, "nope", MethodAPIKey, nil, ""); err == nil {
		t.Fatal("SaveFields accepted an unknown platform")
	}
}
