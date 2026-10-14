package people

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The people commands are tested here with the CLI around them faked through Deps. Most of them
// stay serial: the workflow store and the vault resolve under $HOME, Jev reads its key from the
// environment, the Outlook stub is one package variable, and the commands print on os.Stdout.

// testConfig is the fake of the CLI's global flags: --db-path, --json and --profile.
type testConfig struct {
	DBPath     string
	JSONOutput bool
	ProfileID  string
}

// cliError is the fake of the CLI's exit-code errors.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

func errorOf(code int) func(format string, a ...interface{}) error {
	return func(format string, a ...interface{}) error {
		return &cliError{code: code, msg: fmt.Sprintf(format, a...)}
	}
}

// exitCode is the exit code the CLI gives err.
func exitCode(err error) int {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.code
	}
	if err != nil {
		return 1
	}
	return 0
}

// outlookStub, when set, is what service.outlook_mail does (see stubOutlookNode).
var outlookStub func(db *sql.DB, config map[string]interface{}) ([]workflow.NodeOutput, error)

// outlookNode is service.outlook_mail backed by outlookStub.
type outlookNode struct{ db *sql.DB }

func (outlookNode) Type() string { return "service.outlook_mail" }

func (n outlookNode) Execute(_ context.Context, _ workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if outlookStub == nil {
		return nil, errors.New("no outlook stub")
	}
	return outlookStub(n.db, config)
}

// testDeps is the CLI around the commands for cfg. OpenDB resolves the profile as the CLI's
// initDB does: "" is the active profile (else "default"), and an id or a name resolves to the
// id; an unknown one is exit 3.
func testDeps(cfg *testConfig) Deps {
	return Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		ProfileID:  func() string { return cfg.ProfileID },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(cfg.DBPath)
			if err != nil {
				return nil, err
			}
			if err := db.ApplyMigrations(); err != nil {
				db.Close()
				return nil, err
			}
			if cfg.ProfileID == "" {
				var id string
				_ = db.DB.QueryRow(`SELECT value FROM settings WHERE key = 'active_profile_id'`).Scan(&id)
				if id == "" {
					id = "default"
				}
				cfg.ProfileID = id
				return db, nil
			}
			var id string
			if err := db.DB.QueryRow(`SELECT id FROM profiles WHERE id = ?`, cfg.ProfileID).Scan(&id); err != nil {
				if err := db.DB.QueryRow(`SELECT id FROM profiles WHERE LOWER(name) = LOWER(?)`, cfg.ProfileID).Scan(&id); err != nil {
					db.Close()
					return nil, errorOf(3)("profile %q not found (checked both id and name)", cfg.ProfileID)
				}
			}
			cfg.ProfileID = id
			return db, nil
		},
		NotFound:       errorOf(2),
		InvalidInput:   errorOf(3),
		AuthConnection: errorOf(4),
		NewTable:       newTestTable,
		WorkflowStore: func(db *storage.Database) *workflow.HybridWorkflowStore {
			return workflow.NewHybridWorkflowStore(nil, workflow.NewSQLiteWorkflowStore(db.DB))
		},
		RunWorkflow: func(context.Context, []string) error { return errors.New("no workflow runs in tests") },
		NodeRegistry: func(db *sql.DB) *workflow.NodeTypeRegistry {
			r := workflow.NewNodeTypeRegistry()
			r.Register("service.outlook_mail", func() workflow.NodeExecutor { return outlookNode{db} })
			return r
		},
		ResolveCredential: func(context.Context, *connections.Store, string, string) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
	}
}

// newTestTable is the CLI's newPlainTable: no borders, no wrapping, left-aligned unless
// perColumnAlign says otherwise.
func newTestTable(w io.Writer, headers []string, perColumnAlign []tw.Align) *tablewriter.Table {
	cellFmt := tw.CellFormatting{AutoWrap: tw.WrapNone}
	rowCfg := tw.CellConfig{Formatting: cellFmt, Alignment: tw.CellAlignment{Global: tw.AlignLeft}}
	if len(perColumnAlign) > 0 {
		rowCfg.Alignment = tw.CellAlignment{PerColumn: perColumnAlign}
	}
	table := tablewriter.NewTable(w,
		tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{Borders: tw.BorderNone})),
		tablewriter.WithConfig(tablewriter.Config{
			Header: tw.CellConfig{Formatting: cellFmt},
			Row:    rowCfg,
		}),
	)
	table.Header(headers)
	return table
}

// captureStdout returns what fn wrote to os.Stdout (the CLI's captureStdout).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return string(out)
}

// withJevDB runs fn on its own connection to dbPath (the CLI's withJevDB).
func withJevDB(t *testing.T, dbPath string, fn func(db *storage.Database)) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fn(db)
}

// enableJev turns Jev on for surface s in profile (the CLI's enableJev).
func enableJev(t *testing.T, dbPath, profile string, s jevconf.Surface) {
	t.Helper()
	withJevDB(t, dbPath, func(db *storage.Database) {
		if err := jevconf.SetEnabled(db.DB, profile, s, true); err != nil {
			t.Fatal(err)
		}
	})
}
