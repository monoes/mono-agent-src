package storage

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

// A run that applies migrations logs the orphans a table rebuild left behind (and does not fail).
func TestForeignKeyViolationsAreFoundAndLogged(t *testing.T) {
	ctx := context.Background()
	db, err := NewDatabase(t.TempDir() + "/fk.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, q := range []string{
		"CREATE TABLE fk_parent (id INTEGER PRIMARY KEY)",
		"CREATE TABLE fk_child (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES fk_parent(id))",
		"PRAGMA foreign_keys = OFF",
		"INSERT INTO fk_child (id, pid) VALUES (1, 99)",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	bad, err := foreignKeyViolations(ctx, conn)
	if err != nil || len(bad) != 1 || !strings.Contains(bad[0], "fk_child") {
		t.Fatalf("violations = %v, %v", bad, err)
	}
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	logForeignKeyViolations(ctx, conn)
	if !strings.Contains(buf.String(), "1 foreign key violation") {
		t.Errorf("log = %q", buf.String())
	}
}
