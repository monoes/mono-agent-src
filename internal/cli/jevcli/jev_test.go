package jevcli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

// testConfig is the CLI around the commands: a migrated database, the profile and --json.
type testConfig struct {
	DBPath     string
	ProfileID  string
	JSONOutput bool
}

// stdinTTY is what Deps.StdinIsTerminal answers (setJevTTY).
var stdinTTY = false

func (c *testConfig) deps() Deps {
	return Deps{
		JSONOutput: func() bool { return c.JSONOutput },
		ProfileID:  func() string { return c.ProfileID },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(c.DBPath)
			if err != nil {
				return nil, err
			}
			if err := db.ApplyMigrations(); err != nil {
				db.Close()
				return nil, err
			}
			return db, nil
		},
		StdinIsTerminal: func() bool { return stdinTTY },
	}
}

// newTestDB is a migrated database in a temp dir.
func newTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "jev-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	return dbPath
}

// runJev runs `jev <args>` against a fresh config and returns stdout,
// stderr and the error.
func runJev(t *testing.T, cfg *testConfig, stdin string, args ...string) (string, string, error) {
	t.Helper()
	cmd := NewCommand(cfg.deps())
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func jevTestCfg(t *testing.T, jsonOut bool) *testConfig {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "")
	t.Setenv("TYPESAFE_BASE_URL", "")
	return &testConfig{DBPath: newTestDB(t), ProfileID: "default", JSONOutput: jsonOut}
}

func openJevDB(t *testing.T, cfg *testConfig) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func setJevTTY(t *testing.T, tty bool) {
	t.Helper()
	old := stdinTTY
	stdinTTY = tty
	t.Cleanup(func() { stdinTTY = old })
}

type jevStatusJSON struct {
	ProfileID string `json:"profile_id"`
	KeySource string `json:"key_source"`
	Model     string `json:"model"`
	Surfaces  []struct {
		Surface   string  `json:"surface"`
		Enabled   bool    `json:"enabled"`
		Threshold float64 `json:"threshold"`
	} `json:"surfaces"`
}

func TestJevStatusJSON(t *testing.T) {
	cfg := jevTestCfg(t, true)
	out, _, err := runJev(t, cfg, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	var st jevStatusJSON
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if st.KeySource != "none" || st.Model != "jev-latest" || st.ProfileID != "default" {
		t.Fatalf("status = %+v", st)
	}
	if len(st.Surfaces) != len(jevconf.Surfaces) {
		t.Fatalf("surfaces = %d, want %d", len(st.Surfaces), len(jevconf.Surfaces))
	}
	for _, s := range st.Surfaces {
		if s.Enabled || s.Threshold != jevconf.DefaultThreshold[jevconf.Surface(s.Surface)] {
			t.Fatalf("surface %+v not at defaults", s)
		}
	}

	t.Setenv("TYPESAFE_API_KEY", "sk-very-secret")
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "jev-1.13")
	out, _, err = runJev(t, cfg, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sk-very-secret") {
		t.Fatal("status printed the key")
	}
	_ = json.Unmarshal([]byte(out), &st)
	if st.KeySource != "env" || st.Model != "jev-1.13" {
		t.Fatalf("status with env key = %+v", st)
	}
}

func TestJevStatusText(t *testing.T) {
	cfg := jevTestCfg(t, false)
	out, _, err := runJev(t, cfg, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"none", "jev key set", "jev-latest", "people_links"} {
		if !strings.Contains(out, want) {
			t.Errorf("status text lacks %q:\n%s", want, out)
		}
	}
}

func TestJevEnableUnknownSurface(t *testing.T) {
	cfg := jevTestCfg(t, false)
	_, _, err := runJev(t, cfg, "", "enable", "nope", "--yes")
	if err == nil || clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "people_review") {
		t.Fatalf("err = %v", err)
	}
}

func TestJevEnableNeedsYesWithoutTTY(t *testing.T) {
	setJevTTY(t, false)
	cfg := jevTestCfg(t, false)
	out, _, err := runJev(t, cfg, "", "enable", "hil")
	if err == nil || clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "This sends to TypeSafe") {
		t.Fatalf("egress not shown before refusing:\n%s", out)
	}
	if jevconf.Enabled(openJevDB(t, cfg), "default", jevconf.HIL) {
		t.Fatal("enabled without consent")
	}
}

func TestJevEnableYesAndThreshold(t *testing.T) {
	setJevTTY(t, false)
	cfg := jevTestCfg(t, false)
	out, _, err := runJev(t, cfg, "", "enable", "capture", "--yes", "--threshold", "0.8")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range jevconf.Egress[jevconf.Capture] {
		if !strings.Contains(out, e) {
			t.Errorf("egress %q not printed:\n%s", e, out)
		}
	}
	db := openJevDB(t, cfg)
	if !jevconf.Enabled(db, "default", jevconf.Capture) || jevconf.Threshold(db, "default", jevconf.Capture, 0) != 0.8 {
		t.Fatal("capture not enabled at 0.8")
	}

	if _, _, err := runJev(t, cfg, "", "disable", "capture"); err != nil {
		t.Fatal(err)
	}
	if jevconf.Enabled(db, "default", jevconf.Capture) {
		t.Fatal("still enabled after disable")
	}
	if _, _, err := runJev(t, cfg, "", "disable", "bogus"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("disable bogus: %v", err)
	}
}

// The auto model of the OpenAI-compatible API is switched on like any surface,
// after saying what it sends: the first 4,000 characters of the last user message
// and the model names go to TypeSafe.
func TestJevEnableAPIAutoSaysWhatItSends(t *testing.T) {
	setJevTTY(t, false)
	cfg := jevTestCfg(t, false)
	if _, _, err := runJev(t, cfg, "", "enable", "api_auto"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("enabling without consent: %v", err)
	}
	out, _, err := runJev(t, cfg, "", "enable", "api_auto", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"surface api_auto", "first 4,000 characters of the last user message", "threshold 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from:\n%s", want, out)
		}
	}
	if !jevconf.Enabled(openJevDB(t, cfg), "default", jevconf.APIAuto) {
		t.Fatal("api_auto not enabled")
	}
}

func TestJevEnableBadThreshold(t *testing.T) {
	cfg := jevTestCfg(t, false)
	for _, v := range []string{"0", "1.5", "-1"} {
		if _, _, err := runJev(t, cfg, "", "enable", "hil", "--yes", "--threshold", v); clicore.ExitCodeFor(err) != 3 {
			t.Fatalf("threshold %s: %v", v, err)
		}
	}
	if jevconf.Enabled(openJevDB(t, cfg), "default", jevconf.HIL) {
		t.Fatal("enabled despite a bad threshold")
	}
}

func TestJevEnableInteractive(t *testing.T) {
	setJevTTY(t, true)
	cfg := jevTestCfg(t, false)
	out, _, err := runJev(t, cfg, "n\n", "enable", "inbox")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[y/N]") || jevconf.Enabled(openJevDB(t, cfg), "default", jevconf.Inbox) {
		t.Fatalf("declined prompt enabled the surface or did not ask:\n%s", out)
	}
	if _, _, err := runJev(t, cfg, "y\n", "enable", "inbox"); err != nil {
		t.Fatal(err)
	}
	if !jevconf.Enabled(openJevDB(t, cfg), "default", jevconf.Inbox) {
		t.Fatal("accepted prompt did not enable")
	}
}

func TestJevEnableJSONKeepsStdoutJSON(t *testing.T) {
	cfg := jevTestCfg(t, true)
	out, errOut, err := runJev(t, cfg, "", "enable", "retry", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Surface string   `json:"surface"`
		Enabled bool     `json:"enabled"`
		Egress  []string `json:"egress"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || !v.Enabled || v.Surface != "retry" || len(v.Egress) == 0 {
		t.Fatalf("json = %q (%v)", out, err)
	}
	if !strings.Contains(errOut, "This sends to TypeSafe") {
		t.Fatalf("egress not on stderr in --json mode: %q", errOut)
	}
}

func TestParseJevSince(t *testing.T) {
	for in, want := range map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90m": 90 * time.Minute} {
		got, err := parseJevSince(in)
		if err != nil || got != want {
			t.Errorf("%s = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "x", "-7d", "0d", "d"} {
		if _, err := parseJevSince(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestJevUsage(t *testing.T) {
	cfg := jevTestCfg(t, true)
	db := openJevDB(t, cfg)
	if _, err := db.Exec(`INSERT INTO jev_usage (profile_id, surface, model, questions, input_tokens, latency_ms, ok) VALUES
		('default','hil','jev-latest',1,1000,200,1), ('default','hil','jev-latest',1,3000,400,0), ('other','hil','jev-latest',1,5,1,1)`); err != nil {
		t.Fatal(err)
	}
	out, _, err := runJev(t, cfg, "", "usage", "--since", "7d")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Surfaces []jevconf.Usage `json:"surfaces"`
		Total    jevconf.Usage   `json:"total"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(v.Surfaces) != 1 || v.Surfaces[0].Calls != 2 || v.Surfaces[0].Failures != 1 || v.Total.InputTokens != 4000 {
		t.Fatalf("usage = %+v", v)
	}
	if _, _, err := runJev(t, cfg, "", "usage", "--since", "soon"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("bad --since: %v", err)
	}
	cfg.JSONOutput = false
	out, _, err = runJev(t, cfg, "", "usage")
	if err != nil || !strings.Contains(out, "hil") || !strings.Contains(out, "4000") {
		t.Fatalf("text usage: %v\n%s", err, out)
	}
}

func TestJevAsk(t *testing.T) {
	cfg := jevTestCfg(t, false)
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{"route": "billing"}))
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	req := `{"state":{"text":"refund please"},"questions":{"route":{"type":"choice","criteria":{"billing":"money","tech":"bugs"}}}}`
	path := filepath.Join(t.TempDir(), "req.json")
	if err := os.WriteFile(path, []byte(req), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"ask", "--request", path}, {"ask", "--request", "-"}} {
		out, _, err := runJev(t, cfg, req, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var resp struct {
			Answers map[string]struct {
				Choice string `json:"choice"`
			} `json:"answers"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.Answers["route"].Choice != "billing" {
			t.Fatalf("%v: %q (%v)", args, out, err)
		}
	}
	if srv.Calls() != 2 {
		t.Fatalf("calls = %d", srv.Calls())
	}
	var n int
	_ = openJevDB(t, cfg).QueryRow(`SELECT COUNT(*) FROM jev_usage WHERE surface = 'cli' AND profile_id = 'default'`).Scan(&n)
	if n != 2 {
		t.Fatalf("usage rows for cli = %d", n)
	}

	if _, _, err := runJev(t, cfg, `{"state":{}}`, "ask", "--request", "-"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("no questions: %v", err)
	}
	if _, _, err := runJev(t, cfg, "", "ask"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("no --request: %v", err)
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	if _, _, err := runJev(t, cfg, req, "ask", "--request", "-"); clicore.ExitCodeFor(err) != 4 {
		t.Fatalf("no key: %v", err)
	}
}

func TestJevModels(t *testing.T) {
	cfg := jevTestCfg(t, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"jev-1.13"},{"id":"jev-latest"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	out, _, err := runJev(t, cfg, "", "models")
	if err != nil || out != "jev-1.13\njev-latest\n" {
		t.Fatalf("models: %v %q", err, out)
	}
	cfg.JSONOutput = true
	out, _, err = runJev(t, cfg, "", "models")
	if err != nil || !strings.Contains(out, `"jev-latest"`) {
		t.Fatalf("models json: %v %q", err, out)
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	if _, _, err := runJev(t, cfg, "", "models"); clicore.ExitCodeFor(err) != 4 {
		t.Fatalf("no key: %v", err)
	}
}
