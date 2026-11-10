package datatable_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/datatable"
	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
	"github.com/monoes/mono-agent/internal/storage"
)

// Update, Upsert and Delete read the rows and then write them. A writer that commits between the two must not make
// them fail with SQLITE_BUSY_SNAPSHOT (517, which busy_timeout does not wait for): the write lock is taken first (#1460).
func TestWritesSurviveAWriterCommittingBetweenTheReadAndTheWrite(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(s *datatable.Store, tbl datatable.Table) error{
		"update": func(s *datatable.Store, tbl datatable.Table) error {
			_, err := s.Update(ctx, &tbl, datatable.Filter{}, map[string]interface{}{"score": 1.0}, false)
			return err
		},
		"upsert": func(s *datatable.Store, tbl datatable.Table) error {
			_, err := s.Upsert(ctx, &tbl, datatable.Filter{}, map[string]interface{}{"score": 1.0}, false)
			return err
		},
		"delete": func(s *datatable.Store, tbl datatable.Table) error {
			_, err := s.Delete(ctx, tbl, datatable.Filter{}, false)
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
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
			competing := dbconntest.CompetingWriter(t, path)
			fired := false
			trip.BeforeExec = func(string) {
				if !fired {
					fired = true
					competing()
				}
			}
			if err := run(datatable.New(db, "p1"), tbl); err != nil {
				t.Fatalf("%s failed when a writer committed between its read and its write: %v", name, err)
			}
			if !fired {
				t.Fatal("the hook never ran")
			}
		})
	}
}
