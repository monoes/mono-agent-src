// wails-app/app_db_readonly.go
//
// The app's own database handle is read-only. The CLI is the one writer and
// the one that migrates (app_db_prepare.go); the app keeps a handle only for
// the reads that stay in process, each listed with its reason in
// doctrine_test.go. query_only makes SQLite refuse any write on this handle,
// so a write that slips into the app fails loudly instead of racing the CLI.
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// openReadOnlyDB opens path for reading. A database that does not exist yet
// (a first start before `db migrate` could run) is created empty and owner-only,
// as the CLI creates it; the CLI migrates it later.
func openReadOnlyDB(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving database path: %w", err)
	}
	_, statErr := os.Stat(abs)
	created := errors.Is(statErr, fs.ErrNotExist)
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: abs}).EscapedPath()+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}
	if created {
		_ = os.Chmod(abs, 0o600)
	}
	return db, nil
}
