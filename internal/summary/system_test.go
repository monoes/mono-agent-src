package summary

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/recording"
)

type fakeAutomations struct {
	infos    []automation.InstalledInfo
	health   []automation.SelectorHealth
	declared map[string]map[string]bool
}

func (f fakeAutomations) List() ([]automation.InstalledInfo, error)            { return f.infos, nil }
func (f fakeAutomations) SelectorHealth() ([]automation.SelectorHealth, error) { return f.health, nil }
func (f fakeAutomations) DeclaredKeys(id string) (map[string]bool, bool) {
	k, ok := f.declared[id]
	return k, ok
}

func TestSystemSections(t *testing.T) {
	db := testDB(t)
	for _, s := range []struct {
		platform string
		expiry   time.Time
	}{
		{"linkedin", now.Add(14 * 24 * time.Hour)},
		{"x", now.Add(20 * time.Hour)},
		{"tiktok", now.Add(-24 * time.Hour)},
	} {
		if _, err := db.DB.Exec(`INSERT INTO crawler_sessions (platform, username, cookies_json, expiry, profile_id)
			VALUES (?, 'me', '[]', ?, 'default')`, s.platform, s.expiry); err != nil {
			t.Fatal(err)
		}
	}
	exec(t, db, `INSERT INTO vault_images (id, seq, path, filename, size_bytes, profile_id) VALUES
		('img-001',1,'/p','a.png',100,'default'), ('img-002',2,'/q','b.png',50,'other')`)

	autos := fakeAutomations{
		infos: []automation.InstalledInfo{
			{ID: "linkedin", Enabled: true, Available: true},
			{ID: "hn", Enabled: false, Available: false, PendingUpdate: "1.2.0", ContainsScripts: true, ScriptsAllowed: false},
		},
		health: []automation.SelectorHealth{
			{AutomationID: "linkedin", Key: "post.like", OK: 10, Recent: "oooooooooo"},
			{AutomationID: "linkedin", Key: "post.send", Fail: 6, Recent: "offfff"},
			{AutomationID: "linkedin", Key: "post.old", OK: 3, Recent: "ooo"},
			{AutomationID: "gone", Key: "x", Fail: 9, Recent: "fffffffff"},
		},
		declared: map[string]map[string]bool{"linkedin": {"post.like": true, "post.send": true, "post.new": true}},
	}
	recs := []recording.Summary{{ID: "r1", Complete: true, StartedAt: "2026-09-25T10:00:00Z"},
		{ID: "r2", Complete: true, Automation: "linkedin", StartedAt: "2026-09-20T10:00:00Z"},
		{ID: "r3", Complete: false, StartedAt: "2026-09-26T10:00:00Z"}}
	s := Build(context.Background(), Options{DB: db.DB, ProfileID: "default", Now: now,
		Automations: autos,
		Recordings:  func() ([]recording.Summary, error) { return recs, nil },
		Daemon:      func() *DaemonStatus { return &DaemonStatus{Running: true, PID: 42, BridgeAddr: "127.0.0.1:9222"} },
		Bridge:      func() (*BridgeStatus, error) { return &BridgeStatus{Status: "connected", Connected: true}, nil },
		OrgServe:    func() (bool, []string) { return true, []string{"acme"} },
		Sections:    map[string]bool{"services": true, "automations": true, "recordings": true, "jev": true, "accounts": true, "vault": true}})

	if sv := s.Services; !sv.Daemon.Running || sv.Daemon.PID != 42 || sv.Bridge == nil || !sv.Bridge.Connected || len(sv.OrgServe.Orgs) != 1 {
		t.Fatalf("services = %+v", sv)
	}
	a := s.Automations
	if a.Error != "" || a.Installed != 2 || a.Enabled != 1 || a.Unavailable != 1 || a.PendingUpdate != 1 || a.ScriptsBlocked != 1 {
		t.Fatalf("automations = %+v", a)
	}
	// post.new is declared with no runs yet: ok, as in `automation doctor`.
	if a.Selectors != (SelectorCounts{OK: 2, Broken: 1, Stale: 1}) || len(a.Broken) != 1 || a.Broken[0].SelectorKey != "post.send" {
		t.Fatalf("selectors = %+v / %+v", a.Selectors, a.Broken)
	}
	if r := s.Recordings; r.Total != 3 || r.Unsaved != 1 || r.Incomplete != 1 || r.LatestStartedAt != "2026-09-26T10:00:00Z" {
		t.Fatalf("recordings = %+v", r)
	}
	if j := s.Jev; j.Error != "" || j.Calls24h != 0 {
		t.Fatalf("jev = %+v", j)
	}
	ac := s.Accounts
	if ac.Error != "" || ac.Active != 2 || ac.Expired != 1 || ac.ExpiringSoon != 1 || len(ac.Sessions) != 3 {
		t.Fatalf("accounts = %+v", ac)
	}
	if ac.Sessions[2].Platform != "x" || ac.Sessions[2].Status != "expiring" {
		t.Fatalf("sessions = %+v", ac.Sessions)
	}
	if v := s.Vault; v.Error != "" || v.Images != 1 || v.ImageBytes != 100 {
		t.Fatalf("vault = %+v", v)
	}
}

func TestJevUsageCountsLocalRows(t *testing.T) {
	db := testDB(t)
	exec(t, db, `INSERT INTO jev_usage (profile_id, surface, input_tokens, ok, created_at) VALUES
		('default','hil',1000,1,'2026-09-26T11:00:00.000Z'), ('default','inbox',500,0,'2026-09-26T10:00:00.000Z'),
		('default','hil',9999,1,'2026-09-20T10:00:00.000Z'), ('other','hil',1,1,'2026-09-26T11:00:00.000Z')`)
	j := Build(context.Background(), Options{DB: db.DB, ProfileID: "default", Now: now, Sections: map[string]bool{"jev": true}}).Jev
	if j.Error != "" || j.Calls24h != 2 || j.Failures24h != 1 || j.EstimatedUSD24h <= 0 {
		t.Fatalf("jev = %+v", j)
	}
}

// The jev block names the default decision provider: TypeSafe unless chosen.
func TestJevSectionNamesTheDefaultProvider(t *testing.T) {
	db := testDB(t)
	opts := Options{DB: db.DB, ProfileID: "default", Now: now, Sections: map[string]bool{"jev": true}}
	if j := Build(context.Background(), opts).Jev; j.Provider != "typesafe" {
		t.Fatalf("provider with none chosen = %q", j.Provider)
	}
	exec(t, db, `INSERT INTO settings (key, value) VALUES ('jev.provider', 'local')`)
	if j := Build(context.Background(), opts).Jev; j.Provider != "local" {
		t.Fatalf("provider = %q", j.Provider)
	}
}

// key_configured is about the provider the block names: a keyless local
// default needs no key; a TypeSafe key does not configure a keyed one.
func TestJevKeyConfiguredFollowsTheDefaultProvider(t *testing.T) {
	db := testDB(t)
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Cleanup(jevconf.RegisterKind("fake-summary", func(jevconf.Provider, string) (jev.Backend, error) {
		return jevtest.NewFakeBackend(nil), nil
	}))
	exec(t, db, `INSERT INTO settings (key, value) VALUES ('jev.providers',
		'[{"name":"keyed","kind":"fake-summary","base_url":"http://localhost:1","secret_name":"decision-keyed"},
		  {"name":"local","kind":"fake-summary","base_url":"http://localhost:1"}]'), ('jev.provider', 'local')`)
	opts := Options{DB: db.DB, ProfileID: "default", Now: now, Sections: map[string]bool{"jev": true}}
	if j := Build(context.Background(), opts).Jev; !j.KeyConfigured || j.KeySource != jevconf.SourceKeyless {
		t.Fatalf("keyless default: %+v", j)
	}
	exec(t, db, `UPDATE settings SET value = 'keyed' WHERE key = 'jev.provider'`)
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	if j := Build(context.Background(), opts).Jev; j.KeyConfigured || j.KeySource != "" {
		t.Fatalf("keyed default without its key: %+v", j)
	}
	exec(t, db, `DELETE FROM settings WHERE key = 'jev.provider'`)
	if j := Build(context.Background(), opts).Jev; !j.KeyConfigured || j.KeySource != jevconf.SourceEnv {
		t.Fatalf("typesafe with a key: %+v", j)
	}
}

// The roll-up carries vault counts only, never anything identifying a secret.
func TestVaultSectionLeaksNoSecretFields(t *testing.T) {
	db := testDB(t)
	exec(t, db, `INSERT INTO vault_secrets (id, seq, profile_id, kind, name, username, url, ciphertext, nonce, created_at, updated_at)
		VALUES ('s1',1,'default','login','ZZ-SECRET-NAME','zz-user','https://zz.example',x'00',x'00','2026-09-26','2026-09-26')`)
	s := Build(context.Background(), Options{DB: db.DB, ProfileID: "default", Now: now})
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"ZZ-SECRET-NAME", "zz-user", "zz.example", "login"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("summary leaked %q: %s", leak, raw)
		}
	}
	if s.Vault.Secrets != 1 {
		t.Fatalf("vault = %+v", s.Vault)
	}
}
