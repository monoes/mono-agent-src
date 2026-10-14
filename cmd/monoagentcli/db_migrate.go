package main

import (
	"context"
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/spf13/cobra"
)

// `db migrate` brings the database and every profile's folder up to date:
// the schema migrations and secret re-encryptions every command runs on open
// (initDB), plus the per-profile folder layout and the vault file moves that
// used to run only inside the desktop app. The app runs it once at startup, so
// one binary (the CLI) owns the schema and there is one schema version.

// dbMigrateResult is `db migrate --json`.
type dbMigrateResult struct {
	SchemaVersion int      `json:"schema_version"` // highest applied migration
	Applied       int      `json:"applied"`        // migrations applied by this run
	ActiveProfile string   `json:"active_profile"`
	Profiles      []string `json:"profiles"`
	VaultMoved    int      `json:"vault_moved"` // vault files moved into their profile's .monoagent/vault
	Errors        []string `json:"errors"`      // per-profile failures; none of them stops the others
}

func newDBCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Database maintenance",
	}
	cmd.AddCommand(newDBMigrateCmd(cfg))
	return cmd
}

func newDBMigrateCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations and per-profile folder/vault migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			before := countAppliedMigrations(expandPath(cfg.DBPath))
			db, err := initDB(cfg)
			if err != nil {
				return err
			}
			defer db.Close()
			res, err := migrateProfileFolders(cmd.Context(), db)
			if err != nil {
				return err
			}
			res.ActiveProfile = cfg.ProfileID
			after := 0
			_ = db.DB.QueryRow(`SELECT COUNT(*), COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&after, &res.SchemaVersion)
			if after > before {
				res.Applied = after - before
			}
			if cfg.JSONOutput {
				return writeJSONTo(cmd.OutOrStdout(), res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "schema version %d (%d migration(s) applied)\n", res.SchemaVersion, res.Applied)
			fmt.Fprintf(cmd.OutOrStdout(), "%d profile(s), %d vault file(s) moved\n", len(res.Profiles), res.VaultMoved)
			for _, e := range res.Errors {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", e)
			}
			return nil
		},
	}
}

// countAppliedMigrations is how many migrations the database at path has
// recorded, 0 when it does not exist yet.
func countAppliedMigrations(path string) int {
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	db, err := storage.NewDatabase(path)
	if err != nil {
		return 0
	}
	defer db.Close()
	var n int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	return n
}

// migrateProfileFolders gives every profile its folder layout and moves its
// vault files off the old shared directory and out of its old visible vault/
// folder. Each step is idempotent and a no-op once a profile is migrated; a
// failure on one profile is reported and does not stop the others.
func migrateProfileFolders(ctx context.Context, db *storage.Database) (*dbMigrateResult, error) {
	res := &dbMigrateResult{Profiles: []string{}, Errors: []string{}}
	rows, err := db.DB.QueryContext(ctx, `SELECT id FROM profiles ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			res.Profiles = append(res.Profiles, id)
		}
	}
	rows.Close()

	for _, id := range res.Profiles {
		if err := profiledir.EnsureLayout(db.DB, id); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("profile %s: creating folder layout: %v", id, err))
			continue
		}
		moved, errs := vault.MigrateVaultFiles(ctx, db.DB, id)
		res.VaultMoved += moved
		for _, e := range errs {
			res.Errors = append(res.Errors, fmt.Sprintf("profile %s: vault file migration: %v", id, e))
		}
		moved, errs = vault.MigrateVaultDirIntoMonoagent(ctx, db.DB, id)
		res.VaultMoved += moved
		for _, e := range errs {
			res.Errors = append(res.Errors, fmt.Sprintf("profile %s: .monoagent vault migration: %v", id, e))
		}
	}
	return res, nil
}
