//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/account/accounttest"
)

const (
	propID   = "0123456789abcdef0123456789abcdef"
	propHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestProposalArgs(t *testing.T) {
	a, err := proposalShowArgs(propID)
	eqArgs(t, a, err, []string{"proposal", "show", propID})
	a, err = proposalDeclineArgs(propID)
	eqArgs(t, a, err, []string{"proposal", "decline", propID})
	// The hash passed is the one the card displayed, verbatim.
	a, err = proposalConfirmArgs(propID, propHash)
	eqArgs(t, a, err, []string{"proposal", "confirm", propID, "--hash", propHash})

	for _, bad := range []string{"", "x", "--hash", propID + "0", strings.ToUpper(propID), propID[:31] + "g", propID + " --org x"} {
		_, err = proposalShowArgs(bad)
		wantErr(t, nil, err, "invalid proposal id")
		_, err = proposalConfirmArgs(bad, propHash)
		wantErr(t, nil, err, "invalid proposal id")
		_, err = proposalDeclineArgs(bad)
		wantErr(t, nil, err, "invalid proposal id")
	}
	for _, bad := range []string{"", "abc", propHash + "a", strings.ToUpper(propHash), "--approval", propHash[:63] + "z"} {
		_, err = proposalConfirmArgs(propID, bad)
		wantErr(t, nil, err, "invalid proposal hash")
	}
}

// The GUI never builds the grant or autonomy command: the bindings' source names neither builder.
func TestProposalBindingsBuildNoGrantOrAutonomyCommand(t *testing.T) {
	src, err := os.ReadFile("app_org_proposals.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"grantAddArgs", "autonomySetArgs", `"grant"`, `"autonomy"`, "specJSON", "Setenv", "cmd.Env", "MarkerEnv"} {
		if strings.Contains(string(src), banned) {
			t.Errorf("app_org_proposals.go mentions %q: the card only passes an id and a hash to org proposal show|confirm|decline", banned)
		}
	}
}

// Each binding shells out to the CLI with the proposal verbs and the id (and the displayed hash), nothing else.
func TestProposalBindingsShellOut(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	log := t.TempDir() + "/args.log"
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+log+`'
echo '{"v":1,"proposal":{"id":"x"}}'
`))
	a := newTestApp(t)
	a.selectForTest("work")

	for _, out := range []string{a.GetOrgProposal(propID), a.ConfirmOrgProposal(propID, propHash), a.DeclineOrgProposal(propID)} {
		if !strings.Contains(out, `"proposal"`) {
			t.Fatalf("CLI output not passed through: %s", out)
		}
	}
	calls := loggedArgs(t, log)
	if len(calls) != 3 {
		t.Fatalf("CLI calls = %v", calls)
	}
	for i, want := range []string{
		"proposal show " + propID,
		"proposal confirm " + propID + " --hash " + propHash,
		"proposal decline " + propID,
	} {
		if !strings.HasPrefix(calls[i], "--profile work --json org") || !strings.HasSuffix(calls[i], " "+want) {
			t.Errorf("call %d = %q, want ... %q", i, calls[i], want)
		}
		if strings.Contains(calls[i], "grant") || strings.Contains(calls[i], "autonomy") {
			t.Errorf("call %d names a grant/autonomy command: %q", i, calls[i])
		}
	}
	// A malformed id or hash never reaches the CLI.
	if out := a.ConfirmOrgProposal(propID, "not-a-hash"); !strings.Contains(out, "invalid proposal hash") {
		t.Fatalf("bad hash: %s", out)
	}
	if out := a.GetOrgProposal("../x"); !strings.Contains(out, "invalid proposal id") {
		t.Fatalf("bad id: %s", out)
	}
	if n := len(loggedArgs(t, log)); n != 3 {
		t.Fatalf("a refused call reached the CLI: %d calls", n)
	}
}

// Signed out, none of the three runs the CLI (TestEveryBindingRefusesSignedOut walks the same rule for every method).
func TestProposalBindingsSignedOutNeverShellOut(t *testing.T) {
	accounttest.Install(t, accounttest.LockedNoLogin)
	log := t.TempDir() + "/args.log"
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+log+`'
echo '{}'
`))
	a := newTestApp(t)
	a.selectForTest("work")
	for _, out := range []string{a.GetOrgProposal(propID), a.ConfirmOrgProposal(propID, propHash), a.DeclineOrgProposal(propID)} {
		if !strings.Contains(out, `"login_required":true`) && !strings.Contains(out, `"login_required": true`) {
			t.Fatalf("signed out answered %s", out)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("the CLI ran while signed out")
	}
}
