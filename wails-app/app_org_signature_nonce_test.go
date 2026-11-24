package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
)

// A terminal-less `org sign --yes` needs the one-time nonce, which the app issues for exactly the hash it signs.
func TestOrgSignYesArgsCarryTheNonceIssuedForThatDefinition(t *testing.T) {
	var gotPurpose, gotSubject string
	var gotInteractive bool
	issueOrgSignNonce = func(purpose, subject string, interactive bool) (string, error) {
		gotPurpose, gotSubject, gotInteractive = purpose, subject, interactive
		return "n1", nil
	}
	t.Cleanup(func() { issueOrgSignNonce = operator.IssueNonce })

	got := orgSignYesArgs("team", "abc")
	want := []string{"--yes", "--expect-hash", "abc", "--nonce", "n1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	if gotPurpose != operator.OrgSignNoncePurpose || gotSubject != "team:abc" || !gotInteractive {
		t.Errorf("issued for %q/%q interactive=%v", gotPurpose, gotSubject, gotInteractive)
	}
}

// When no nonce can be issued the CLI is still asked, so that it refuses with its own message.
func TestOrgSignYesArgsWithoutANonceStillAskTheCLI(t *testing.T) {
	issueOrgSignNonce = func(string, string, bool) (string, error) { return "", errors.New("not the operator") }
	t.Cleanup(func() { issueOrgSignNonce = operator.IssueNonce })
	got := orgSignYesArgs("team", "abc")
	if want := []string{"--yes", "--expect-hash", "abc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}
