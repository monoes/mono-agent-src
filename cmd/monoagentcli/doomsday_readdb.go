//go:build doomsday

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// errNoReadableSchema is why a read verb has nothing to read: no database file, or one older than the open-ended
// mode's schema. No run can be live behind either, so the read verbs treat them as the mode off.
var errNoReadableSchema = errors.New("no database with the open-ended mode's schema")

// doomsdayTables are what a database must hold before the mode can have run on it.
var doomsdayTables = []string{"doomsday_runs", "doomsday_charters", "doomsday_audit"}

// openReadDB is the read verbs' open: it creates nothing, migrates nothing and writes nothing (mode=ro). A missing
// file or a schema older than the mode is errNoReadableSchema; a file that cannot be read is any other error, and
// the caller never calls that "off". It resolves cfg.ProfileID against the profiles table as initDB does.
func openReadDB(cfg *globalConfig) (*sql.DB, error) {
	if inClientMode() {
		return nil, errClientModeDB
	}
	path := expandPath(cfg.DBPath)
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNoReadableSchema
	}
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	var n int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN (?, ?, ?)`,
		doomsdayTables[0], doomsdayTables[1], doomsdayTables[2]).Scan(&n); err != nil {
		db.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}
	if n != len(doomsdayTables) {
		db.Close()
		return nil, errNoReadableSchema
	}
	if cfg.ProfileID != "" {
		id, rerr := profiledir.Resolve(ctx, db, cfg.ProfileID)
		if rerr != nil {
			db.Close()
			return nil, fmt.Errorf("profile %q: %w", cfg.ProfileID, rerr)
		}
		cfg.ProfileID = id
	}
	return db, nil
}
