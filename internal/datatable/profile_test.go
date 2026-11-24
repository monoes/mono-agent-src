package datatable_test

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/datatable"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/testdb"
)

// A store built with no profile refuses every call: there is no default
// profile to fall back on.
func TestEmptyProfileRefusesEveryMethod(t *testing.T) {
	ctx := context.Background()
	s := datatable.New(testdb.Open(t).DB, "")
	tbl := datatable.Table{ID: "t", Name: "T"}
	rows := []map[string]interface{}{{"a": 1}}
	calls := map[string]func() error{
		"CreateTable": func() error { _, err := s.CreateTable(ctx, "T", nil); return err },
		"FindTable":   func() error { _, err := s.FindTable(ctx, "T"); return err },
		"ListTables":  func() error { _, err := s.ListTables(ctx); return err },
		"CountRows":   func() error { _, err := s.CountRows(ctx, tbl); return err },
		"RenameTable": func() error { _, err := s.RenameTable(ctx, tbl, "U"); return err },
		"DeleteTable": func() error { return s.DeleteTable(ctx, tbl) },
		"ClearTable":  func() error { _, err := s.ClearTable(ctx, tbl); return err },
		"Select":      func() error { _, err := s.Select(ctx, tbl, datatable.Query{}); return err },
		"Insert":      func() error { _, err := s.Insert(ctx, &tbl, rows); return err },
		"Update":      func() error { _, err := s.Update(ctx, &tbl, datatable.Filter{}, rows[0], false); return err },
		"Upsert":      func() error { _, err := s.Upsert(ctx, &tbl, datatable.Filter{}, rows[0], false); return err },
		"Delete":      func() error { _, err := s.Delete(ctx, tbl, datatable.Filter{}, false); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, profiledir.ErrNoProfile) {
			t.Errorf("%s with no profile: %v, want ErrNoProfile", name, err)
		}
	}
}

// One profile's tables are invisible to another's.
func TestTablesAreIsolatedPerProfile(t *testing.T) {
	ctx := context.Background()
	db := testdb.Open(t).DB
	a, b := datatable.New(db, "A"), datatable.New(db, "B")
	if _, err := a.CreateTable(ctx, "Leads", []datatable.Column{{Name: "name"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.FindTable(ctx, "Leads"); err == nil {
		t.Error("profile B found profile A's table")
	}
	if list, err := b.ListTables(ctx); err != nil || len(list) != 0 {
		t.Errorf("profile B lists %v, %v", list, err)
	}
}

// A Table value that names another profile's table (stale or forged) reaches none of that table's rows.
func TestRowOpsRefuseAnotherProfilesTable(t *testing.T) {
	ctx := context.Background()
	db := testdb.Open(t).DB
	a, b := datatable.New(db, "A"), datatable.New(db, "B")
	tbl, err := a.CreateTable(ctx, "Leads", []datatable.Column{{Name: "name"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Insert(ctx, &tbl, []map[string]interface{}{{"name": "x"}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.CountRows(ctx, tbl); n != 0 {
		t.Errorf("B counted %d rows of A's table", n)
	}
	if n, _ := b.ClearTable(ctx, tbl); n != 0 {
		t.Errorf("B cleared %d rows of A's table", n)
	}
	if err := b.DeleteTable(ctx, tbl); err == nil {
		t.Error("B deleted A's table without an error")
	}
	if got, _ := b.Select(ctx, tbl, datatable.Query{}); len(got) != 0 {
		t.Errorf("B selected %d rows of A's table", len(got))
	}
	if n, err := a.CountRows(ctx, tbl); err != nil || n != 1 {
		t.Errorf("A's rows after B's attempts: %d, %v; want 1", n, err)
	}
}

// Names are unique ignoring case, as lookups are.
func TestCreateTableRefusesCaseVariant(t *testing.T) {
	ctx := context.Background()
	s := datatable.New(testdb.Open(t).DB, "A")
	if _, err := s.CreateTable(ctx, "Leads", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTable(ctx, "leads", nil); err == nil {
		t.Error("created \"leads\" next to \"Leads\"")
	}
}
