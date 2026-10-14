package apikey

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apikeys"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// `api key create|list|show|update|revoke`: the store is internal/apikeys' (tested there); these
// tests are about the commands: flags, exit codes, what goes to stdout and stderr. Each test has a
// database of its own and the CLI around the commands is faked through Deps, so the tests share
// nothing and run in parallel.

// cliError is the fake of the CLI's exit-code errors.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

// code is the exit code the CLI gives err: 2 not found, 3 invalid input, 1 any other error.
func code(err error) int {
	var ce *cliError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ce):
		return ce.code
	default:
		return 1
	}
}

func errorOf(code int) func(format string, a ...interface{}) error {
	return func(format string, a ...interface{}) error {
		return &cliError{code: code, msg: fmt.Sprintf(format, a...)}
	}
}

// run runs `api key <args>` on the database at dbPath as profile and returns stdout and stderr
// separately. stdin is never a terminal, so `revoke` does not ask.
func run(t *testing.T, dbPath, profile string, jsonOut bool, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewCommand(Deps{
		JSONOutput:      func() bool { return jsonOut },
		ProfileID:       func() string { return profile },
		OpenDB:          func() (*storage.Database, error) { return storage.NewDatabase(dbPath) },
		StdinIsTerminal: func() bool { return false },
		NotFound:        errorOf(2),
		InvalidInput:    errorOf(3),
		WriteJSON: func(w io.Writer, v any) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		},
	})
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

// seedProfile adds the profile id to the database at dbPath.
func seedProfile(t *testing.T, dbPath, id string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES (?, ?)`, id, id); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyCreateHumanOutputIsTheKeyAloneOnStdout(t *testing.T) {
	t.Parallel()
	db := testdb.Path(t)
	stdout, stderr, err := run(t, db, "default", false, "create", "--name", "app")
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(stdout)
	if !strings.HasPrefix(key, apikeys.KeyPrefix) || strings.Contains(key, " ") || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("stdout must be the key alone so scripts can capture it, got %q", stdout)
	}
	if !strings.Contains(stderr, "only time") || strings.Contains(stderr, key) {
		t.Errorf("stderr must warn that the key is shown once, without repeating it: %q", stderr)
	}
}

func TestAPIKeyCreateValidationExitCodes(t *testing.T) {
	t.Parallel()
	db := testdb.Path(t)
	if _, _, err := run(t, db, "default", true, "create", "--name", ""); code(err) != 3 {
		t.Errorf("empty name: exit %d (%v), want 3", code(err), err)
	}
	if _, _, err := run(t, db, "default", true, "create"); err == nil {
		t.Error("a missing --name must be an error")
	}
	if _, _, err := run(t, db, "default", true, "create", "--name", "app"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, db, "default", true, "create", "--name", "app"); code(err) != 3 {
		t.Errorf("duplicate name: exit %d (%v), want 3", code(err), err)
	}
}

func TestAPIKeyListIsAnEmptyArrayNotNull(t *testing.T) {
	t.Parallel()
	db := testdb.Path(t)
	out, _, err := run(t, db, "default", true, "list")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("list on an empty profile: %q, %v; want []", out, err)
	}
}

func TestAPIKeyUpdateAndRevoke(t *testing.T) {
	t.Parallel()
	db := testdb.Path(t)
	if _, _, err := run(t, db, "default", true, "create", "--name", "app"); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, db, "default", true, "update", "app", "--name", "app-2", "--context")
	if err != nil {
		t.Fatal(err)
	}
	var updated map[string]any
	_ = json.Unmarshal([]byte(out), &updated)
	if updated["name"] != "app-2" || updated["context"] != true {
		t.Fatalf("update: %v", updated)
	}
	out, _, _ = run(t, db, "default", true, "update", "app-2", "--no-context")
	_ = json.Unmarshal([]byte(out), &updated)
	if updated["context"] != false {
		t.Fatalf("--no-context: %v", updated)
	}
	if _, _, err := run(t, db, "default", true, "update", "app-2", "--context", "--no-context"); err == nil {
		t.Error("--context and --no-context together must be rejected")
	}
	if _, _, err := run(t, db, "default", true, "update", "ghost", "--context"); code(err) != 2 {
		t.Errorf("update of an unknown key: exit %d, want 2", code(err))
	}

	// Revoking needs --yes when stdin is not a terminal.
	if _, _, err := run(t, db, "default", true, "revoke", "app-2"); code(err) != 3 {
		t.Errorf("revoke without --yes: exit %d (%v), want 3", code(err), err)
	}
	out, _, err = run(t, db, "default", true, "revoke", "app-2", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	var revoked map[string]any
	_ = json.Unmarshal([]byte(out), &revoked)
	if revoked["revoked_at"] == nil {
		t.Fatalf("revoke: %v", revoked)
	}
	// Revoking is idempotent by id. By name it is not found afterwards: a
	// name belongs to active keys only, so the name is free again.
	id, _ := revoked["id"].(string)
	if _, _, err := run(t, db, "default", true, "revoke", id, "--yes"); err != nil {
		t.Errorf("revoking twice by id: %v", err)
	}
	if _, _, err := run(t, db, "default", true, "revoke", "app-2", "--yes"); code(err) != 2 {
		t.Errorf("revoking a revoked key by name: exit %d, want 2", code(err))
	}
	list, _, _ := run(t, db, "default", true, "list")
	if strings.TrimSpace(list) != "[]" {
		t.Errorf("a revoked key is still listed: %s", list)
	}
	withRevoked, _, _ := run(t, db, "default", true, "list", "--include-revoked")
	if !strings.Contains(withRevoked, "app-2") {
		t.Errorf("--include-revoked lost the key: %s", withRevoked)
	}
}

// The flags' own values count: --context=false must not turn context on.
func TestAPIKeyUpdateHonoursExplicitFlagValues(t *testing.T) {
	t.Parallel()
	db := testdb.Path(t)
	if _, _, err := run(t, db, "default", true, "create", "--name", "app", "--context"); err != nil {
		t.Fatal(err)
	}
	contextAfter := func(args ...string) bool {
		t.Helper()
		out, _, err := run(t, db, "default", true, append([]string{"update", "app"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		var k apikeys.Key
		if err := json.Unmarshal([]byte(out), &k); err != nil {
			t.Fatal(err)
		}
		return k.Context
	}
	if contextAfter("--context=false") {
		t.Error("--context=false must turn context off")
	}
	if !contextAfter("--context=true") {
		t.Error("--context=true must turn context on")
	}
	if contextAfter("--no-context") {
		t.Error("--no-context must turn context off")
	}
	// --no-context=false is not "turn context on": on its own it changes
	// nothing, and next to a rename it leaves the context as it was.
	if _, _, err := run(t, db, "default", true, "update", "app", "--no-context=false"); code(err) != 3 {
		t.Errorf("--no-context=false alone has nothing to change: exit %d (%v), want 3", code(err), err)
	}
	if _, _, err := run(t, db, "default", true, "create", "--name", "plain"); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, db, "default", true, "update", "plain", "--no-context=false", "--name", "renamed")
	if err != nil {
		t.Fatal(err)
	}
	var k apikeys.Key
	if err := json.Unmarshal([]byte(out), &k); err != nil || k.Context {
		t.Errorf("--no-context=false turned context on: %+v (%v)", k, err)
	}
}

func TestAPIKeyCreateWithContextSaysWhichModelsServeIt(t *testing.T) {
	t.Parallel()
	db := testdb.Path(t)
	_, withNote, err := run(t, db, "default", false, "create", "--name", "notes", "--context")
	if err != nil || !strings.Contains(withNote, "chat-only") || !strings.Contains(withNote, "--context-confinement") || !strings.Contains(withNote, "tool calling") {
		t.Errorf("a context key must say it is served by chat-only models, and refused tool calling, unless the server raises --context-confinement: %q (%v)", withNote, err)
	}
	_, plainNote, err := run(t, db, "default", false, "create", "--name", "plain")
	if err != nil || strings.Contains(plainNote, "chat-only") {
		t.Errorf("a plain key needs no such note: %q (%v)", plainNote, err)
	}
}

func TestAPIKeysAreProfileScoped(t *testing.T) {
	t.Parallel()
	db := testdb.Path(t)
	work := "work"
	seedProfile(t, db, work)
	if _, _, err := run(t, db, "default", true, "create", "--name", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, db, work, true, "create", "--name", "beta"); err != nil {
		t.Fatal(err)
	}

	list, _, _ := run(t, db, "default", true, "list")
	if strings.Contains(list, "beta") || !strings.Contains(list, "alpha") {
		t.Errorf("the default profile sees %s", list)
	}
	all, _, _ := run(t, db, "default", true, "list", "--all-profiles")
	if !strings.Contains(all, "alpha") || !strings.Contains(all, "beta") {
		t.Errorf("--all-profiles must list every profile: %s", all)
	}
	// Another profile's key is "not found" — never "forbidden".
	for _, args := range [][]string{{"show", "beta"}, {"update", "beta", "--context"}, {"revoke", "beta", "--yes"}} {
		_, _, err := run(t, db, "default", true, args...)
		if code(err) != 2 || (err != nil && strings.Contains(strings.ToLower(err.Error()), "forbidden")) {
			t.Errorf("%v: exit %d (%v), want 2 and a plain not-found", args, code(err), err)
		}
	}
}

func TestAPIKeyAuthenticatesAfterCreate(t *testing.T) {
	t.Parallel()
	db := testdb.Path(t)
	stdout, _, err := run(t, db, "default", false, "create", "--name", "app")
	if err != nil {
		t.Fatal(err)
	}
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	key, err := apikeys.NewStore(d.DB).Authenticate(context.Background(), strings.TrimSpace(stdout))
	if err != nil || key.ProfileID != "default" {
		t.Fatalf("the key the CLI printed does not authenticate: %+v, %v", key, err)
	}
	if !errors.Is(func() error { _, e := apikeys.NewStore(d.DB).Authenticate(context.Background(), "nope"); return e }(), apikeys.ErrInvalidKey) {
		t.Error("garbage must not authenticate")
	}
}

// A key pasted as a name is refused, by create and by update, as invalid input
// (exit 3): the name would be stored in clear and shown by `api key list` and by
// every other front end. The message does not repeat what was passed.
func TestAPIKeyNameThatIsAKeyIsRefusedWithExit3(t *testing.T) {
	t.Parallel()
	db := testdb.Path(t)
	secret, err := apikeys.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := run(t, db, "default", true, "create", "--name", secret)
	if code(err) != 3 {
		t.Fatalf("create with a key as the name: exit %d, want 3", code(err))
	}
	if strings.Contains(stdout+stderr+err.Error(), secret) {
		t.Error("create printed the key it was refused")
	}

	if _, _, err := run(t, db, "default", true, "create", "--name", "app"); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = run(t, db, "default", true, "update", "app", "--name", secret)
	if code(err) != 3 {
		t.Fatalf("update to a key as the name: exit %d, want 3", code(err))
	}
	if strings.Contains(stdout+stderr+err.Error(), secret) {
		t.Error("update printed the key it was refused")
	}

	list, _, err := run(t, db, "default", true, "list", "--include-revoked")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(list, secret) || strings.Count(list, `"name"`) != 1 {
		t.Error("a refused name reached the stored keys")
	}
}

// On a terminal, revoke without --yes asks; anything but y/yes keeps the key.
func TestAPIKeyRevokeAsksOnATerminal(t *testing.T) {
	t.Parallel()
	db := testdb.Path(t)
	if _, _, err := run(t, db, "default", true, "create", "--name", "app"); err != nil {
		t.Fatal(err)
	}
	revoke := func(answer string) (string, error) {
		cmd := NewCommand(Deps{
			JSONOutput:      func() bool { return false },
			ProfileID:       func() string { return "default" },
			OpenDB:          func() (*storage.Database, error) { return storage.NewDatabase(db) },
			StdinIsTerminal: func() bool { return true },
			NotFound:        errorOf(2),
			InvalidInput:    errorOf(3),
		})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetIn(strings.NewReader(answer))
		cmd.SetArgs([]string{"revoke", "app"})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err := cmd.Execute()
		return out.String(), err
	}
	if out, err := revoke("n\n"); err != nil || out != "Revoke key app? [y/N] Not revoked.\n" {
		t.Fatalf("answer n: %q, %v", out, err)
	}
	if out, err := revoke("yes\n"); err != nil || !strings.HasPrefix(out, "Revoke key app? [y/N] Revoked key \"app\" (") {
		t.Fatalf("answer yes: %q, %v", out, err)
	}
	if out, _, _ := run(t, db, "default", true, "list"); strings.TrimSpace(out) != "[]" {
		t.Errorf("the key is still active after yes: %s", out)
	}
}
