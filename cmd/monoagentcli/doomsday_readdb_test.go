package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The read engine's handle refuses every write, by BOTH guards: the file is opened mode=ro, and query_only is on. Each
// is checked alone: with query_only switched off on a pinned connection the file itself still refuses the write.
func TestReadEngineHandleRefusesWrites(t *testing.T) {
	cfg := sendEnv(t)
	db, err := openReadDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var qo int
	if err := conn.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&qo); err != nil || qo != 1 {
		t.Fatalf("query_only = %d (%v), want 1", qo, err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE read_engine_wrote (x)`); err == nil {
		t.Fatal("the read handle accepted a write")
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA query_only = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE read_engine_wrote (x)`); err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("with query_only off the file still accepted a write (mode=ro missing?): %v", err)
	}
}

const witnessHeaderLen = 8 + 8 + 32 // magic, SYNC, header MAC

func writeWitness(t *testing.T, cfg *globalConfig, record string) string {
	t.Helper()
	dir := doomsdayRunDirPath(cfg)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b := append([]byte("MAWH1\x00\x00\x00"), make([]byte, witnessHeaderLen-8)...)
	b = append(b, record...)
	p := filepath.Join(dir, "witness")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A missing database, or one without the mode's tables, is "off" only when the profile's witness holds no run. With a
// committed or pending record there it is an error (db_unavailable), so a live run's nav item and Stop do not vanish.
func TestReadEngineWitnessWithoutDatabaseIsAnErrorNotOff(t *testing.T) {
	missing := func(t *testing.T) *globalConfig {
		cfg := sendEnv(t)
		cfg.DBPath = filepath.Join(t.TempDir(), ".monoagent", "monoagent.db")
		cfg.ProfileID = "p1"
		return cfg
	}
	noTables := func(t *testing.T) *globalConfig {
		cfg := sendEnv(t)
		cfg.DBPath = filepath.Join(t.TempDir(), "old.db")
		cfg.ProfileID = "p1"
		raw, err := sql.Open("sqlite", cfg.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(`CREATE TABLE profiles (id TEXT PRIMARY KEY, name TEXT)`); err != nil {
			t.Fatal(err)
		}
		raw.Close()
		return cfg
	}
	for name, mk := range map[string]func(*testing.T) *globalConfig{"no database file": missing, "database without the tables": noTables} {
		t.Run(name, func(t *testing.T) {
			cfg := mk(t)
			if _, ok := doomsdayReadEngine(cfg).(doomsdaycli.Off); !ok {
				t.Fatal("no witness record: must stay off")
			}
			writeWitness(t, cfg, "") // header only: no run was ever recorded
			if _, ok := doomsdayReadEngine(cfg).(doomsdaycli.Off); !ok {
				t.Fatal("a header-only witness: must stay off")
			}
			for _, rec := range []string{`{"t":"committed","gen":1,"to":3}`, `{"t":"pending","gen":1,"to":4}`} {
				writeWitness(t, cfg, rec)
				eng := doomsdayReadEngine(cfg)
				if _, ok := eng.(doomsdaycli.Unreadable); !ok {
					t.Fatalf("%s: got %T, want the unreadable engine (db_unavailable)", rec, eng)
				}
				_, err := eng.(doomsdaycli.ViewReader).ReadView(context.Background(), store.ViewQuery{})
				if !errors.Is(err, doomsdaycli.ErrUnreadable) || !strings.Contains(err.Error(), "witness") {
					t.Fatalf("%s: %v", rec, err)
				}
			}
		})
	}
}

// Another profile's witness, a link and a path-like profile id never turn off into an error.
func TestReadEngineWitnessLookIsPerProfileAndNoFollow(t *testing.T) {
	cfg := sendEnv(t)
	cfg.DBPath = filepath.Join(t.TempDir(), "monoagent.db")
	cfg.ProfileID = "p1"
	other := *cfg
	other.ProfileID = "p2"
	writeWitness(t, &other, `{"t":"committed","gen":1,"to":3}`)
	if _, ok := doomsdayReadEngine(cfg).(doomsdaycli.Off); !ok {
		t.Fatal("another profile's witness made this profile's off an error")
	}
	target := writeWitness(t, &other, `{"t":"committed","gen":1,"to":3}`)
	if err := os.MkdirAll(doomsdayRunDirPath(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(doomsdayRunDirPath(cfg), "witness")); err != nil {
		t.Skip("no symlinks here")
	}
	// A link where the witness should be is "cannot tell": an error (db_unavailable), never off, and never followed.
	if _, ok := doomsdayReadEngine(cfg).(doomsdaycli.Unreadable); !ok {
		t.Fatal("a symlinked witness must read as an error, not off, and must not be followed")
	}
	for _, bad := range []string{"", ".", "..", "../p2", "a/b"} {
		c := *cfg
		c.ProfileID = bad
		if held, err := witnessHoldsRun(&c); held || err != nil {
			t.Fatalf("profile id %q looked into a witness: %v %v", bad, held, err)
		}
	}
}
