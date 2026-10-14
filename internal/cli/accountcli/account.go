package accountcli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// NewCommand is `account`: the monoes.me sign-in of this machine. One session
// per OS user is shared by every profile; `library login`, `logout` and `status`
// are aliases of the commands here.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Sign in to monoes.me: the one account MonoAgent uses on this machine",
		Long: "MonoAgent signs in to monoes.me once per machine. The session is signed by monoes.me, " +
			"checked here without a network call, renewed in the background, and shared by every profile.\n\n" +
			"  monoagentcli account login                     # log in (opens the browser)\n" +
			"  monoagentcli account login --email you@x.com   # a code by email, for a machine without a browser\n" +
			"  monoagentcli account status [--offline]        # who is logged in, and until when\n" +
			"  monoagentcli account logout                    # revoke and forget the session\n\n" +
			"`status` exits 0 while the session is good (including offline grace) and 4 when it is not.",
	}
	for _, c := range []*cobra.Command{d.newAccountLoginCmd(), d.newAccountLogoutCmd(), d.newAccountStatusCmd()} {
		d.WithJSONErrors(c)
		cmd.AddCommand(c)
	}
	return cmd
}

func (d Deps) newAccountLoginCmd() *cobra.Command {
	var signIn func(cmd *cobra.Command) (account.Status, bool, error)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to monoes.me (opens the browser; --email for a code by email)",
		Long: "Logs this machine in to monoes.me.\n\n" +
			"By default this opens the browser at monoes.me's sign-in page and waits (up to --timeout) for it " +
			"to redirect back to a one-time listener on 127.0.0.1 (OAuth 2.1 with PKCE). --no-browser only " +
			"prints the URL to open. New accounts are made on that page.\n\n" +
			"On a machine without a browser, use a code sent by email:\n" +
			"  monoagentcli account login --email you@example.com            # sends a code, then asks for it\n" +
			"  monoagentcli account login --email you@example.com --send     # only send the code\n" +
			"  monoagentcli account login --email you@example.com --code 123456\n\n" +
			"The session is kept in ~/.monoagent/account, its refresh token sealed by the system key store.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, sent, err := signIn(cmd)
			if err != nil || sent {
				return err
			}
			return d.printAccountStatus(cmd, st)
		},
	}
	signIn = d.AddSignIn(cmd, "account login")
	return cmd
}

func (d Deps) newAccountLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Log out of monoes.me on this machine (revokes and forgets the session)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := account.Logout(cmd.Context()); err != nil {
				return err
			}
			return d.print(cmd, map[string]any{"logged_out": true, "base_url": account.Host()}, func(w io.Writer) {
				fmt.Fprintln(w, "Logged out of monoes.me.")
			})
		},
	}
}

func (d Deps) newAccountStatusCmd() *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the monoes.me session: who is logged in, and until when",
		Long: "Shows the session's state: ok, grace (the login works until the time shown: monoes.me is unreachable, or " +
			"this machine could not confirm a renewal and must log in again) or locked, with the reason. It asks monoes.me to renew the session first when that is due, unless " +
			"--offline. With --json it prints the full status document. Exit 0 for ok and grace, 4 for locked.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := account.NewDefaultGuard()
			if err != nil {
				return err
			}
			defer g.Close()
			st := g.Status()
			if !offline && st.Reason != account.ReasonNotLoggedIn { // no session: nothing to renew
				if renewed, _ := g.Refresh(cmd.Context()); renewed.V != 0 {
					st = renewed
				}
			}
			if st.State != account.StateLocked {
				return d.printAccountStatus(cmd, st)
			}
			if d.JSONOutput() {
				if err := clicore.WriteJSONTo(cmd.OutOrStdout(), st); err != nil {
					return err
				}
			}
			return clicore.Reported{Err: clicore.AuthConnection("%s", DescribeStatus(st))}
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "Report the saved session without asking monoes.me to renew it")
	return cmd
}

func (d Deps) printAccountStatus(cmd *cobra.Command, st account.Status) error {
	return d.print(cmd, st, func(w io.Writer) { fmt.Fprintln(w, DescribeStatus(st)) })
}

// DescribeStatus is the sentence for a status: who is logged in, or what to do. A
// locked session is described in B1a's frozen words (index §3.4): what the refusal of
// a gated command says after its first line.
func DescribeStatus(st account.Status) string {
	as := ""
	if u := st.User; u != nil && (u.Username != "" || u.Email != "") {
		as = " as " + nonEmptyStr(u.Username, u.Email)
		if u.Username != "" && u.Email != "" {
			as += " (" + u.Email + ")"
		}
	}
	switch st.State {
	case account.StateOK:
		return "Logged in to monoes.me" + as + "."
	case account.StateGrace:
		if st.Reason == account.ReasonUnconfirmed { // monoes.me is not the problem: this machine gave up its saved login (A24)
			return fmt.Sprintf("Logged in to monoes.me%s until %s, but this machine can no longer renew the login: monoes.me may have received a refresh whose answer never arrived, so this machine stopped using its saved login to protect your other installs. Sign in again on this machine: monoagentcli account login",
				as, st.GraceUntil.Local().Format(time.RFC3339))
		}
		return fmt.Sprintf("Logged in to monoes.me%s, but monoes.me could not be reached (%s). This login works offline until %s.",
			as, st.Reason, st.GraceUntil.Local().Format(time.RFC3339))
	}
	if _, line, ok := strings.Cut((&account.LoginRequiredError{Status: st}).Error(), "\n"); ok {
		return line
	}
	return "Not logged in to monoes.me. Run: monoagentcli account login"
}
