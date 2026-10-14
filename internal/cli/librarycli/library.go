package librarycli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/storage"
)

// NewCommand is `library`: the monoes.me library of workflows, orgs, web
// automations and API packages — log in, browse, install and publish. The desktop app
// shells out to these commands (spec: monoes.me Library ↔ MonoAgent v1).
func NewCommand(deps Deps) *cobra.Command {
	env := &libEnv{deps: deps}
	cmd := &cobra.Command{
		Use:   "library",
		Short: "Browse, install and publish workflows, orgs, web automations and API packages on monoes.me",
		Long: "The monoes.me library holds workflows, orgs, web automations and API packages (app.* nodes): official ones " +
			"published by monoes, public ones from the community, and your own private ones.\n\n" +
			"  monoagentcli library login                       # log in (opens the browser)\n" +
			"  monoagentcli library list --kind automation       # official + public automations\n" +
			"  monoagentcli library install automation instagram # download, verify, install\n" +
			"  monoagentcli library publish workflow <id>        # upload (private unless --public)\n" +
			"  monoagentcli library install api trello           # an API package: the app.trello node\n" +
			"  monoagentcli library update                       # newer versions of what you installed\n\n" +
			"The host is https://monoes.me unless MONOES_BASE_URL says otherwise. The login is the machine-wide " +
			"monoes.me session (`monoagentcli account status`), shared by every profile.",
		PersistentPostRun: func(cmd *cobra.Command, args []string) { env.Close() },
	}
	for _, c := range []*cobra.Command{
		newLibraryLoginCmd(env), newLibraryLogoutCmd(env), newLibraryStatusCmd(env),
		newLibraryListCmd(env), newLibraryShowCmd(env), newLibraryInstalledCmd(env),
		newLibraryInstallCmd(env), newLibraryPublishCmd(env), newLibraryUpdateCmd(env),
	} {
		deps.WithJSONErrors(c)
		cmd.AddCommand(c)
	}
	return cmd
}

// libEnv opens the database, the profile's library client and the
// automation registry lazily, once per command.
type libEnv struct {
	deps    Deps
	db      *storage.Database
	client  *library.Client
	reg     *automation.Registry
	session *library.AccountSession // the machine session every library call reads with
	guard   *account.Guard
}

func (e *libEnv) Close() {
	if e.db != nil {
		e.db.Close()
		e.db = nil
	}
	if e.guard != nil {
		e.guard.Close()
		e.guard = nil
	}
}

func (e *libEnv) open() (*library.Client, error) {
	if e.client != nil {
		return e.client, nil
	}
	db, err := e.deps.OpenDB()
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	e.db = db
	base := library.BaseURL()
	c, err := library.NewClient(base, &library.VaultStore{DB: db.DB, ProfileID: e.deps.ProfileID(), BaseURL: base})
	if err != nil {
		return nil, clicore.InvalidInput("%v", err)
	}
	// The machine session serves every library call. The profile's own vault login is
	// only what a release before the session left behind (spec D21, D22).
	if store, err := account.DefaultStore(); err == nil {
		if g, err := account.NewDefaultGuard(); err == nil {
			e.guard = g
			e.session = &library.AccountSession{Guard: g, Store: store}
			c.Session = e.session
		}
	}
	e.client = c
	return c, nil
}

func (e *libEnv) registry() (*automation.Registry, error) {
	if e.reg != nil {
		return e.reg, nil
	}
	reg, err := e.deps.OpenRegistry()
	if err != nil {
		return nil, err
	}
	e.reg = reg
	return reg, nil
}

func (e *libEnv) provenance() (*library.Provenance, error) {
	reg, err := e.registry()
	if err != nil {
		return nil, err
	}
	return library.OpenProvenance(reg.Home()), nil
}

// requireLogin opens the client and fails with "log in first" (exit 4)
// when the profile has no monoes.me login. Every library read needs one,
// official items included; this check sends nothing.
func (e *libEnv) requireLogin(ctx context.Context) (*library.Client, error) {
	c, err := e.open()
	if err != nil {
		return nil, err
	}
	t, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, e.deps.Err(library.ErrNotLoggedIn)
	}
	return c, nil
}

// Err gives a library failure the CLI's exit code: 2 not found, 3
// rejected input, 4 login/connection. No login, or a 401 the token
// refresh did not cure, says "Log in to monoes.me first".
func (d Deps) Err(err error) error {
	if err == nil {
		return nil
	}
	var ce *clicore.Error
	if errors.As(err, &ce) {
		return err
	}
	var ae *library.APIError
	switch {
	case errors.Is(err, library.ErrNotLoggedIn):
		return d.LoginRequired()
	case errors.As(err, &ae):
		switch ae.Status {
		case http.StatusNotFound:
			return &clicore.Error{Code: 2, Msg: err.Error()}
		case http.StatusBadRequest, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
			return &clicore.Error{Code: 3, Msg: err.Error()}
		case http.StatusForbidden, http.StatusTooManyRequests:
			return &clicore.Error{Code: 4, Msg: err.Error()}
		}
		if ae.Status >= 500 {
			return &clicore.Error{Code: 4, Msg: err.Error()}
		}
	case errors.Is(err, library.ErrSHA256Mismatch), errors.Is(err, automation.ErrSHA256Mismatch):
		return &clicore.Error{Code: 3, Msg: err.Error() + " — nothing was installed"}
	case strings.Contains(err.Error(), "monoes.me unreachable"):
		return &clicore.Error{Code: 4, Msg: err.Error()}
	}
	return err
}

// print writes v as indented JSON with --json, else calls human.
func (e *libEnv) print(cmd *cobra.Command, v any, human func(w io.Writer)) error {
	if e.deps.JSONOutput() {
		return clicore.WriteJSONTo(cmd.OutOrStdout(), v)
	}
	human(cmd.OutOrStdout())
	return nil
}

// Status is `library status` (and the result of a login).
type Status struct {
	BaseURL   string        `json:"base_url"`
	Profile   string        `json:"profile"`
	LoggedIn  bool          `json:"logged_in"`
	User      *library.User `json:"user"`
	Scopes    []string      `json:"scopes"`
	ExpiresAt string        `json:"expires_at,omitempty"`
	Method    string        `json:"method,omitempty"`
	Error     string        `json:"error,omitempty"`
}

func statusFrom(e *libEnv, t *library.Token) Status {
	s := Status{BaseURL: e.client.BaseURL, Profile: e.deps.ProfileID(), Scopes: []string{}}
	if t == nil {
		return s
	}
	s.LoggedIn, s.User, s.Method = true, t.User, t.Method
	if t.Scope != "" {
		s.Scopes = strings.Fields(t.Scope)
	}
	if !t.ExpiresAt.IsZero() {
		s.ExpiresAt = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return s
}

// statusFromAccount is the library's view of the machine session st: what
// `library login` prints.
func statusFromAccount(e *libEnv, st account.Status) Status {
	s := Status{BaseURL: e.client.BaseURL, Profile: e.deps.ProfileID(), Scopes: []string{}, LoggedIn: true, Method: "session"}
	if u := st.User; u != nil {
		s.User = &library.User{ID: u.ID, Email: u.Email, Username: u.Username}
	}
	if !st.ValidUntil.IsZero() {
		s.ExpiresAt = st.ValidUntil.UTC().Format(time.RFC3339)
	}
	return s
}

func (e *libEnv) printStatus(cmd *cobra.Command, s Status) error {
	return e.print(cmd, s, func(w io.Writer) {
		switch {
		case !s.LoggedIn:
			fmt.Fprintf(w, "Not logged in to %s. Run `monoagentcli library login` to browse and install from the library.\n", s.BaseURL)
		case s.User != nil:
			fmt.Fprintf(w, "Logged in to %s as %s (%s)\n", s.BaseURL, nonEmptyStr(s.User.Username, s.User.Name), s.User.Email)
		default:
			fmt.Fprintf(w, "Logged in to %s\n", s.BaseURL)
		}
		if s.Error != "" {
			fmt.Fprintf(w, "Note: %s\n", s.Error)
		}
	})
}

func nonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func newLibraryStatusCmd(e *libEnv) *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether this profile is logged in to monoes.me, and as whom",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := e.open()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if e.session != nil {
				e.session.Offline = offline // --offline never asks monoes.me to renew the session
			}
			t, err := c.Token(ctx)
			if err != nil {
				return err
			}
			s := statusFrom(e, t)
			if t == nil || offline {
				return e.printStatus(cmd, s)
			}
			me, err := c.Me(ctx)
			var ae *library.APIError
			switch {
			case err == nil:
				t2, _ := c.Token(ctx) // refreshed while asking, perhaps
				s = statusFrom(e, t2)
				s.User = &me.User
				if len(me.Scopes) > 0 {
					s.Scopes = me.Scopes
				}
			case errors.As(err, &ae) && ae.Status == http.StatusUnauthorized:
				s = statusFrom(e, nil)
				s.Error = "the saved login has expired; run `monoagentcli library login`"
			default:
				s.Error = err.Error() // offline: report the saved login
			}
			return e.printStatus(cmd, s)
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "Report the saved login without asking monoes.me")
	return cmd
}

func newLibraryLoginCmd(e *libEnv) *cobra.Command {
	var signIn func(cmd *cobra.Command) (account.Status, bool, error)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to monoes.me (an alias of `account login`; --email for a code by email)",
		Long: "An alias of `monoagentcli account login`: one monoes.me login per machine serves the whole CLI, " +
			"the library included, whatever the profile.\n\n" +
			"By default this opens the browser at monoes.me's sign-in page and waits (up to --timeout) for it " +
			"to redirect back to a one-time listener on 127.0.0.1 (OAuth 2.1 with PKCE). --no-browser only " +
			"prints the URL to open.\n\n" +
			"On a machine without a browser, use a code sent by email:\n" +
			"  monoagentcli library login --email you@example.com            # sends a code, then asks for it\n" +
			"  monoagentcli library login --email you@example.com --send     # only send the code\n" +
			"  monoagentcli library login --email you@example.com --code 123456",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if base := library.BaseURL(); base != account.Host() {
				return clicore.InvalidInput("MONOES_BASE_URL is %s but this build logs in to %s; logging in to another monoes.me needs a build made with -tags devaccount", base, account.Host())
			}
			if _, err := e.open(); err != nil {
				return err
			}
			st, sent, err := signIn(cmd)
			if err != nil || sent {
				return err
			}
			return e.printStatus(cmd, statusFromAccount(e, st))
		},
	}
	signIn = e.deps.AddSignIn(cmd, "library login")
	return cmd
}

func newLibraryLogoutCmd(e *libEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Log out of monoes.me (an alias of `account logout`; also forgets this profile's older login)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := e.open()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if c.BaseURL == account.Host() { // MONOES_BASE_URL elsewhere: the machine session is not that host's
				if err := account.Logout(ctx); err != nil {
					return err
				}
			}
			if err := c.LogoutLegacy(ctx); err != nil {
				return err
			}
			return e.print(cmd, map[string]any{"logged_out": true, "base_url": c.BaseURL}, func(w io.Writer) {
				fmt.Fprintf(w, "Logged out of %s.\n", c.BaseURL)
			})
		},
	}
}
