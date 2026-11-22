package storage

import "testing"

// Migration 170 adds four nullable columns to jev_usage. It is additive: a database at the 169 head keeps its rows,
// which read NULL in the new columns, a new row can fill them, and a second run changes nothing.
func TestMigration170AddsTheProviderColumnsToJevUsage(t *testing.T) {
	db := open156(t)
	applyMigrationsBelow(t, db, 170)
	if _, err := db.DB.Exec(`INSERT INTO jev_usage (profile_id, surface, model, input_tokens) VALUES ('p', 'hil', 'jev-1', 40)`); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	var n, nulls int
	if err := db.DB.QueryRow(`SELECT COUNT(*), SUM(provider IS NULL AND output_tokens IS NULL AND probability_kind IS NULL AND cost_usd IS NULL)
		FROM jev_usage WHERE input_tokens = 40`).Scan(&n, &nulls); err != nil || n != 1 || nulls != 1 {
		t.Fatalf("the old row after the upgrade: %d rows, %d with NULL new columns, %v", n, nulls, err)
	}
	if _, err := db.DB.Exec(`INSERT INTO jev_usage (profile_id, surface, model, provider, output_tokens, probability_kind, cost_usd)
		VALUES ('p', 'hil', 'm', 'local', 1, 'logprob', 0.5)`); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 170`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("170 recorded %d times, %v", v, err)
	}
}
