package record

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/recordanalyze"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The record tests set HOME (internal/recording keeps its stores under it) and the recording
// profile is a package global of internal/recording, so they run serially.

// seams are the Deps seams a test stubs: AnalyzeRunner, SecretLookup, SelfHeal, VerifyExec,
// ReplayPage and StdinIsTerminal. A nil one is testDeps' default.
var seams Deps

// cliError is the fake of the CLI's coded errors (3: invalid input).
type cliError struct {
	error
	code int
}

// reportedError is an error the command already printed as its --json document.
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

// withJSONErrors is the fake of the CLI's: with --json, a failing command prints {"error"} (with "code"
// for invalid input, and the error's own fields) on stdout, unless the command already printed its result.
func withJSONErrors(jsonOut bool, cmd *cobra.Command) {
	run := cmd.RunE
	if run == nil {
		return
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		err := run(c, args)
		var rep reportedError
		if err == nil || !jsonOut || errors.As(err, &rep) {
			return err
		}
		body := map[string]any{"error": err.Error()}
		var ce cliError
		if errors.As(err, &ce) && ce.code == 3 {
			body["code"] = "invalid_input"
		}
		var fields interface{ JSONErrorFields() map[string]any }
		if errors.As(err, &fields) {
			for k, v := range fields.JSONErrorFields() {
				if k != "error" {
					body[k] = v
				}
			}
		}
		b, _ := json.Marshal(body)
		fmt.Fprintln(c.OutOrStdout(), string(b))
		return reportedError{err}
	}
}

// testDeps fakes the CLI for the record commands, with the database at dbPath.
func testDeps(jsonOut bool, dbPath string) Deps {
	d := Deps{
		JSONOutput:     func() bool { return jsonOut },
		Verbose:        func() bool { return false },
		ProfileID:      func() string { return "" },
		BrowserProfile: func() string { return "" },
		ResolveProfile: func(idOrName string) string { return idOrName },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(dbPath)
			if err != nil {
				return nil, err
			}
			if err := db.ApplyMigrations(); err != nil {
				db.Close()
				return nil, err
			}
			return db, nil
		},
		StoreWorkflow: storeWorkflow,
		OpenRegistry:  openRegistry,
		InvalidInput: func(format string, a ...interface{}) error {
			return cliError{fmt.Errorf(format, a...), 3}
		},
		Reported: func(err error) error { return reportedError{err} },
		WriteJSON: func(w io.Writer, v any) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		},
		WithJSONErrors:  func(cmd *cobra.Command) { withJSONErrors(jsonOut, cmd) },
		StdinIsTerminal: func() bool { return false },
		AnalyzeRunner: func(string, string, time.Duration) recordanalyze.Runner {
			return recordanalyze.RunnerFunc(func(context.Context, string) (string, error) {
				return "", errors.New("no AI in tests: stub it with stubRecordAI")
			})
		},
		VerifyExec: func(context.Context, string, bool, bool, func(string) (string, bool)) (recordanalyze.ExecFunc, error) {
			return nil, errors.New("browser bridge not connected: no browser in tests")
		},
		ReplayPage: func(context.Context, bool) (browserpkg.PageInterface, zerolog.Logger, error) {
			return nil, zerolog.Nop(), errors.New("browser bridge not connected: no browser in tests")
		},
	}
	d.SecretLookup = d.VaultSecretLookup
	d.SelfHeal = d.JevSelfHeal
	if seams.AnalyzeRunner != nil {
		d.AnalyzeRunner = seams.AnalyzeRunner
	}
	if seams.SecretLookup != nil {
		d.SecretLookup = seams.SecretLookup
	}
	if seams.SelfHeal != nil {
		d.SelfHeal = seams.SelfHeal
	}
	if seams.VerifyExec != nil {
		d.VerifyExec = seams.VerifyExec
	}
	if seams.ReplayPage != nil {
		d.ReplayPage = seams.ReplayPage
	}
	if seams.StdinIsTerminal != nil {
		d.StdinIsTerminal = seams.StdinIsTerminal
	}
	return d
}

// newTestDeps is testDeps with a fresh database path of the test's.
func newTestDeps(t *testing.T, jsonOut bool) Deps {
	return testDeps(jsonOut, filepath.Join(t.TempDir(), "none.db"))
}

// storeWorkflow is the fake of the CLI's `workflow import` persistence, in SQLite only:
// the workflow row, then its nodes and connections.
func storeWorkflow(ctx context.Context, db *storage.Database, wf *workflow.Workflow) error {
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	if err := store.CreateWorkflow(ctx, wf); err != nil {
		return err
	}
	for i := range wf.Nodes {
		if err := wf.Nodes[i].MarshalConfig(); err != nil {
			return err
		}
	}
	if err := store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
		return err
	}
	return store.SaveWorkflowConnections(ctx, wf.ID, wf.Connections)
}

// openRegistry is the fake of the CLI's openAutomationRegistry: the default registry, booted.
func openRegistry() (*automation.Registry, error) {
	reg, err := automation.Default()
	if err != nil {
		return nil, err
	}
	if _, err := reg.Boot(); err != nil {
		return nil, err
	}
	return reg, nil
}
