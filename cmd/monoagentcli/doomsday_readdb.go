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

// errWitnessUnreadable is why a read verb cannot call a missing database "off" when the profile's witness place is
// not plainly empty: a linked or odd directory, a witness that is a link, a FIFO or a directory, one that cannot be
// opened or read, or an oversized one with no record in its first MiB. A read that cannot tell is an error, never
// "off": it reaches the page as db_unavailable.
var errWitnessUnreadable = errors.New("the open-ended mode's database is missing or lacks its tables, and the profile's witness place cannot be read plainly")

// witnessHoldsRun says whether the profile's witness file carries a committed or pending record. Without the database
// there is no key to authenticate it, so this is a presence check only: the worst a forged file does is turn "off"
// into an error, which fails safe. It answers (false, nil) only when nothing is plainly there (an invalid profile
// id, or the parent directory, the run directory or the witness file does not exist, or the file holds no record
// within the cap); any other oddity is an error, never "off".
func witnessHoldsRun(cfg *globalConfig) (bool, error) {
	if p := cfg.ProfileID; p == "" || p == "." || p == ".." || filepath.Base(p) != p {
		return false, nil
	}
	runDir := doomsdayRunDirPath(cfg)
	// The run directory and its parent must be real directories: a link there would send the read elsewhere, and the
	// writer's own directory can be a link (it resolves symlinks), so a link is "cannot tell", not "off".
	for _, d := range []string{filepath.Dir(runDir), runDir} {
		st, err := os.Lstat(d)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil || !st.IsDir() {
			return false, errWitnessUnreadable
		}
	}
	path := filepath.Join(runDir, "witness")
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !st.Mode().IsRegular() {
		return false, errWitnessUnreadable
	}
	// Open without following a link or blocking on a FIFO (the path may have been swapped since the Lstat), then
	// judge the file actually opened.
	f, err := openWitnessNoFollow(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errWitnessUnreadable
	}
	defer f.Close()
	ost, err := f.Stat()
	if err != nil || !ost.Mode().IsRegular() {
		return false, errWitnessUnreadable
	}
	b, rerr := io.ReadAll(io.LimitReader(f, witnessReadCap))
	if rerr != nil {
		return false, errWitnessUnreadable
	}
	if bytes.Contains(b, []byte(`"t":"committed"`)) || bytes.Contains(b, []byte(`"t":"pending"`)) {
		return true, nil
	}
	if ost.Size() > witnessReadCap {
		return false, errWitnessUnreadable // a record may sit past the cap: cannot tell
	}
	return false, nil
}

// noSchema is errNoReadableSchema, errWitnessWithoutDB when a witness record says a run existed, or
// errWitnessUnreadable when the witness place cannot be read plainly.
func noSchema(cfg *globalConfig) error {
	held, err := witnessHoldsRun(cfg)
	if err != nil {
		return err
	}
	if held {
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
