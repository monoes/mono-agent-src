package summarycli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/storage"
)

func newSummaryCLITestDB(t *testing.T) *testConfig {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(t.TempDir(), "hb.json"))
	// Never probe the developer's real extension bridge.
	t.Setenv(extension.ExtensionPortEnv, "1")
	dbPath := filepath.Join(t.TempDir(), "s.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','A',1,'default');
		INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, profile_id) VALUES ('h1','e','w1','n','N','pending','default')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}
}

func runSummary(t *testing.T, cfg *testConfig, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		cmd := NewCommand(testDeps(cfg))
		cmd.SetArgs(args)
		err = cmd.Execute()
	})
	return out, err
}

func TestSummaryJSONHasEverySection(t *testing.T) {
	cfg := newSummaryCLITestDB(t)
	out, err := runSummary(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	for _, k := range []string{"v", "generated_at", "profile_id",
		"workflows", "executions", "schedules", "hil", "people", "activity", "applications",
		"services", "automations", "recordings", "jev", "accounts", "vault"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %q in %s", k, out)
		}
	}
	var hil struct {
		WorkflowPending int `json:"workflow_pending"`
	}
	if err := json.Unmarshal(got["hil"], &hil); err != nil || hil.WorkflowPending != 1 {
		t.Errorf("hil = %s", got["hil"])
	}
	for _, k := range []string{"executions", "hil", "people", "activity", "applications", "jev", "accounts", "vault"} {
		if strings.Contains(string(got[k]), `"error"`) {
			t.Errorf("section %s reported an error: %s", k, got[k])
		}
	}
}

func TestSummarySectionFilter(t *testing.T) {
	cfg := newSummaryCLITestDB(t)
	out, err := runSummary(t, cfg, "--section", "hil,workflows")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["executions"]; ok {
		t.Fatalf("unrequested section present: %s", out)
	}
	if _, ok := got["hil"]; !ok {
		t.Fatalf("requested section missing: %s", out)
	}
	if _, err := runSummary(t, cfg, "--section", "nope"); exitCode(err) != 3 {
		t.Fatalf("unknown section exit = %d, want 3 (invalid input)", exitCode(err))
	}
}

func TestSummaryText(t *testing.T) {
	cfg := newSummaryCLITestDB(t)
	cfg.JSONOutput = false
	out, err := runSummary(t, cfg, "--section", "hil,workflows")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "needs you     1") || !strings.Contains(out, "workflows") {
		t.Fatalf("text = %s", out)
	}
}

// The dashboard polls this every 15 s: it must never reach TypeSafe, even
// with a key configured and surfaces enabled.
func TestSummaryNeverCallsJev(t *testing.T) {
	cfg := newSummaryCLITestDB(t)
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	srv := jevtest.NewServer(t, jevtest.Fixed(nil))
	out, err := runSummary(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Jev struct {
			KeyConfigured bool `json:"key_configured"`
		} `json:"jev"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || !got.Jev.KeyConfigured {
		t.Fatalf("expected the env key to be seen: %s", out)
	}
	if n := srv.Calls(); n != 0 {
		t.Fatalf("summary made %d Jev calls", n)
	}
}

// Captures are counted from the profile's own inbox, not the unprofiled one.
func TestSummaryCountsTheProfilesCaptures(t *testing.T) {
	cfg := newSummaryCLITestDB(t)
	inbox, err := capture.ProfileInbox("default")
	if err != nil {
		t.Fatal(err)
	}
	for i, dir := range []string{filepath.Join(inbox, "c1"), filepath.Join(inbox, "c2"), filepath.Join(capture.DefaultInbox(), "stray")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		meta := fmt.Sprintf(`{"url":"https://example.com/%d","capturedAt":"2026-09-25T10:00:00Z"}`, i)
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runSummary(t, cfg, "--section", "activity")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Activity struct {
			CapturesTotal int `json:"captures_total"`
		} `json:"activity"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Activity.CapturesTotal != 2 {
		t.Fatalf("captures_total = %d, want 2 (the profile inbox only)\n%s", got.Activity.CapturesTotal, out)
	}
}

func TestSummaryAllProfilesRollsUp(t *testing.T) {
	cfg := newSummaryCLITestDB(t) // profile "default": workflow w1 (active), one pending HIL
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES ('p-work', 'Work');
		INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w2','B',1,'p-work'), ('w3','C',0,'p-work');
		INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, profile_id) VALUES ('h2','e2','w2','n','N','pending','p-work')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	out, err := runSummary(t, cfg, "--all-profiles")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Scope     string `json:"scope"`
		Workflows struct {
			Total  int `json:"total"`
			Active int `json:"active"`
		} `json:"workflows"`
		HIL struct {
			WorkflowPending int `json:"workflow_pending"`
		} `json:"hil"`
		Services json.RawMessage `json:"services"`
		Profiles []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Current bool   `json:"current"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Scope != "global" || got.Workflows.Total != 3 || got.Workflows.Active != 2 || got.HIL.WorkflowPending != 2 || len(got.Services) == 0 {
		t.Fatalf("global summary = %s", out)
	}
	if len(got.Profiles) != 2 || got.Profiles[0].ID != "default" || !got.Profiles[0].Current || got.Profiles[1].Name != "Work" {
		t.Fatalf("profiles = %+v", got.Profiles)
	}

	// The profile view is unchanged apart from saying which it is.
	out, err = runSummary(t, cfg)
	if err != nil || !strings.Contains(out, `"scope": "profile"`) || strings.Contains(out, `"profiles"`) {
		t.Fatalf("profile summary = %s (%v)", out, err)
	}
}
