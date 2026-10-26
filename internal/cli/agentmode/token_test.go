package agentmode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

func errorOf(code int) func(string, ...interface{}) error {
	return func(f string, a ...interface{}) error {
		return fmt.Errorf("exit %d: "+f, append([]interface{}{code}, a...)...)
	}
}

func run(t *testing.T, dbPath, refusal string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewCommand(Deps{
		JSONOutput:   func() bool { return false },
		ProfileID:    func() string { return "default" },
		OpenDB:       func() (*storage.Database, error) { return storage.NewDatabase(dbPath) },
		NotFound:     errorOf(2),
		InvalidInput: errorOf(3),
		WriteJSON: func(w io.Writer, v any) error {
			return json.NewEncoder(w).Encode(v)
		},
		Refusal:  func() string { return refusal },
		APIAddr:  func() string { return ":9322" },
		StateDir: func() string { return filepath.Join(filepath.Dir(dbPath), "state") },
	})
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

func newDB(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.db")
	db, err := storage.NewDatabase(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	return p
}

func storeOf(t *testing.T, p string) (*agenttokens.Store, func()) {
	t.Helper()
	db, err := storage.NewDatabase(p)
	if err != nil {
		t.Fatal(err)
	}
	return agenttokens.NewStore(db.DB), func() { _ = db.Close() }
}

func TestCreateListEnvRevokeRoundTrip(t *testing.T) {
	p := newDB(t)
	out, errOut, err := run(t, p, "", "token", "create", "--name", "rev", "--scope", "read", "--expires", "2h")
	if err != nil {
		t.Fatal(err, errOut)
	}
	secret := strings.TrimSpace(out)
	if !strings.HasPrefix(secret, agenttokens.Prefix) || strings.Contains(errOut, secret) {
		t.Fatalf("stdout %q stderr %q", out, errOut)
	}
	st, closeDB := storeOf(t, p)
	tok, err := st.Verify(context.Background(), secret)
	closeDB()
	if err != nil || tok.Scope != agenttokens.ScopeRead || tok.ExpiresAt == nil {
		t.Fatalf("%+v %v", tok, err)
	}

	list, _, err := run(t, p, "", "token", "list")
	if err != nil || !strings.Contains(list, "rev") || !strings.Contains(list, "read") || strings.Contains(list, secret) {
		t.Fatalf("list %q %v", list, err)
	}

	env, _, err := run(t, p, "", "env", "--token", "rev")
	if err != nil {
		t.Fatal(err)
	}
	want := "export MONOAGENT_API_URL='http://127.0.0.1:9322/profiles/default'\nexport MONOAGENT_API_TOKEN='" + secret + "'\n"
	if env != want {
		t.Fatalf("env = %q, want %q", env, want)
	}
	js, _, err := run(t, p, "", "env", "--token", "rev", "--format", "json")
	var m map[string]string
	if err != nil || json.Unmarshal([]byte(js), &m) != nil || m[EnvToken] != secret || m[EnvURL] != "http://127.0.0.1:9322/profiles/default" {
		t.Fatalf("json env %q %v", js, err)
	}
	if _, _, err := run(t, p, "", "env", "--token", "rev", "--format", "xml"); err == nil {
		t.Fatal("bad format accepted")
	}

	if _, _, err := run(t, p, "", "token", "revoke", "rev"); err != nil {
		t.Fatal(err)
	}
	out, _, err = run(t, p, "", "env", "--token", "rev")
	if err == nil || strings.Contains(out, secret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("env of a revoked token: %q %v", out, err)
	}
	st, closeDB = storeOf(t, p)
	defer closeDB()
	if _, err := st.Verify(context.Background(), secret); !errors.Is(err, agenttokens.ErrInvalid) {
		t.Fatalf("revoked token still verifies: %v", err)
	}
}

// An agent (a process that is not the user's terminal) can neither mint, revoke nor read out a token.
func TestOnlyTheOperatorMakesRevokesOrReadsOutTokens(t *testing.T) {
	p := newDB(t)
	if _, _, err := run(t, p, "", "token", "create", "--name", "keep", "--scope", "read"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"token", "create", "--name", "wider", "--scope", "run"},
		{"token", "revoke", "keep"},
		{"env", "--token", "keep"},
	} {
		out, _, err := run(t, p, "not the user's terminal", args...)
		if err == nil || !strings.Contains(err.Error(), "operator") && !strings.Contains(err.Error(), "user") {
			t.Errorf("%v: %v", args, err)
		}
		if !isOperatorOnly(err) || out != "" {
			t.Errorf("%v: not an operator_only refusal (%v) or printed %q", args, err, out)
		}
	}
	st, closeDB := storeOf(t, p)
	defer closeDB()
	toks, _ := st.List(context.Background(), "default", true)
	if len(toks) != 1 || toks[0].Name != "keep" || toks[0].RevokedAt != nil {
		t.Fatalf("tokens after refused attempts: %+v", toks)
	}
	// An unwired operator check refuses too.
	cmd := NewCommand(Deps{OpenDB: func() (*storage.Database, error) { return storage.NewDatabase(p) }, ProfileID: func() string { return "default" }})
	cmd.SetArgs([]string{"token", "create", "--name", "x", "--scope", "read"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("create ran with no operator check wired")
	}
	// Listing is metadata only and open.
	if _, _, err := run(t, p, "not the user's terminal", "token", "list"); err != nil {
		t.Errorf("list: %v", err)
	}
}

func isOperatorOnly(err error) bool {
	var f interface{ JSONErrorFields() map[string]any }
	return errors.As(err, &f) && f.JSONErrorFields()["code"] == "operator_only"
}

func TestCreateRefusesBadScopeAndExpiry(t *testing.T) {
	p := newDB(t)
	for _, args := range [][]string{
		{"--name", "a", "--scope", "full"}, {"--name", "a", "--scope", "admin"},
		{"--name", "a", "--scope", "read", "--expires", "soon"}, {"--name", "a", "--scope", "read", "--expires", "-1h"},
		{"--name", "mat-x", "--scope", "read"},
	} {
		if _, _, err := run(t, p, "", append([]string{"token", "create"}, args...)...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	for v, ok := range map[string]bool{"24h": true, "7d": true, "never": true, "0d": false, "": false} {
		_, err := parseExpiry(v)
		if (err == nil) != ok {
			t.Errorf("parseExpiry(%q) = %v", v, err)
		}
	}
}

func TestWindowsIsRefusedWithGuidance(t *testing.T) {
	out, _, err := run(t, "", "", "sandbox", "--runtime", "windows")
	if err == nil || out != "" {
		t.Fatalf("out %q err %v", out, err)
	}
	for _, w := range []string{"WSL", "Windows Sandbox", "separate Windows user"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("guidance does not mention %q: %v", w, err)
		}
	}
}

func TestStatusIsAdvisory(t *testing.T) {
	t.Setenv("MONOAGENT_SANDBOX", "bwrap")
	out, _, err := run(t, "", "", "status")
	if err != nil || !strings.Contains(out, "looks sandboxed") || !strings.Contains(out, "advisory") {
		t.Fatalf("%q %v", out, err)
	}
	t.Setenv("MONOAGENT_SANDBOX", "")
	out, _, _ = run(t, "", "", "status")
	if !strings.Contains(out, "No sign") || !strings.Contains(out, "advisory") {
		t.Fatalf("%q", out)
	}
}

func TestShQuoteSurvivesAQuote(t *testing.T) {
	if got := shQuote("a'b"); got != `'a'\''b'` {
		t.Fatal(got)
	}
	if got := apiBase("0.0.0.0:1"); got != "http://127.0.0.1:1" {
		t.Fatal(got)
	}
	if got := apiBase("[::1]:9"); got != "http://[::1]:9" {
		t.Fatal(got)
	}
}

// The documented one-liner: variables set outside the sandbox reach the process inside it,
// which also carries the marker `status` looks for.
func TestBwrapProfilePassesTheTokenEnvAndMarksTheSandbox(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	if out, err := exec.Command("bwrap", "--bind", "/", "/", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot create namespaces here: %s", out)
	}
	home := t.TempDir()
	script, _ := Profile(Bwrap)
	path := filepath.Join(t.TempDir(), "sandbox.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", path, "sh", "-c", `echo "$MONOAGENT_API_URL|$MONOAGENT_API_TOKEN|$MONOAGENT_SANDBOX"`)
	c.Env = append(os.Environ(), "HOME="+home, "MONOAGENT_API_URL=http://u", "MONOAGENT_API_TOKEN=mat-t")
	out, err := c.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "http://u|mat-t|bwrap" {
		t.Fatalf("%q %v", out, err)
	}
}

func vaultNames(t *testing.T, p string) []string {
	t.Helper()
	db, err := storage.NewDatabase(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries, err := secrets.List(context.Background(), db.DB, "default")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names
}

// An expired token's text does not stay in the vault: listing, handing out (env) and
// revoking all prune it.
func TestExpiredTokensVaultTextIsPruned(t *testing.T) {
	for _, cmd := range [][]string{{"token", "list"}, {"env", "--token", "gone"}} {
		p := newDB(t)
		if _, _, err := run(t, p, "", "token", "create", "--name", "gone", "--scope", "read", "--expires", "1ms"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := run(t, p, "", "token", "create", "--name", "live", "--scope", "read"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		_, _, _ = run(t, p, "", cmd...)
		got := strings.Join(vaultNames(t, p), ",")
		if got != "agent-token-live" {
			t.Errorf("after %v the vault holds %q, want only agent-token-live", cmd, got)
		}
	}
}

// Revoking removes the vault text although the entry is reserved.
func TestRevokeDropsTheReservedVaultEntry(t *testing.T) {
	p := newDB(t)
	if _, _, err := run(t, p, "", "token", "create", "--name", "x", "--scope", "read"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, p, "", "token", "revoke", "x"); err != nil {
		t.Fatal(err)
	}
	if n := vaultNames(t, p); len(n) != 0 {
		t.Errorf("vault after revoke: %v", n)
	}
}

func TestCreateBindsATokenToAnExistingTaskOfTheConversation(t *testing.T) {
	p := newDB(t)
	db, err := storage.NewDatabase(p)
	if err != nil {
		t.Fatal(err)
	}
	tasks := &aitask.Store{DB: db.DB, Profile: "default"}
	if _, err := tasks.Insert(aitask.Task{ID: "at-1", Kind: "generic", ConversationID: "conv-1", Status: aitask.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	for _, c := range []struct {
		name string
		args []string
	}{
		{"task only", []string{"--task", "at-1"}},
		{"conversation only", []string{"--conversation", "conv-1"}},
		{"no such task", []string{"--task", "at-9", "--conversation", "conv-1"}},
		{"other conversation", []string{"--task", "at-1", "--conversation", "conv-2"}},
		{"run scope", []string{"--task", "at-1", "--conversation", "conv-1", "--scope", "run"}},
	} {
		args := append([]string{"token", "create", "--name", "bad", "--scope", "write"}, c.args...)
		if _, _, err := run(t, p, "", args...); err == nil {
			t.Errorf("%s: a bad binding was issued", c.name)
		}
	}
	out, errOut, err := run(t, p, "", "token", "create", "--name", "turn", "--scope", "write", "--task", "at-1", "--conversation", "conv-1")
	if err != nil {
		t.Fatal(err, errOut)
	}
	st, closeDB := storeOf(t, p)
	defer closeDB()
	tok, err := st.Verify(context.Background(), strings.TrimSpace(out))
	if err != nil || !tok.BoundTo("at-1", "conv-1") {
		t.Fatalf("%+v %v", tok, err)
	}
}
