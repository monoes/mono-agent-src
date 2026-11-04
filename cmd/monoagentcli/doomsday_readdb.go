//go:build doomsday

package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// errNoReadableSchema is why a read verb has nothing to read: no database file, or one older than the open-ended
// mode's schema. No run can be live behind either, so the read verbs treat them as the mode off.
var errNoReadableSchema = errors.New("no database with the open-ended mode's schema")

// errWitnessWithoutDB is why a read verb cannot call a missing database or missing doomsday tables "off": the profile's
// witness directory still holds a committed or pending record, so a run existed (and may be live) and the database
// that should describe it is gone or replaced. It reaches the page as db_unavailable, never as off.
var errWitnessWithoutDB = errors.New("the open-ended mode's database is missing or lacks its tables, but the profile's witness holds a run record")

// witnessReadCap bounds how much of the witness file is looked at.
const witnessReadCap = 1 << 20

// doomsdayRunDirPath is the profile's run directory (witness, markers), next to the database.
func doomsdayRunDirPath(cfg *globalConfig) string {
	return filepath.Join(filepath.Dir(expandPath(cfg.DBPath)), "doomsday", cfg.ProfileID)
}

// witnessHoldsRun says whether the profile's witness file (a plain regular file, not followed through a link) carries a
// committed or pending record. Without the database there is no key to authenticate it, so this is a presence check
// only: the worst a forged file does is turn "off" into an error, which fails safe.
func witnessHoldsRun(cfg *globalConfig) bool {
	if p := cfg.ProfileID; p == "" || p == "." || p == ".." || filepath.Base(p) != p {
		return false
	}
	runDir := doomsdayRunDirPath(cfg)
	// The run directory and its parent must be real directories: a link there would send the read elsewhere.
	for _, d := range []string{filepath.Dir(runDir), runDir} {
		if st, err := os.Lstat(d); err != nil || !st.IsDir() {
			return false
		}
	}
	path := filepath.Join(runDir, "witness")
	if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() {
		return false
	}
	// Open without following a link or blocking on a FIFO (the path may have been swapped since the Lstat), then
	// judge the file actually opened.
	f, err := openWitnessNoFollow(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return false
	}
	b, _ := io.ReadAll(io.LimitReader(f, witnessReadCap))
	return bytes.Contains(b, []byte(`"t":"committed"`)) || bytes.Contains(b, []byte(`"t":"pending"`))
}

// noSchema is errNoReadableSchema, or errWitnessWithoutDB when a witness record says a run existed.
func noSchema(cfg *globalConfig) error {
	if witnessHoldsRun(cfg) {
		return errWitnessWithoutDB
	}
	return errNoReadableSchema
}

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
		return nil, noSchema(cfg)
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
		return nil, noSchema(cfg)
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
