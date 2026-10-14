package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/library"
)

func TestLoginRequiredErrorCarriesTheAccountState(t *testing.T) {
	err := newLoginRequiredError(account.Status{V: 1, State: account.StateLocked, Reason: account.ReasonRefused})
	if exitCodeFor(err) != 4 || !isLoginRequired(err) ||
		!strings.HasPrefix(err.Error(), "Log in to monoes.me first: monoagentcli account login") {
		t.Fatalf("err = %v (exit %d)", err, exitCodeFor(err))
	}
	// The document `main` prints for --json: the error, its class and the account state.
	var out bytes.Buffer
	reportCommandError([]string{"--json", "status"}, err, &out, &bytes.Buffer{})
	var doc struct {
		Error         string
		Code          string
		LoginRequired bool `json:"login_required"`
		Account       struct{ State, Reason string }
	}
	if json.Unmarshal(out.Bytes(), &doc) != nil || doc.Code != "auth_or_connection" || !doc.LoginRequired ||
		doc.Account.State != "locked" || doc.Account.Reason != "refused" || !strings.HasPrefix(doc.Error, "Log in to monoes.me first") {
		t.Fatalf("document = %s", out.String())
	}
}

// Every way the library and the account commands say "log in first" is exit 4 and
// isLoginRequired. A bare *account.LoginRequiredError, which is what a gate or a
// runner returns, is recognized too; giving it an exit code is B2's.
func TestEveryLoginRequiredShapeExitsFour(t *testing.T) {
	for name, err := range map[string]error{
		"the library's own":      libraryLoginRequired(),
		"library.ErrNotLoggedIn": libraryDeps(&globalConfig{}).Err(library.ErrNotLoggedIn),
		"an account refusal":     newLoginRequiredError(account.Status{V: 1, State: account.StateLocked, Reason: account.ReasonNotLoggedIn}),
	} {
		if exitCodeFor(err) != 4 || !isLoginRequired(err) {
			t.Errorf("%s: exit %d, isLoginRequired %v", name, exitCodeFor(err), isLoginRequired(err))
		}
	}
	bare := &account.LoginRequiredError{Status: account.Status{State: account.StateLocked}}
	if !isLoginRequired(bare) || !isLoginRequired(errors.Join(errors.New("context"), bare)) {
		t.Error("a bare account.LoginRequiredError is not recognized")
	}
	if err := libraryLoginRequired(); err.Error() != "Log in to monoes.me first: monoagentcli library login" {
		t.Errorf("the library's message changed: %q", err)
	}
	if isLoginRequired(errors.New("boom")) {
		t.Error("an unrelated error counts as login required")
	}
}
