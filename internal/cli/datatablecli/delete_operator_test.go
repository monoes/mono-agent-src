package datatablecli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// `table delete` drops a table and every row: it asks Deps.RequireOperator before it opens anything,
// and a refusal leaves the table where it was.
func TestTableDeleteIsOperatorOnly(t *testing.T) {
	path := testdb.Path(t)
	if _, err := run(t, path, false, "create", "Leads", "--column", "email"); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("operator_only")
	var asked string
	cmd := NewCommand(Deps{
		JSONOutput:      func() bool { return false },
		ProfileID:       func() string { return "p1" },
		OpenDB:          func() (*storage.Database, error) { return storage.NewDatabase(path) },
		RequireOperator: func(what string) error { asked = what; return refused },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"delete", "Leads"})
	if err := cmd.Execute(); !errors.Is(err, refused) {
		t.Fatalf("delete past a refusing gate: %v", err)
	}
	if asked == "" {
		t.Error("the gate was not told what it guards")
	}
	if _, err := run(t, path, false, "show", "Leads"); err != nil {
		t.Errorf("the table is gone after a refused delete: %v", err)
	}
}
