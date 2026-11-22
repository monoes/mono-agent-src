package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// A process inside a coder turn must stay refused whatever database a command is pointed at: the
// hosts' databases come from the turn registry, not from the process's own words (#1332).

// registeredTurnDB marks this process as a turn in a fresh database X, registers X in a registry
// directory of the test's own (the seam: the real ~/.monoagent is never read or written), and returns X.
func registeredTurnDB(t *testing.T) string {
	t.Helper()
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	coderhost.SetRegistryDirForTest(t, filepath.Join(t.TempDir(), "coder-hosts"))
	x := &globalConfig{DBPath: newHILTestDB(t)}
	markThisProcessAsACoderTurn(t, x)
	unregister, err := coderhost.RegisterHostDB(x.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
	return x.DBPath
}

// dbOverride is one way a process names a database that holds no turn.
type dbOverride struct {
	apply func(*globalConfig)
	// hasProfile: the named database has the profile 'default' that sendEnv's --profile names (a TEST
	// FIXTURE: a new real database has no profile, see internal/storage/no_seed_profile_migration_test.go).
	// A command that opens the database before its gate (the task verbs and rows below) gets as far as the
	// turn gate only then; without it --profile resolution refuses first.
	hasProfile bool
}

// overrides are the ways a process names a database that holds no turn. An empty file and a path with no
// database are named as they are: they hold no profile, so a verb that opens the database before its turn
// check stops at --profile (exit 3) instead; either way it is refused and nothing may be written there.
// The other databases have the fixture profile, so every verb reaches the turn gate.
func overrides(t *testing.T) map[string]dbOverride {
	empty := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	seeded := func(path string) string {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		writeMigratedDB(t, path)
		return path
	}
	return map[string]dbOverride{
		"an empty file, which has no profile": {apply: func(c *globalConfig) { c.DBPath = empty }},
		"a nonexistent path, which has no profile": {apply: func(c *globalConfig) {
			c.DBPath = filepath.Join(t.TempDir(), "nowhere", "x.db")
		}},
		"a fresh database given the fixture profile": {hasProfile: true, apply: func(c *globalConfig) {
			c.DBPath = seeded(filepath.Join(t.TempDir(), "fresh", "x.db"))
		}},
		"a second database": {hasProfile: true, apply: func(c *globalConfig) { c.DBPath = newHILTestDB(t) }},
		"another HOME": {hasProfile: true, apply: func(c *globalConfig) {
			c.DBPath = seeded(filepath.Join(t.TempDir(), ".monoagent", "monoagent.db"))
		}},
		"a clean environment": {hasProfile: true, apply: func(c *globalConfig) {
			for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
				t.Setenv(k, "")
			}
			c.DBPath = seeded(filepath.Join(t.TempDir(), "x.db"))
		}},
	}
}

// requireRefusedWritingNothing: a command inside a turn that names a database without the profile it asks
// for is refused, by the turn layer (a verb that checks it before opening the database) or at --profile
// resolution (exit 3), and the database holds nothing beyond what migrating it writes.
func requireRefusedWritingNothing(t *testing.T, dbPath string, err error) {
	t.Helper()
	byTurn := isOperatorOnly(err) && strings.Contains(err.Error(), "coder session's turn")
	atProfile := exitCodeFor(err) == 3 && err != nil && strings.Contains(err.Error(), `profile "default" not found`)
	if !byTurn && !atProfile {
		t.Fatalf("not refused by the turn layer nor at --profile (exit %d): %v", exitCodeFor(err), err)
	}
	if _, serr := os.Stat(dbPath); os.IsNotExist(serr) {
		return // nothing was even created
	}
	got, cerr := rowCounts(dbPath)
	if cerr != nil {
		t.Fatalf("reading %s: %v", dbPath, cerr)
	}
	want := migratedRowCounts(t)
	for table, n := range got {
		if n != want[table] {
			t.Errorf("the refused command wrote to %s: %d row(s), a just-migrated database has %d", table, n, want[table])
		}
	}
}

var migrated struct {
	once   sync.Once
	counts map[string]int
	err    error
}

// migratedRowCounts are the rows per table of a database that was only migrated (no profile, no test fixture).
func migratedRowCounts(t *testing.T) map[string]int {
	t.Helper()
	migrated.once.Do(func() {
		dir, err := os.MkdirTemp("", "migrated-counts-")
		if err != nil {
			migrated.err = err
			return
		}
		defer os.RemoveAll(dir)
		p := filepath.Join(dir, "ref.db")
		d, err := storage.NewDatabase(p)
		if err != nil {
			migrated.err = err
			return
		}
		if err := d.ApplyMigrations(); err != nil {
			d.Close()
			migrated.err = err
			return
		}
		d.Close()
		migrated.counts, migrated.err = rowCounts(p)
	})
	if migrated.err != nil {
		t.Fatalf("migrating the reference database: %v", migrated.err)
	}
	return migrated.counts
}

// rowCounts counts the rows of every table of the database at path.
func rowCounts(path string) (map[string]int, error) {
	d, err := storage.NewDatabase(path)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	rows, err := d.DB.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, n)
	}
	rows.Close()
	counts := make(map[string]int, len(tables))
	for _, tb := range tables {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM "` + tb + `"`).Scan(&n); err != nil {
			return nil, err
		}
		counts[tb] = n
	}
	return counts, nil
}

func TestEveryGatedVerbStillRefusesAProcessInsideATurnWhateverDatabaseItNames(t *testing.T) {
	for name := range overrides(t) {
		for _, v := range allGatedVerbs() {
			t.Run(name+"/"+v.name, func(t *testing.T) {
				cfg := sendEnv(t)
				registeredTurnDB(t)
				o := overrides(t)[name]
				o.apply(cfg)
				if v.atTerminal {
					atATerminal(t)
				}
				_, err := runSendCLI(t, cfg, newTaskCmd(cfg), v.args...)
				if !o.hasProfile {
					requireRefusedWritingNothing(t, cfg.DBPath, err)
					return
				}
				requireOperatorOnly(t, err)
				for _, want := range []string{"coder session's turn", "only the operator can " + v.what} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refused by another layer (missing %q): %v", want, err)
					}
				}
			})
		}
	}
}

func TestEveryRequireOperatorRowStillRefusesAProcessInsideATurnWhateverDatabaseItNames(t *testing.T) {
	for name := range overrides(t) {
		for _, r := range requireOperatorRows() {
			t.Run(name+"/"+r.name, func(t *testing.T) {
				cfg := sendEnv(t)
				registeredTurnDB(t)
				o := overrides(t)[name]
				o.apply(cfg)
				err := r.run(t, cfg)
				if !o.hasProfile {
					requireRefusedWritingNothing(t, cfg.DBPath, err)
					return
				}
				requireOperatorOnly(t, err)
				if !strings.Contains(err.Error(), "coder session's turn") {
					t.Fatalf("not refused by the turn layer: %v", err)
				}
			})
		}
	}
}

// What works today keeps working: a real operator (no turn recorded anywhere) passes with and without a
// registry, with --db-path on a second database, and with a registry entry whose database holds no turn.
func TestTheOperatorPassesTheGateWithAndWithoutARegistry(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	t.Run("no registry", func(t *testing.T) {
		coderhost.SetRegistryDirForTest(t, "")
		cfg := sendEnv(t)
		if err := cfg.requireOperator("do the thing"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an empty registry and a second database", func(t *testing.T) {
		coderhost.SetRegistryDirForTest(t, filepath.Join(t.TempDir(), "coder-hosts"))
		cfg := sendEnv(t)
		cfg.DBPath = newHILTestDB(t)
		if err := cfg.requireOperator("do the thing"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an entry for a database with no turn rows", func(t *testing.T) {
		coderhost.SetRegistryDirForTest(t, filepath.Join(t.TempDir(), "coder-hosts"))
		cfg := sendEnv(t)
		other := newHILTestDB(t)
		unregister, err := coderhost.RegisterHostDB(other)
		if err != nil {
			t.Fatal(err)
		}
		defer unregister()
		cfg.DBPath = newHILTestDB(t)
		if err := cfg.requireOperator("do the thing"); err != nil {
			t.Fatal(err)
		}
		for _, v := range allGatedVerbs() {
			if v.hybrid {
				continue
			}
			if v.atTerminal {
				atATerminal(t)
			}
			if _, err := runSendCLI(t, cfg, newTaskCmd(cfg), v.args...); isOperatorOnly(err) {
				t.Errorf("%s refused the operator: %v", v.name, err)
			}
		}
	})
	t.Run("an entry whose database is gone", func(t *testing.T) {
		coderhost.SetRegistryDirForTest(t, filepath.Join(t.TempDir(), "coder-hosts"))
		cfg := sendEnv(t)
		gone := newHILTestDB(t)
		unregister, err := coderhost.RegisterHostDB(gone)
		if err != nil {
			t.Fatal(err)
		}
		defer unregister()
		if err := os.Remove(gone); err != nil {
			t.Fatal(err)
		}
		if err := cfg.requireOperator("do the thing"); err != nil {
			t.Fatal(err)
		}
	})
}

// A registered database that cannot be read refuses (fails closed), whatever database the command names.
func TestARegisteredDatabaseThatCannotBeReadRefuses(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	coderhost.SetRegistryDirForTest(t, filepath.Join(t.TempDir(), "coder-hosts"))
	cfg := sendEnv(t)
	bad := newHILTestDB(t)
	d, err := storage.NewDatabase(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE coder_sessions RENAME COLUMN turn_pid TO turn_pid_gone`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	unregister, err := coderhost.RegisterHostDB(bad)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	cfg.DBPath = newHILTestDB(t)
	if err := cfg.requireOperator("do the thing"); err == nil || !strings.Contains(err.Error(), "turn_pid") {
		t.Fatalf("an unreadable registered database did not refuse: %v", err)
	}
}

// A recovery command skips a registered database it cannot read, and says so (the database and its entry file),
// instead of dropping the error: the skip is how a turn behind that entry could go unseen.
func TestARecoveryCommandNamesTheRegistryEntryItSkipped(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	coderhost.SetRegistryDirForTest(t, filepath.Join(t.TempDir(), "coder-hosts"))
	bad := newHILTestDB(t)
	d, err := storage.NewDatabase(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`ALTER TABLE coder_sessions RENAME COLUMN turn_pid TO turn_pid_gone`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	unregister, err := coderhost.RegisterHostDB(bad)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	var logged []string
	old := recoverySkippedEntry
	recoverySkippedEntry = func(err error) { logged = append(logged, err.Error()) }
	defer func() { recoverySkippedEntry = old }()

	cfg := &globalConfig{DBPath: newHILTestDB(t)}
	if in, err := cfg.callerInATurnFor(true); in || err != nil {
		t.Fatalf("recovery on an unreadable registered database: in=%v err=%v", in, err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], bad) {
		t.Fatalf("the skipped entry was not named: %q", logged)
	}
	logged = nil
	if _, err := cfg.callerInATurnFor(false); err == nil || len(logged) != 0 {
		t.Fatalf("the non-recovery flavour must refuse and log nothing: err=%v logged=%q", err, logged)
	}
}

// The same lock through the gate: the plain flavour refuses, the recovery flavour treats the database as
// holding no turn (after the bounded wait), so a lock held by anything never locks the operator out of repair.
func TestALockedDatabaseRefusesPlainAndLetsRecoveryThrough(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	cfg := &globalConfig{DBPath: newHILTestDB(t)}
	markThisProcessAsACoderTurn(t, cfg)
	w, err := sql.Open("sqlite", "file:"+cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	// a rollback-journal database: in WAL mode readers are never blocked by a writer
	if _, err := w.Exec(`PRAGMA journal_mode = DELETE`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	if in, err := cfg.callerInATurnFor(false); in || err == nil {
		t.Fatalf("plain flavour on a locked database: in=%v err=%v", in, err)
	}
	if in, err := cfg.callerInATurnFor(true); in || err != nil {
		t.Fatalf("recovery flavour on a locked database: in=%v err=%v", in, err)
	}
}
