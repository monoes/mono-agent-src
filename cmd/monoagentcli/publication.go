package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/monoes/mono-agent/internal/publication"
	"github.com/spf13/cobra"
)

func publicationError(err error) error {
	if errors.Is(err, publication.ErrNotFound) {
		return errNotFound("%v", err)
	}
	if errors.Is(err, publication.ErrInvalidInput) {
		return errInvalidInput("%v", err)
	}
	return err
}
func withPublicationStore(cfg *globalConfig, fn func(*publication.Store) error) error {
	return withImageDB(cfg, func(db *sql.DB) error { return publicationError(fn(publication.NewStore(db, cfg.ProfileID))) })
}
func printPublication(cmd *cobra.Command, cfg *globalConfig, e *publication.Entry) error {
	if cfg.JSONOutput {
		return writeJSONTo(cmd.OutOrStdout(), e)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\nPlatform: %s\nKind: %s\nPublished: %s\nTitle: %s\n%s\nURL: %s\n", e.ID, e.Platform, e.Kind, e.PublishedAt, e.Title, e.Body, e.URL)
	return nil
}
func newPublicationCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{Use: "publication", Short: "Track posts, comments and other published content"}
	var f publication.Filter
	list := &cobra.Command{Use: "list", Short: "List this profile's publications, newest first", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if f.Limit < 1 || f.Limit > 1000 || f.Offset < 0 {
			return errInvalidInput("--limit must be 1–1000 and --offset nonnegative")
		}
		return withPublicationStore(cfg, func(store *publication.Store) error {
			entries, err := store.List(cmd.Context(), f)
			if err != nil {
				return err
			}
			if cfg.JSONOutput {
				return writeJSONTo(cmd.OutOrStdout(), entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No publications.")
				return nil
			}
			table := newPlainTable(cmd.OutOrStdout(), []string{"ID", "Platform", "Kind", "Content", "Published"}, nil)
			for _, e := range entries {
				body := e.Title
				if body == "" {
					body = e.Body
				}
				table.Append([]string{e.ID, e.Platform, e.Kind, truncateStr(body, 60), e.PublishedAt})
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
	get := &cobra.Command{Use: "get <id>", Short: "Show a publication", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return withPublicationStore(cfg, func(store *publication.Store) error {
			e, err := store.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printPublication(cmd, cfg, e)
		})
	}}
	var stdinJSON bool
	register := &cobra.Command{Use: "register --stdin-json", Short: "Record an already successful publication; does not publish remotely", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if !stdinJSON {
			return errInvalidInput("registration requires --stdin-json")
		}
		const maxInput = 4 * 1024 * 1024
		input, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxInput+1))
		if err != nil {
			return err
		}
		if len(input) > maxInput {
			return errInvalidInput("publication JSON exceeds 4 MiB")
		}
		var e publication.Entry
		if err := json.Unmarshal(input, &e); err != nil {
			return errInvalidInput("invalid publication JSON: %v", err)
		}
		return withPublicationStore(cfg, func(store *publication.Store) error {
			registered, err := store.Register(cmd.Context(), e)
			if err != nil {
				return err
			}
			return printPublication(cmd, cfg, registered)
		})
	}}
	register.Flags().BoolVar(&stdinJSON, "stdin-json", false, "Read a publication object from stdin")
	stats := &cobra.Command{Use: "stats", Short: "Count publications by platform and kind", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return withPublicationStore(cfg, func(store *publication.Store) error {
			st, err := store.Stats(cmd.Context())
			if err != nil {
				return err
			}
			if cfg.JSONOutput {
				return writeJSONTo(cmd.OutOrStdout(), st)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%v publications\n", st["total"])
			return nil
		})
	}}
	cmd.AddCommand(list, get, register, stats)
	return cmd
}
