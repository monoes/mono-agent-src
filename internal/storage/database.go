package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/data"

	_ "modernc.org/sqlite"
)

// Database wraps a *sql.DB connection to a SQLite database and provides
// migration and maintenance helpers.
type Database struct {
	DB     *sql.DB
	dbPath string
	kg     kgSyncs
}

// kgSyncCloseWait bounds how long Close waits for the knowledge-graph syncs
// still running (each is capped by its own monomind timeout too).
const kgSyncCloseWait = 5 * time.Second

// kgSyncs tracks the knowledge-graph syncs a Database's upserts start in the
// background. They read the database, so Close waits for them, for at most
// kgSyncCloseWait: a short-lived command finishes the syncs it started
// instead of cutting them off, and a stuck one cannot hold its exit. Once
// closing starts, no new sync starts.
type kgSyncs struct {
	mu      sync.Mutex
	closing bool
	wg      sync.WaitGroup
}

func (k *kgSyncs) start(fn func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closing {
		return
	}
	k.wg.Add(1)
	go func() {
		defer k.wg.Done()
		fn()
	}()
}

// stop refuses new syncs and waits up to limit for the running ones; false
// means some were still running.
func (k *kgSyncs) stop(limit time.Duration) bool {
	k.mu.Lock()
	k.closing = true
	k.mu.Unlock()
	done := make(chan struct{})
	go func() {
		k.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(limit):
		return false
	}
}

// pingDeadline bounds how long the first open retries a locked database;
// pingContext is the probe (replaced in tests).
var (
	pingDeadline = 10 * time.Second
	pingContext  = func(ctx context.Context, db *sql.DB) error { return db.PingContext(ctx) }
)

// NewDatabase opens (or creates) a SQLite database at dbPath and configures it
// for production use: WAL journal mode, a busy timeout of 5 seconds, and
// foreign key enforcement.
func NewDatabase(dbPath string) (*Database, error) {
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolving database path: %w", err)
	}

	// Pragmas are passed via the DSN (not a one-off db.Exec after opening) so
	// modernc.org/sqlite applies them to EVERY pooled connection, not just
	// whichever one happened to run an Exec call. A prior fix capped the pool
	// at 1 connection to work around exactly this gap, but that serializes
	// ALL access through a single connection — any code path that holds one
	// query's rows open while starting a second query on the same *sql.DB
	// (a normal, supported database/sql pattern) deadlocks waiting for the
	// pool's only connection to free up. DSN-level pragmas fix the root cause
	// (missing busy_timeout on fresh connections) without that constraint.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)",
		absPath,
	)
	created := isNewFile(absPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Verify the connection is usable.
	// The first open of a fresh file switches it to WAL, which needs an
	// exclusive lock the busy timeout does not cover; a concurrent first
	// start can hit SQLITE_BUSY here, so a busy answer is retried. No ping is
	// cut off: a slow one is disk I/O, not a lock (a fresh file's WAL switch
	// took up to 14s on a loaded disk); only the retrying is bounded
	// (retryBusyPing).
	pingErr := pingContext(context.Background(), db)
	if isBusy(pingErr) {
		pingErr = retryBusyPing(db, absPath, pingErr)
	}
	if pingErr != nil {
		db.Close()
		if errors.Is(pingErr, errStayedLocked) {
			return nil, pingErr
		}
		return nil, fmt.Errorf("pinging database: %w", pingErr)
	}
	privatizeNewDB(created, absPath)

	return &Database{DB: db, dbPath: absPath}, nil
}

// errStayedLocked marks the error of a database that answered SQLITE_BUSY
// for the whole retry window.
var errStayedLocked = errors.New("stayed locked")

func isBusy(err error) bool { return err != nil && strings.Contains(err.Error(), "SQLITE_BUSY") }

// retryBusyPing pings db again, every 50ms, until it answers, fails with
// something other than SQLITE_BUSY, or pingDeadline has passed since the first
// busy answer (first). No new retry starts once that window has passed, but a
// retry already running is awaited, never cut off: it is either waiting for the
// lock, which SQLite's busy_timeout ends, or it got the lock and is switching
// the file to WAL, disk I/O that may be slow and must not be mistaken for a
// lock. A locked database therefore fails after at most the window plus one
// busy_timeout (10s + 5s).
func retryBusyPing(db *sql.DB, absPath string, first error) error {
	window := time.Now().Add(pingDeadline)
	last := first
	for {
		time.Sleep(50 * time.Millisecond)
		if time.Now().After(window) {
			return fmt.Errorf("database %s %w for %s: %w", absPath, errStayedLocked, pingDeadline, last)
		}
		err := pingContext(context.Background(), db)
		if !isBusy(err) {
			return err // answered, or failed with something other than a lock
		}
		last = err
	}
}

// ApplyMigrations reads embedded SQL migration files from data/migrations/ and
// applies any that have not yet been recorded in the schema_migrations table.
// Migrations are applied in filename order (e.g. 001_initial.sql before
// 002_add_feature.sql).
func (d *Database) ApplyMigrations() error {
	// Concurrent first starts (the desktop app launches several CLI calls at
	// once) would each see the same pending set and race to apply it. A
	// cross-process lock serialises them; the applied set is read after the
	// lock is held, so the losers find everything already recorded.
	if d.dbPath != "" && !strings.Contains(d.dbPath, ":memory:") {
		lf, err := os.OpenFile(d.dbPath+".migrate.lock", os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return fmt.Errorf("opening migration lock: %w", err)
		}
		defer lf.Close()
		if err := lockFile(lf); err != nil {
			return fmt.Errorf("taking migration lock: %w", err)
		}
		defer unlockFile(lf)
	}
	return d.applyMigrations()
}

// recordMigrationSQL tolerates an already-recorded version: an applier that
// ran without the cross-process lock may have recorded it first.
const recordMigrationSQL = "INSERT OR IGNORE INTO schema_migrations (version, filename) VALUES (?, ?)"

type migrationExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func recordMigration(e migrationExecer, version int, filename string) error {
	_, err := e.Exec(recordMigrationSQL, version, filename)
	return err
}

func (d *Database) applyMigrations() error {
	ctx := context.Background()

	// Migrations run on a single dedicated connection with foreign key
	// enforcement DISABLED. The DSN bakes foreign_keys(ON) into every pooled
	// connection, but table-rebuild migrations (the standard SQLite
	// "CREATE new / copy / DROP old / RENAME" recipe, e.g. migration 014)
	// require FK enforcement OFF: dropping the old table fires an implicit
	// DELETE that would cascade-delete child rows (person_messages, people_tags)
	// or fail outright on NO-ACTION references (action_targets, posts). PRAGMA
	// foreign_keys is a no-op inside a transaction, so it must be toggled here,
	// outside any transaction, and it only affects the connection it runs on —
	// hence a dedicated *sql.Conn used for the entire migration run.
	conn, err := d.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquiring migration connection: %w", err)
	}
	defer func() {
		// Restore FK enforcement before this connection returns to the pool,
		// otherwise later queries reusing it would run with FKs disabled.
		conn.ExecContext(ctx, "PRAGMA foreign_keys = ON")
		conn.Close()
	}()

	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("disabling foreign keys for migrations: %w", err)
	}

	// Ensure the schema_migrations table itself exists before we query it.
	_, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return fmt.Errorf("ensuring schema_migrations table: %w", err)
	}

	// Runner hardening: record each applied migration's FILENAME alongside
	// its version. schema_migrations keys on the version INT alone, which is
	// how two branches shipping different files at 023/024 silently skipped
	// each other's migrations on merged binaries — the version said
	// "applied" while the shape said otherwise. The version stays the dedup
	// key (no behavior change); the filename only makes future version
	// collisions detectable, via the aliasing WARN below.
	fnameCols, err := columnExistsOn(ctx, conn, "schema_migrations", "filename")
	if err != nil {
		return fmt.Errorf("inspecting schema_migrations: %w", err)
	}
	if !fnameCols {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE schema_migrations ADD COLUMN filename TEXT`); err != nil {
			return fmt.Errorf("adding schema_migrations.filename: %w", err)
		}
	}

	migrations, err := embeddedMigrations()
	if err != nil {
		return err
	}

	// Aliasing detection: a recorded version whose (non-empty) filename
	// doesn't match the current file at that version — or whose file has
	// vanished from the set entirely — means two branches shipped different
	// migrations under one number. Rows recorded before the filename column
	// existed have no filename to compare and are skipped.
	currentFile := make(map[int]string, len(migrations))
	for _, m := range migrations {
		currentFile[m.version] = m.filename
	}
	binaryVersion := highestEmbeddedVersion(migrations)
	applied := map[int]bool{}
	lastApplied := 0
	recRows, err := conn.QueryContext(ctx, `SELECT version, filename FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("reading schema_migrations: %w", err)
	}
	for recRows.Next() {
		var version int
		var filename sql.NullString
		if err := recRows.Scan(&version, &filename); err != nil {
			recRows.Close()
			return fmt.Errorf("scanning schema_migrations: %w", err)
		}
		applied[version] = true
		if version > lastApplied {
			lastApplied = version
		}
		if !filename.Valid || filename.String == "" {
			continue
		}
		cur, ok := currentFile[version]
		switch {
		case !ok && version > binaryVersion:
			// Not aliasing: a newer monoagent migrated this database;
			// reported once below.
		case !ok:
			log.Printf("WARN: schema_migrations version %d recorded as %q but no migration file with that version exists anymore — version aliasing suspected", version, filename.String)
		case cur != filename.String:
			log.Printf("WARN: schema_migrations version %d recorded as %q but the current migration file is %q — version aliasing suspected", version, filename.String, cur)
		}
	}
	recRows.Close()
	if err := recRows.Err(); err != nil {
		return fmt.Errorf("iterating schema_migrations: %w", err)
	}

	// A database migrated by a newer binary: say so clearly, but keep
	// running (no new restrictions) — doctor reports it too.
	if lastApplied > binaryVersion {
		d.noticeSchemaAhead(lastApplied, binaryVersion)
	}

	// Snapshot before changing the schema of a database that has data, so
	// a bad migration (or an irreversible DROP) can be rolled back by hand.
	for _, m := range migrations {
		if !applied[m.version] {
			d.snapshotBeforeMigrate(ctx, conn, lastApplied)
			break
		}
	}

	for _, m := range migrations {
		// Check if this version has already been applied.
		var exists int
		err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("checking migration version %d: %w", m.version, err)
		}
		if exists > 0 {
			continue
		}

		// Read the migration SQL.
		content, err := data.MigrationsFS.ReadFile("migrations/" + m.filename)
		if err != nil {
			return fmt.Errorf("reading migration file %s: %w", m.filename, err)
		}

		// Execute the migration within a transaction.
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("beginning transaction for migration %d: %w", m.version, err)
		}

		// Split on semicolons and execute each statement individually.
		// This is necessary because modernc.org/sqlite's Exec does not
		// support multiple statements in a single call reliably.
		statements := splitStatements(string(content))
		for _, stmt := range statements {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := tx.Exec(stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("executing migration %d statement %q: %w", m.version, truncate(stmt, 80), err)
			}
		}

		// Record the migration as applied.
		if err := recordMigration(tx, m.version, m.filename); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %d: %w", m.version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", m.version, err)
		}

		log.Printf("applied migration %03d (%s)", m.version, m.filename)
	}

	// Heal any schema-shape drift the recorded versions can't see (see
	// reconcile.go for why versions alone lie). Runs on every open — every
	// ApplyMigrations call site (CLI initDB, MCP bootstrap, wails startup)
	// funnels through here, making this the single chokepoint.
	if err := d.ReconcileSchema(ctx); err != nil {
		return err
	}

	// One-time data migration: fold the legacy standalone actions/
	// action_targets tables into workflows/workflow_executions/
	// workflow_node_targets, then drop them. Needs real per-row branching
	// (JSON config construction), which plain SQL migrations can't do
	// reliably, so it runs as a Go step after the schema migrations above
	// have created workflow_node_targets (030_workflow_action_bridge.sql).
	// Self-idempotent: no-ops once the actions table is gone.
	if err := MigrateActionsToWorkflows(ctx, d.DB); err != nil {
		return err
	}
	for _, step := range afterMigrations {
		if err := step(ctx, d.DB); err != nil {
			return err
		}
	}
	return nil
}

// afterMigrations are data steps packages this one cannot import need once
// the schema is current (RegisterAfterMigrations).
var afterMigrations []func(ctx context.Context, db *sql.DB) error

// RegisterAfterMigrations adds step to what ApplyMigrations runs last, on
// every open: it must be idempotent, and cheap once its work is done. Call
// it from an init.
func RegisterAfterMigrations(step func(ctx context.Context, db *sql.DB) error) {
	afterMigrations = append(afterMigrations, step)
}

// migration is one embedded schema migration file.
type migration struct {
	version  int
	filename string
}

// embeddedMigrations lists the embedded .sql migrations sorted by version.
func embeddedMigrations() ([]migration, error) {
	entries, err := data.MigrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("reading embedded migrations dir: %w", err)
	}

	// Filter to .sql files and sort by name.
	var migrations []migration
	for _, e := range entries {
		name := e.Name()
		// macOS AppleDouble sidecar files (e.g. "._001_initial.sql") show up
		// in the embedded FS when built on a network volume that generates
		// them for every real file. They're never real migrations — skip
		// silently rather than logging a "non-numeric prefix" warning for
		// every one of them, every run.
		if strings.HasPrefix(name, "._") {
			continue
		}
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		// Extract version number from the filename prefix (e.g. "001" from "001_initial.sql").
		parts := strings.SplitN(name, "_", 2)
		if len(parts) < 2 {
			continue
		}
		ver, err := strconv.Atoi(parts[0])
		if err != nil {
			log.Printf("skipping migration file with non-numeric prefix: %s", name)
			continue
		}
		migrations = append(migrations, migration{version: ver, filename: name})
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})
	if err := checkDuplicateVersions(migrations); err != nil {
		return nil, err
	}
	return migrations, nil
}

// checkDuplicateVersions fails when two migration files share a version
// number: schema_migrations is keyed by version, so the second file would
// silently never be applied. Expects migrations sorted by version. There are
// no historic duplicates, so no allow-list is needed.
func checkDuplicateVersions(migrations []migration) error {
	for i := 1; i < len(migrations); i++ {
		if migrations[i].version == migrations[i-1].version {
			return fmt.Errorf("duplicate migration version %d: %s and %s",
				migrations[i].version, migrations[i-1].filename, migrations[i].filename)
		}
	}
	return nil
}

// splitStatements splits a SQL script by semicolons while respecting quoted
// strings and comments.
func splitStatements(sql string) []string {
	var statements []string
	var current strings.Builder
	inSingleQuote := false
	inLineComment := false
	runes := []rune(sql)

	for i := 0; i < len(runes); i++ {
		ch := runes[i]

		// Handle line comments (-- ...)
		if !inSingleQuote && ch == '-' && i+1 < len(runes) && runes[i+1] == '-' {
			inLineComment = true
			current.WriteRune(ch)
			continue
		}
		if inLineComment {
			current.WriteRune(ch)
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}

		// Handle single-quoted strings.
		if ch == '\'' && !inLineComment {
			inSingleQuote = !inSingleQuote
			current.WriteRune(ch)
			continue
		}

		if ch == ';' && !inSingleQuote {
			stmt := strings.TrimSpace(current.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current.Reset()
			continue
		}

		current.WriteRune(ch)
	}

	// Capture any trailing statement without a semicolon.
	if stmt := strings.TrimSpace(current.String()); stmt != "" {
		statements = append(statements, stmt)
	}

	return statements
}

// truncate shortens a string to at most n characters, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Close closes the underlying database connection.
func (d *Database) Close() error {
	d.kg.stop(kgSyncCloseWait)
	if d.DB != nil {
		return d.DB.Close()
	}
	return nil
}

// CleanExpiredSessions deletes all crawler_sessions whose expiry timestamp is
// in the past.
func (d *Database) CleanExpiredSessions() error {
	_, err := d.DB.Exec("DELETE FROM crawler_sessions WHERE expiry < ?", time.Now().UTC())
	if err != nil {
		return fmt.Errorf("cleaning expired sessions: %w", err)
	}
	return nil
}

// NormalizePlatformNames migrates legacy platform name "twitter" to "x" in the
// crawler_sessions table.
func (d *Database) NormalizePlatformNames() error {
	_, err := d.DB.Exec("UPDATE crawler_sessions SET platform = 'x' WHERE platform = 'twitter'")
	if err != nil {
		return fmt.Errorf("normalizing platform names: %w", err)
	}
	return nil
}
