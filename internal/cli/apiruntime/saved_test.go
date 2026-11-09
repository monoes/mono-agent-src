package apiruntime

// The API's server reads the settings saved with `api config` when it starts: flag, then
// environment, then saved, then default. With nothing saved it does what it always did (the
// other tests of newAPIRuntime are the proof).

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/apiconfig"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/tlsserve"
)

// savedAPIDB is a database with the given settings saved, and a home and an environment of
// the test's own: every variable of the API is clear except the ones given.
func savedAPIDB(t *testing.T, saved map[string]string, env map[string]string) *sql.DB {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	for _, sp := range apiconfig.Specs() {
		t.Setenv(sp.Env, env[sp.Env])
	}
	db := testdb.Open(t).DB
	if err := apiconfig.Update(context.Background(), db, func(s *apiconfig.Settings) error {
		for k, v := range saved {
			if err := s.Set(k, v); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

func savedAPIRuntime(t *testing.T, f Flags, saved, env map[string]string) *Runtime {
	t.Helper()
	rt, err := newAPIRuntime(savedAPIDB(t, saved, env), f, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestTheServerStartsWithTheSavedSettings(t *testing.T) {
	rt := savedAPIRuntime(t, Flags{}, map[string]string{
		"v1_addr": "127.0.0.1:0", "confinement": "sandboxed", "context_confinement": "any", "auto_confinement": "sandboxed",
		"max_concurrent": "2", "turn_timeout": "15m", "image_runtimes": "none", "tool_runtimes": "claude",
	}, nil)
	if got := rt.Policy("127.0.0.1:9322").String(); got != "sandboxed" {
		t.Errorf("confinement on loopback: %s", got)
	}
	if got := rt.Policy("0.0.0.0:9443").String(); got != "sandboxed" {
		t.Errorf("a saved confinement applies to every listener: %s", got)
	}
	if got := rt.contextReport(); got != "any" {
		t.Errorf("context confinement: %s", got)
	}
	if got := rt.autoReport(); got != "sandboxed" {
		t.Errorf("auto confinement: %s", got)
	}
	if rt.v1Addr != "127.0.0.1:0" {
		t.Errorf("v1 address: %q", rt.v1Addr)
	}
	if rt.conf.MaxConcurrent != 2 || rt.conf.TurnTimeout != 15*time.Minute {
		t.Errorf("limits: %d, %v", rt.conf.MaxConcurrent, rt.conf.TurnTimeout)
	}
	if !rt.conf.ImagesOff() {
		t.Errorf("image_runtimes none: %v", rt.conf.ImageRuntimes)
	}
	if got := rt.conf.ToolRuntimeList(); len(got) != 1 || got[0] != "claude" {
		t.Errorf("tool_runtimes: %v", got)
	}
}

// Flag, then environment, then saved, then default, one setting at a time. Each row gives
// the text of a layer and what the runtime then reads; flag is "" for the five settings
// that have none.
func TestFlagThenEnvironmentThenSavedThenDefault(t *testing.T) {
	loopback := func(rt *Runtime) string { return rt.Policy("127.0.0.1:9322").String() }
	for i, c := range []struct {
		key                                    string
		flag                                   func(v string) Flags
		read                                   func(*Runtime) string
		flagV, envV, savedV                    string // what each layer is given
		wantFlag, wantEnv, wantSaved, wantNone string
	}{
		{"confinement", func(v string) Flags { return Flags{confinement: v} }, loopback, "chat-only", "sandboxed", "any", "chat-only", "sandboxed", "any", "any"},
		{"confinement", func(v string) Flags { return Flags{confinement: v} }, loopback, "any", "any", "chat-only", "any", "any", "chat-only", "any"},
		{"context_confinement", func(v string) Flags { return Flags{contextConfinement: v} }, func(rt *Runtime) string { return rt.contextReport() },
			"any", "sandboxed", "chat-only", "any", "sandboxed", "chat-only", "chat-only"},
		{"context_confinement", func(v string) Flags { return Flags{contextConfinement: v} }, func(rt *Runtime) string { return rt.contextReport() },
			"chat-only", "chat-only", "any", "chat-only", "chat-only", "any", "chat-only"},
		{"auto_confinement", func(v string) Flags { return Flags{autoConfinement: v} }, func(rt *Runtime) string { return rt.autoReport() },
			"any", "sandboxed", "chat-only", "any", "sandboxed", "chat-only", "chat-only"},
		{"max_concurrent", func(v string) Flags { n, _ := strconv.Atoi(v); return Flags{maxConcurrent: n} }, func(rt *Runtime) string { return strconv.Itoa(rt.conf.MaxConcurrent) },
			"9", "8", "7", "9", "8", "7", "0"}, // 0 is "the default" in a Config: the gateway applies 4 when it is built
		{"v1_addr", func(v string) Flags { return Flags{v1Addr: v} }, func(rt *Runtime) string { return rt.v1Addr },
			"127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3", "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3", ""},
		{"turn_timeout", nil, func(rt *Runtime) string { return rt.conf.TurnTimeout.String() },
			"", "20m", "15m", "", "20m0s", "15m0s", "0s"}, // likewise: the gateway applies 10m
		{"image_runtimes", nil, func(rt *Runtime) string { return strings.Join(rt.conf.ImageRuntimeList(), ",") },
			"", "codex", "antigravity", "", "codex", "antigravity", "codex,antigravity"},
		{"tool_runtimes", nil, func(rt *Runtime) string { return strings.Join(rt.conf.ToolRuntimeList(), ",") },
			"", "codex", "claude", "", "codex", "claude", "claude,codex"},
	} {
		// A name of its own with no '#': the database is opened by a URI, and Go makes the
		// second subtest of a name "name#01", whose folder then ends at the '#'.
		t.Run(fmt.Sprintf("%d-%s", i, c.key), func(t *testing.T) {
			env := func(v string) map[string]string {
				return map[string]string{apiconfig.Specs()[indexOf(t, c.key)].Env: v}
			}
			saved := func(v string) map[string]string { return map[string]string{c.key: v} }
			if c.flag != nil {
				if got := c.read(savedAPIRuntime(t, c.flag(c.flagV), saved(c.savedV), env(c.envV))); got != c.wantFlag {
					t.Errorf("flag, environment and saved all given: %q, want the flag's %q", got, c.wantFlag)
				}
			}
			if got := c.read(savedAPIRuntime(t, Flags{}, saved(c.savedV), env(c.envV))); got != c.wantEnv {
				t.Errorf("environment and saved: %q, want the environment's %q", got, c.wantEnv)
			}
			if got := c.read(savedAPIRuntime(t, Flags{}, saved(c.savedV), nil)); got != c.wantSaved {
				t.Errorf("saved only: %q, want %q", got, c.wantSaved)
			}
			if got := c.read(savedAPIRuntime(t, Flags{}, nil, nil)); got != c.wantNone {
				t.Errorf("nothing: %q, want the default %q", got, c.wantNone)
			}
		})
	}
}

func indexOf(t *testing.T, key string) int {
	t.Helper()
	for i, k := range apiconfig.Keys() {
		if k == key {
			return i
		}
	}
	t.Fatalf("no key %s", key)
	return -1
}

// An empty variable is an unset one: it does not hide the saved value.
func TestAnEmptyVariableDoesNotHideTheSavedValue(t *testing.T) {
	rt := savedAPIRuntime(t, Flags{}, map[string]string{"max_concurrent": "7"}, map[string]string{"MONOAGENT_API_MAX_CONCURRENT": ""})
	if rt.conf.MaxConcurrent != 7 {
		t.Errorf("max concurrent %d, want the saved 7", rt.conf.MaxConcurrent)
	}
}

// A server does not start on settings that fail their rules, and says which and how to fix
// them: the same exit code as a bad flag.
func TestInvalidSavedSettingsStopTheStartByName(t *testing.T) {
	db := savedAPIDB(t, nil, nil)
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, apiconfig.Row,
		`{"v":1,"confinement":"everything-supersecret","max_concurrent":99,"tls_cert_file":"/only/a/cert.pem"}`); err != nil {
		t.Fatal(err)
	}
	_, err := newAPIRuntime(db, Flags{}, func(string, ...any) {})
	if exitCode(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitCode(err), err)
	}
	msg := err.Error()
	for _, want := range []string{"confinement must be", "max_concurrent must be", "tls_cert_file and tls_key_file must be set together", "api config unset"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message %q does not say %q", msg, want)
		}
	}
	if strings.Contains(msg, "supersecret") {
		t.Errorf("the message repeats a value: %q", msg)
	}
}

func TestASavedDocumentOfANewerFormatStopsTheStart(t *testing.T) {
	db := savedAPIDB(t, nil, nil)
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, apiconfig.Row, `{"v":2}`); err != nil {
		t.Fatal(err)
	}
	_, err := newAPIRuntime(db, Flags{}, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), apiconfig.Row) || exitCode(err) != 1 {
		t.Errorf("exit %d, %v: a newer format is an error that names the row", exitCode(err), err)
	}
}

// A bad flag is still the error it always was, whatever is saved.
func TestABadFlagIsStillABadFlagWhateverIsSaved(t *testing.T) {
	db := savedAPIDB(t, map[string]string{"confinement": "any", "max_concurrent": "3"}, nil)
	for name, f := range map[string]Flags{
		"confinement": {confinement: "everything"}, "max": {maxConcurrent: -1}, "v1": {v1Addr: "9443"},
	} {
		if _, err := newAPIRuntime(db, f, func(string, ...any) {}); exitCode(err) != 3 {
			t.Errorf("%s: exit %d (%v), want 3", name, exitCode(err), err)
		}
	}
}

// The saved certificate and key are served by the dedicated listener, which then speaks TLS
// on loopback too, as it does for a pair in the environment; the environment's pair wins.
func TestStartV1ServesTheSavedTLSFiles(t *testing.T) {
	dir := t.TempDir()
	savedCert, savedKey, savedDER := writeTLSPair(t, dir, "saved")
	envCert, envKey, envDER := writeTLSPair(t, dir, "env")

	served := func(t *testing.T, env map[string]string) []byte {
		t.Helper()
		rt := savedAPIRuntime(t, Flags{}, map[string]string{"v1_addr": "127.0.0.1:0", "tls_cert_file": savedCert, "tls_key_file": savedKey}, env)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		addr, err := rt.StartV1(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + addr + "/health"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				t.Fatal("a listener with a certificate answered plain HTTP")
			}
		}
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		resp, err := client.Get("https://" + addr + "/health")
		if err != nil {
			t.Fatalf("GET /health over TLS: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 || len(resp.TLS.PeerCertificates) == 0 {
			t.Fatalf("status %d, certificates %d", resp.StatusCode, len(resp.TLS.PeerCertificates))
		}
		return resp.TLS.PeerCertificates[0].Raw
	}
	if got := served(t, nil); !bytes.Equal(got, savedDER) {
		t.Error("the listener does not serve the saved certificate")
	}
	if got := served(t, map[string]string{"MONOAGENT_API_TLS_CERT": envCert, "MONOAGENT_API_TLS_KEY": envKey}); !bytes.Equal(got, envDER) {
		t.Error("the environment's pair should win over the saved one")
	}
}

func writeTLSPair(t *testing.T, dir, name string) (certPath, keyPath string, der []byte) {
	t.Helper()
	cert, certPEM, keyPEM, err := tlsserve.GenerateSelfSigned(name)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath = filepath.Join(dir, name+"-cert.pem"), filepath.Join(dir, name+"-key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath, cert.Certificate[0]
}
