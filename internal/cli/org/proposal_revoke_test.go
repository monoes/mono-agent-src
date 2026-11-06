package org

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/orgsign/orgsigntest"
)

// Revoking a grant goes through the same proposal path as granting one (#1477): anyone may propose it, only the
// operator confirms it, and confirm runs the real `org grant remove`.

const revokeArgs = `{"role":"lead","automation":"summarize"}`

// granted is the fixture with the grant a revoke proposal is about.
func grantedFixture(t *testing.T) *orgCLIFixture {
	t.Helper()
	f := propFixture(t)
	f.mustRun(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize", "--approval", "required")
	f.mustRun(t, "grant", "add", "growth", "--role", "writer", "--automation", "summarize", "--approval", "required")
	return f
}

func TestAnAgentCanProposeARevokeAndNothingChanges(t *testing.T) {
	f := grantedFixture(t)
	before := f.orgFile(t)
	notOperatorContexts["agent"](t)
	p := f.propose(t, "growth", "grant.remove", revokeArgs, "--conversation", "conv-1")
	if p["status"] != "pending" || p["verb"] != "grant.remove" || p["org"] != "growth" {
		t.Fatalf("proposal %v", p)
	}
	if by, _ := p["created_by"].(string); !strings.HasPrefix(by, "agent:") {
		t.Errorf("created_by %q, want the agent that made it", by)
	}
	prev, _ := p["preview_text"].(string)
	for _, want := range []string{"Revoke", "lead", "summarize", "growth"} {
		if !strings.Contains(prev, want) {
			t.Errorf("preview %q lacks %q", prev, want)
		}
	}
	if len(f.grants(t)) != 2 || string(f.orgFile(t)) != string(before) {
		t.Error("creating a revoke proposal changed the org")
	}
}

func TestARevokeProposalNeedsAGrantToRevokeAndNothingElse(t *testing.T) {
	f := grantedFixture(t)
	for name, args := range map[string]string{
		"no grant for that automation": `{"role":"lead","automation":"digest"}`,
		"unknown role":                 `{"role":"nobody","automation":"summarize"}`,
		"unknown field":                `{"role":"lead","automation":"summarize","approval":"none"}`,
		"no automation":                `{"role":"lead"}`,
		"trailing data":                revokeArgs + `{}`,
	} {
		if _, err := f.run(t, "proposal", "create", "growth", "--verb", "grant.remove", "--args-json", args); err == nil {
			t.Errorf("%s: not refused", name)
		}
	}
	if got := f.mustRun(t, "proposal", "list"); len(got["proposals"].([]interface{})) != 0 {
		t.Fatalf("a refused create left a row: %v", got)
	}
	f.propose(t, "growth", "grant.remove", revokeArgs) // while the one with a grant is accepted
}

func TestRevokeConfirmAndDeclineAreRefusedToEverythingButTheOperator(t *testing.T) {
	f := grantedFixture(t)
	p := f.propose(t, "growth", "grant.remove", revokeArgs)
	for name, ctx := range notOperatorContexts {
		t.Run(name, func(t *testing.T) {
			ctx(t)
			for _, args := range [][]string{
				{"proposal", "confirm", p["id"].(string), "--hash", p["hash"].(string)},
				{"proposal", "decline", p["id"].(string)},
			} {
				if _, err := f.run(t, args...); err == nil || clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "is the user's to do") {
					t.Fatalf("%v: %v", args, err)
				}
			}
		})
	}
	asOrgOperator(t)
	if len(f.grants(t)) != 2 || f.proposalRow(t, p["id"].(string)) != "pending" {
		t.Fatal("a refused caller revoked or moved the proposal")
	}
}

func TestTheOperatorConfirmsARevokeAndItIsTheSameAsGrantRemove(t *testing.T) {
	f := grantedFixture(t)
	notOperatorContexts["agent"](t)
	p := f.propose(t, "growth", "grant.remove", revokeArgs)
	asOrgOperator(t)
	out := f.mustRun(t, "proposal", "confirm", p["id"].(string), "--hash", p["hash"].(string))
	if out["proposal"].(map[string]interface{})["status"] != "applied" || out["result"].(map[string]interface{})["removed"] != true {
		t.Fatalf("confirm printed %v", out)
	}
	gr := f.grants(t)
	if len(gr) != 1 || gr[0].(map[string]interface{})["role"] != "writer" {
		t.Fatalf("grants after the confirm: %v", gr)
	}
	direct := f.mustRun(t, "grant", "remove", "growth", "--role", "writer", "--automation", "summarize")
	if direct["removed"] != true || len(f.grants(t)) != 0 {
		t.Fatalf("grant remove by hand: %v", direct)
	}
}

func TestARevokeIsAppliedOnceAndAReplayIsRefused(t *testing.T) {
	f := grantedFixture(t)
	p := f.propose(t, "growth", "grant.remove", revokeArgs)
	if _, err := f.confirm(t, p); err != nil {
		t.Fatal(err)
	}
	// the grant is given again; a replay of the old id must not take it away a second time
	f.mustRun(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize", "--approval", "required")
	if _, err := f.confirm(t, p); err == nil || !strings.Contains(err.Error(), "proposal_already_decided") {
		t.Fatalf("replay: %v", err)
	}
	if len(f.grants(t)) != 2 {
		t.Fatalf("grants %v", f.grants(t))
	}
}

func TestTwoConfirmsOfARevokeAtOnceApplyOnce(t *testing.T) {
	f := grantedFixture(t)
	p := f.propose(t, "growth", "grant.remove", revokeArgs)
	var wg sync.WaitGroup
	var won, refused atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := newOrgCmd(f.cfg)
			cmd.SetArgs([]string{"proposal", "confirm", p["id"].(string), "--hash", p["hash"].(string)})
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			switch err := cmd.Execute(); {
			case err == nil:
				won.Add(1)
			case strings.Contains(err.Error(), "proposal_already_decided"):
				refused.Add(1)
			default:
				t.Errorf("confirm: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 || refused.Load() != 1 {
		t.Fatalf("%d confirms won, %d refused as already decided", won.Load(), refused.Load())
	}
	if gr := f.grants(t); len(gr) != 1 {
		t.Fatalf("grants %v", gr)
	}
}

func TestARevokeGoesStaleWhenTheGrantChangedOrIsGone(t *testing.T) {
	f := grantedFixture(t)
	changed := f.propose(t, "growth", "grant.remove", revokeArgs)
	f.mustRun(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize", "--mode", "status", "--approval", "required")
	if _, err := f.confirm(t, changed); err == nil || !strings.Contains(err.Error(), "proposal_stale") {
		t.Fatalf("after the grant was changed: %v", err)
	}
	gone := f.propose(t, "growth", "grant.remove", `{"role":"writer","automation":"summarize"}`)
	f.mustRun(t, "grant", "remove", "growth", "--role", "writer", "--automation", "summarize")
	if _, err := f.confirm(t, gone); err == nil || !strings.Contains(err.Error(), "proposal_stale") {
		t.Fatalf("after the grant was removed by hand: %v", err)
	}
	if gr := f.grants(t); len(gr) != 1 || gr[0].(map[string]interface{})["mode"] != "status" {
		t.Fatalf("a stale revoke changed the grants: %v", gr)
	}
}

// A row rewritten to revoke another role's grant (with its hash recomputed to match) is not applied.
func TestARevokeRowRewrittenToAnotherRoleIsNotApplied(t *testing.T) {
	f := grantedFixture(t)
	p := f.propose(t, "growth", "grant.remove", revokeArgs)
	f.sql(t, `UPDATE org_proposals SET args_json = ? WHERE id = ?`, `{"role":"writer","automation":"summarize"}`, p["id"].(string))
	if _, err := f.confirm(t, p); err == nil {
		t.Fatal("a rewritten revoke was applied")
	}
	if len(f.grants(t)) != 2 {
		t.Fatalf("grants %v", f.grants(t))
	}
}

func TestARevokeSupersedesTheOlderPendingOneForTheSameGrant(t *testing.T) {
	f := grantedFixture(t)
	first := f.propose(t, "growth", "grant.remove", revokeArgs, "--conversation", "c1")
	f.propose(t, "growth", "grant.remove", revokeArgs, "--conversation", "c1")
	if st := f.proposalRow(t, first["id"].(string)); st != "declined" {
		t.Fatalf("the older revoke is %s", st)
	}
}

func TestConfirmReSignsASignedOrgAfterARevoke(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.21.0")
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.plainWF, "--alias", "summarize")
	f.mustRun(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize", "--approval", "required")
	p := f.propose(t, "growth", "grant.remove", revokeArgs)
	orgsigntest.Sign(t, f.root, "growth")
	s.calls = nil
	captureStderr(t, func() { f.mustRun(t, "proposal", "confirm", p["id"].(string), "--hash", p["hash"].(string)) })
	if st, _, err := orgsign.VerifyFile(f.root, "growth"); err != nil || !st.OK() || len(s.calls) == 0 {
		t.Fatalf("after the operator's confirm: sign calls %d, state %+v, %v", len(s.calls), st, err)
	}
}
