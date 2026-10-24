package secret

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/secrets"
)

// newSecretAgentAccessCmd manages profile vault permissions. Mutations remain
// operator-only; on/off are compatibility aliases for scoped use/strict.
func (d Deps) newSecretAgentAccessCmd() *cobra.Command {
	var (
		yes, anyHost bool
		names, hosts []string
		expires      string
	)
	cmd := &cobra.Command{
		Use:   "agent-access [strict|use|reveal]",
		Short: "Set profile vault access: strict, use without revealing, or reveal",
		Long: "Choose strict (default), use (supported secret references without plaintext retrieval), or reveal (plaintext retrieval too).\n" +
			"Only the operator can change this profile setting. use/reveal require --yes.\n" +
			"on/off remain aliases for scoped use/strict; on requires --host or --any-host.\n" +
			"--secret and --expires narrow a grant; --host narrows use to supported destinations.\n" +
			"Use masks supported outputs best-effort; arbitrary code can transform secrets past masking.\n" +
			"Every agent read is audited by name, never value. Internal authentication tokens stay reserved.",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"strict", "use", "reveal", "on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] != "on" && args[0] != "off" && args[0] != "strict" && args[0] != "use" && args[0] != "reveal" {
				return fmt.Errorf("agent-access takes strict, use or reveal (on/off aliases), not %q", args[0])
			}
			var grant secrets.AccessGrant
			if len(args) == 1 {
				if why := operator.Refusal(); why != "" {
					return clicore.OperatorOnly("agent-access decides whether an agent may use your vault, which is yours to choose: %s; run it yourself in your terminal or the app", why)
				}
				if args[0] == "on" || args[0] == "use" || args[0] == "reveal" {
					if !yes {
						if args[0] == "reveal" {
							return fmt.Errorf("pass --yes to confirm: agents may retrieve plaintext passwords from this profile's vault")
						}
						return fmt.Errorf("pass --yes to confirm: the secrets named (every one by default) become usable by agent-written workflows, and masking in their stored output is best-effort (any expression or code step can defeat it), so this trusts those workflows with the secrets and hosts you name")
					}
					if args[0] != "on" && len(hosts) == 0 {
						anyHost = true
					}
					var err error
					if grant, err = buildGrant(names, hosts, anyHost, expires); err != nil {
						return err
					}
					grant.Level = "use"
					if args[0] == "reveal" {
						grant.Level = "reveal"
					}
				}
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			// After OpenDB: only then is the profile resolved from a name to its id.
			profileID := d.ProfileID()
			if profileID == "" {
				return ErrProfileRequired
			}
			if len(args) == 1 {
				var err error
				if args[0] == "on" || args[0] == "use" || args[0] == "reveal" {
					err = secrets.SetAgentAccessGrant(cmd.Context(), db.DB, profileID, grant)
				} else {
					err = secrets.SetAgentAccess(cmd.Context(), db.DB, profileID, false)
				}
				if err != nil {
					return fmt.Errorf("saving agent access: %w", err)
				}
			}
			g, err := secrets.GetAgentAccess(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("reading agent access: %w", err)
			}
			level := "strict"
			if g != nil {
				level = g.Level
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				out := map[string]any{"profile_id": profileID, "agent_access": g != nil, "level": level}
				if g != nil {
					out["secrets"] = g.Secrets
					out["hosts"] = g.Hosts
					out["any_host"] = g.AnyHost
					out["expires_at"] = g.ExpiresAt
				}
				return enc.Encode(out)
			}
			w := cmd.OutOrStdout()
			if g == nil {
				fmt.Fprintln(w, "Strict: agents cannot use or reveal vault secrets without your approval.")
				return nil
			}
			fmt.Fprintf(w, "Level: %s (profile %s)\n", level, profileID)
			fmt.Fprintf(w, "  secrets: %s\n", orAll(g.Secrets))
			if g.AnyHost {
				fmt.Fprintln(w, "  hosts:   ANY (a secret can be sent to any address a workflow names)")
			} else {
				fmt.Fprintf(w, "  hosts:   %s\n", strings.Join(g.Hosts, ", "))
			}
			if g.ExpiresAt != nil {
				fmt.Fprintf(w, "  expires: %s\n", g.ExpiresAt.Local().Format(time.RFC3339))
			} else {
				fmt.Fprintln(w, "  expires: never")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm enabling use or plaintext reveal")
	cmd.Flags().StringSliceVar(&names, "secret", nil, "Allow only these secrets (default: all of the profile's)")
	cmd.Flags().StringSliceVar(&hosts, "host", nil, "Send a secret only to these hosts")
	cmd.Flags().BoolVar(&anyHost, "any-host", false, "Do not bind secrets to hosts: a workflow can send an allowed secret to any address it names")
	cmd.Flags().StringVar(&expires, "expires", "", "Turn off after this long, e.g. 24h or 7d (default: never)")
	cmd.AddCommand(d.newSecretAgentAccessAuditCmd())
	return cmd
}

func orAll(names []string) string {
	if len(names) == 0 {
		return "all"
	}
	return strings.Join(names, ", ")
}

// buildGrant checks the flags of `agent-access on` and turns them into a grant.
func buildGrant(names, hosts []string, anyHost bool, expires string) (secrets.AccessGrant, error) {
	g := secrets.AccessGrant{AnyHost: anyHost}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			g.Secrets = append(g.Secrets, n)
		}
	}
	for _, h := range hosts {
		if h = strings.TrimSpace(h); h == "" {
			continue
		}
		if secrets.HostOf(h) == "" {
			return g, fmt.Errorf("--host %q is not a host name", h)
		}
		g.Hosts = append(g.Hosts, secrets.HostOf(h))
	}
	if len(g.Hosts) == 0 && !anyHost {
		return g, fmt.Errorf("pass --host HOST[,HOST...] to say where a secret may be sent, or --any-host to let a workflow an agent wrote send it to any address it names")
	}
	if anyHost && len(g.Hosts) > 0 {
		return g, fmt.Errorf("--host and --any-host exclude each other")
	}
	if expires != "" {
		dur, err := parseExpiry(expires)
		if err != nil {
			return g, err
		}
		t := time.Now().UTC().Add(dur)
		g.ExpiresAt = &t
	}
	return g, nil
}

func parseExpiry(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	} else if dur, err := time.ParseDuration(s); err == nil && dur > 0 {
		return dur, nil
	}
	return 0, fmt.Errorf("--expires %q: want a positive duration such as 24h or 7d", s)
}

// newSecretAgentAccessAuditCmd is `secret agent-access audit`: the secrets
// agent-written workflows read under agent access. Clearing it is the
// user's own, like the switch.
func (d Deps) newSecretAgentAccessAuditCmd() *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "List the secrets agent-written workflows read under agent access (names, never values)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if clear {
				if why := operator.Refusal(); why != "" {
					return clicore.OperatorOnly("clearing the agent-access audit erases the record of what an agent read, which is yours to decide: %s; run it yourself in your terminal or the app", why)
				}
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			profileID := d.ProfileID()
			if profileID == "" {
				return ErrProfileRequired
			}
			rows, err := secrets.AgentAccessAudit(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("reading the audit: %w", err)
			}
			if rows == nil {
				rows = []secrets.AuditRow{}
			}
			w := cmd.OutOrStdout()
			if d.JSONOutput() {
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				if err := enc.Encode(rows); err != nil {
					return err
				}
			} else if len(rows) == 0 {
				fmt.Fprintln(w, "No secret was read by an agent-written workflow.")
			} else {
				for _, r := range rows {
					fmt.Fprintf(w, "%s  secret %s  workflow %s  run %s  step %s\n", r.At, r.Secret, r.WorkflowID, r.ExecutionID, r.NodeName)
				}
			}
			if clear {
				n, err := secrets.ClearAgentAccessAudit(cmd.Context(), db.DB, profileID)
				if err != nil {
					return fmt.Errorf("clearing the audit: %w", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Cleared %d audit rows.\n", n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "Print the audit, then delete it (yours to do, not an agent's)")
	return cmd
}
