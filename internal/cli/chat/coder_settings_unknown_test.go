package chat

import (
	"context"
	"encoding/json"
	"fmt"
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
	s.PeercredBinding = bindingUnset // back to the default: the key is not stored
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

// With nothing stored the write is the struct's alone; something stored that is not a JSON object is refused, not overwritten.
func TestSavingTheCoderSettingsWithNothingStoredWritesTheStructAlone(t *testing.T) {
	dbPath := newChatCLITestDB(t)
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
}

func TestSavingTheCoderSettingsRefusesToOverwriteSomethingThatIsNotAnObject(t *testing.T) {
	for name, stored := range map[string]string{"an array": `["x"]`, "not json": `{`, "a string": `"text"`} {
		t.Run(name, func(t *testing.T) {
			dbPath := newChatCLITestDB(t)
			putRawCoderSettings(t, dbPath, stored)
			db, err := storage.NewDatabase(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := saveCoderSettings(db.DB, coderSettings{Enabled: true}.withDefaults()); err == nil {
				t.Fatal("something that is not a settings object was overwritten")
			}
			var raw string
			if err := db.DB.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, coderConfigName).Scan(&raw); err != nil || raw != stored {
				t.Fatalf("the stored value changed: %q (%v)", raw, err)
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
	if !strings.Contains(text, "anotherNewOne") || !strings.Contains(text, "fieldFromTheFuture") || !strings.Contains(text, "does not know") {
		t.Fatalf("status does not warn about the unknown fields:\n%s", text)
	}
}

// encoding/json reads a field whatever the case of its key, so a stored key that differs from a known one only in case is
// that field, not an unknown one: keeping it beside the struct's own key would let it win on the next read.
func TestSavingTheCoderSettingsDropsACaseVariantOfAKnownKey(t *testing.T) {
	t.Run("turned off stays off", func(t *testing.T) {
		dbPath := newChatCLITestDB(t)
		putRawCoderSettings(t, dbPath, `{"PEERCREDBINDING":true,"ENABLED":true,"SENDAPPROVAL":false,"keep":1}`)
		db, err := storage.NewDatabase(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		s, err := loadCoderSettings(db.DB)
		if err != nil {
			t.Fatal(err)
		}
		s.PeercredBinding, s.Enabled = bindingUnset, false
		on := true
		s.SendApproval = &on
		if err := saveCoderSettings(db.DB, s); err != nil {
			t.Fatal(err)
		}
		back, err := loadCoderSettings(db.DB)
		if err != nil {
			t.Fatal(err)
		}
		if back.PeercredBinding != bindingUnset || back.Enabled || back.SendApproval == nil || !*back.SendApproval {
			t.Fatalf("a case variant won on read-back: %+v", back)
		}
		got := storedCoderJSON(t, dbPath)
		for _, k := range []string{"PEERCREDBINDING", "ENABLED", "SENDAPPROVAL"} {
			if _, ok := got[k]; ok {
				t.Errorf("the case variant %s was kept", k)
			}
		}
		if string(got["keep"]) != "1" {
			t.Errorf("the unknown key was dropped: %v", got)
		}
	})
	t.Run("turned on stays on", func(t *testing.T) {
		dbPath := newChatCLITestDB(t)
		putRawCoderSettings(t, dbPath, `{"ENABLED":false}`)
		db, err := storage.NewDatabase(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		s, err := loadCoderSettings(db.DB)
		if err != nil {
			t.Fatal(err)
		}
		s.Enabled = true
		if err := saveCoderSettings(db.DB, s); err != nil {
			t.Fatal(err)
		}
		back, err := loadCoderSettings(db.DB)
		if err != nil {
			t.Fatal(err)
		}
		if !back.Enabled {
			t.Fatal("a stored ENABLED:false won over the enabled the operator just set")
		}
	})
}

// The unknown fields kept are bounded, so a stored blob cannot grow the row on every save; the ones kept are the first in key
// order.
func TestSavingTheCoderSettingsBoundsTheUnknownFieldsItKeeps(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	var b strings.Builder
	b.WriteString(`{"enabled":true`)
	for i := 0; i < maxKeptCoderKeys*3; i++ {
		fmt.Fprintf(&b, `,"k%04d":%d`, i, i)
	}
	b.WriteString(`}`)
	putRawCoderSettings(t, dbPath, b.String())
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := loadCoderSettings(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveCoderSettings(db.DB, s); err != nil {
		t.Fatal(err)
	}
	got := storedCoderJSON(t, dbPath)
	kept := 0
	for k := range got {
		if strings.HasPrefix(k, "k") {
			kept++
		}
	}
	if kept != maxKeptCoderKeys {
		t.Fatalf("kept %d unknown keys, want the cap %d", kept, maxKeptCoderKeys)
	}
	if _, ok := got["k0000"]; !ok {
		t.Error("the first key in key order was not kept")
	}
}

// A retired key is not carried any more.
func TestSavingTheCoderSettingsDoesNotCarryARetiredKey(t *testing.T) {
	old := retiredCoderKeys
	retiredCoderKeys = map[string]bool{"oldRetiredField": true}
	defer func() { retiredCoderKeys = old }()
	dbPath := newChatCLITestDB(t)
	putRawCoderSettings(t, dbPath, `{"enabled":true,"oldRetiredField":1,"stays":2}`)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, _ := loadCoderSettings(db.DB)
	if err := saveCoderSettings(db.DB, s); err != nil {
		t.Fatal(err)
	}
	got := storedCoderJSON(t, dbPath)
	if _, ok := got["oldRetiredField"]; ok {
		t.Error("a retired key was carried")
	}
	if string(got["stays"]) != "2" {
		t.Errorf("another unknown key was dropped: %v", got)
	}
}

// The status carries the names of the unknown fields for the GUI too, and the warning quotes them: a key name is data.
func TestCoderStatusJSONCarriesTheUnknownFieldNamesAndTheWarningQuotesThem(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	withCoderCaps(t)
	putRawCoderSettings(t, dbPath, "{\"enabled\":true,\"evil\\u001b[31m\\nname\":1}")
	out, code := runCoderCLI(t, dbPath, "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var st struct {
		Unknown []string `json:"unknownSettings"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil || len(st.Unknown) != 1 {
		t.Fatalf("status json has no unknownSettings: %s (%v)", out, err)
	}
	cmd := newCoderCmd(testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: false}))
	var text strings.Builder
	cmd.SetOut(&text)
	cmd.SetArgs([]string{"status"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(text.String(), 0x1b) || strings.Contains(text.String(), "evil\n") {
		t.Fatalf("a key name reached the terminal raw:\n%q", text.String())
	}
}
