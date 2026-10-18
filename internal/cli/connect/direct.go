package connect

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/revealgate"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/spf13/cobra"
)

// newConnectSaveCmd saves a connection from credential field values read as
// a JSON object on stdin — never argv, where other local users could read
// them in the process list. It is the non-interactive counterpart of
// `connect <platform>`, used by the desktop app's connection form.
func (d Deps) newConnectSaveCmd() *cobra.Command {
	var method string
	var stdinJSON bool

	cmd := &cobra.Command{
		Use:   "save <platform> --method <method> --stdin-json",
		Short: "Validate and save a connection from field values given as JSON on stdin",
		Long: "Reads the credential fields as one JSON object on stdin, validates them against the " +
			"platform and saves the connection under the profile named by --profile. Field values are never " +
			"printed; --json prints the saved connection without its credentials. Exit code 3 for an " +
			"unknown platform or malformed input, 4 when the platform rejects the credentials.",
		Example: `  printf '%s' '{"api_key":"..."}' | monoagentcli connect save linear --method apikey --stdin-json`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !stdinJSON {
				return d.InvalidInput("pass --stdin-json and the field values as a JSON object on stdin")
			}
			if _, ok := connections.Get(args[0]); !ok {
				return d.InvalidInput("unknown platform %q", args[0])
			}
			if method == "" {
				return d.InvalidInput("--method is required")
			}
			secrets.MarkStdinConsumed()
			var fields map[string]interface{}
			if err := json.NewDecoder(cmd.InOrStdin()).Decode(&fields); err != nil {
				// The decoder's message can quote the payload; keep it out.
				return d.InvalidInput("stdin must be a JSON object of field values")
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			mgr, err := connections.NewManager(db.DB)
			if err != nil {
				return err
			}
			pid, perr := d.requireProfile()
			if perr != nil {
				return perr
			}
			conn, err := mgr.SaveFields(cmd.Context(), args[0], connections.AuthMethod(method), fields, pid)
			if err != nil {
				return d.AuthFailed("%s", d.ScrubSecrets(err, d.ConnectionSecrets(&connections.Connection{Data: fields})...))
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), conn.Redact())
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Saved as %s (id: %s)\n", conn.Label, conn.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&method, "method", "", "Auth method (apikey, apppassword, connstring, sshkey, ...)")
	cmd.Flags().BoolVar(&stdinJSON, "stdin-json", false, "Read the field values as a JSON object from stdin")
	return cmd
}

// oauthClientView is `connect get-oauth-client --json`. client_secret is
// present only with --reveal.
type oauthClientView struct {
	Platform        string  `json:"platform"`
	ProfileID       string  `json:"profile_id"`
	ClientID        string  `json:"client_id"`
	HasClientSecret bool    `json:"has_client_secret"`
	ClientSecret    *string `json:"client_secret,omitempty"`
}

// newConnectGetOAuthClientCmd shows the OAuth app credentials stored for a
// platform under the active profile. Like `secret reveal`, the client
// secret is printed only when --reveal is passed.
func (d Deps) newConnectGetOAuthClientCmd() *cobra.Command {
	var reveal bool

	cmd := &cobra.Command{
		Use:   "get-oauth-client <platform>",
		Short: "Show a platform's stored OAuth client ID (and, with --reveal, its secret)",
		Long: "Prints the OAuth client_id stored for a platform under the profile named by --profile (see " +
			"set-oauth-client). The client_secret is printed only with --reveal. Exit code 2 when " +
			"nothing is stored.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if reveal {
				if err := revealgate.Refuse("connect get-oauth-client --reveal"); err != nil {
					return err
				}
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			profileID, perr := d.requireProfile()
			if perr != nil {
				return perr
			}
			clientID, clientSecret := connections.NewStore(db.DB).GetOAuthClient(cmd.Context(), args[0], profileID)
			if clientID == "" && clientSecret == "" {
				return d.NotFound("no OAuth client stored for %s (profile: %s)", args[0], profileID)
			}
			view := oauthClientView{Platform: args[0], ProfileID: profileID, ClientID: clientID, HasClientSecret: clientSecret != ""}
			if reveal {
				view.ClientSecret = &clientSecret
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), view)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "client_id: %s\n", clientID)
			switch {
			case reveal:
				fmt.Fprintf(cmd.OutOrStdout(), "client_secret: %s\n", clientSecret)
			case clientSecret != "":
				fmt.Fprintln(cmd.OutOrStdout(), "client_secret: (set; pass --reveal to print it)")
			default:
				fmt.Fprintln(cmd.OutOrStdout(), "client_secret: (none)")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&reveal, "reveal", false, "Also print the client secret (the user's own: an agent, the chat or a workflow is refused)")
	return cmd
}

// credentialOption is one `connect for-node --json` row.
type credentialOption struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Platform string `json:"platform"`
	Method   string `json:"method"`
}

// socialNodePlatforms are the browser-session platforms a node type names
// as one of its dot-separated segments (e.g. action.instagram.publish_post).
var socialNodePlatforms = []string{"instagram", "linkedin", "tiktok", "x", "twitter", "hackernews", "producthunt"}

// serviceNodePlatforms are the API platforms recognised anywhere in a node
// type (e.g. service.google_sheets); a node type maps to itself.
var serviceNodePlatforms = []string{
	"airtable", "asana", "bluesky", "devto", "discord", "github", "gmail", "google_drive",
	"google_sheets", "hashnode", "hubspot", "huggingface", "jira", "linear", "mastodon",
	"notion", "outlook", "producthunt", "reddit", "salesforce", "shopify",
	"slack", "stripe", "telegram", "twilio", "whatsapp", "youtube",
}

// nodeCredentialPlatform is the connection platform a workflow node type
// uses, or "" when it names none (every connection is then a candidate).
func nodeCredentialPlatform(nodeType string) string {
	lower := strings.ToLower(nodeType)
	segments := strings.Split(lower, ".")
	for _, sp := range socialNodePlatforms {
		for _, seg := range segments {
			if seg == sp {
				return sp
			}
		}
	}
	// Longest match first, so a name that contains another wins.
	services := append([]string(nil), serviceNodePlatforms...)
	sort.Slice(services, func(i, j int) bool { return len(services[i]) > len(services[j]) })
	for _, sp := range services {
		if strings.Contains(lower, sp) {
			return sp
		}
	}
	return ""
}

// newConnectForNodeCmd lists the active profile's connections a workflow
// node type can use — the node editor's credential picker.
func (d Deps) newConnectForNodeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "for-node <node-type>",
		Short: "List saved connections usable by a workflow node type",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			mgr, err := connections.NewManager(db.DB)
			if err != nil {
				return err
			}
			platform := nodeCredentialPlatform(args[0])
			pid, perr := d.requireProfile()
			if perr != nil {
				return perr
			}
			conns, err := mgr.ListUsable(cmd.Context(), platform, pid)
			if err != nil {
				return err
			}
			opts := make([]credentialOption, 0, len(conns))
			for _, c := range conns {
				opts = append(opts, credentialOption{ID: c.ID, Label: c.Label, Platform: c.Platform, Method: string(c.Method)})
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), opts)
			}
			if len(opts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No matching connections.")
				return nil
			}
			for _, o := range opts {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", o.ID, o.Platform, o.Label)
			}
			return nil
		},
	}
}

// oauthProgress is one `connect oauth --json` progress line on stderr.
type oauthProgress struct {
	Message string `json:"message"`
	Kind    string `json:"kind"`
}

// newConnectOAuthCmd runs a platform's browser OAuth flow without prompts,
// using the OAuth client stored for the active profile (or the
// MONOAGENT_<PLATFORM>_CLIENT_ID/SECRET env vars), and reuses the
// profile's existing OAuth connection for the platform when there is one.
// A platform with only OAuth 1.0a runs that flow instead, with the consumer
// key and secret from --field (asked on a terminal when missing).
// --stdin-json reads more field values as a JSON object on stdin, keeping
// secrets out of argv. Progress goes to stderr, one JSON object per line
// with --json, so the desktop app can stream it; the saved connection (no
// credentials) goes to stdout.
func (d Deps) newConnectOAuthCmd() *cobra.Command {
	var scopeGroups, fieldFlags []string
	var stdinJSON bool
	cmd := &cobra.Command{
		Use:   "oauth <platform>",
		Short: "Connect a platform through its browser OAuth (2.0 or 1.0a) flow, without prompts",
		Long: "Runs the platform's browser OAuth flow and saves the connection under the profile named by --profile.\n\n" +
			"--field key=value (repeatable) gives the values the flow needs before the browser opens: an " +
			"OAuth 2.0 platform's connect fields (a tenant subdomain, a Salesforce environment — a reconnect " +
			"keeps the earlier values of the ones not given), or an OAuth 1.0a platform's form (consumer_key, " +
			"consumer_secret and, for generic_oauth1, the request_token_url, authorization_url and " +
			"access_token_url). An unknown key is an error naming the allowed ones; `connect show <platform>` " +
			"lists them. On a terminal (without --json) the missing required OAuth 1.0a fields are asked, " +
			"secrets without echo — prefer that, or --stdin-json, to a --field consumer_secret, which other " +
			"local users can read in the process list. --stdin-json reads field values as one JSON object on " +
			"stdin; a --field of the same key wins.\n\nExit code 3 for an unknown platform or bad --field, 4 when the flow " +
			"or the validation fails.",
		Example: `  monoagentcli connect oauth salesforce --field environment=sandbox
  monoagentcli connect oauth trello            # asks for the consumer key and secret
  monoagentcli connect oauth generic_oauth1 --field request_token_url=https://api.example.com/oauth/request_token \
    --field authorization_url=https://api.example.com/oauth/authorize --field access_token_url=https://api.example.com/oauth/access_token`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, ok := connections.Get(args[0])
			if !ok {
				return d.InvalidInput("unknown platform %q", args[0])
			}
			oauth1 := p.OAuth == nil && p.OAuth1 != nil && hasMethod(p, connections.MethodOAuth1)
			if p.OAuth == nil && !oauth1 {
				return d.InvalidInput("platform %s does not support OAuth", args[0])
			}
			var allowed []connections.CredentialField
			if oauth1 {
				if len(scopeGroups) > 0 {
					return d.InvalidInput("--scopes is for the OAuth 2.0 umbrella platforms; %s uses OAuth 1.0a", args[0])
				}
				allowed = p.Fields[connections.MethodOAuth1]
			} else {
				allowed = p.OAuth.ConnectFields
			}
			flags := fieldFlags
			if stdinJSON {
				secrets.MarkStdinConsumed()
				var fromStdin map[string]string
				if err := json.NewDecoder(cmd.InOrStdin()).Decode(&fromStdin); err != nil {
					// The decoder's message can quote the payload; keep it out.
					return d.InvalidInput("stdin must be a JSON object of string field values")
				}
				flags = nil
				for k, v := range fromStdin {
					if strings.TrimSpace(k) == "" {
						return d.InvalidInput("stdin has a field with an empty key")
					}
					flags = append(flags, k+"="+v)
				}
				flags = append(flags, fieldFlags...)
			}
			values, err := d.parseOAuthFieldFlags(args[0], flags, allowed)
			if err != nil {
				return err
			}
			if oauth1 {
				if !stdinJSON && !d.JSONOutput() && stdinIsTerminal() {
					secrets.MarkStdinConsumed()
					if err := connections.PromptMissingFields(allowed, values, cmd.InOrStdin()); err != nil {
						return d.InvalidInput("%v", err)
					}
				}
				if missing := missingRequired(allowed, values); len(missing) > 0 {
					return d.InvalidInput("%s's OAuth 1.0a needs --field %s=… (or run on a terminal to be asked)", args[0], strings.Join(missing, "=… --field "))
				}
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			mgr, err := connections.NewManager(db.DB)
			if err != nil {
				return err
			}
			profileID, perr := d.requireProfile()
			if perr != nil {
				return perr
			}
			errOut := cmd.ErrOrStderr()
			progress := func(msg, kind string) {
				if d.JSONOutput() {
					b, _ := json.Marshal(oauthProgress{Message: msg, Kind: kind})
					fmt.Fprintln(errOut, string(b))
					return
				}
				fmt.Fprintln(errOut, msg)
			}
			var conn *connections.Connection
			if oauth1 {
				conn, err = mgr.ConnectOAuth1(cmd.Context(), args[0], values, progress, profileID)
				if err != nil {
					return d.AuthFailed("%s", d.ScrubSecrets(err, secretValues(allowed, values)...))
				}
			} else {
				conn, err = mgr.ConnectOAuthWithFields(cmd.Context(), args[0], progress, "", "", profileID, scopeGroups, values)
				if err != nil {
					_, clientSecret := connections.NewStore(db.DB).GetOAuthClient(cmd.Context(), args[0], profileID)
					return d.AuthFailed("%s", d.ScrubSecrets(err, append(secretValues(allowed, values), clientSecret)...))
				}
			}
			d.warnIfAccountProfileMismatch(db.DB, conn)
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), conn.Redact())
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Saved as %s (id: %s)\n", conn.Label, conn.ID)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&scopeGroups, "scopes", nil,
		"Scope groups to grant an umbrella platform, comma-separated (default: every group not marked opt-in; \"all\" adds the opt-in ones too). "+connections.ScopeGroupsHelp())
	cmd.Flags().StringArrayVar(&fieldFlags, "field", nil,
		"A value the flow needs, as key=value (repeatable): an OAuth 2.0 connect field (tenant subdomain, environment) or an OAuth 1.0a form field (consumer_key, consumer_secret, endpoint URLs)")
	cmd.Flags().BoolVar(&stdinJSON, "stdin-json", false, "Also read field values as a JSON object from stdin (keeps secrets out of the process list)")
	return cmd
}
