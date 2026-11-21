package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/zalando/go-keyring"
)

// TestAutomationListLegacyGoogleMapsSession: a legacy ~/.monoagent/actions/
// google_maps package is wrapped as local-google-maps; `automation list`
// shows the login saved under the platform "google_maps".
func TestAutomationListLegacyGoogleMapsSession(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()
	legacy := filepath.Join(home, ".monoagent", "actions", "google_maps")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(legacy, "search.json"), []byte(`{"actionType":"search","platform":"GOOGLE_MAPS",
		"steps":[{"id":"open","type":"navigate","url":"https://www.google.com/maps"}]}`), 0o644)

	db, err := storage.NewDatabase(filepath.Join(home, ".monoagent", "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, db.DB)
	if err := upsertSessionRow(context.Background(), db.DB, "default", "google_maps", "maps-user", []byte(`[]`)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var got struct {
		Automations []automationListRow `json:"automations"`
	}
	mustJSON(t, home, &got, "automation", "list")
	for _, a := range got.Automations {
		if a.LegacyPlatform == "google_maps" {
			if a.ID != "local-google-maps" || !a.Session.LoggedIn || a.Session.Username != "maps-user" {
				t.Fatalf("legacy row = id %s session %+v", a.ID, a.Session)
			}
			return
		}
	}
	t.Fatalf("no legacy google_maps package listed: %+v", got.Automations)
}
