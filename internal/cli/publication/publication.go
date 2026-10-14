// Package publication is `monoagentcli publication`: the profile's history of posts, comments and
// other published content (the store is internal/publication's). Everything the commands need from
// the CLI around them comes in through Deps, so they can be tested in isolation.
package publication

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// ProfileID is the active profile (--profile, resolved); it is read when a command runs.
	ProfileID func() string
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// WriteJSON writes the --json document.
	WriteJSON func(w io.Writer, v any) error
	// NewTable is the CLI's borderless table with these headers.
	NewTable func(w io.Writer, headers []string) *tablewriter.Table
	// Operator refuses an agent-driven caller (--as, MONOAGENT_ACTOR or an org agent's
	// context) what only the operator may do; nil for the operator.
	Operator func(what string) error
	// RequireApproval refuses `register` from an agent unless approvalID is an approved, sent
	// send (when approvals of sends are on); nil for the operator.
	RequireApproval func(approvalID string) error
}

// truncate shortens s to n bytes for a table cell, ending in "..." when it cuts (the CLI's
// truncateStr).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func (d Deps) publicationError(err error) error {
	if errors.Is(err, publication.ErrNotFound) {
		return d.NotFound("%v", err)
	}
	if errors.Is(err, publication.ErrInvalidInput) {
		return d.InvalidInput("%v", err)
	}
	return err
}
func (d Deps) withStore(fn func(*publication.Store) error) error {
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	return d.publicationError(fn(publication.NewStore(db.DB, d.ProfileID())))
}
func (d Deps) printPublication(cmd *cobra.Command, e *publication.Entry) error {
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), e)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\nPlatform: %s\nKind: %s\nPublished: %s\nTitle: %s\n%s\nURL: %s\n", e.ID, e.Platform, e.Kind, e.PublishedAt, e.Title, e.Body, e.URL)
	return nil
}

// NewCommand is the `publication` group: the profile's history of published content.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "publication", Short: "Track posts, comments and other published content"}
	var f publication.Filter
	list := &cobra.Command{Use: "list", Short: "List this profile's publications, newest first", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if f.Limit < 1 || f.Limit > 1000 || f.Offset < 0 {
			return d.InvalidInput("--limit must be 1–1000 and --offset nonnegative")
		}
		return d.withStore(func(store *publication.Store) error {
			entries, next, err := store.ListPage(cmd.Context(), f)
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				if cmd.Flags().Changed("cursor") {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"publications": entries, "next_cursor": next})
				}
				return d.WriteJSON(cmd.OutOrStdout(), entries)
			}
			if next != "" {
				defer fmt.Fprintf(cmd.OutOrStdout(), "Next page: --cursor %s\n", next)
			}
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No publications.")
				return nil
			}
			table := d.NewTable(cmd.OutOrStdout(), []string{"ID", "Platform", "Kind", "Content", "Published"})
			for _, e := range entries {
				body := e.Title
				if body == "" {
					body = e.Body
				}
				table.Append([]string{e.ID, e.Platform, e.Kind, truncate(body, 60), e.PublishedAt})
			}
			table.Render()
			return nil
		})
	}}
	list.Flags().StringVar(&f.Search, "search", "", "Search publication content, title, URL and account")
	list.Flags().StringVar(&f.Platform, "platform", "", "Filter by publishing destination")
	list.Flags().StringVar(&f.Kind, "kind", "", "Filter by publication kind")
	list.Flags().StringVar(&f.WorkflowID, "workflow", "", "Filter by workflow ID")
	list.Flags().StringVar(&f.AgentID, "agent", "", "Filter by agent ID")
	list.Flags().StringVar(&f.Since, "since", "", "Published on or after RFC3339 timestamp or YYYY-MM-DD")
	list.Flags().StringVar(&f.Until, "until", "", "Published on or before RFC3339 timestamp or YYYY-MM-DD")
	list.Flags().IntVar(&f.Limit, "limit", 50, "Maximum records (1–1000)")
	list.Flags().IntVar(&f.Offset, "offset", 0, "Records to skip")
	list.Flags().StringVar(&f.Cursor, "cursor", "", "Continue after a previous page; --cursor '' starts keyset paging and JSON output gains next_cursor")
	del := &cobra.Command{Use: "delete <id>", Short: "Remove a publication from this profile's history (operator only)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := d.Operator("delete publication history"); err != nil {
			return err
		}
		return d.withStore(func(store *publication.Store) error {
			if err := store.Delete(cmd.Context(), args[0]); err != nil {
				return err
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"deleted": args[0]})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s\n", args[0])
			return nil
		})
	}}
	var redactTitle bool
	redact := &cobra.Command{Use: "redact <id>", Short: "Replace a publication's body with [redacted], keeping the entry (operator only)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := d.Operator("redact publication history"); err != nil {
			return err
		}
		return d.withStore(func(store *publication.Store) error {
			e, err := store.Redact(cmd.Context(), args[0], redactTitle)
			if err != nil {
				return err
			}
			return d.printPublication(cmd, e)
		})
	}}
	redact.Flags().BoolVar(&redactTitle, "title", false, "Redact the title too")
	get := &cobra.Command{Use: "get <id>", Short: "Show a publication", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return d.withStore(func(store *publication.Store) error {
			e, err := store.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return d.printPublication(cmd, e)
		})
	}}
	var stdinJSON bool
	var approval string
	register := &cobra.Command{Use: "register --stdin-json", Short: "Record an already successful publication; does not publish remotely", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if !stdinJSON {
			return d.InvalidInput("registration requires --stdin-json")
		}
		const maxInput = 4 * 1024 * 1024
		input, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxInput+1))
		if err != nil {
			return err
		}
		if len(input) > maxInput {
			return d.InvalidInput("publication JSON exceeds 4 MiB")
		}
		var e publication.Entry
		if err := json.Unmarshal(input, &e); err != nil {
			return d.InvalidInput("invalid publication JSON: %v", err)
		}
		if err := d.RequireApproval(approval); err != nil {
			return err
		}
		return d.withStore(func(store *publication.Store) error {
			registered, err := store.Register(cmd.Context(), e)
			if err != nil {
				return err
			}
			return d.printPublication(cmd, registered)
		})
	}}
	register.Flags().StringVar(&approval, "approval", "", "From an agent: the id of the approved, sent send item this publication belongs to")
	register.Flags().BoolVar(&stdinJSON, "stdin-json", false, "Read a publication object from stdin")
	stats := &cobra.Command{Use: "stats", Short: "Count publications by platform and kind", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return d.withStore(func(store *publication.Store) error {
			st, err := store.Stats(cmd.Context())
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), st)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%v publications\n", st["total"])
			return nil
		})
	}}
	cmd.AddCommand(list, get, register, stats, del, redact)
	return cmd
}
