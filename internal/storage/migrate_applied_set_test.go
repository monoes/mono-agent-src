package storage

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// appliedLines runs fn and returns the "applied migration NNN" log lines it produced.
func appliedLines(t *testing.T, fn func()) []string {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	fn()
	var out []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if i := strings.Index(l, "applied migration "); i >= 0 {
			out = append(out, l[i:])
		}
	}
	return out
}

func openAppliedSetDB(t *testing.T) *Database {
	t.Helper()
	db, err := NewDatabase(filepath.Join(t.TempDir(), "applied-set.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

type recordedRow struct {
	Version   int
	Filename  *string
	AppliedAt string
}

func recordedRows(t *testing.T, db *Database) []recordedRow {
	t.Helper()
	rows, err := db.DB.Query(`SELECT version, filename, CAST(applied_at AS TEXT) FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []recordedRow
	for rows.Next() {
		var r recordedRow
		var at *string
		if err := rows.Scan(&r.Version, &r.Filename, &at); err != nil {
			t.Fatal(err)
		}
		if at != nil {
			r.AppliedAt = *at
		}
		out = append(out, r)
	}
	return out
}

// A database recorded up to the version below the newest gets exactly the newest applied: not an older one again,
// not a skipped one.
func TestApplyMigrationsAppliesOnlyTheOneMissingMigration(t *testing.T) {
	all, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	newest := all[len(all)-1]
	db := openAppliedSetDB(t)
	applyMigrationsBelow(t, db, newest.version)

	lines := appliedLines(t, func() {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatal(err)
		}
	})
	want := []string{fmt.Sprintf("applied migration %03d (%s)", newest.version, newest.filename)}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("applied %q, want exactly %q", lines, want)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, newest.version).Scan(&n); err != nil || n != 1 {
		t.Fatalf("newest recorded %d times, %v", n, err)
	}
	if got := len(recordedRows(t, db)); got != len(all) {
		t.Fatalf("%d rows recorded, want %d", got, len(all))
	}
}

// Migrations already recorded are not run again, and a fully migrated database is left byte-for-byte as it was in
// schema_migrations (rows and timestamps), however often ApplyMigrations runs.
func TestApplyMigrationsOnAMigratedDatabaseRunsNothingAndRecordsNothing(t *testing.T) {
	db := openAppliedSetDB(t)
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	before := recordedRows(t, db)
	if len(before) == 0 {
		t.Fatal("nothing recorded after the first run")
	}
	for run := 1; run <= 3; run++ {
		lines := appliedLines(t, func() {
			if err := db.ApplyMigrations(); err != nil {
				t.Fatalf("run %d: %v", run, err)
			}
		})
		if len(lines) != 0 {
			t.Fatalf("run %d applied %q on a migrated database", run, lines)
		}
		if after := recordedRows(t, db); !reflect.DeepEqual(before, after) {
			t.Fatalf("run %d changed schema_migrations", run)
		}
	}
}

// ApplyMigrations on a database that is already current runs on every CLI start and on each test database a command
// opens, so its cost is what this tracks.
func BenchmarkApplyMigrationsOnAMigratedDatabase(b *testing.B) {
	db, err := NewDatabase(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		b.Fatal(err)
	}
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := db.ApplyMigrations(); err != nil {
			b.Fatal(err)
		}
	}
}
