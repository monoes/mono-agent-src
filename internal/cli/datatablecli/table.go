// Package datatablecli is `monoagentcli table`: list, create, show, read
// and delete the active profile's data tables (internal/datatable), the
// tables core.data_table nodes read and write. Everything the commands need
// from the CLI around them comes in through Deps.
package datatablecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/datatable"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the profile the command runs in once OpenDB has resolved it.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// RequireOperator refuses an agent: only the operator may delete a table (the CLI's
	// requireOperator). Nil asks nothing.
	RequireOperator func(what string) error
}

// NewCommand returns `table` with its subcommands.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "table",
		Short: "Manage data tables",
		Long: `Data tables are tables of typed columns (string, number, boolean, date) a
profile keeps across workflow runs, like n8n's Data tables. Workflows read and
write them with core.data_table nodes; rows have an id, createdAt and updatedAt.`,
	}
	cmd.AddCommand(d.listCmd(), d.createCmd(), d.showCmd(), d.rowsCmd(), d.deleteCmd())
	return cmd
}

// withStore opens the database and runs fn with the profile's store.
func (d Deps) withStore(fn func(s *datatable.Store) error) error {
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	return fn(datatable.New(db.DB, d.ProfileID()))
}

func (d Deps) find(ctx context.Context, s *datatable.Store, ref string) (datatable.Table, error) {
	t, err := s.FindTable(ctx, ref)
	if errors.Is(err, datatable.ErrNotFound) {
		return t, clicore.NotFound("data table %q not found (monoagentcli table list)", ref)
	}
	return t, err
}

type tableSummary struct {
	datatable.Table
	Rows int `json:"rows"`
}

func (d Deps) listCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List data tables",
		Example: `  monoagentcli table list
  monoagentcli table list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withStore(func(s *datatable.Store) error {
				ctx := cmd.Context()
				tables, err := s.ListTables(ctx)
				if err != nil {
					return err
				}
				out := make([]tableSummary, len(tables))
				for i, t := range tables {
					out[i].Table = t
					if out[i].Rows, err = s.CountRows(ctx, t); err != nil {
						return err
					}
				}
				w := cmd.OutOrStdout()
				if d.JSONOutput() {
					return clicore.WriteJSONTo(w, out)
				}
				if len(out) == 0 {
					fmt.Fprintln(w, "No data tables. Create one with: monoagentcli table create <name> --column email:string")
					return nil
				}
				tw := clicore.NewPlainTable(w, []string{"ID", "Name", "Columns", "Rows", "Updated"}, nil)
				for _, t := range out {
					tw.Append([]string{t.ID, clicore.TruncateStr(t.Name, 40), fmt.Sprint(len(t.Columns)), fmt.Sprint(t.Rows), t.UpdatedAt})
				}
				tw.Render()
				return nil
			})
		},
	}
}

// parseColumns reads --column name[:type] values.
func parseColumns(specs []string) ([]datatable.Column, error) {
	var cols []datatable.Column
	for _, spec := range specs {
		name, typ, _ := strings.Cut(spec, ":")
		if typ == "" {
			typ = datatable.TypeString
		}
		if !datatable.ValidType(typ) {
			return nil, clicore.InvalidInput("--column %s: type %q (use string, number, boolean or date)", spec, typ)
		}
		cols = append(cols, datatable.Column{Name: strings.TrimSpace(name), Type: typ})
	}
	return cols, nil
}

func (d Deps) createCmd() *cobra.Command {
	var columns []string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a data table",
		Example: `  monoagentcli table create Leads --column email:string --column score:number
  monoagentcli table create Seen --column url --column at:date --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cols, err := parseColumns(columns)
			if err != nil {
				return err
			}
			return d.withStore(func(s *datatable.Store) error {
				t, err := s.CreateTable(cmd.Context(), args[0], cols)
				if err != nil {
					return clicore.InvalidInput("%v", err)
				}
				if d.JSONOutput() {
					return clicore.WriteJSONTo(cmd.OutOrStdout(), t)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Created data table %s (%s)\n", t.Name, t.ID)
				return nil
			})
		},
	}
	cmd.Flags().StringArrayVar(&columns, "column", nil, "A column as name or name:type (string, number, boolean, date); repeat for more")
	return cmd
}

func (d Deps) showCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <name|id>",
		Short:   "Show a data table's columns and row count",
		Example: `  monoagentcli table show Leads --json`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withStore(func(s *datatable.Store) error {
				t, err := d.find(cmd.Context(), s, args[0])
				if err != nil {
					return err
				}
				n, err := s.CountRows(cmd.Context(), t)
				if err != nil {
					return err
				}
				w := cmd.OutOrStdout()
				if d.JSONOutput() {
					return clicore.WriteJSONTo(w, tableSummary{Table: t, Rows: n})
				}
				fmt.Fprintf(w, "%s (%s): %d row(s), created %s, updated %s\n", t.Name, t.ID, n, t.CreatedAt, t.UpdatedAt)
				for _, c := range t.Columns {
					fmt.Fprintf(w, "  %-24s %s\n", c.Name, c.Type)
				}
				return nil
			})
		},
	}
}

func (d Deps) rowsCmd() *cobra.Command {
	var (
		limit  int
		where  []string
		sortBy string
		desc   bool
	)
	cmd := &cobra.Command{
		Use:   "rows <name|id>",
		Short: "Show a data table's rows",
		Example: `  monoagentcli table rows Leads
  monoagentcli table rows Leads --where email=a@x.com --json
  monoagentcli table rows Leads --sort score --desc --limit 0`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var f datatable.Filter
			f.All = true
			for _, w := range where {
				col, val, ok := strings.Cut(w, "=")
				if !ok || strings.TrimSpace(col) == "" {
					return clicore.InvalidInput("--where %q: use column=value", w)
				}
				f.Conditions = append(f.Conditions, datatable.Condition{Column: strings.TrimSpace(col), Op: datatable.OpEq, Value: val})
			}
			return d.withStore(func(s *datatable.Store) error {
				t, err := d.find(cmd.Context(), s, args[0])
				if err != nil {
					return err
				}
				rows, err := s.Select(cmd.Context(), t, datatable.Query{Filter: f, SortBy: sortBy, Desc: desc, Limit: limit})
				if err != nil {
					return clicore.InvalidInput("%v", err)
				}
				if d.JSONOutput() {
					return clicore.WriteJSONTo(cmd.OutOrStdout(), rows)
				}
				printRows(cmd.OutOrStdout(), t, rows)
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Show at most this many rows (0: all)")
	cmd.Flags().StringArrayVar(&where, "where", nil, "Only rows whose column equals the value (column=value); repeat to require more")
	cmd.Flags().StringVar(&sortBy, "sort", "", "Sort by this column (default: id)")
	cmd.Flags().BoolVar(&desc, "desc", false, "Sort descending")
	return cmd
}

func printRows(w io.Writer, t datatable.Table, rows []map[string]interface{}) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No rows.")
		return
	}
	headers := []string{"id"}
	for _, c := range t.Columns {
		headers = append(headers, c.Name)
	}
	// Keys outside the columns (none unless the table changed) go last.
	var extra []string
	for k := range rows[0] {
		if _, ok := t.ColumnType(k); !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	headers = append(append(headers, extra...), "updatedAt")
	tw := clicore.NewPlainTable(w, headers, nil)
	for _, r := range rows {
		line := make([]string, len(headers))
		for i, h := range headers {
			if v := r[h]; v != nil {
				line[i] = clicore.TruncateStr(fmt.Sprint(v), 40)
			}
		}
		tw.Append(line)
	}
	tw.Render()
}

func (d Deps) deleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <name|id>",
		Short:   "Delete a data table and all its rows",
		Long:    "Removes the table and every row in it, with no confirmation, so an agent is refused: it is the user's to run.",
		Example: `  monoagentcli table delete Leads`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if d.RequireOperator != nil {
				if err := d.RequireOperator("delete the data table " + args[0] + " and all its rows"); err != nil {
					return err
				}
			}
			return d.withStore(func(s *datatable.Store) error {
				t, err := d.find(cmd.Context(), s, args[0])
				if err != nil {
					return err
				}
				if err := s.DeleteTable(cmd.Context(), t); err != nil {
					return err
				}
				if d.JSONOutput() {
					return clicore.WriteJSONTo(cmd.OutOrStdout(), map[string]interface{}{"success": true, "deletedTableId": t.ID, "name": t.Name})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Deleted data table %s (%s)\n", t.Name, t.ID)
				return nil
			})
		},
	}
}
