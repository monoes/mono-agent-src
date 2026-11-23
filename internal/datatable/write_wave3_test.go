package datatable_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/datatable"
	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
	"github.com/monoes/mono-agent/internal/storage"
)

// A dry run changes nothing, the table's schema included (R12-01).
func TestDryRunWritesDoNotAddColumns(t *testing.T) {
	ctx := context.Background()
	for name, run := range map[string]func(s *datatable.Store, tbl *datatable.Table) error{
		"update": func(s *datatable.Store, tbl *datatable.Table) error {
			_, err := s.Update(ctx, tbl, datatable.Filter{}, map[string]interface{}{"brandnew": "x"}, true)
			return err
		},
		"upsert": func(s *datatable.Store, tbl *datatable.Table) error {
			_, err := s.Upsert(ctx, tbl, datatable.Filter{}, map[string]interface{}{"brandnew": "x"}, true)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, tbl := newTable(t)
			seed(t, s, &tbl)
			before := len(tbl.Columns)
			if err := run(s, &tbl); err != nil {
				t.Fatal(err)
			}
			got, err := s.FindTable(ctx, "Leads")
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Columns) != before {
				t.Fatalf("a dry run added columns: %d -> %d", before, len(got.Columns))
			}
		})
	}
}

// An equality filter on a text column is answered by the database, not by loading and decoding every row (R12-03).
func TestEqFilterOnTextColumnIsPushedToSQL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.db")
	d, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	s0, tbl := newTableIn(t, d.DB)
	seed(t, s0, &tbl)
	d.Close()
	db, trip := dbconntest.OpenPath(t, path)
	var selects []string
	trip.BeforePrepare = func(q string) {
		if strings.Contains(q, "FROM data_table_rows") {
			selects = append(selects, q)
		}
	}
	s := datatable.New(db, "p1")
	f := datatable.Filter{Conditions: []datatable.Condition{{Column: "name", Op: datatable.OpEq, Value: "bob"}}}
	rows, err := s.Select(ctx, tbl, datatable.Query{Filter: f})
	if err != nil || len(rows) != 1 || rows[0]["name"] != "bob" {
		t.Fatalf("select: %v, %v", rows, err)
	}
	up, err := s.Upsert(ctx, &tbl, f, map[string]interface{}{"score": 1.0}, false)
	if err != nil || len(up) != 1 {
		t.Fatalf("upsert: %v, %v", up, err)
	}
	if _, err := s.Delete(ctx, tbl, f, false); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, q := range selects {
		if strings.Contains(q, "SELECT id, data") {
			n++
			if !strings.Contains(q, "json_extract") {
				t.Errorf("a whole-table read for an eq filter: %s", q)
			}
		}
	}
	if n < 3 {
		t.Fatalf("saw %d row reads, want 3", n)
	}
	// equal text of a different case is not a match
	f.Conditions[0].Value = "BOB"
	if rows, _ := s.Select(ctx, tbl, datatable.Query{Filter: f}); len(rows) != 0 {
		t.Fatalf("case-different text matched: %v", rows)
	}
}
