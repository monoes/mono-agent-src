package secret

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

// The reserved vault entry httpapi-token is the HTTP API's bearer: whoever writes it chooses which bearer the
// API accepts. Only the user's own terminal writes it. What opgate refuses (a marked agent, the chat, a
// workflow, a token holder, a process inside a coder turn that cleared its markers, a process that cannot be
// placed) is refused the write, the replace and the delete, and the entry stays as it was. The real gate's
// contexts are in cmd/monoagentcli; here the registered body is a stand-in.

func jsonField(t *testing.T, out, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	s, _ := m[key].(string)
	return s
}

// runImport runs `secret import path` with the passphrase on stdin and returns its stdout.
func runImport(t *testing.T, db, path, pass string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()
	go func() {
		w.WriteString(pass + "\n")
		w.Close()
	}()
	out, err := runSecretCmd(t, db, "import", path)
	if err != nil {
		t.Fatalf("secret import: %v (%s)", err, out)
	}
	return out
}

const (
	bearerBefore = "bearer-set-by-the-user"
	bearerWanted = "bearer-an-agent-would-pick"
)

func reservedFixture(t *testing.T) string {
	t.Helper()
	db := newSecretCLITestDB(t)
	profileP1(t, db)
	if _, err := runSecretCmd(t, db, "add", "--name", secrets.ReservedEntry, "--field", "token="+bearerBefore); err != nil {
		t.Fatal(err)
	}
	return db
}

// reservedState is what the vault holds under the reserved name: the number of entries and the bearer.
func reservedState(t *testing.T, dbPath string) (int, string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	entries, err := secrets.List(context.Background(), db.DB, "p1")
	if err != nil {
		t.Fatal(err)
	}
	n, bearer := 0, ""
	for _, e := range entries {
		if e.Name != secrets.ReservedEntry {
			continue
		}
		n++
		fields, _, err := secrets.DecryptFields(context.Background(), db.DB, "p1", e.ID)
		if err != nil {
			t.Fatal(err)
		}
		bearer = fields[secrets.ReservedTokenField]
	}
	return n, bearer
}

func checkReservedRefusal(t *testing.T, c revealContext, what string, err error) {
	t.Helper()
	if err == nil || clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("not refused (exit 3): %v", err)
	}
	tail := "; run it yourself in your terminal or the app (its bearer is the field token: --field token=<value>)"
	if opgate.Unplaced(c.why()) {
		tail = "; retry, or repair the database"
	}
	want := what + ": " + secrets.ErrReservedEntry.Error() + ": " + c.why() + tail
	if err.Error() != want {
		t.Fatalf("refusal text\n got: %s\nwant: %s", err.Error(), want)
	}
}

func TestReservedEntryWritesAreRefusedToEveryNonOperatorContext(t *testing.T) {
	type verb struct {
		name, what string
		args       []string
	}
	verbs := []verb{
		{"update the bearer", "updating entry", []string{"update", secrets.ReservedEntry, "--field", "token=" + bearerWanted}},
		{"update merge", "updating entry", []string{"update", secrets.ReservedEntry, "--merge", "--field", "token=" + bearerWanted}},
		{"rename another entry to it", "updating entry", []string{"update", "other", "--name", secrets.ReservedEntry}},
		{"rm", "deleting entry", []string{"rm", secrets.ReservedEntry}},
	}
	for _, c := range revealContexts() {
		for _, v := range verbs {
			t.Run(c.name+"/"+v.name, func(t *testing.T) {
				db := reservedFixture(t)
				if _, err := runSecretCmd(t, db, "add", "--name", "other", "--value", "x"); err != nil {
					t.Fatal(err)
				}
				c.setup(t)
				_, err := runSecretCmd(t, db, v.args...)
				checkReservedRefusal(t, c, v.what, err)
				if n, got := reservedState(t, db); n != 1 || got != bearerBefore {
					t.Fatalf("the reserved entry changed: %d entries, bearer %q", n, got)
				}
			})
		}
	}
}

func TestReservedEntryCannotBeCreatedByANonOperatorContext(t *testing.T) {
	for _, c := range revealContexts() {
		t.Run(c.name+"/add", func(t *testing.T) {
			db := newSecretCLITestDB(t)
			profileP1(t, db)
			c.setup(t)
			_, err := runSecretCmd(t, db, "add", "--name", secrets.ReservedEntry, "--field", "token="+bearerWanted)
			checkReservedRefusal(t, c, "adding secret", err)
			if n, _ := reservedState(t, db); n != 0 {
				t.Fatalf("a refused add wrote %d reserved entries", n)
			}
		})
	}
}

// An import of an export that carries the reserved name skips it for a non-operator context, as for a marked
// one, and creates no entry.
func TestReservedEntryIsNotImportedByANonOperatorContext(t *testing.T) {
	asTheOperator(t)
	src := reservedFixture(t) // an export holding the reserved entry
	exportPath := filepath.Join(t.TempDir(), "export.json.enc")
	out, err := runSecretCmd(t, src, "export", "--output", exportPath)
	if err != nil {
		t.Fatal(err)
	}
	pass := jsonField(t, out, "passphrase")
	for _, c := range revealContexts() {
		t.Run(c.name, func(t *testing.T) {
			db := newSecretCLITestDB(t)
			profileP1(t, db)
			c.setup(t)
			out := runImport(t, db, exportPath, pass)
			if strings.Contains(out, `"imported": 1`) || !strings.Contains(out, `"skipped": 1`) {
				t.Fatalf("the reserved entry was imported: %s", out)
			}
			if n, _ := reservedState(t, db); n != 0 {
				t.Fatalf("a refused import wrote %d reserved entries", n)
			}
		})
	}
	t.Run("operator", func(t *testing.T) {
		db := newSecretCLITestDB(t)
		profileP1(t, db)
		out := runImport(t, db, exportPath, pass)
		if !strings.Contains(out, `"imported": 1`) {
			t.Fatalf("the operator's import did not bring the entry: %s", out)
		}
		if n, got := reservedState(t, db); n != 1 || got != bearerBefore {
			t.Fatalf("operator import: %d entries, bearer %q", n, got)
		}
	})
}

func TestReservedEntryIsTheOperatorsToWrite(t *testing.T) {
	db := reservedFixture(t) // asTheOperator: the add above is the operator's
	if _, err := runSecretCmd(t, db, "update", secrets.ReservedEntry, "--field", "token="+bearerWanted); err != nil {
		t.Fatalf("the operator's update: %v", err)
	}
	if n, got := reservedState(t, db); n != 1 || got != bearerWanted {
		t.Fatalf("after the operator's update: %d entries, bearer %q", n, got)
	}
	if _, err := runSecretCmd(t, db, "rm", secrets.ReservedEntry); err != nil {
		t.Fatalf("the operator's rm: %v", err)
	}
	if n, _ := reservedState(t, db); n != 0 {
		t.Fatalf("the operator's rm left %d entries", n)
	}
}

// A marked agent's refusal is what it always was, byte for byte (golden), the same text any other refused
// context gets with its own reason.
func TestReservedEntryRefusalForAMarkedAgentIsUnchanged(t *testing.T) {
	db := reservedFixture(t)
	t.Setenv("CLAUDECODE", "1")
	_, err := runSecretCmd(t, db, "update", secrets.ReservedEntry, "--field", "token="+bearerWanted)
	if err == nil {
		t.Fatal("not refused")
	}
	const golden = `updating entry: the vault entry "httpapi-token" is the HTTP API's bearer, which only the user writes; the same goes for entries named "agent-token-*", the agent tokens: ` +
		`CLAUDECODE is set, so an agent is running this command (` +
		`if you started the MonoAgent app, the daemon or the browser bridge from an agent shell, a chat or a workflow run, ` +
		`it inherited this marker: restart it from your own terminal or the desktop launcher ` +
		`(` + "`systemctl --user restart monoagent-bridge` for the bridge service, `systemctl --user restart monoagent-daemon` for the daemon" + `)` +
		`); run it yourself in your terminal or the app (its bearer is the field token: --field token=<value>)`
	if err.Error() != golden {
		t.Fatalf("golden\n got: %s\nwant: %s", err.Error(), golden)
	}
}

// warnDropped names the dropped fields to the user only; every refused context gets the count.
func TestWarnDroppedNamesTheFieldsOnlyForTheOperator(t *testing.T) {
	cases := append([]revealContext{{"operator", func(t *testing.T) { asTheOperator(t) }, nil}}, revealContexts()...)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			asTheOperator(t)
			c.setup(t)
			var b strings.Builder
			warnDropped(&b, "e", []string{"password", "secretfield"})
			got := b.String()
			named := strings.Contains(got, `"password"`) || strings.Contains(got, "secretfield")
			if c.name == "operator" {
				if !named {
					t.Fatalf("the operator is not told the names: %s", got)
				}
				return
			}
			if named || !strings.Contains(got, "dropped 2 fields;") {
				t.Fatalf("%s: field names leaked or the count is missing: %s", c.name, got)
			}
		})
	}
}
