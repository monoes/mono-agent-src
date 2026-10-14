package automation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/automation"
)

// writeTestAutomation writes a small valid, non-built-in package directory
// ("acme-test") and returns it.
func writeTestAutomation(t *testing.T) string {
	t.Helper()
	return writeTestAutomationID(t, "acme-test")
}

// writeTestAutomationID writes the same package under another id.
func writeTestAutomationID(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	files := map[string]string{
		"automation.json": `{
  "schema": "monoagent.automation/v1",
  "id": "acme-test",
  "name": "Acme Test",
  "version": "1.0.0",
  "site": {"startUrl": "https://example.com/", "domains": ["example.com"]},
  "permissions": {"steps": ["navigate", "extract_text"], "scripts": [], "downloads": false},
  "actions": ["get_title"],
  "policy": {"tier": "standard"}
}
`,
		"actions/get_title.json": `{"actionType":"get_title","automation":"acme-test","sideEffects":"read","steps":[
  {"id":"open","type":"navigate","url":"https://example.com/"},
  {"id":"title","type":"extract_text","configKey":"page.title","variable_name":"title"}
]}
`,
		"selectors.json": `{
  "page.title": {"candidates": [{"css": "h1"}, {"text": "Example Domain"}]},
  "page.link": {"candidates": [{"css": "a"}]}
}
`,
	}
	for name, body := range files {
		body = strings.ReplaceAll(body, "acme-test", id)
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// installTestAutomation installs acme-test into the registry under $HOME.
func installTestAutomation(t *testing.T) {
	t.Helper()
	reg, err := openTestRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(writeTestAutomation(t), automation.InstallOptions{}); err != nil {
		t.Fatalf("install acme-test: %v", err)
	}
}

func TestAutomationDoctorJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installTestAutomation(t)

	cfg := &testConfig{DBPath: filepath.Join(t.TempDir(), "doctor.db"), JSONOutput: true, ProfileID: "default"}
	db, err := testDeps(cfg).OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	rec := automation.NewHealthRecorder(db.DB, automation.HealthOptions{Interval: time.Hour})
	for i := 0; i < 5; i++ {
		rec.ObserveSelector("acme-test", "page.link", -1, false, false)
	}
	rec.ObserveSelector("acme-test", "page.title", 0, true, false)
	rec.ObserveSelector("acme-test", "page.title", 1, true, true)
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(24 * time.Hour).UTC()
	if _, err := db.DB.Exec(`INSERT INTO crawler_sessions (username, platform, cookies_json, expiry, profile_id)
		VALUES ('jane', 'acme-test', '[]', ?, 'default')`, expiry); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var out bytes.Buffer
	cmd := testDeps(cfg).newAutomationDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"acme-test"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var got struct {
		Automations []struct {
			ID        string                 `json:"id"`
			Issues    []automation.IssueJSON `json:"issues"`
			Selectors []map[string]any       `json:"selectors"`
			Session   automationSession      `json:"session"`
		} `json:"automations"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse %s: %v", out.String(), err)
	}
	if len(got.Automations) != 1 || got.Automations[0].ID != "acme-test" {
		t.Fatalf("automations = %s", out.String())
	}
	a := got.Automations[0]
	if !a.Session.LoggedIn || a.Session.Username != "jane" {
		t.Fatalf("session = %+v", a.Session)
	}
	if a.Issues == nil {
		t.Fatal("issues must be an array, not null")
	}
	status := map[string]map[string]any{}
	for _, s := range a.Selectors {
		for _, k := range []string{"key", "ok", "fail", "healed", "lastOk", "lastFail", "status"} {
			if _, ok := s[k]; !ok {
				t.Fatalf("selector %v lacks %q", s, k)
			}
		}
		status[s["key"].(string)] = s
	}
	if s := status["page.link"]; s["status"] != "broken" || s["fail"].(float64) != 5 || s["lastOk"] != "" {
		t.Fatalf("page.link = %v", s)
	}
	// 1 of 2 successes healed (> 30%): decaying.
	if s := status["page.title"]; s["status"] != "decaying" || s["ok"].(float64) != 2 || s["healed"].(float64) != 1 {
		t.Fatalf("page.title = %v", s)
	}

	// Human output: a table row plus the selectors that need attention.
	var human bytes.Buffer
	cfg.JSONOutput = false
	cmd = testDeps(cfg).newAutomationDoctorCmd()
	cmd.SetOut(&human)
	cmd.SetArgs([]string{"acme-test"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"acme-test", "jane", "0/1/1", "broken  selector page.link"} {
		if !strings.Contains(human.String(), want) {
			t.Fatalf("human output lacks %q:\n%s", want, human.String())
		}
	}
}

func TestAutomationDoctorWithoutDatabase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installTestAutomation(t)
	dbPath := filepath.Join(t.TempDir(), "never.db")
	cfg := &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}
	var out bytes.Buffer
	cmd := testDeps(cfg).newAutomationDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"acme-test"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"key": "page.title"`) || !strings.Contains(out.String(), `"status": "ok"`) {
		t.Fatalf("declared selectors should be listed as ok with no data:\n%s", out.String())
	}
	if _, err := os.Stat(dbPath); err == nil {
		t.Fatal("doctor must not create a database")
	}
}

// A health row for a key the installed version no longer declares is
// reported stale: flagged, status "stale", no re-record suggestion.
func TestAutomationDoctorStaleSelector(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installTestAutomation(t)
	cfg := &testConfig{DBPath: filepath.Join(t.TempDir(), "doctor.db"), JSONOutput: true, ProfileID: "default"}
	db, err := testDeps(cfg).OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	rec := automation.NewHealthRecorder(db.DB, automation.HealthOptions{Interval: time.Hour})
	for i := 0; i < 5; i++ {
		rec.ObserveSelector("acme-test", "page.removed", -1, false, false) // broken, but gone
		rec.ObserveSelector("acme-test", "page.link", -1, false, false)    // broken and declared
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var out bytes.Buffer
	cmd := testDeps(cfg).newAutomationDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"acme-test"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Automations []struct {
			Selectors []doctorSelectorJSON `json:"selectors"`
		} `json:"automations"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	byKey := map[string]doctorSelectorJSON{}
	for _, s := range got.Automations[0].Selectors {
		byKey[s.Key] = s
	}
	if s := byKey["page.removed"]; !s.Stale || s.Status != "stale" || s.Suggestion != "" || s.Fail != 5 {
		t.Fatalf("page.removed = %+v, want stale with no suggestion", s)
	}
	if s := byKey["page.link"]; s.Stale || s.Status != "broken" || s.Suggestion == "" {
		t.Fatalf("page.link = %+v, want broken with a re-record suggestion", s)
	}
	if s := byKey["page.title"]; s.Stale || s.Status != "ok" {
		t.Fatalf("page.title (declared, no data) = %+v", s)
	}
	if strings.Contains(out.String(), `"stale": false`) {
		t.Fatal("stale should be omitted when false")
	}

	var human bytes.Buffer
	cfg.JSONOutput = false
	cmd = testDeps(cfg).newAutomationDoctorCmd()
	cmd.SetOut(&human)
	cmd.SetArgs([]string{"acme-test"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "stale   selector page.removed: not in acme-test 1.0.0") ||
		strings.Contains(human.String(), "rerecord acme-test page.removed") {
		t.Fatalf("human output:\n%s", human.String())
	}
}

// After a re-record resets a selector's health, doctor reports it ok with
// no suggestion and shows when it was re-recorded.
func TestAutomationDoctorAfterRerecordReset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installTestAutomation(t)
	cfg := &testConfig{DBPath: filepath.Join(t.TempDir(), "doctor.db"), JSONOutput: true, ProfileID: "default"}
	db, err := testDeps(cfg).OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	rec := automation.NewHealthRecorder(db.DB, automation.HealthOptions{Interval: time.Hour})
	rec.ObserveSelector("acme-test", "page.title", 1, true, true)
	rec.ObserveSelector("acme-test", "page.title", 1, true, true)
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if err := automation.ResetSelectorHealth(db.DB, "acme-test", "page.title"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var out bytes.Buffer
	cmd := testDeps(cfg).newAutomationDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"acme-test"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Automations []struct {
			Selectors []doctorSelectorJSON `json:"selectors"`
		} `json:"automations"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, s := range got.Automations[0].Selectors {
		if s.Key != "page.title" {
			continue
		}
		if s.Status != "ok" || s.Suggestion != "" || s.OK != 0 || s.RerecordedAt == "" {
			t.Fatalf("page.title after reset = %+v", s)
		}
		return
	}
	t.Fatal("page.title missing from doctor output")
}
