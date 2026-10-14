package messagetemplate

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/spf13/cobra"
)

// NewCommand returns `message-template` with its subcommands.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "message-template",
		Aliases: []string{"template"},
		Short:   "Manage message templates (alias: template)",
		Long: "Create, list, view, and delete message templates used for bulk messaging.\n\n" +
			"Not workflow templates: those are `monoagentcli workflow templates`. " +
			"`monoagentcli template` still works as an alias.",
	}

	cmd.AddCommand(
		d.newTemplateListCmd(),
		d.newTemplateGetCmd(),
		d.newTemplateCreateCmd(),
		d.newTemplateDeleteCmd(),
	)

	return cmd
}

func (d Deps) newTemplateListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all templates",
		Example: `  monoagentcli message-template list
  monoagentcli message-template list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			rows, err := db.DB.Query(
				`SELECT id, name, COALESCE(subject,''), body, COALESCE(metadata,''), created_at, updated_at
				 FROM templates ORDER BY name ASC`,
			)
			if err != nil {
				return fmt.Errorf("querying templates: %w", err)
			}
			defer rows.Close()

			var templates []storage.Template
			for rows.Next() {
				var t storage.Template
				if err := rows.Scan(&t.ID, &t.Name, &t.Subject, &t.Body, &t.Metadata, &t.CreatedAt, &t.UpdatedAt); err != nil {
					return fmt.Errorf("scanning template: %w", err)
				}
				templates = append(templates, t)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating templates: %w", err)
			}

			if d.JSONOutput() {
				if templates == nil {
					templates = []storage.Template{}
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(templates)
			}

			if len(templates) == 0 {
				fmt.Println("No templates found.")
				return nil
			}

			table := clicore.NewPlainTable(cmd.OutOrStdout(), []string{"ID", "Name", "Subject", "Body", "Created"},
				[]tw.Align{tw.AlignRight, tw.AlignLeft, tw.AlignLeft, tw.AlignLeft, tw.AlignLeft})

			for _, t := range templates {
				table.Append([]string{
					fmt.Sprintf("%d", t.ID),
					clicore.TruncateStr(t.Name, 20),
					clicore.TruncateStr(t.Subject, 20),
					clicore.TruncateStr(t.Body, 30),
					t.CreatedAt.Format("2006-01-02 15:04"),
				})
			}
			table.Render()
			fmt.Fprintf(cmd.ErrOrStderr(), "\nTotal: %d template(s)\n", len(templates))
			return nil
		},
	}
}

func (d Deps) newTemplateGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show template details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var templateID int
			if _, err := fmt.Sscanf(args[0], "%d", &templateID); err != nil {
				return fmt.Errorf("invalid template ID %q: must be an integer", args[0])
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			var t storage.Template
			var subject, metadata sql.NullString
			err = db.DB.QueryRow(
				`SELECT id, name, subject, body, metadata, created_at, updated_at
				 FROM templates WHERE id = ?`, templateID,
			).Scan(&t.ID, &t.Name, &subject, &t.Body, &metadata, &t.CreatedAt, &t.UpdatedAt)
			if err == sql.ErrNoRows {
				return fmt.Errorf("template %d not found", templateID)
			}
			if err != nil {
				return fmt.Errorf("querying template: %w", err)
			}
			t.Subject = subject.String
			t.Metadata = metadata.String

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(t)
			}

			table := clicore.NewPlainTable(os.Stdout, []string{"Field", "Value"}, []tw.Align{tw.AlignRight, tw.AlignLeft})

			table.Append([]string{"ID", fmt.Sprintf("%d", t.ID)})
			table.Append([]string{"Name", t.Name})
			table.Append([]string{"Subject", t.Subject})
			table.Append([]string{"Body", t.Body})
			if t.Metadata != "" {
				table.Append([]string{"Metadata", clicore.TruncateStr(t.Metadata, 60)})
			}
			table.Append([]string{"Created", t.CreatedAt.Format("2006-01-02 15:04:05")})
			table.Append([]string{"Updated", t.UpdatedAt.Format("2006-01-02 15:04:05")})
			table.Render()

			return nil
		},
	}
}

func (d Deps) newTemplateCreateCmd() *cobra.Command {
	var (
		name    string
		body    string
		subject string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new template",
		Example: `  monoagentcli message-template create --name "Welcome" --body "Hello {{name}}!" --subject "Welcome aboard"
  monoagentcli message-template create --name "Follow-up" --body "Just checking in..."`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if body == "" {
				return fmt.Errorf("--body is required")
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			now := time.Now().UTC()

			var subjectVal sql.NullString
			if subject != "" {
				subjectVal = sql.NullString{String: subject, Valid: true}
			}

			result, err := db.DB.Exec(
				`INSERT INTO templates (name, subject, body, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?)`,
				name, subjectVal, body, now, now,
			)
			if err != nil {
				return fmt.Errorf("creating template: %w", err)
			}

			id, _ := result.LastInsertId()

			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]interface{}{
					"id":      id,
					"name":    name,
					"subject": subject,
					"body":    body,
				})
			}

			fmt.Fprintf(os.Stdout, "Created template %d (%s)\n", id, name)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Template name (required)")
	cmd.Flags().StringVar(&body, "body", "", "Template body (required)")
	cmd.Flags().StringVar(&subject, "subject", "", "Template subject (optional)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("body")

	return cmd
}

func (d Deps) newTemplateDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var templateID int
			if _, err := fmt.Sscanf(args[0], "%d", &templateID); err != nil {
				return fmt.Errorf("invalid template ID %q: must be an integer", args[0])
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			result, err := db.DB.Exec("DELETE FROM templates WHERE id = ?", templateID)
			if err != nil {
				return fmt.Errorf("deleting template: %w", err)
			}

			affected, _ := result.RowsAffected()
			if affected == 0 {
				return fmt.Errorf("template %d not found", templateID)
			}

			fmt.Fprintf(os.Stdout, "Deleted template %d.\n", templateID)
			return nil
		},
	}
}
