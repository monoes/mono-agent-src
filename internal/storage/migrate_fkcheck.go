package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

// foreignKeyViolations runs PRAGMA foreign_key_check and returns one "table row N -> parent (fk i)" line per
// orphaned row. It works with enforcement off, which is how the migrations run.
func foreignKeyViolations(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s row %d -> %s (fk %d)", table, rowid.Int64, parent, fkid))
	}
	return out, rows.Err()
}

// logForeignKeyViolations is the check after a run that applied migrations: table rebuilds run with
// foreign keys off and can orphan children without a word. It only logs (a violation is not a reason to refuse
// to start); at most the first 20 are listed.
func logForeignKeyViolations(ctx context.Context, conn *sql.Conn) {
	bad, err := foreignKeyViolations(ctx, conn)
	if err != nil {
		log.Printf("WARN: foreign key check after migrations failed: %v", err)
		return
	}
	if len(bad) == 0 {
		return
	}
	shown := bad
	if len(shown) > 20 {
		shown = shown[:20]
	}
	log.Printf("WARN: %d foreign key violation(s) after migrations (first %d): %v", len(bad), len(shown), shown)
}
