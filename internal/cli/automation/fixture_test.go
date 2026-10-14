package automation

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/storage"
)

// The automation commands are tested here with the CLI around them faked through Deps. The
// registry and profile folders resolve under $HOME, so most tests stay serial.

// testConfig is the fake of the CLI's global flags: --db-path, --json, --profile, --verbose.
type testConfig struct {
	DBPath     string
	JSONOutput bool
	ProfileID  string
	Verbose    bool
}

// reportedError is an error the command already reported.
type reportedError struct{ error }

// installResultError is a failed install that keeps its result.
type installResultError struct {
	err error
	res *automation.InstallResult
}

func (e *installResultError) Error() string { return e.err.Error() }
func (e *installResultError) Unwrap() error { return e.err }

// openTestRegistry opens the registry under $HOME as the CLI's openAutomationRegistry does
// (a dev build: no engine range is enforced).
func openTestRegistry() (*automation.Registry, error) {
	reg, err := automation.Default()
	if err != nil {
		return nil, fmt.Errorf("open automation registry: %w", err)
	}
	if _, err := reg.Boot(); err != nil {
		return nil, fmt.Errorf("fold legacy actions: %w", err)
	}
	return reg, nil
}

// testDeps is the CLI around the commands for cfg. OpenDB resolves the profile as the CLI's
// initDB does: "" is the active profile (else "default"), and an id or a name resolves to the
// id; an unknown one is an error.
func testDeps(cfg *testConfig) Deps {
	return Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		ProfileID:  func() string { return cfg.ProfileID },
		Verbose:    func() bool { return cfg.Verbose },
		DBPath:     func() string { return cfg.DBPath },
		OpenDB: func() (*storage.Database, error) {
			if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
				return nil, err
			}
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
			err = db.DB.QueryRow(`SELECT id FROM profiles WHERE id = ? OR LOWER(name) = LOWER(?) ORDER BY id = ? DESC LIMIT 1`,
				cfg.ProfileID, cfg.ProfileID, cfg.ProfileID).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				db.Close()
				return nil, fmt.Errorf("profile %q not found (checked both id and name)", cfg.ProfileID)
			}
			if err == nil {
				cfg.ProfileID = id
			}
			return db, nil
		},
		OpenRegistry: openTestRegistry,
		NewTable:     newTestTable,
		WithJSONErrors: func(cmd *cobra.Command) {
			run := cmd.RunE
			if run == nil {
				return
			}
			cmd.RunE = func(c *cobra.Command, args []string) error {
				err := run(c, args)
				var rep reportedError
				if err != nil && cfg.JSONOutput && !errors.As(err, &rep) {
					fmt.Fprintf(c.OutOrStdout(), "{\"error\":%q}\n", err.Error())
					return reportedError{err}
				}
				return err
			}
		},
		Reported: func(err error) error { return reportedError{err} },
		WithInstallResult: func(err error, res *automation.InstallResult) error {
			if err == nil || res == nil {
				return err
			}
			return &installResultError{err: err, res: res}
		},
		SetupBridge: func(string, zerolog.Logger, time.Duration) browserpkg.ExtensionBridge { return nil },
		EnsureConnected: func(browserpkg.ExtensionBridge, time.Duration) error {
			return errors.New("no browser in tests")
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
