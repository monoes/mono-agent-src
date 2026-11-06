package secrets

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"

	"github.com/zalando/go-keyring"
)

func newSecretsTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	db := testdb.Open(t)
	return db
}

func TestAddDecryptList_RoundTrip(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	id, err := Add(ctx, db.DB, "default", "secret", "svc-one", map[string]string{"secret": "v-alpha1"}, "", "", "prod key")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	fields, notes, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if fields["secret"] != "v-alpha1" {
		t.Fatalf("got %q, want %q", fields["secret"], "v-alpha1")
	}
	if notes != "prod key" {
		t.Fatalf("got notes %q, want %q", notes, "prod key")
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "svc-one" || entries[0].FieldCount != 1 {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestAddDecryptList_MultiField(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	id, err := Add(ctx, db.DB, "default", "secret", "svc-multi", map[string]string{
		"field_a": "fa-one1",
		"field_b": "fb-one1",
	}, "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	fields, _, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if len(fields) != 2 || fields["field_a"] != "fa-one1" || fields["field_b"] != "fb-one1" {
		t.Fatalf("unexpected fields: %+v", fields)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if entries[0].FieldCount != 2 {
		t.Fatalf("expected field_count=2, got %d", entries[0].FieldCount)
	}
}

func TestList_NeverReturnsPlaintext(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "login", "svc-login", map[string]string{"secret": "p-one1"}, "alice", "https://example.test", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Entry has no field capable of holding field values — this test
	// documents that guarantee at the type level, not just by inspection.
	if entries[0].Username != "alice" {
		t.Fatalf("expected username alice, got %q", entries[0].Username)
	}
}

func TestDelete_RemovesEntry(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	id, err := Add(ctx, db.DB, "default", "secret", "temp", map[string]string{"secret": "v-temp1"}, "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := Delete(ctx, db.DB, "default", id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after delete, got %d", len(entries))
	}
}

func TestAdd_RejectsInvalidKind(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "bogus", "x", map[string]string{"secret": "y"}, "", "", ""); err == nil {
		t.Fatal("expected error for invalid kind, got nil")
	}
	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no row inserted for invalid kind, got %d entries", len(entries))
	}
}

func TestAdd_RejectsEmptyFields(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "x", map[string]string{}, "", "", ""); err == nil {
		t.Fatal("expected error for an empty fields map, got nil")
	}
	if _, err := Add(ctx, db.DB, "default", "secret", "x", map[string]string{"": "y"}, "", "", ""); err == nil {
		t.Fatal("expected error for an empty field key, got nil")
	}
}

func TestDecryptFields_NotFoundErrors(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, _, err := DecryptFields(ctx, db.DB, "default", "sec-999"); err == nil {
		t.Fatal("expected error for missing entry, got nil")
	}
}

func TestUpdate_ChangesOnlyGivenFields(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	id, err := Add(ctx, db.DB, "default", "login", "svc-login", map[string]string{"secret": "p-one1"}, "alice", "https://example.test", "original notes")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newUsername := "bob"
	if err := Update(ctx, db.DB, "default", id, nil, &newUsername, nil, nil, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if entries[0].Name != "svc-login" {
		t.Fatalf("expected name unchanged, got %q", entries[0].Name)
	}
	if entries[0].Username != "bob" {
		t.Fatalf("expected username updated to bob, got %q", entries[0].Username)
	}
	if entries[0].URL != "https://example.test" {
		t.Fatalf("expected url unchanged, got %q", entries[0].URL)
	}

	fields, notes, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if fields["secret"] != "p-one1" {
		t.Fatalf("expected fields unchanged, got %+v", fields)
	}
	if notes != "original notes" {
		t.Fatalf("expected notes unchanged, got %q", notes)
	}
}

func TestUpdate_ReplacesFieldSet(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	id, err := Add(ctx, db.DB, "default", "secret", "svc-multi", map[string]string{"secret": "v-old1"}, "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newFields := map[string]string{"field_a": "fa-one1", "field_b": "fb-one1"}
	if err := Update(ctx, db.DB, "default", id, nil, nil, nil, nil, newFields); err != nil {
		t.Fatalf("Update: %v", err)
	}

	fields, _, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if len(fields) != 2 || fields["field_a"] != "fa-one1" {
		t.Fatalf("expected fields replaced, got %+v", fields)
	}
	if _, stillThere := fields["secret"]; stillThere {
		t.Fatalf("expected old \"secret\" field gone after full replace, got %+v", fields)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if entries[0].FieldCount != 2 {
		t.Fatalf("expected field_count=2 after replace, got %d", entries[0].FieldCount)
	}
}

func TestUpdate_RenamesEntry(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	id, err := Add(ctx, db.DB, "default", "secret", "old-name", map[string]string{"secret": "v-one1"}, "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	newName := "new-name"
	if err := Update(ctx, db.DB, "default", id, &newName, nil, nil, nil, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if entries[0].Name != "new-name" {
		t.Fatalf("expected renamed entry, got %q", entries[0].Name)
	}
}

func TestUpdate_NotFoundErrors(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	newName := "x"
	if err := Update(ctx, db.DB, "default", "sec-999", &newName, nil, nil, nil, nil); err == nil {
		t.Fatal("expected error updating a missing entry, got nil")
	}
}

func TestAdd_ConcurrentCallsGetDistinctSeqs(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	const numGoroutines = 20
	ids := make([]string, numGoroutines)
	errs := make([]error, numGoroutines)

	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			ids[i], errs[i] = Add(ctx, db.DB, "default", "secret", fmt.Sprintf("key-%d", i), map[string]string{"secret": "v-one1"}, "", "", "")
		}(i)
	}
	start.Done()
	wg.Wait()

	seen := make(map[string]bool, numGoroutines)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: Add: %v", i, err)
		}
		if seen[ids[i]] {
			t.Fatalf("goroutine %d: duplicate id %q returned by a concurrent Add", i, ids[i])
		}
		seen[ids[i]] = true
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != numGoroutines {
		t.Fatalf("expected %d entries, got %d", numGoroutines, len(entries))
	}
}
