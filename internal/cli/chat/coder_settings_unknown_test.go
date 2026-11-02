package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// A binary writes the coder settings back whole, so a field that a newer binary stored and this one does not know must
// survive the write: an older CLI that saves the settings would otherwise silently reset what a newer version set.

func storedCoderJSON(t *testing.T, dbPath string) map[string]json.RawMessage {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.DB.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, coderConfigName).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func putRawCoderSettings(t *testing.T, dbPath, raw string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO configs (name, config_data, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data`, coderConfigName, raw); err != nil {
		t.Fatal(err)
	}
}

func TestSavingTheCoderSettingsKeepsAFieldThisVersionDoesNotKnow(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	putRawCoderSettings(t, dbPath, `{"enabled":true,"maxTurns":7,"fieldFromTheFuture":{"a":[1,2]},"anotherNewOne":"x"}`)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := loadCoderSettings(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	s.MaxTurns = 9 // any change through this version
	if err := saveCoderSettings(db.DB, s); err != nil {
		t.Fatal(err)
	}
	got := storedCoderJSON(t, dbPath)
	if string(got["fieldFromTheFuture"]) != `{"a":[1,2]}` || string(got["anotherNewOne"]) != `"x"` {
		t.Fatalf("unknown fields were dropped or changed: %v", got)
	}
	if string(got["maxTurns"]) != "9" {
		t.Fatalf("the known field was not written from the struct: %v", got["maxTurns"])
	}
}

// Known fields are still written from the struct: turning one off removes it, and a stale value of a known field in the
// stored JSON never comes back.
func TestSavingTheCoderSettingsStillRemovesAKnownFieldTurnedOff(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	putRawCoderSettings(t, dbPath, `{"enabled":true,"peercredBinding":true,"box":"compat","fieldFromTheFuture":1}`)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := loadCoderSettings(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	s.PeercredBinding = false
	s.Box = ""
	if err := saveCoderSettings(db.DB, s); err != nil {
		t.Fatal(err)
	}
	got := storedCoderJSON(t, dbPath)
	if _, ok := got["peercredBinding"]; ok {
		t.Errorf("a known field turned off is still stored: %s", got["peercredBinding"])
	}
	if _, ok := got["box"]; ok {
		t.Errorf("a known field cleared is still stored: %s", got["box"])
	}
	if string(got["fieldFromTheFuture"]) != "1" {
		t.Errorf("the unknown field was dropped: %v", got)
	}
}

// With nothing stored, or something that is not an object, the write is the struct's alone.
func TestSavingTheCoderSettingsWithNothingUsableStoredWritesTheStructAlone(t *testing.T) {
	for name, stored := range map[string]string{"nothing": "", "not an object": `["x"]`, "not json": `{`} {
		t.Run(name, func(t *testing.T) {
			dbPath := newChatCLITestDB(t)
			if stored != "" {
				putRawCoderSettings(t, dbPath, stored)
			}
			db, err := storage.NewDatabase(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := saveCoderSettings(db.DB, coderSettings{Enabled: true}.withDefaults()); err != nil {
				t.Fatal(err)
			}
			got := storedCoderJSON(t, dbPath)
			if string(got["enabled"]) != "true" || len(got) < 5 {
				t.Fatalf("stored = %v", got)
			}
		})
	}
}

// `coder status` says when the stored settings hold fields this version does not know.
func TestCoderStatusSaysWhenTheStoredSettingsHoldFieldsItDoesNotKnow(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	withCoderCaps(t)
	putRawCoderSettings(t, dbPath, `{"enabled":true,"fieldFromTheFuture":1,"anotherNewOne":2}`)
	cmd := newCoderCmd(testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: false}))
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"status"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "anotherNewOne") || !strings.Contains(text, "fieldFromTheFuture") || !strings.Contains(text, "newer version") {
		t.Fatalf("status does not warn about the unknown fields:\n%s", text)
	}
}
