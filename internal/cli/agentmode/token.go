package agentmode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/secrets"
)

// vaultPrefix names the vault entry that keeps a token's text, so `agent-mode env` can
// hand it out later: the token table keeps only its hash.
const vaultPrefix = "agent-token-"

const defaultExpiry = 24 * time.Hour

func (d Deps) newTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Create, list, audit and revoke the scoped tokens an agent uses for the HTTP API",
		Long: "A token opens the daemon's profile-scoped HTTP API (/profiles/{profile}/...) for one profile with a scope: " +
			"read (GET only), write (also validate and other changes that start or send nothing) or run (also run and " +
			"(de)activate workflows). It expires (24h unless --expires says otherwise) and can be revoked. " +
			"Mind what run means: it is the authority of EVERY approved or active workflow of the profile, including ones that " +
			"send mail or read the vault; the engine's approval gates apply whatever the caller (an unapproved or AI-authored " +
			"workflow is still refused @secret references and vault.secret_get), and approving or rejecting needs the " +
			"profile's own bearer (scope full), which no token has. Scope a token to what the agent should be able to start. " +
			"The profile's own bearer token keeps working as the full scope unless the server was started with " +
			"MONOAGENT_HTTPAPI_DISABLE_PROFILE_BEARER=1.",
	}
	cmd.AddCommand(d.newTokenCreateCmd(), d.newTokenListCmd(), d.newTokenRevokeCmd(), d.newTokenAuditCmd())
	return cmd
}

func (d Deps) operatorOnly(what string) error {
	if why := clicore.OperatorRefusal(d.Refusal); why != "" {
		return clicore.OperatorOnly("%s is the user's own, so that an agent cannot give itself a wider token: %s; run it yourself in your terminal or the app", what, why)
	}
	return nil
}

func (d Deps) withTokens(cmd *cobra.Command, fn func(ctx context.Context, db *dbHandle, profileID string) error) error {
	if d.OpenDB == nil {
		return errors.New("agent-mode: the database is not wired in")
	}
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	h := &dbHandle{Store: agenttokens.NewStore(db.DB), DB: db.DB}
	profileID := d.ProfileID()
	pruneInactive(cmd.Context(), h, profileID)
	return fn(cmd.Context(), h, profileID)
}

// pruneInactive removes the vault text of every token that expired or was revoked, unless an
// active token has the same name (a name is reusable once its token is revoked). Best effort.
func pruneInactive(ctx context.Context, h *dbHandle, profileID string) {
	toks, err := h.Store.List(ctx, profileID, true)
	if err != nil {
		return
	}
	now := time.Now()
	live := map[string]bool{}
	for _, t := range toks {
		if t.Active(now) {
			live[t.Name] = true
		}
	}
	for _, t := range toks {
		if !t.Active(now) && !live[t.Name] {
			dropVaultCopy(ctx, h, profileID, t.Name)
		}
	}
}

func (d Deps) tokenErr(err error) error {
	switch {
	case errors.Is(err, agenttokens.ErrNotFound):
		return d.NotFound("%v", err)
	case errors.Is(err, agenttokens.ErrNameTaken), errors.Is(err, agenttokens.ErrInvalidName), errors.Is(err, agenttokens.ErrBinding):
		return d.InvalidInput("%v", err)
	}
	return err
}

// parseExpiry reads --expires: a Go duration (24h), days (7d) or "never".
func parseExpiry(v string) (time.Duration, error) {
	if v == "never" {
		return 0, nil
	}
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("--expires %q: want a positive duration such as 24h or 7d, or never", v)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	dur, err := time.ParseDuration(v)
	if err != nil || dur <= 0 {
		return 0, fmt.Errorf("--expires %q: want a positive duration such as 24h or 7d, or never", v)
	}
	return dur, nil
}

type createdToken struct {
	agenttokens.Token
	Secret string `json:"token"`
}

func (d Deps) newTokenCreateCmd() *cobra.Command {
	var name, scope, expires, taskID, conversationID string
	cmd := &cobra.Command{
		Use:   "create --name <name> --scope read|write|run [--expires 24h|7d|never] [--task ID --conversation ID]",
		Short: "Create a scoped token for the profile named by --profile (shown once)",
		Long: "Creates a token for the HTTP API and prints it once. Without --json stdout is the token alone and the notes " +
			"go to stderr. Hand it to an agent with `agent-mode env --token <name>`. --task and --conversation (both, scope read or " +
			"write) bind the token to one AI task and the conversation that runs it: the agent API routes of that task accept it " +
			"and no other task's. " + clicore.OperatorOnlyHelp,
		Example: "  monoagentcli --profile work agent-mode token create --name reviewer --scope read --expires 24h",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := d.operatorOnly("agent-mode token create"); err != nil {
				return err
			}
			sc, err := agenttokens.ParseIssuable(scope)
			if err != nil {
				return d.InvalidInput("%v", err)
			}
			ttl, err := parseExpiry(expires)
			if err != nil {
				return d.InvalidInput("%v", err)
			}
			if (taskID == "") != (conversationID == "") {
				return d.InvalidInput("--task and --conversation go together")
			}
			return d.withTokens(cmd, func(ctx context.Context, h *dbHandle, profileID string) error {
				bind := agenttokens.Binding{TaskID: taskID, ConversationID: conversationID}
				if taskID != "" {
					t, err := (&aitask.Store{DB: h.DB, Profile: profileID}).Get(taskID)
					if err != nil {
						return d.NotFound("%v", err)
					}
					if t.ConversationID != conversationID {
						return d.InvalidInput("task %s does not run in conversation %s", taskID, conversationID)
					}
				}
				tok, secret, err := h.Store.CreateBound(ctx, profileID, name, sc, ttl, bind)
				if err != nil {
					return d.tokenErr(err)
				}
				// Keep the text in the vault so `env` can hand it out; a token that could not be kept
				// could never be handed out, so it does not survive the failure.
				if _, err := secrets.Add(secrets.AllowReservedWrite(ctx), h.DB, profileID, "secret", vaultPrefix+tok.Name,
					map[string]string{"token": secret}, "", "", "agent-mode token "+tok.ID); err != nil {
					_, _ = h.Store.Revoke(ctx, profileID, tok.ID)
					return fmt.Errorf("keeping the token in the vault: %w", err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), createdToken{Token: tok, Secret: secret})
				}
				fmt.Fprintln(cmd.OutOrStdout(), secret)
				exp := "never expires"
				if tok.ExpiresAt != nil {
					exp = "expires " + tok.ExpiresAt.Format(time.RFC3339)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Created %s token %q (%s) for profile %s; %s.\n"+
					"Hand it to an agent with: eval \"$(monoagentcli --profile %s agent-mode env --token %s)\"\n",
					tok.Scope, tok.Name, tok.ID, tok.ProfileID, exp, tok.ProfileID, tok.Name)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Token name: 1-64 characters, unique among the profile's unrevoked tokens")
	cmd.Flags().StringVar(&scope, "scope", "", "What the token may do: read, write or run")
	cmd.Flags().StringVar(&expires, "expires", defaultExpiry.String(), "How long it lasts: a duration (24h), days (7d) or never")
	cmd.Flags().StringVar(&taskID, "task", "", "Bind the token to this AI task (with --conversation)")
	cmd.Flags().StringVar(&conversationID, "conversation", "", "The conversation that runs the task (with --task)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("scope")
	return cmd
}

func (d Deps) newTokenListCmd() *cobra.Command {
	var includeRevoked bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the profile's tokens (metadata only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return d.withTokens(cmd, func(ctx context.Context, h *dbHandle, profileID string) error {
				toks, err := h.Store.List(ctx, profileID, includeRevoked)
				if err != nil {
					return err
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), toks)
				}
				printTokens(cmd.OutOrStdout(), toks)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&includeRevoked, "include-revoked", false, "Include revoked tokens")
	return cmd
}

func printTokens(w io.Writer, toks []agenttokens.Token) {
	if len(toks) == 0 {
		fmt.Fprintln(w, "No tokens.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSCOPE\tID\tEXPIRES\tLAST USED\tSTATE")
	now := time.Now()
	for _, t := range toks {
		exp, last, state := "never", "-", "active"
		if t.ExpiresAt != nil {
			exp = t.ExpiresAt.Format(time.RFC3339)
		}
		if t.LastUsedAt != nil {
			last = t.LastUsedAt.Format(time.RFC3339)
		}
		switch {
		case t.RevokedAt != nil:
			state = "revoked"
		case !t.Active(now):
			state = "expired"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, t.Scope, t.ID, exp, last, state)
	}
	_ = tw.Flush()
}

func (d Deps) newTokenRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <name|id>",
		Short: "Revoke a token now",
		Long:  "Ends the token at once: the API refuses it from the next request. " + clicore.OperatorOnlyHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := d.operatorOnly("agent-mode token revoke"); err != nil {
				return err
			}
			return d.withTokens(cmd, func(ctx context.Context, h *dbHandle, profileID string) error {
				tok, err := h.Store.Revoke(ctx, profileID, args[0])
				if err != nil {
					return d.tokenErr(err)
				}
				dropVaultCopy(ctx, h, profileID, tok.Name)
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), tok)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Revoked token %q (%s).\n", tok.Name, tok.ID)
				return nil
			})
		},
	}
}

// dropVaultCopy removes the vault entry holding a revoked token's text. Best effort: the
// token is dead whether or not this works.
func dropVaultCopy(ctx context.Context, h *dbHandle, profileID, name string) {
	entries, err := secrets.List(ctx, h.DB, profileID)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Kind == "secret" && e.Name == vaultPrefix+name {
			_ = secrets.Delete(secrets.AllowReservedWrite(ctx), h.DB, profileID, e.ID)
		}
	}
}

func (d Deps) newTokenAuditCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "audit <name|id>",
		Short: "Show what a token was used for (path, method, status; newest first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withTokens(cmd, func(ctx context.Context, h *dbHandle, profileID string) error {
				tok, err := h.Store.Get(ctx, profileID, args[0])
				if err != nil {
					return d.tokenErr(err)
				}
				uses, err := h.Store.Audit(ctx, tok, limit)
				if err != nil {
					return err
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), uses)
				}
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "AT\tTOKEN\tMETHOD\tPATH\tSTATUS")
				for _, u := range uses {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", u.At.Format(time.RFC3339), u.TokenName, u.Method, u.Path, u.Status)
				}
				return tw.Flush()
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "How many uses to show")
	return cmd
}
