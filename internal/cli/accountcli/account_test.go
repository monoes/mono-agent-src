package accountcli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

// A24: after a refresh whose answer never arrived this machine has dropped its refresh token. The status
// does not call that an outage: it says what happened, until when the login still works, and the way out.
func TestDescribeStatusNamesARefreshWhoseAnswerNeverArrived(t *testing.T) {
	t.Parallel()
	until := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	grace := DescribeStatus(account.Status{State: account.StateGrace, Reason: account.ReasonUnconfirmed, GraceUntil: until, User: &account.User{Username: "ada"}})
	locked := DescribeStatus(account.Status{State: account.StateLocked, Reason: account.ReasonUnconfirmed})
	for name, got := range map[string]string{"grace": grace, "locked": locked} {
		if !strings.Contains(got, "answer never arrived") || !strings.Contains(got, "monoagentcli account login") || strings.Contains(got, "could not be reached") {
			t.Errorf("%s: %q", name, got)
		}
	}
	if !strings.Contains(grace, until.Local().Format(time.RFC3339)) || !strings.Contains(grace, " as ada") {
		t.Errorf("grace: %q must say who is logged in and until when", grace)
	}
}

// Index §3.4: the text of every locked reason is B1a's, frozen word for word. `account status` says what
// a refused command says after its first line; only a machine that is simply not logged in, which has
// no such line, gets a sentence of its own.
func TestDescribeStatusOfALockedSessionIsTheRefusalsReasonLine(t *testing.T) {
	t.Parallel()
	for _, r := range []account.Reason{account.ReasonExpired, account.ReasonRefused, account.ReasonClockRollback,
		account.ReasonClockSkew, account.ReasonKeyUnknown, account.ReasonInvalid, account.ReasonUnconfirmed} {
		st := account.Status{State: account.StateLocked, Reason: r}
		_, want, _ := strings.Cut((&account.LoginRequiredError{Status: st}).Error(), "\n")
		if got := DescribeStatus(st); want == "" || got != want {
			t.Errorf("%s: %q, want the refusal's reason line %q", r, got, want)
		}
	}
	st := account.Status{State: account.StateLocked, Reason: account.ReasonNotLoggedIn}
	if got := DescribeStatus(st); got != "Not logged in to monoes.me. Run: monoagentcli account login" {
		t.Errorf("not_logged_in: %q", got)
	}
}

func TestGroupHasEverySubcommand(t *testing.T) {
	t.Parallel()
	d := Deps{WithJSONErrors: func(*cobra.Command) {}, AddSignIn: func(*cobra.Command, string) func(*cobra.Command) (account.Status, bool, error) {
		return nil
	}}
	var got []string
	for _, c := range NewCommand(d).Commands() {
		got = append(got, c.Name())
	}
	if strings.Join(got, " ") != "login logout status" {
		t.Fatalf("subcommands = %v", got)
	}
}

// `account login` prints the session it signed in (JSON or text), or nothing when it only
// sent a code; its failure is the sign-in's.
func TestLoginPrintsWhatTheSignInReturns(t *testing.T) {
	t.Parallel()
	ok := account.Status{V: 1, State: account.StateOK, User: &account.User{Username: "ada", Email: "ada@example.com"}}
	for _, c := range []struct {
		json, sent bool
		err        error
		want       string
	}{
		{want: "Logged in to monoes.me as ada (ada@example.com).\n"},
		{json: true, want: `"state": "ok"`},
		{sent: true, want: ""},
		{err: errors.New("boom"), want: ""},
	} {
		var flags []string
		d := Deps{JSONOutput: func() bool { return c.json }, WithJSONErrors: func(*cobra.Command) {},
			AddSignIn: func(cmd *cobra.Command, name string) func(*cobra.Command) (account.Status, bool, error) {
				flags = append(flags, name)
				cmd.Flags().String("email", "", "")
				return func(*cobra.Command) (account.Status, bool, error) { return ok, c.sent, c.err }
			}}
		cmd := NewCommand(d)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"login"})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err := cmd.Execute()
		if err != c.err || (c.want == "" && out.Len() != 0) || !strings.Contains(out.String(), c.want) {
			t.Errorf("%+v: %v %q", c, err, out.String())
		}
		if strings.Join(flags, ",") != "account login" {
			t.Errorf("sign-in named %v", flags)
		}
	}
}
