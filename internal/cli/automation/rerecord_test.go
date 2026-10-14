package automation

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/recording"
	"github.com/monoes/mono-agent/internal/storage"
)

type fakePicker struct {
	fp     *recording.Fingerprint
	url    string
	err    error
	prompt string
	closed bool
}

func (f *fakePicker) Pick(_ context.Context, prompt string, _ time.Duration) (*recording.Fingerprint, string, error) {
	f.prompt = prompt
	return f.fp, f.url, f.err
}
func (f *fakePicker) Close() error { f.closed = true; return nil }

type fakeReplacer struct {
	id, key string
	entry   action.SelectorEntry
}

func (f *fakeReplacer) ReplaceSelector(id, key string, e action.SelectorEntry) (string, error) {
	f.id, f.key, f.entry = id, key, e
	return "overlay", nil
}

type dirGetter struct{ dir string }

func (d dirGetter) Get(string) (*automation.Package, error) { return automation.OpenDir(d.dir) }

func rerecordFixturePackage(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "acme")
	os.MkdirAll(filepath.Join(dir, "actions"), 0o755)
	files := map[string]string{
		"automation.json": `{"schema":"monoagent.automation/v1","id":"acme","name":"Acme","version":"0.1.0",
			"site":{"startUrl":"https://acme.test/","domains":["acme.test"]},
			"permissions":{"steps":["click"],"scripts":[]},"actions":["a"],"policy":{"tier":"standard"}}`,
		"actions/a.json": `{"actionType":"a","automation":"acme","sideEffects":"none","steps":[{"id":"c","type":"click","configKey":"save.button"}]}`,
		"selectors.json": `{"save.button":{"candidates":[{"css":"#old"}],"intent":"the Save button"}}`,
	}
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAutomationRerecordBuildsEntry(t *testing.T) {
	t.Parallel()
	dir := rerecordFixturePackage(t)
	picker := &fakePicker{url: "https://acme.test/edit", fp: &recording.Fingerprint{Candidates: []recording.Candidate{
		{Kind: "css", Value: ".btn", Unique: false, Score: 0.9},
		{Kind: "text", Value: "Save", Unique: true, Score: 0.5},
		{Kind: "aria", Role: "button", Name: "Save", Unique: true, Score: 0.8},
		{Kind: "css", Value: "#save", Unique: true, Score: 0.95},
		{Kind: "bogus", Value: "x", Unique: true, Score: 1},
	}}}
	var openedURL string
	open := func(_ context.Context, url string, _ bool) (elementPicker, error) {
		openedURL = url
		return picker, nil
	}

	rep := &fakeReplacer{}
	res, err := rerecordSelector(context.Background(), open, dirGetter{dir}, rep, "acme", "save.button", "", time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	if openedURL != "https://acme.test/" || picker.prompt != "Click: the Save button" || !picker.closed {
		t.Fatalf("opened %q prompt %q closed %v", openedURL, picker.prompt, picker.closed)
	}
	c := rep.entry.Candidates
	if len(c) != 4 || c[0].CSS != "#save" || c[1].Aria == nil || c[2].Text != "Save" || c[3].CSS != ".btn" {
		t.Fatalf("candidates = %+v", c)
	}
	if rep.entry.Intent != "the Save button" || rep.entry.VerifiedAt == "" || rep.key != "save.button" {
		t.Fatalf("entry = %+v", rep.entry)
	}
	if res.Where != "overlay" || res.URL != "https://acme.test/edit" || res.Automation != "acme" || len(res.Candidates) != 4 {
		t.Fatalf("result = %+v", res)
	}
}

func TestAutomationRerecordErrors(t *testing.T) {
	t.Parallel()
	dir := rerecordFixturePackage(t)
	for _, tc := range []struct {
		key, want string
		pickErr   error
	}{
		{"no.such.key", `automation acme has no selector "no.such.key"`, nil},
		{"save.button", "cancelled", errors.New("extension: cancelled")},
		{"save.button", "timeout", errors.New("pick_element: timeout")},
		{"save.button", "cancelled", extension.ErrPickCancelled},
		{"save.button", "timeout", context.DeadlineExceeded},
		{"save.button", "browser bridge not connected", extension.ErrBridgeNotConnected},
	} {
		picker := &fakePicker{err: tc.pickErr}
		open := func(context.Context, string, bool) (elementPicker, error) { return picker, nil }
		rep := &fakeReplacer{}
		_, err := rerecordSelector(context.Background(), open, dirGetter{dir}, rep, "acme", tc.key, "", time.Minute, false)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: err = %v, want %q", tc.key, err, tc.want)
		}
		if rep.key != "" {
			t.Errorf("%s: selector replaced despite the error", tc.key)
		}
	}
}

func TestAutomationRerecordDropsSensitiveText(t *testing.T) {
	t.Parallel()
	fp := &recording.Fingerprint{Sensitive: true, Candidates: []recording.Candidate{
		{Kind: "text", Value: "hunter2", Unique: true, Score: 0.9},
		{Kind: "css", Value: "#pw", Unique: true, Score: 0.8},
	}}
	e := selectorEntryFromFingerprint(fp, "", time.Unix(0, 0))
	if len(e.Candidates) != 1 || e.Candidates[0].CSS != "#pw" {
		t.Fatalf("candidates = %+v", e.Candidates)
	}
}

func TestAutomationDoctorSuggestsRerecord(t *testing.T) {
	t.Parallel()
	if got := rerecordSuggestion("acme", "save.button"); got != "run: monoagentcli automation rerecord acme save.button" {
		t.Fatalf("suggestion = %q", got)
	}
}

func TestAutomationRerecordResetsHealth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := &testConfig{DBPath: filepath.Join(home, ".monoagent", "monoagent.db")}
	// No database: nothing to reset, and none is created.
	if err := testDeps(cfg).resetRerecordedHealth("acme", "save.button"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.DBPath); !os.IsNotExist(err) {
		t.Fatalf("database created: %v", err)
	}
	os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700)
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	db.DB.Exec(`INSERT INTO automation_selector_health (automation_id, selector_key, ok_count, fail_count, healed_count, last_candidate_index, recent, updated_at)
		VALUES ('acme','save.button',1,9,0,0,'','2026-01-01T00:00:00Z')`)
	db.Close()

	if err := testDeps(cfg).resetRerecordedHealth("acme", "save.button"); err != nil {
		t.Fatal(err)
	}
	db, err = storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var fails int
	var rerecorded sql.NullString
	if err := db.DB.QueryRow(`SELECT fail_count, rerecorded_at FROM automation_selector_health WHERE automation_id='acme' AND selector_key='save.button'`).
		Scan(&fails, &rerecorded); err != nil {
		t.Fatal(err)
	}
	if fails != 0 || !rerecorded.Valid {
		t.Fatalf("fail_count %d rerecorded_at %v", fails, rerecorded)
	}
}

// TestAutomationSessionLookupLocalWrap (D7): local-<p> uses <p>'s session.
func TestAutomationSessionLookupLocalWrap(t *testing.T) {
	t.Parallel()
	idx := sessionIndex{"instagram": {LoggedIn: true, Username: "me", Status: "active"}}
	if got := idx.lookup("local-instagram"); !got.LoggedIn || got.Username != "me" {
		t.Fatalf("local-instagram = %+v", got)
	}
	if got := idx.lookup("Instagram"); !got.LoggedIn {
		t.Fatalf("case-insensitive = %+v", got)
	}
	if got := idx.lookup("local-"); got.Status != "logged_out" {
		t.Fatalf("bare prefix = %+v", got)
	}
	idx["local-x"] = automationSession{Username: "own", Status: "expired"}
	idx["x"] = automationSession{Username: "shared", Status: "active"}
	if got := idx.lookup("local-x"); got.Username != "own" {
		t.Fatalf("own session wins: %+v", got)
	}
}

// TestAutomationSessionLookupLegacyPlatform: a legacy package whose
// platform name has an underscore (id local-google-maps) finds the session
// saved under "google_maps" through InstalledInfo.LegacyPlatform.
func TestAutomationSessionLookupLegacyPlatform(t *testing.T) {
	t.Parallel()
	idx := sessionIndex{"google_maps": {LoggedIn: true, Username: "maps-user", Status: "active"}}
	info := automation.InstalledInfo{ID: "local-google-maps", LegacyPlatform: "google_maps"}
	if got := idx.lookupInfo(info); !got.LoggedIn || got.Username != "maps-user" {
		t.Fatalf("lookupInfo = %+v", got)
	}
	// The id alone ("google-maps" once stripped) does not find it.
	if got := idx.lookup("local-google-maps"); got.Status != "logged_out" {
		t.Fatalf("stripped id matched unexpectedly: %+v", got)
	}
	// Its own session still wins.
	idx["local-google-maps"] = automationSession{Username: "own", Status: "active"}
	if got := idx.lookupInfo(info); got.Username != "own" {
		t.Fatalf("own session = %+v", got)
	}
}
