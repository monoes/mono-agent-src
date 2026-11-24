package agentmode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/secrets"
)

// dbHandle is an open database and the token store over it.
type dbHandle struct {
	Store *agenttokens.Store
	DB    *sql.DB
}

// Names of the variables the sandboxed process reads. A convention only: nothing in
// mono-agent reads them.
const (
	EnvURL   = "MONOAGENT_API_URL"
	EnvToken = "MONOAGENT_API_TOKEN"
)

func (d Deps) newEnvCmd() *cobra.Command {
	var tokenName, format, url string
	cmd := &cobra.Command{
		Use:   "env --token <name> [--format sh|json] [--url URL]",
		Short: "Print " + EnvURL + " and " + EnvToken + " for an agent, to be set OUTSIDE the sandbox",
		Long: "Prints the two environment variables a sandboxed agent needs to call the HTTP API: the profile's API base URL " +
			"and a token made with `agent-mode token create`. Run it outside the sandbox, before starting the agent, so the " +
			"token reaches the agent through its environment and not through a file or command it could read:\n\n" +
			"  eval \"$(monoagentcli --profile work agent-mode env --token reviewer)\"\n" +
			"  monoagentcli agent-mode sandbox --runtime bwrap > agent-sandbox.sh\n" +
			"  sh agent-sandbox.sh claude\n\n" +
			"The token is read from the vault; an expired or revoked one is refused. " + clicore.OperatorOnlyHelp,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := d.operatorOnly("agent-mode env"); err != nil {
				return err
			}
			if format != "sh" && format != "json" {
				return d.InvalidInput("--format %q: want sh or json", format)
			}
			return d.withTokens(cmd, func(ctx context.Context, h *dbHandle, profileID string) error {
				tok, secret, err := tokenText(ctx, h, profileID, tokenName)
				if err != nil {
					return d.tokenErr(err)
				}
				base := url
				if base == "" {
					base = apiBase(d.addr())
				}
				if w := plainHTTPWarning(base); w != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+w)
				}
				vars := [][2]string{{EnvURL, strings.TrimRight(base, "/") + "/profiles/" + tok.ProfileID}, {EnvToken, secret}}
				return writeEnv(cmd, vars, format)
			})
		},
	}
	cmd.Flags().StringVar(&tokenName, "token", "", "Name (or id) of the token to hand over")
	cmd.Flags().StringVar(&format, "format", "sh", "sh (export lines for eval) or json")
	cmd.Flags().StringVar(&url, "url", "", "API base URL (default: the daemon's address on loopback)")
	_ = cmd.MarkFlagRequired("token")
	return cmd
}

func (d Deps) addr() string {
	if d.APIAddr == nil {
		return ""
	}
	return d.APIAddr()
}

// apiBase turns a listen address into a URL a local client can use: an unspecified host
// (":9322", "0.0.0.0:9322") becomes loopback.
func apiBase(addr string) string {
	if addr == "" {
		addr = "127.0.0.1:9322"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// plainHTTPWarning says why an http:// base on a host that is not loopback will not work: the
// agent client only talks plain http to loopback and refuses it anywhere else. "" when fine.
func plainHTTPWarning(base string) string {
	rest, ok := strings.CutPrefix(base, "http://")
	if !ok {
		return ""
	}
	host := rest
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return ""
	}
	return "the API address " + base + " is plain http on a host that is not loopback, which the agent client refuses; " +
		"put the API behind https and pass --url https://…, or listen on loopback"
}

// tokenText reads an active token's text back from the vault.
func tokenText(ctx context.Context, h *dbHandle, profileID, ref string) (agenttokens.Token, string, error) {
	tok, err := h.Store.Get(ctx, profileID, ref)
	if err != nil {
		return agenttokens.Token{}, "", err
	}
	entries, err := secrets.List(ctx, h.DB, profileID)
	if err != nil {
		return agenttokens.Token{}, "", err
	}
	for _, e := range entries {
		if e.Kind != "secret" || e.Name != vaultPrefix+tok.Name {
			continue
		}
		fields, _, err := secrets.DecryptFields(ctx, h.DB, profileID, e.ID)
		if err != nil {
			return agenttokens.Token{}, "", err
		}
		secret := fields["token"]
		// Verify, not just compare: it refuses a token that expired or was revoked.
		if got, err := h.Store.Verify(ctx, secret); err != nil || got.ID != tok.ID {
			return agenttokens.Token{}, "", fmt.Errorf("token %q is expired or revoked: make a new one with `agent-mode token create`", tok.Name)
		}
		return tok, secret, nil
	}
	return agenttokens.Token{}, "", fmt.Errorf("token %q is not in the vault (it was made elsewhere or its entry was deleted): make a new one with `agent-mode token create`", tok.Name)
}

func writeEnv(cmd *cobra.Command, vars [][2]string, format string) error {
	out := cmd.OutOrStdout()
	if format == "json" {
		m := map[string]string{}
		for _, v := range vars {
			m[v[0]] = v[1]
		}
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(b))
		return err
	}
	for _, v := range vars {
		if _, err := fmt.Fprintf(out, "export %s=%s\n", v[0], shQuote(v[1])); err != nil {
			return err
		}
	}
	return nil
}

// shQuote single-quotes s for a POSIX shell.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
