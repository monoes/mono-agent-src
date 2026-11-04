package accountcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// SignIn is the sign-in of `account login` and of its alias `library login`: what it takes
// from the CLI around it.
type SignIn struct {
	// JSONOutput reports the global --json flag; it is read when the sign-in runs.
	JSONOutput func() bool
	// OpenLoginURL opens the authorize URL in the default browser.
	OpenLoginURL func(u string) error
}

// Add adds the sign-in flags to cmd and returns the sign-in they ask for. name is the
// command, for the hints.
func (s SignIn) Add(cmd *cobra.Command, name string) func(*cobra.Command) (account.Status, bool, error) {
	var f signInFlags
	f.add(cmd)
	return func(cmd *cobra.Command) (account.Status, bool, error) { return s.signIn(cmd, &f, name) }
}

// signInFlags are the flags of `account login` and of its alias `library login`.
type signInFlags struct {
	email, code     string
	send, noBrowser bool
	codeStdin       bool
	timeout         time.Duration
}

func (f *signInFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.email, "email", "", "Log in with a code sent to this address instead of the browser")
	cmd.Flags().BoolVar(&f.send, "send", false, "With --email: only send the code (then run again with --code)")
	cmd.Flags().StringVar(&f.code, "code", "", "With --email: the code from the email (visible in the process list; prefer --code-stdin)")
	cmd.Flags().BoolVar(&f.codeStdin, "code-stdin", false, "With --email: read the code from stdin, so it never appears in the process list")
	cmd.Flags().BoolVar(&f.noBrowser, "no-browser", false, "Print the sign-in URL instead of opening the browser")
	cmd.Flags().DurationVar(&f.timeout, "timeout", 5*time.Minute, "How long to wait for the browser sign-in")
}

// signIn runs the sign-in the flags ask for and returns the session's status. sent
// is true when it only emailed the code. name is the command, for the hints.
func (s SignIn) signIn(cmd *cobra.Command, f *signInFlags, name string) (st account.Status, sent bool, err error) {
	if f.email == "" && (f.send || f.code != "" || f.codeStdin) {
		return st, false, clicore.InvalidInput("--send, --code and --code-stdin need --email")
	}
	if f.email != "" {
		return s.emailSignIn(cmd, f, name)
	}
	stderr := cmd.ErrOrStderr()
	opts := account.LoginOptions{Timeout: f.timeout, OnURL: func(u string) {
		if s.JSONOutput() {
			b, _ := json.Marshal(map[string]string{"kind": "url", "url": u})
			fmt.Fprintln(stderr, string(b))
			return
		}
		if f.noBrowser {
			fmt.Fprintf(stderr, "Open this URL to log in to monoes.me:\n\n  %s\n\nWaiting for the sign-in to finish…\n", u)
		}
	}}
	if !f.noBrowser {
		opts.Open = func(u string) error {
			err := s.OpenLoginURL(u)
			// A refused open is not an open: it says nothing of opening and the sign-in ends at once.
			if errors.Is(err, account.ErrBrowserRefused) || s.JSONOutput() {
				return err
			}
			fmt.Fprintf(stderr, "Opening your browser to log in to monoes.me…\nIf it does not open, visit:\n\n  %s\n\n", u)
			return err
		}
	}
	st, err = account.Login(cmd.Context(), opts)
	return st, false, signInErr(err)
}

func (s SignIn) emailSignIn(cmd *cobra.Command, f *signInFlags, name string) (st account.Status, sent bool, err error) {
	ctx := cmd.Context()
	code := f.code
	if f.codeStdin {
		if f.code != "" || f.send {
			return st, false, clicore.InvalidInput("--code-stdin cannot be combined with --code or --send")
		}
		line, err := readLine(cmd.InOrStdin())
		if err != nil {
			return st, false, fmt.Errorf("read the code from stdin: %w", err)
		}
		if code = line; code == "" {
			return st, false, clicore.InvalidInput("no code given on stdin")
		}
	}
	if code == "" {
		if err := account.SendEmailCode(ctx, f.email); err != nil {
			return st, false, signInErr(err)
		}
		if f.send || s.JSONOutput() {
			err := s.print(cmd, map[string]any{"code_sent": true, "email": f.email}, func(w io.Writer) {
				fmt.Fprintf(w, "If %s has a monoes.me account, a code is on its way. Then run:\n  monoagentcli %s --email %s --code <code>\n", f.email, name, f.email)
			})
			return st, true, err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "If %s has a monoes.me account, a code is on its way.\nCode: ", f.email)
		line, err := readLine(cmd.InOrStdin())
		if err != nil {
			return st, false, fmt.Errorf("read the code: %w", err)
		}
		code = line
	}
	if code = strings.TrimSpace(code); code == "" {
		return st, false, clicore.InvalidInput("no code given")
	}
	st, err = account.VerifyEmailCode(ctx, f.email, code)
	if errors.Is(err, account.ErrBadCode) {
		return st, false, clicore.AuthConnection("the code is wrong or expired; ask for a new one with `monoagentcli %s --email %s --send`", name, f.email)
	}
	return st, false, signInErr(err)
}

// signInErr gives a sign-in failure the CLI's exit code: 4 for anything between
// the user and monoes.me (no answer, a refusal, a wrong code), and the error as it
// is for a local failure such as a missing key store.
func signInErr(err error) error {
	var ce *clicore.Error
	if err == nil || errors.As(err, &ce) || errors.Is(err, account.ErrKeyringUnavailable) {
		return err
	}
	return clicore.AuthConnection("%v", err)
}

// print writes v as indented JSON with --json, else calls human.
func (s SignIn) print(cmd *cobra.Command, v any, human func(w io.Writer)) error {
	if s.JSONOutput() {
		return clicore.WriteJSONTo(cmd.OutOrStdout(), v)
	}
	human(cmd.OutOrStdout())
	return nil
}

func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return strings.TrimSpace(b.String()), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			if err == io.EOF && b.Len() > 0 {
				return strings.TrimSpace(b.String()), nil
			}
			return "", err
		}
	}
}
