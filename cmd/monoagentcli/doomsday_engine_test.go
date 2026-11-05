package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
)

// A database that is present but cannot be opened (here: not a database) is an error for the control room's read
// verbs, never "off": a run may be live behind it. The exit is non-zero, and start still starts nothing.
func TestControlRoomOnAnUnopenableDatabaseIsAnErrorNotOff(t *testing.T) {
	// The real engine constructor, on a database file that is not a database (the operator gate reads the healthy
	// test database, so the verbs are run with an engine built from the broken one).
	broken := sendEnv(t)
	broken.DBPath = filepath.Join(t.TempDir(), "broken.db")
	if err := os.WriteFile(broken.DBPath, []byte(strings.Repeat("this is not a sqlite database ", 400)), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, readEng := doomsdayEngine(broken), doomsdayReadEngine(broken)
	for _, e := range []doomsdaycli.Engine{eng, readEng} {
		if _, ok := e.(doomsdaycli.Unreadable); !ok {
			t.Fatalf("an unopenable database gave %T, not the unreadable engine", e)
		}
	}
	real, realRead := doomsdayEngine, doomsdayReadEngine
	doomsdayEngine = func(*globalConfig) doomsdaycli.Engine { return eng }
	doomsdayReadEngine = func(*globalConfig) doomsdaycli.Engine { return readEng }
	defer func() { doomsdayEngine, doomsdayReadEngine = real, realRead }()
	for _, verb := range doomsdaycli.ControlRoomVerbs {
		cfg := sendEnv(t)
		out, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), verb)
		var doc map[string]any
		if err == nil || json.Unmarshal([]byte(out), &doc) != nil || doc["available"] != false || doc["reason"] != "error" || doc["error_code"] != "db_unavailable" {
			t.Fatalf("%s: %v\n%s", verb, err, out)
		}
	}
	cfg := sendEnv(t)
	if _, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), "start", "--charter", writeDoomsdayCharter(t), "--yes-i-lift", "--hash", strings.Repeat("a", 64)); err == nil {
		t.Fatal("start went ahead on an unopenable database")
	}
}

func TestClientModeOpenFailureIsOffNotAnError(t *testing.T) {
	if _, ok := engineForOpenFailure(errClientModeDB).(doomsdaycli.Off); !ok {
		t.Fatal("client mode is not the mode off")
	}
}

// What the Wails side receives from a failed read: exactly ONE JSON document on stdout (the control room's error
// document, available:false reason:error), a non-zero exit, and the error text on stderr. main's generic {"error"}
// envelope must not follow it.
func TestFailedControlRoomReadPrintsOneDocumentAndExitsNonZero(t *testing.T) {
	broken := sendEnv(t)
	broken.DBPath = filepath.Join(t.TempDir(), "broken.db")
	if err := os.WriteFile(broken.DBPath, []byte(strings.Repeat("this is not a sqlite database ", 400)), 0o600); err != nil {
		t.Fatal(err)
	}
	readEng := doomsdayReadEngine(broken)
	real := doomsdayReadEngine
	doomsdayReadEngine = func(*globalConfig) doomsdaycli.Engine { return readEng }
	defer func() { doomsdayReadEngine = real }()
	for _, verb := range doomsdaycli.ControlRoomVerbs {
		cfg := sendEnv(t)
		args := []string{"doomsday", verb, "--json"}
		cmd := newDoomsdayCmd(cfg)
		cmd.SetArgs([]string{verb})
		var err error
		out := captureStdout(t, func() { err = cmd.Execute() })
		if err == nil {
			t.Fatalf("%s: a failed read exited zero", verb)
		}
		var stdout, stderr strings.Builder
		reportCommandError(args, err, &stdout, &stderr)
		all := out + stdout.String()
		dec := json.NewDecoder(strings.NewReader(all))
		var first, second map[string]any
		if dec.Decode(&first) != nil || first["error_code"] != "db_unavailable" || first["reason"] != "error" {
			t.Fatalf("%s: first document: %q", verb, all)
		}
		if dec.Decode(&second) == nil {
			t.Fatalf("%s: a second document followed the control room's: %q", verb, all)
		}
		if exitCodeFor(err) == 0 || stderr.Len() == 0 {
			t.Fatalf("%s: exit %d, stderr %q", verb, exitCodeFor(err), stderr.String())
		}
	}
}

// A read verb creates and migrates nothing: with no database it is the mode off, with a database older than the
// open-ended mode's schema it is off too, and in both cases the directory and the file are as they were.
func TestReadEngineCreatesAndMigratesNothing(t *testing.T) {
	cfg := sendEnv(t)
	dir := filepath.Join(t.TempDir(), "fresh", ".monoagent")
	cfg.DBPath = filepath.Join(dir, "monoagent.db")
	if _, ok := doomsdayReadEngine(cfg).(doomsdaycli.Off); !ok {
		t.Fatalf("no database file: not the mode off")
	}
	if _, err := os.Stat(filepath.Dir(dir)); err == nil {
		t.Fatalf("a read made the directory %s", dir)
	}

	old := sendEnv(t)
	old.DBPath = filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", old.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE profiles (id TEXT PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	before, _ := os.ReadFile(old.DBPath)
	if _, ok := doomsdayReadEngine(old).(doomsdaycli.Off); !ok {
		t.Fatalf("a database older than the schema: not the mode off")
	}
	after, _ := os.ReadFile(old.DBPath)
	if string(before) != string(after) {
		t.Fatal("a read changed the database file")
	}
	if ents, _ := os.ReadDir(filepath.Dir(old.DBPath)); len(ents) != 1 {
		t.Fatalf("a read left files next to the database: %v", ents)
	}
}
