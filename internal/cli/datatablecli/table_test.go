package datatablecli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

func run(t *testing.T, path string, jsonOut bool, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(Deps{
		JSONOutput: func() bool { return jsonOut },
		ProfileID:  func() string { return "p1" },
		OpenDB:     func() (*storage.Database, error) { return storage.NewDatabase(path) },
		// The person at the keyboard: delete_operator_test.go covers the refusals.
		RequireOperator: func(string) error { return nil },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestTableCommands(t *testing.T) {
	path := testdb.Path(t)
	if _, err := run(t, path, false, "create", "Leads", "--column", "email", "--column", "score:number"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, path, false, "create", "Bad", "--column", "x:blob"); err == nil {
		t.Error("bad column type accepted")
	}
	out, err := run(t, path, true, "list")
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 || list[0]["name"] != "Leads" || list[0]["rows"] != 0.0 {
		t.Fatalf("list: %s %v", out, err)
	}
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec(`INSERT INTO data_table_rows (table_id, data, created_at, updated_at) VALUES (?, '{"email":"a@x","score":3}', 'c', 'u'), (?, '{"email":"b@x","score":9}', 'c', 'u')`, list[0]["id"], list[0]["id"])
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, path, false, "show", "leads"); err != nil || !strings.Contains(out, "2 row(s)") || !strings.Contains(out, "score") {
		t.Errorf("show: %s %v", out, err)
	}
	out, err = run(t, path, true, "rows", "Leads", "--where", "score=9")
	var rows []map[string]interface{}
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0]["email"] != "b@x" {
		t.Errorf("rows: %s %v", out, err)
	}
	if out, err = run(t, path, false, "rows", "Leads", "--sort", "score", "--desc"); err != nil || strings.Index(out, "b@x") > strings.Index(out, "a@x") {
		t.Errorf("sorted rows: %s %v", out, err)
	}
	if _, err = run(t, path, false, "delete", "Leads"); err != nil {
		t.Fatal(err)
	}
	if _, err = run(t, path, false, "show", "Leads"); err == nil {
		t.Error("deleted table still shown")
	}
}
