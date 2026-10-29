package agentpurposes

import (
	"context"
	"database/sql"
	"testing"

	"github.com/monoes/mono-agent/internal/testdb"
)

func TestStoreRoundTripAndReset(t *testing.T) {
	db := testdb.Open(t).DB
	ctx := context.Background()
	set, err := Load(ctx, db)
	if err != nil || len(set) != 0 {
		t.Fatalf("empty store: %v %v", set, err)
	}
	set["coding"] = Settings{Pairs: []Pair{{"claude", "opus"}}, Rule: RuleOrder, DifferentFromAuthor: true, Fallback: true, BudgetSharePct: 30, Effort: "high"}
	set["reviewing"] = Settings{Pairs: []Pair{{"codex", ""}}}
	if err := Save(ctx, db, set); err != nil {
		t.Fatal(err)
	}
	got, err := Load(ctx, db)
	if err != nil || len(got) != 2 || got["coding"].Rule != RuleOrder || !got["coding"].DifferentFromAuthor || got["coding"].Pairs[0] != (Pair{"claude", "opus"}) {
		t.Fatalf("loaded %+v %v", got, err)
	}
	delete(got, "coding")
	if err := Save(ctx, db, got); err != nil {
		t.Fatal(err)
	}
	again, _ := Load(ctx, db)
	if _, ok := again["coding"]; ok || len(again) != 1 {
		t.Fatalf("after reset: %+v", again)
	}
	if err := Save(ctx, db, Set{}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db); n != 0 {
		t.Fatalf("an empty set leaves %d rows", n)
	}
}

// A row that cannot be read is an error, never an empty set: the operator's restriction is unknown.
func TestLoadRefusesACorruptRowAndUnknownPurposes(t *testing.T) {
	db := testdb.Open(t).DB
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO configs (name, config_data) VALUES (?, ?)`, ConfigName, `{not json`); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ctx, db); err == nil {
		t.Fatal("a corrupt row was read as an empty set")
	}
	if _, err := db.Exec(`UPDATE configs SET config_data = ? WHERE name = ?`, `{"v":1,"purposes":{"made-up":{"pairs":[{"runtime":"claude","model":"opus"}]}}}`, ConfigName); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ctx, db); err == nil {
		t.Fatal("a purpose that is not in the catalog was loaded")
	}
	if _, err := db.Exec(`UPDATE configs SET config_data = ? WHERE name = ?`, `{"v":1,"purposes":{"coding":{"rule":"random"}}}`, ConfigName); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ctx, db); err == nil {
		t.Fatal("settings that do not validate were loaded")
	}
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM configs WHERE name = ?`, ConfigName).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
