package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/testdb"
)

// recordingStore is a ConfigStore that keeps what it was asked to save.
type recordingStore struct{ saved map[string]string }

func (s *recordingStore) GetConfig(string) (*Config, error) { return nil, nil }
func (s *recordingStore) SaveConfig(name, data string) error {
	s.saved[name] = data
	return nil
}

// generateNamed runs the config manager's generation (tier 3) against a
// fake local agent whose answer names its config name.
func generateNamed(t *testing.T, name string) (*recordingStore, error) {
	t.Helper()
	bin := writeRecordingMonomind(t)
	src, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(strings.Replace(string(src), `\"config_name\":\"gen\"`, `\"config_name\":\"`+name+`\"`, 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTGEN_RECORD", filepath.Join(t.TempDir(), "record.txt"))
	t.Setenv(monomind.EnvOverride, bin)
	t.Setenv(RuntimeEnvVar, "claude")
	store := &recordingStore{saved: map[string]string{}}
	cm := NewConfigManager(t.TempDir(), store, zerolog.Nop())
	_, err = cm.GetConfig("linkedin", "login", "", "<html>GENERATE_MARKER_HTML</html>", "log in", nil)
	return store, err
}

// The local AI names the config it generates: a name that is a reserved
// setting is refused, neither saved nor cached; an ordinary one saves.
func TestGeneratedConfigCannotNameAReservedSetting(t *testing.T) {
	for _, name := range []string{"coder_mode", "browser_agent_permissions", "Sandbox.Fallback", "coder-mission-confirm/default/c/t/1", "Coder-Mission-CONFIRM/x"} {
		store, err := generateNamed(t, name)
		if err == nil || !strings.Contains(err.Error(), "reserved setting") {
			t.Errorf("%s: %v", name, err)
		}
		if len(store.saved) != 0 {
			t.Errorf("%s: saved %v", name, store.saved)
		}
	}
	store, err := generateNamed(t, "linkedin_login_gen")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.saved["linkedin_login_gen"]; !ok || len(store.saved) != 1 {
		t.Fatalf("an ordinary config: %v", store.saved)
	}
}

// The database store refuses a reserved row whoever calls it, and saves an
// ordinary config.
func TestDBConfigStoreRefusesReservedRows(t *testing.T) {
	db := testdb.Open(t)
	s := &DBConfigStore{DB: db}
	for _, name := range []string{"coder_mode", "browser_agent_permissions", " BROWSER_AGENT_ALLOWLIST ", "coder-mission-confirm/default/c/t/1", "  CODER-MISSION-CONFIRM/x"} {
		if err := s.SaveConfig(name, `{"enabled":true}`); err == nil || !strings.Contains(err.Error(), "reserved setting") {
			t.Errorf("%q: %v", name, err)
		}
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM configs WHERE lower(trim(name)) IN ('coder_mode', 'browser_agent_permissions', 'browser_agent_allowlist') OR lower(trim(name)) LIKE 'coder-mission-confirm/%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("reserved rows written: %d %v", n, err)
	}
	if err := s.SaveConfig("linkedin_login", `{"config_name":"linkedin_login","fields":{}}`); err != nil {
		t.Fatal(err)
	}
	if cfg, err := s.GetConfig("linkedin_login"); err != nil || cfg == nil || cfg.ConfigName != "linkedin_login" {
		t.Fatalf("an ordinary config: %+v %v", cfg, err)
	}
}
