package accountcli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// runSignIn runs a command with the sign-in flags and args, and returns what the sign-in returned.
// Only the refusals that never reach monoes.me are run here.
func runSignIn(t *testing.T, stdin string, args ...string) (sent bool, err error) {
	t.Helper()
	opened := false
	s := SignIn{JSONOutput: func() bool { return false }, OpenLoginURL: func(string) error { opened = true; return nil }}
	cmd := &cobra.Command{Use: "login", SilenceErrors: true, SilenceUsage: true}
	signIn := s.Add(cmd, "account login")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		_, sent, err = signIn(cmd)
		return err
	}
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	_ = cmd.Execute()
	if opened {
		t.Errorf("%v opened the browser", args)
	}
	return sent, err
}

func TestSignInFlags(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{Use: "login"}
	SignIn{}.Add(cmd, "account login")
	for _, name := range []string{"email", "send", "code", "code-stdin", "no-browser", "timeout"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("no --%s", name)
		}
	}
	if got := cmd.Flags().Lookup("timeout").DefValue; got != "5m0s" {
		t.Errorf("--timeout default %s", got)
	}
}

// Flag combinations that make no sense are refused with exit 3 before anything is sent.
func TestSignInRefusesBadFlagsWithExit3(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{"", []string{"--send"}, "--send, --code and --code-stdin need --email"},
		{"", []string{"--code", "1"}, "--send, --code and --code-stdin need --email"},
		{"", []string{"--code-stdin"}, "--send, --code and --code-stdin need --email"},
		{"", []string{"--email", "a@b.c", "--code-stdin", "--code", "1"}, "--code-stdin cannot be combined with --code or --send"},
		{"", []string{"--email", "a@b.c", "--code-stdin", "--send"}, "--code-stdin cannot be combined with --code or --send"},
		{"\n", []string{"--email", "a@b.c", "--code-stdin"}, "no code given on stdin"},
		{"", []string{"--email", "a@b.c", "--code", "   "}, "no code given"},
	} {
		sent, err := runSignIn(t, c.stdin, c.args...)
		if sent || err == nil || err.Error() != c.want || clicore.ExitCodeFor(err) != 3 {
			t.Errorf("%v: sent=%v err=%v (exit %d), want %q exit 3", c.args, sent, err, clicore.ExitCodeFor(err), c.want)
		}
	}
	// An empty stdin is a read failure, not a code.
	if _, err := runSignIn(t, "", "--email", "a@b.c", "--code-stdin"); err == nil || !strings.HasPrefix(err.Error(), "read the code from stdin: ") {
		t.Errorf("empty stdin: %v", err)
	}
}

// signInErr: exit 4 for anything between the user and monoes.me; a CLI error or a missing key
// store as it is.
func TestSignInErr(t *testing.T) {
	t.Parallel()
	if signInErr(nil) != nil {
		t.Error("nil")
	}
	invalid := clicore.InvalidInput("x")
	if signInErr(invalid) != invalid {
		t.Error("a CLI error is kept")
	}
	keyring := fmt.Errorf("seal: %w", account.ErrKeyringUnavailable)
	if !errors.Is(signInErr(keyring), account.ErrKeyringUnavailable) || clicore.ExitCodeFor(signInErr(keyring)) == 4 {
		t.Error("a missing key store is a local failure")
	}
	err := signInErr(errors.New("dial tcp: refused"))
	if clicore.ExitCodeFor(err) != 4 || err.Error() != "dial tcp: refused" {
		t.Errorf("network failure: %v (exit %d)", err, clicore.ExitCodeFor(err))
	}
}

func TestReadLine(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"123456\nrest": "123456", "  42  \n": "42", "7": "7"} {
		r := strings.NewReader(in)
		if got, err := readLine(r); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
		if in == "123456\nrest" {
			if rest, _ := io.ReadAll(r); string(rest) != "rest" {
				t.Errorf("read past the line: %q left", rest)
			}
		}
	}
	if _, err := readLine(&bytes.Buffer{}); err != io.EOF {
		t.Errorf("empty: %v", err)
	}
}

func TestSignInPrintsTheCodeSentInJSON(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	s := SignIn{JSONOutput: func() bool { return true }}
	if err := s.print(cmd, map[string]any{"code_sent": true}, func(io.Writer) { t.Error("human output with --json") }); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\n  \"code_sent\": true\n}\n" {
		t.Errorf("%q", out.String())
	}
}
