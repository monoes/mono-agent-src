package storage

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// keepPreMigrateSnapshots is how many pre-migrate-*.db snapshots survive in
// the backups folder; older ones are deleted after each new snapshot.
const keepPreMigrateSnapshots = 3

// migrateStderr receives the snapshot warnings and the database-ahead
// notice. A variable so tests can capture it.
var migrateStderr io.Writer = os.Stderr

// migrateNow is the clock used for snapshot names (tests pin it).
var migrateNow = time.Now

// aheadNoticed remembers the databases whose "migrated by a newer
// monoagent" notice was already printed in this process, so a process that
// opens the same database through several paths says it once.
var aheadNoticed sync.Map

// backupsDir is where pre-migration snapshots go: a "backups" folder next
// to the database (~/.monoagent/backups for the default database), so a
// custom --db-path keeps its snapshots beside it.
func (d *Database) backupsDir() string {
	return filepath.Join(filepath.Dir(d.dbPath), "backups")
}

// isFileBacked reports whether d is a real on-disk database that can be
// snapshotted (not one wrapped around an existing *sql.DB, nor in-memory).
func (d *Database) isFileBacked() bool {
	p := d.dbPath
	return p != "" && !strings.Contains(p, ":memory:") && !strings.Contains(p, "mode=memory")
}

// hasUserTables reports whether the database holds anything besides the
// migration bookkeeping table — a fresh database has nothing worth a
// snapshot.
func hasUserTables(ctx context.Context, conn *sql.Conn) (bool, error) {
	var n int
	err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name <> 'schema_migrations' AND name NOT LIKE 'sqlite_%'`).Scan(&n)
	return n > 0, err
}

// snapshotBeforeMigrate writes a consistent copy of the database to
// <backups>/pre-migrate-<UTC timestamp>-v<lastApplied>.db (mode 0600) before
// pending migrations run, then prunes older snapshots down to the newest
// keepPreMigrateSnapshots. It never blocks migrating: every failure is a
// warning on stderr and the caller carries on.
func (d *Database) snapshotBeforeMigrate(ctx context.Context, conn *sql.Conn, lastApplied int) {
	if !d.isFileBacked() {
		return
	}
	hasData, err := hasUserTables(ctx, conn)
	if err != nil {
		fmt.Fprintf(migrateStderr, "monoagent: warning: could not inspect the database before migrating, no snapshot taken: %v\n", err)
		return
	}
	if !hasData {
		return
	}
	path, err := d.writeSnapshot(ctx, conn, lastApplied)
	if err != nil {
		fmt.Fprintf(migrateStderr, "monoagent: warning: could not snapshot the database before migrating (continuing without one): %v\n", err)
		return
	}
	fmt.Fprintf(migrateStderr, "monoagent: database snapshot before migrating: %s\n", path)
	if err := prunePreMigrateSnapshots(d.backupsDir(), keepPreMigrateSnapshots); err != nil {
		fmt.Fprintf(migrateStderr, "monoagent: warning: could not prune old database snapshots: %v\n", err)
	}
}

// SnapshotBeforeAdoption writes the same kind of snapshot as snapshotBeforeMigrate
// for the first-run account adoption. Migrations have already run by then, so
// the snapshot holds the database as migrated, before adoption changes it.
// It never blocks the caller: every failure is a warning on stderr.
func (d *Database) SnapshotBeforeAdoption(ctx context.Context) {
	if !d.isFileBacked() {
		return
	}
	conn, err := d.DB.Conn(ctx)
	if err != nil {
		fmt.Fprintf(migrateStderr, "monoagent: warning: could not snapshot the database before adopting a login (continuing without one): %v\n", err)
		return
	}
	defer conn.Close()
	hasData, err := hasUserTables(ctx, conn)
	if err != nil || !hasData {
		return
	}
	var last sql.NullInt64
	_ = conn.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&last)
	// The claim that follows can keep failing (a read-only database), and
	// then this runs on every start: one snapshot of this schema version a
	// day is enough.
	if d.hasRecentAdoptionSnapshot(int(last.Int64)) {
		return
	}
	name := fmt.Sprintf("pre-migrate-%s-v%03d-adopt.db", migrateNow().UTC().Format("20060102T150405"), last.Int64)
	path, err := d.writeSnapshotAs(ctx, conn, name)
	if err != nil {
		fmt.Fprintf(migrateStderr, "monoagent: warning: could not snapshot the database before adopting a login (continuing without one): %v\n", err)
		return
	}
	fmt.Fprintf(migrateStderr, "monoagent: database snapshot before adopting a login: %s\n", path)
	if err := prunePreMigrateSnapshots(d.backupsDir(), keepPreMigrateSnapshots); err != nil {
		fmt.Fprintf(migrateStderr, "monoagent: warning: could not prune old database snapshots: %v\n", err)
	}
}

// adoptionSnapshotEvery is how long an adoption snapshot of a schema version
// stands in for a new one.
const adoptionSnapshotEvery = time.Hour

// hasRecentAdoptionSnapshot reports whether backups holds an adoption
// snapshot of schema version v written within adoptionSnapshotEvery, not
// dated in the future, and not older than the database's last change (one
// that predates the data stands in for nothing).
func (d *Database) hasRecentAdoptionSnapshot(v int) bool {
	var changed time.Time
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(d.dbPath + suffix); err == nil && fi.ModTime().After(changed) {
			changed = fi.ModTime()
		}
	}
	paths, _ := filepath.Glob(filepath.Join(d.backupsDir(), fmt.Sprintf("pre-migrate-*-v%03d-adopt.db", v)))
	now := migrateNow()
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		age := now.Sub(fi.ModTime())
		if age >= 0 && age < adoptionSnapshotEvery && !fi.ModTime().Before(changed) {
			return true
		}
	}
	return false
}

func (d *Database) writeSnapshot(ctx context.Context, conn *sql.Conn, lastApplied int) (string, error) {
	name := fmt.Sprintf("pre-migrate-%s-v%03d.db", migrateNow().UTC().Format("20060102T150405"), lastApplied)
	return d.writeSnapshotAs(ctx, conn, name)
}

// writeSnapshotAs writes the snapshot to <backups>/<name>.
func (d *Database) writeSnapshotAs(ctx context.Context, conn *sql.Conn, name string) (string, error) {
	dir := d.backupsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	// VACUUM INTO accepts an existing empty file, so the snapshot is
	// created 0600 up front instead of being chmod'ed after it was written.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	f.Close()
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// keepAdoptionSnapshots is how many pre-migrate-*-adopt.db snapshots survive.
// They are pruned apart from the real pre-migrate ones: up to one is written an
// hour, and they must not push out the only snapshot taken before a migration.
const keepAdoptionSnapshots = 3

// prunePreMigrateSnapshots deletes pre-migrate-*.db files in dir beyond the
// newest keep (names sort by their timestamp), and -adopt.db ones beyond the
// newest keepAdoptionSnapshots, counted separately. Other files are left alone.
func prunePreMigrateSnapshots(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var snaps, adopt []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "pre-migrate-") || !strings.HasSuffix(n, ".db") {
			continue
		}
		if strings.HasSuffix(n, "-adopt.db") {
			adopt = append(adopt, n)
		} else {
			snaps = append(snaps, n)
		}
	}
	firstErr := pruneOldest(dir, snaps, keep)
	if err := pruneOldest(dir, adopt, keepAdoptionSnapshots); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// pruneOldest removes all but the newest keep of names (which sort by time).
func pruneOldest(dir string, names []string, keep int) error {
	if len(names) <= keep {
		return nil
	}
	sort.Strings(names)
	var firstErr error
	for _, n := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && firstErr == nil {
			firstErr = err
		}
		// Side files left by anyone who opened the snapshot in WAL mode.
		for _, side := range []string{"-wal", "-shm", "-journal"} {
			os.Remove(filepath.Join(dir, n+side))
		}
	}
	return firstErr
}

// highestEmbeddedVersion is the newest migration this binary carries.
func highestEmbeddedVersion(migrations []migration) int {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].version
}

// noticeSchemaAhead prints, once per database per process, that the
// database records migrations this binary doesn't know. It never refuses to
// run: the older binary keeps working, the user is just told to update.
func (d *Database) noticeSchemaAhead(dbVersion, binaryVersion int) {
	if _, seen := aheadNoticed.LoadOrStore(d.dbPath, true); seen {
		return
	}
	fmt.Fprintf(migrateStderr,
		"monoagent: this database was migrated by a newer monoagent (v%03d > v%03d); update monoagentcli (`monoagentcli update`) — this older binary may not understand newer data in %s\n",
		dbVersion, binaryVersion, d.dbPath)
}

// SchemaAhead compares the newest migration recorded in schema_migrations
// with the newest one embedded in this binary, read-only (it creates
// nothing). ahead is true when the database was migrated by a newer
// monoagent than the running one. A database with no schema_migrations
// table reports dbVersion 0.
func (d *Database) SchemaAhead(ctx context.Context) (dbVersion, binaryVersion int, ahead bool, err error) {
	migrations, err := embeddedMigrations()
	if err != nil {
		return 0, 0, false, err
	}
	binaryVersion = highestEmbeddedVersion(migrations)
	var hasTable int
	if err := d.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&hasTable); err != nil {
		return 0, binaryVersion, false, fmt.Errorf("inspecting schema_migrations: %w", err)
	}
	if hasTable == 0 {
		return 0, binaryVersion, false, nil
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&dbVersion); err != nil {
		return 0, binaryVersion, false, fmt.Errorf("reading schema_migrations: %w", err)
	}
	return dbVersion, binaryVersion, dbVersion > binaryVersion, nil
}

// SnapshotBeforeProfileDelete writes a consistent copy of the database to
// <backups>/pre-delete-profile-<UTC timestamp>-<counter>.db (mode 0600) and returns its
// path. Unlike the pre-migrate snapshots these are never pruned, and a
// database that cannot be snapshotted is an error: the delete must not go on
// without its backup.
func (d *Database) SnapshotBeforeProfileDelete(ctx context.Context) (string, error) {
	if !d.isFileBacked() {
		return "", fmt.Errorf("storage: the database is not a file, so it cannot be backed up")
	}
	conn, err := d.DB.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	stamp := migrateNow().UTC().Format("20060102T150405")
	// The counter is always there and zero-padded so that names sort in the
	// order they were made (T-02.db would sort before T.db). Two deletes in
	// the same second must not share a snapshot.
	var name string
	for n := 1; n < 1000; n++ {
		name = fmt.Sprintf("pre-delete-profile-%s-%03d.db", stamp, n)
		if _, err := os.Stat(filepath.Join(d.backupsDir(), name)); os.IsNotExist(err) {
			break
		}
	}
	return d.writeSnapshotAs(ctx, conn, name)
}

// ProfileDeleteBackups lists the pre-delete-profile-*.db snapshots, oldest first.
func (d *Database) ProfileDeleteBackups() ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(d.backupsDir(), "pre-delete-profile-*.db"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}
