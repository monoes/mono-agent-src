package configcli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/spf13/cobra"
)

// NewCommand returns `config` with its subcommands.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration entries",
		Long:  "List, get, set, and delete key-value configuration entries stored in the database.",
	}

	cmd.AddCommand(
		d.newConfigListCmd(),
		d.newConfigGetCmd(),
		d.newConfigSetCmd(),
		d.newConfigDeleteCmd(),
	)

	return cmd
}

func (d Deps) newConfigListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all config entries",
		Example: `  monoagentcli config list
  monoagentcli config list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			rows, err := db.DB.Query(
				"SELECT name, config_data, created_at, updated_at FROM configs ORDER BY name ASC",
			)
			if err != nil {
				return fmt.Errorf("querying configs: %w", err)
			}
			defer rows.Close()

			var configs []storage.ConfigEntry
			for rows.Next() {
				var c storage.ConfigEntry
				if err := rows.Scan(&c.Name, &c.ConfigData, &c.CreatedAt, &c.UpdatedAt); err != nil {
					return fmt.Errorf("scanning config: %w", err)
				}
				configs = append(configs, c)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating configs: %w", err)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(configs)
			}

			if len(configs) == 0 {
				fmt.Println("No config entries found.")
				return nil
			}

			table := clicore.NewPlainTable(os.Stdout, []string{"Name", "Value", "Updated"}, nil)

			for _, c := range configs {
				table.Append([]string{
					c.Name,
					clicore.TruncateStr(c.ConfigData, 50),
					c.UpdatedAt.Format("2006-01-02 15:04"),
				})
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nTotal: %d config(s)\n", len(configs))
			return nil
		},
	}
}

func (d Deps) newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Show config value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			var c storage.ConfigEntry
			err = db.DB.QueryRow(
				"SELECT name, config_data, created_at, updated_at FROM configs WHERE name = ?", name,
			).Scan(&c.Name, &c.ConfigData, &c.CreatedAt, &c.UpdatedAt)
			if err == sql.ErrNoRows {
				return fmt.Errorf("config %q not found", name)
			}
			if err != nil {
				return fmt.Errorf("querying config: %w", err)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(c)
			}

			table := clicore.NewPlainTable(os.Stdout, []string{"Field", "Value"}, []tw.Align{tw.AlignRight, tw.AlignLeft})

			table.Append([]string{"Name", c.Name})
			table.Append([]string{"Value", c.ConfigData})
			table.Append([]string{"Created", c.CreatedAt.Format("2006-01-02 15:04:05")})
			table.Append([]string{"Updated", c.UpdatedAt.Format("2006-01-02 15:04:05")})
			table.Render()

			return nil
		},
	}
}

func (d Deps) newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name> <value>",
		Short: "Set a config value",
		Example: `  monoagentcli config set api_key "sk-abc123"
  monoagentcli config set max_retries "3"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			value := args[1]

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			_, err = db.DB.Exec(
				`INSERT INTO configs (name, config_data, created_at, updated_at)
				 VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
				 ON CONFLICT(name)
				 DO UPDATE SET config_data = excluded.config_data,
				               updated_at  = excluded.updated_at`,
				name, value,
			)
			if err != nil {
				return fmt.Errorf("setting config: %w", err)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{
					"name":  name,
					"value": value,
				})
			}

			fmt.Fprintf(os.Stdout, "Set config %s = %s\n", name, value)
			return nil
		},
	}
}

func (d Deps) newConfigDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a config entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			result, err := db.DB.Exec("DELETE FROM configs WHERE name = ?", name)
			if err != nil {
				return fmt.Errorf("deleting config: %w", err)
			}

			affected, _ := result.RowsAffected()
			if affected == 0 {
				return fmt.Errorf("config %q not found", name)
			}

			fmt.Fprintf(os.Stdout, "Deleted config %s.\n", name)
			return nil
		},
	}
}
