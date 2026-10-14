// Package apikey is `monoagentcli api key create|list|show|update|revoke`: the lifecycle of a
// profile's API keys (the store is internal/apikeys'). Everything the commands need from the CLI
// around them comes in through Deps, so they can be tested in isolation.
package apikey

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/apikeys"
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
	// StdinIsTerminal decides whether `revoke` may ask instead of requiring --yes.
	StdinIsTerminal func() bool
	// NotFound builds the CLI's not-found error (exit code 2).
	NotFound func(format string, a ...interface{}) error
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// WriteJSON writes the --json document.
	WriteJSON func(w io.Writer, v any) error
}

// NewCommand groups the lifecycle of the active profile's API keys. A key
// belongs to one profile and authenticates /v1 requests as that profile only.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Create, list, update and revoke the active profile's API keys",
		Long: "API keys authenticate the OpenAI-compatible HTTP API (/v1) served by `monoagentcli httpapi` " +
			"and `monoagentcli daemon`. A key belongs to exactly one profile (select it with --profile): its " +
			"requests run as that profile and add only that profile's knowledge, and what a runtime can read " +
			"on the machine is set by its confinement class (see `api models`). Only the key's SHA-256 is " +
			"stored, so a key is shown once, when it is created.",
	}
	cmd.AddCommand(d.newCreateCmd(), d.newListCmd(), d.newShowCmd(), d.newUpdateCmd(), d.newRevokeCmd())
	return cmd
}

// withKeys opens the database and hands fn the key store and the resolved
// profile id.
func (d Deps) withKeys(cmd *cobra.Command, fn func(ctx context.Context, store *apikeys.Store, profileID string) error) error {
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	return fn(cmd.Context(), apikeys.NewStore(db.DB), d.ProfileID())
}

// keyErr maps a store error to the CLI's exit codes: 2 not found, 3 invalid.
func (d Deps) keyErr(err error) error {
	switch {
	case errors.Is(err, apikeys.ErrNotFound):
		return d.NotFound("%v", err)
	case errors.Is(err, apikeys.ErrNameTaken), errors.Is(err, apikeys.ErrInvalidName):
		return d.InvalidInput("%v", err)
	}
	return err
}

// createdKey is `key create --json`: the key's metadata plus the key itself,
// printed this once.
type createdKey struct {
	apikeys.Key
	Secret string `json:"key"`
}

func (d Deps) newCreateCmd() *cobra.Command {
	var name string
	var withContext bool
	cmd := &cobra.Command{
		Use:   "create --name <name> [--context]",
		Short: "Create an API key for the active profile (shown once)",
		Long: "Creates a key and prints it once; only its hash is kept. With --context, requests made with " +
			"this key get excerpts of the profile's own knowledge (documents and captures) added to the " +
			"prompt; without it the key reaches a plain model. Without --json, stdout is the key alone and " +
			"the notes go to stderr, so `KEY=$(monoagentcli api key create --name app)` works.",
		Example: "  monoagentcli api key create --name my-app\n  monoagentcli api key create --name notes-bot --context --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withKeys(cmd, func(ctx context.Context, store *apikeys.Store, profileID string) error {
				key, secret, err := store.Create(ctx, profileID, name, withContext)
				if err != nil {
					return d.keyErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), createdKey{Key: key, Secret: secret})
				}
				fmt.Fprintln(cmd.OutOrStdout(), secret)
				fmt.Fprintf(cmd.ErrOrStderr(),
					"Created API key %q (%s) for profile %s.\n"+
						"This is the only time the key is shown: store it now, only its hash is kept.\n"+
						"Send it as `Authorization: Bearer <key>` to the /v1 base URL (see `monoagentcli api status`).\n",
					key.Name, key.ID, key.ProfileID)
				if key.Context {
					fmt.Fprintln(cmd.ErrOrStderr(),
						"This key adds the profile's knowledge to requests, so it is served only by chat-only models, and is refused tool calling, unless the server was started with --context-confinement (and --confinement, on a chat-only listener) above chat-only (see `monoagentcli api models`).")
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Key name: 1-64 characters, unique among the profile's active keys")
	cmd.Flags().BoolVar(&withContext, "context", false, "Add the profile's knowledge to requests made with this key (served by chat-only models unless the server raises --context-confinement)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func (d Deps) newListCmd() *cobra.Command {
	var allProfiles, includeRevoked bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List API keys (metadata only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withKeys(cmd, func(ctx context.Context, store *apikeys.Store, profileID string) error {
				var keys []apikeys.Key
				var err error
				if allProfiles {
					keys, err = store.ListAll(ctx, includeRevoked)
				} else {
					keys, err = store.List(ctx, profileID, includeRevoked)
				}
				if err != nil {
					return err
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), keys)
				}
				printKeyTable(cmd.OutOrStdout(), keys, allProfiles)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&allProfiles, "all-profiles", false, "List the keys of every profile, not just the active one")
	cmd.Flags().BoolVar(&includeRevoked, "include-revoked", false, "Include revoked keys")
	return cmd
}

func (d Deps) newShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id|name>",
		Short: "Show one key's metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withKeys(cmd, func(ctx context.Context, store *apikeys.Store, profileID string) error {
				key, err := store.Get(ctx, profileID, args[0])
				if err != nil {
					return d.keyErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), key)
				}
				printKey(cmd.OutOrStdout(), key)
				return nil
			})
		},
	}
}

func (d Deps) newUpdateCmd() *cobra.Command {
	var name string
	var withContext, noContext bool
	cmd := &cobra.Command{
		Use:   "update <id|name> [--name N] [--context | --no-context]",
		Short: "Rename an active key or switch its knowledge context on or off",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var u apikeys.Update
			if cmd.Flags().Changed("name") {
				u.Name = &name
			}
			// The flags' own values count: --context=false turns context off.
			switch {
			case cmd.Flags().Changed("context"):
				u.Context = &withContext
			case noContext: // --no-context=false asks for nothing
				off := false
				u.Context = &off
			}
			if u.IsEmpty() {
				return d.InvalidInput("nothing to change: pass --name, --context or --no-context")
			}
			return d.withKeys(cmd, func(ctx context.Context, store *apikeys.Store, profileID string) error {
				key, err := store.Update(ctx, profileID, args[0], u)
				if err != nil {
					return d.keyErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), key)
				}
				printKey(cmd.OutOrStdout(), key)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "New name")
	cmd.Flags().BoolVar(&withContext, "context", false, "Add the profile's knowledge to requests made with this key")
	cmd.Flags().BoolVar(&noContext, "no-context", false, "Stop adding the profile's knowledge")
	cmd.MarkFlagsMutuallyExclusive("context", "no-context")
	return cmd
}

func (d Deps) newRevokeCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "revoke <id|name>",
		Short: "Revoke a key now (idempotent)",
		Long:  "Requests made with a revoked key are refused from the next request on. Without --yes the command asks first; with stdin not a terminal it needs --yes.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if d.JSONOutput() || !d.StdinIsTerminal() {
					return d.InvalidInput("revoking a key needs --yes when stdin is not a terminal")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Revoke key %s? [y/N] ", args[0])
				ans, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
					fmt.Fprintln(cmd.OutOrStdout(), "Not revoked.")
					return nil
				}
			}
			return d.withKeys(cmd, func(ctx context.Context, store *apikeys.Store, profileID string) error {
				key, err := store.Revoke(ctx, profileID, args[0])
				if err != nil {
					return d.keyErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), key)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Revoked key %q (%s).\n", key.Name, key.ID)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Revoke without asking")
	return cmd
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func keyStatus(k apikeys.Key) string {
	if k.RevokedAt != nil {
		return "revoked"
	}
	return "active"
}

func printKeyTable(w io.Writer, keys []apikeys.Key, withProfile bool) {
	if len(keys) == 0 {
		fmt.Fprintln(w, "No API keys.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "ID\tNAME\tPREFIX\tCONTEXT\tCREATED\tLAST USED\tSTATUS"
	if withProfile {
		header = "PROFILE\t" + header
	}
	fmt.Fprintln(tw, header)
	for _, k := range keys {
		row := fmt.Sprintf("%s\t%s\t%s\t%v\t%s\t%s\t%s", k.ID, k.Name, k.Prefix, k.Context, fmtTime(&k.CreatedAt), fmtTime(k.LastUsedAt), keyStatus(k))
		if withProfile {
			row = k.ProfileID + "\t" + row
		}
		fmt.Fprintln(tw, row)
	}
	_ = tw.Flush()
}

func printKey(w io.Writer, k apikeys.Key) {
	fmt.Fprintf(w, "ID:         %s\nName:       %s\nProfile:    %s\nPrefix:     %s\nContext:    %v\nCreated:    %s\nLast used:  %s\nStatus:     %s\n",
		k.ID, k.Name, k.ProfileID, k.Prefix, k.Context, fmtTime(&k.CreatedAt), fmtTime(k.LastUsedAt), keyStatus(k))
}
