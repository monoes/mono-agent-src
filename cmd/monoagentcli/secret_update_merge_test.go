package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

// `secret update --field` replaces the entry's whole field set, and once it
// has, says which fields it dropped: by name to the user, and only how many
// to an agent, which sees an entry's field count and no field names. With
// --merge it sets the fields given and keeps the others. A write that is
// refused (an agent's of httpapi-token) warns of nothing, and no warning
// holds a field's value. --merge without fields is refused.
func TestSecretUpdateMergesOrSaysWhatItDrops(t *testing.T) {
	account.SetEnforceFromForTest(t, time.Time{})
	keyring.MockInit()
	asOperatorProcess(t)
	home := t.TempDir()
	seedHomeDB(t, home)
	run := func(args ...string) (string, error) {
		t.Helper()
		_, stderr, err := runCLIIn(t, home, "", append([]string{"--profile", "default", "--json"}, args...)...)
		return stderr, err
	}
	fields := func() map[string]string {
		t.Helper()
		db, err := storage.NewDatabase(home + "/.monoagent/monoagent.db")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		entries, err := secrets.List(context.Background(), db.DB, "default")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name == "aws" {
				f, _, err := secrets.DecryptFields(context.Background(), db.DB, "default", e.ID)
				if err != nil {
					t.Fatal(err)
				}
				return f
			}
		}
		t.Fatal("no aws entry")
		return nil
	}
	if _, _, err := runCLIIn(t, home, "", "--json", "db", "migrate"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("secret", "add", "--kind", "secret", "--name", "aws", "--field", "key_id=A1", "--field", "secret_key=S1", "--field", "region=eu"); err != nil {
		t.Fatal(err)
	}

	values := []string{"A1", "S1", "S2", "S3", "eu", "A9", "S9"}
	noValues := func(what, stderr string) {
		t.Helper()
		for _, v := range values {
			if strings.Contains(stderr, "="+v) || strings.Contains(stderr, `"`+v+`"`) || strings.Contains(stderr, " "+v+" ") {
				t.Errorf("%s: the warning holds the value %q:\n%s", what, v, stderr)
			}
		}
	}

	stderr, err := run("secret", "update", "aws", "--field", "secret_key=S2", "--merge")
	noValues("a merge", stderr)
	if err != nil || strings.Contains(stderr, "Warning") {
		t.Fatalf("a merge: %v\n%s", err, stderr)
	}
	if got := fields(); !reflect.DeepEqual(got, map[string]string{"key_id": "A1", "secret_key": "S2", "region": "eu"}) {
		t.Errorf("after a merge: %v", got)
	}

	stderr, err = run("secret", "update", "aws", "--field", "secret_key=S3")
	noValues("the user's replace", stderr)
	if err != nil || !strings.Contains(stderr, `this dropped "key_id", "region"; pass --merge to keep them`) {
		t.Errorf("the user's replace that drops fields: %v\n%s", err, stderr)
	}
	if got := fields(); !reflect.DeepEqual(got, map[string]string{"secret_key": "S3"}) {
		t.Errorf("after a replace: %v", got)
	}

	// An agent's replace: the count, no names.
	if _, err := run("secret", "update", "aws", "--field", "key_id=A9", "--field", "secret_key=S9", "--field", "region=eu"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDECODE", "1")
	stderr, err = run("secret", "update", "aws", "--field", "secret_key=S3")
	noValues("an agent's replace", stderr)
	if err != nil || !strings.Contains(stderr, "this dropped 2 fields; pass --merge to keep them") ||
		strings.Contains(stderr, "key_id") || strings.Contains(stderr, "region") {
		t.Errorf("an agent's replace that drops fields: %v\n%s", err, stderr)
	}
	// An agent's write of httpapi-token is refused, and nothing is said of its fields.
	t.Setenv("CLAUDECODE", "")
	if _, err := run("secret", "add", "--kind", "secret", "--name", "httpapi-token", "--field", "token=T1", "--field", "spare=X1"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDECODE", "1")
	stderr, err = run("secret", "update", "httpapi-token", "--field", "token=T2")
	if exitCodeFor(err) != 3 || strings.Contains(stderr, "Warning") || strings.Contains(stderr, "spare") {
		t.Errorf("an agent's refused write of httpapi-token: %v\n%s", err, stderr)
	}
	t.Setenv("CLAUDECODE", "")

	if _, err := run("secret", "update", "aws", "--merge"); err == nil {
		t.Error("--merge without fields went through")
	}
}

// `secret import` reads its passphrase from a pipe without echo, so the
// prompt's line ends once it is read and what follows starts a line of its
// own.
func TestSecretImportEndsThePromptLine(t *testing.T) {
	account.SetEnforceFromForTest(t, time.Time{})
	keyring.MockInit()
	asOperatorProcess(t)
	home := t.TempDir()
	seedHomeDB(t, home)
	if _, _, err := runCLIIn(t, home, "", "--json", "db", "migrate"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLIIn(t, home, "", "--profile", "default", "--json", "secret", "add", "--kind", "secret", "--name", "one", "--field", "secret=v1"); err != nil {
		t.Fatal(err)
	}
	exportTo := filepath.Join(t.TempDir(), "vault.enc")
	out, _, err := runCLIIn(t, home, "", "--profile", "default", "--json", "secret", "export", "--output", exportTo)
	var export struct{ Passphrase string }
	if err != nil || json.Unmarshal([]byte(out), &export) != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	_, stderr, err := importExport(t, home, exportTo, export.Passphrase)
	if err != nil || !strings.HasPrefix(stderr, "Passphrase: \n") {
		t.Errorf("the prompt's line is not ended: %v\n%q", err, stderr)
	}
}
