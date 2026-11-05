package org

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/orgsign/orgsigntest"
)

// Confirming a proposal is the user's: a refused context changes nothing, and learns nothing about the row.

func (f *orgCLIFixture) confirm(t *testing.T, p map[string]interface{}) (map[string]interface{}, error) {
	t.Helper()
	return f.run(t, "proposal", "confirm", p["id"].(string), "--hash", p["hash"].(string))
}

func TestConfirmAndDeclineAreRefusedToEverythingButTheOperator(t *testing.T) {
	f := propFixture(t)
	p := f.propose(t, "growth", "grant.add", grantArgs)
	a := f.propose(t, "growth", "autonomy.set", `{"level":"mid"}`)
	before := f.orgFile(t)
	for name, ctx := range notOperatorContexts {
		t.Run(name, func(t *testing.T) {
			ctx(t)
			var msgs []string
			for _, args := range [][]string{
				{"proposal", "confirm", p["id"].(string), "--hash", p["hash"].(string)},
				{"proposal", "confirm", a["id"].(string), "--hash", a["hash"].(string)},
				{"proposal", "confirm", "00000000000000000000000000000000", "--hash", "x"}, // an id that does not exist: the same answer
				{"proposal", "decline", p["id"].(string)},
				{"proposal", "decline", "00000000000000000000000000000000"},
			} {
				_, err := f.run(t, args...)
				if err == nil || clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "is the user's to do") {
					t.Fatalf("%v: %v", args, err)
				}
				msgs = append(msgs, strings.ReplaceAll(strings.ReplaceAll(err.Error(), p["id"].(string), "ID"), a["id"].(string), "ID"))
			}
			if msgs[0] != msgs[2] || msgs[3] != msgs[4] || msgs[0] == "" {
				t.Errorf("a refused caller can tell a real id from an unknown one: %q", msgs)
			}
		})
	}
	asOrgOperator(t)
	if len(f.grants(t)) != 0 || string(f.orgFile(t)) != string(before) {
		t.Fatal("a refused confirm changed the org")
	}
	for _, id := range []string{p["id"].(string), a["id"].(string)} {
		if st := f.proposalRow(t, id); st != "pending" {
			t.Errorf("a refused caller moved %s to %s", id, st)
		}
	}
}

// Nothing stored in the row stands in for the gate: whoever made the row and whatever it says about itself,
// the confirming process is asked again.
func TestConfirmAsksTheGateAtConfirmTimeAndTrustsNothingInTheRow(t *testing.T) {
	f := propFixture(t)
	p := f.propose(t, "growth", "grant.add", grantArgs) // made by the operator
	f.sql(t, `UPDATE org_proposals SET created_by = 'operator', result_json = '{"approved":true}' WHERE id = ?`, p["id"].(string))
	notOperatorContexts["agent"](t)
	if _, err := f.confirm(t, p); err == nil || !strings.Contains(err.Error(), "is the user's to do") {
		t.Fatalf("confirm by an agent of a row that claims the operator made it: %v", err)
	}
	if len(f.grants(t)) != 0 {
		t.Fatal("granted")
	}
}

func TestTheOperatorConfirmsAGrantAndItIsTheSameGrantAsGrantAdd(t *testing.T) {
	f := propFixture(t)
	notOperatorContexts["agent"](t)
	p := f.propose(t, "growth", "grant.add", grantArgs, "--conversation", "c1")
	asOrgOperator(t)
	out := f.mustRun(t, "proposal", "confirm", p["id"].(string), "--hash", p["hash"].(string))
	if got := out["proposal"].(map[string]interface{})["status"]; got != "applied" {
		t.Fatalf("confirm printed %v", out)
	}
	gv := out["result"].(map[string]interface{})["grant"].(map[string]interface{})
	if gv["role"] != "lead" || gv["alias"] != "summarize" || gv["approval"] != "required" {
		t.Fatalf("result %v", gv)
	}
	if st := f.proposalRow(t, p["id"].(string)); st != "applied" {
		t.Fatalf("status %s", st)
	}
	// the same arguments typed by hand for another role give the same grant
	direct := f.mustRun(t, "grant", "add", "growth", "--role", "writer", "--automation", "summarize", "--mode", "run", "--wait=true",
		"--timeout", "600", "--approval", "required", "--max-calls-per-run", "20", "--max-calls-per-day", "200", "--max-output-bytes", "16384")["grant"].(map[string]interface{})
	for _, k := range []string{"id", "role", "created_at"} {
		delete(gv, k)
		delete(direct, k)
	}
	if jsonOf(t, gv) != jsonOf(t, direct) {
		t.Fatalf("confirm made %v, grant add made %v", gv, direct)
	}
	if len(f.grants(t)) != 2 {
		t.Fatalf("grants %v", f.grants(t))
	}
}

func TestConfirmingAnAutonomyProposalSetsTheSameAutonomyAsAutonomySet(t *testing.T) {
	f := propFixture(t)
	notOperatorContexts["agent"](t)
	p := f.propose(t, "growth", "autonomy.set", `{"level":"mid","decider":"model","decider_runtime":"claude","on_decider_failure":"deny"}`)
	asOrgOperator(t)
	if _, err := f.confirm(t, p); err != nil {
		t.Fatal(err)
	}
	got := f.mustRun(t, "autonomy", "show", "growth")
	if got["level"] != "mid" || got["updated_by"] != "cli" {
		t.Fatalf("autonomy %v", got)
	}
}

func TestAProposalIsAppliedOnceAndAReplayIsRefused(t *testing.T) {
	f := propFixture(t)
	p := f.propose(t, "growth", "grant.add", grantArgs)
	if _, err := f.confirm(t, p); err != nil {
		t.Fatal(err)
	}
	f.mustRun(t, "grant", "remove", "growth", "--role", "lead", "--automation", "summarize")
	_, err := f.confirm(t, p)
	if err == nil || exitCodeOf(t, err) != 3 || !strings.Contains(err.Error(), "proposal_already_decided") {
		t.Fatalf("replay: %v", err)
	}
	if len(f.grants(t)) != 0 {
		t.Fatal("a replayed confirm granted again")
	}
}

func TestTwoConfirmsAtOnceApplyOnce(t *testing.T) {
	f := propFixture(t)
	p := f.propose(t, "growth", "grant.add", grantArgs)
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
	if len(f.grants(t)) != 1 {
		t.Fatalf("grants %v", f.grants(t))
	}
}

func TestAStaleProposalIsRefusedAndAppliesNothing(t *testing.T) {
	f := propFixture(t)
	g := f.propose(t, "growth", "grant.add", grantArgs)
	a := f.propose(t, "growth", "autonomy.set", `{"level":"mid"}`)
	// the live grant changes after the proposal was made
	f.mustRun(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize", "--mode", "status", "--approval", "required")
	f.mustRun(t, "autonomy", "set", "growth", "--max-decisions", "9")
	for _, p := range []map[string]interface{}{g, a} {
		_, err := f.confirm(t, p)
		if err == nil || exitCodeOf(t, err) != 3 || !strings.Contains(err.Error(), "proposal_stale") {
			t.Fatalf("%v: %v", p["verb"], err)
		}
		if st := f.proposalRow(t, p["id"].(string)); st != "stale" {
			t.Errorf("status %s", st)
		}
	}
	if gr := f.grants(t); len(gr) != 1 || gr[0].(map[string]interface{})["mode"] != "status" {
		t.Fatalf("grants %v", gr)
	}
	if got := f.mustRun(t, "autonomy", "show", "growth"); got["level"] != "manual" {
		t.Fatalf("autonomy %v", got)
	}
}

func TestAGrantProposalGoesStaleWhenTheGrantIsRevokedOrTheRoleGoes(t *testing.T) {
	f := propFixture(t)
	f.mustRun(t, "grant", "add", "growth", "--role", "lead", "--automation", "digest", "--approval", "required")
	p := f.propose(t, "growth", "grant.add", `{"role":"lead","automation":"digest","max_calls_per_run":3}`)
	f.mustRun(t, "grant", "remove", "growth", "--role", "lead", "--automation", "digest")
	if _, err := f.confirm(t, p); err == nil || !strings.Contains(err.Error(), "proposal_stale") {
		t.Fatalf("after the grant was revoked: %v", err)
	}
	q := f.propose(t, "growth", "grant.add", `{"role":"writer","automation":"digest"}`)
	f.editOutside(t, func(m map[string]interface{}) { // the role goes from the org file, the way a hand edit would
		var keep []interface{}
		for _, r := range m["roles"].([]interface{}) {
			if r.(map[string]interface{})["id"] != "writer" {
				keep = append(keep, r)
			}
		}
		m["roles"] = keep
	})
	if _, err := f.confirm(t, q); err == nil || !strings.Contains(err.Error(), "proposal_stale") {
		t.Fatalf("after the role was removed: %v", err)
	}
}

func TestAnExpiredProposalIsRefused(t *testing.T) {
	f := propFixture(t)
	p := f.propose(t, "growth", "grant.add", grantArgs)
	f.sql(t, `UPDATE org_proposals SET expires_at = '2020-01-01T00:00:00Z' WHERE id = ?`, p["id"].(string))
	_, err := f.confirm(t, p)
	if err == nil || exitCodeOf(t, err) != 3 || !strings.Contains(err.Error(), "proposal_expired") {
		t.Fatalf("expired: %v", err)
	}
	if len(f.grants(t)) != 0 || f.proposalRow(t, p["id"].(string)) != "expired" {
		t.Fatal("an expired proposal applied or kept its status")
	}
	// the expiry is not a field the row can be revived through
	f.sql(t, `UPDATE org_proposals SET status = 'pending' WHERE id = ?`, p["id"].(string))
	if _, err := f.confirm(t, p); err == nil || !strings.Contains(err.Error(), "proposal_expired") {
		t.Fatalf("revived by status alone: %v", err)
	}
}

func TestTheShownHashMustBeTheHashOfTheStoredProposal(t *testing.T) {
	f := propFixture(t)
	a := f.propose(t, "growth", "grant.add", grantArgs)
	b := f.propose(t, "growth", "grant.add", `{"role":"writer","automation":"digest"}`)
	for name, hash := range map[string]string{"empty": "", "another proposal's": b["hash"].(string), "garbage": strings.Repeat("0", 64)} {
		_, err := f.run(t, "proposal", "confirm", a["id"].(string), "--hash", hash)
		if err == nil || exitCodeOf(t, err) != 3 || !strings.Contains(err.Error(), "proposal_hash_mismatch") {
			t.Errorf("%s hash: %v", name, err)
		}
	}
	// the card was drawn, then the row was rewritten: the shown hash no longer fits
	shown := f.mustRun(t, "proposal", "show", a["id"].(string))["proposal"].(map[string]interface{})["hash"].(string)
	f.sql(t, `UPDATE org_proposals SET args_json = ? WHERE id = ?`, `{"role":"lead","automation":"publish_post","mode":"run","wait":true,"timeout_seconds":600,"approval":"required","max_calls_per_run":20,"max_calls_per_day":200,"max_output_bytes":16384}`, a["id"].(string))
	if _, err := f.run(t, "proposal", "confirm", a["id"].(string), "--hash", shown); err == nil || !strings.Contains(err.Error(), "proposal_hash_mismatch") {
		t.Fatalf("rewritten row: %v", err)
	}
	if len(f.grants(t)) != 0 || f.proposalRow(t, a["id"].(string)) != "pending" {
		t.Fatal("a mismatched hash applied or moved the row")
	}
}

// A row that was rewritten into something the creator would have refused is refused again at confirm.
func TestConfirmValidatesTheStoredArgumentsAgain(t *testing.T) {
	f := propFixture(t)
	a := f.propose(t, "growth", "grant.add", grantArgs)
	f.sql(t, `UPDATE org_proposals SET args_json = ? WHERE id = ?`, `{"role":"lead","automation":"summarize","approval":"none"}`, a["id"].(string))
	shown := f.mustRun(t, "proposal", "show", a["id"].(string))["proposal"].(map[string]interface{})["hash"].(string)
	f.sql(t, `UPDATE org_proposals SET hash = ? WHERE id = ?`, shown, a["id"].(string)) // a rewrite that kept the hash column in step
	if _, err := f.run(t, "proposal", "confirm", a["id"].(string), "--hash", shown); err == nil || exitCodeOf(t, err) != 3 || strings.Contains(err.Error(), "proposal_hash_mismatch") {
		t.Fatalf("approval none from a rewritten row: %v", err)
	}
	if len(f.grants(t)) != 0 {
		t.Fatal("granted approval none")
	}
}

// The apply is the real command: its own gate answers again, and a refusal there fails the proposal.
func TestTheApplyAsksTheGateAgainThroughTheRealCommand(t *testing.T) {
	f := propFixture(t)
	p := f.propose(t, "growth", "grant.add", grantArgs)
	var calls atomic.Int32
	opgate.SetForTest(t, func(bool) string {
		if calls.Add(1) == 1 {
			return ""
		}
		return "a coder session's turn runs this command"
	})
	_, err := f.confirm(t, p)
	if err == nil || !strings.Contains(err.Error(), "is the user's to do") {
		t.Fatalf("confirm: %v", err)
	}
	if calls.Load() < 2 {
		t.Fatalf("the gate was asked %d time(s): the apply is not the real command", calls.Load())
	}
	if len(f.grants(t)) != 0 || f.proposalRow(t, p["id"].(string)) != "failed" {
		t.Fatal("a refused apply granted or left the row claimable")
	}
	opgate.SetForTest(t, func(bool) string { return "" })
	if _, err := f.confirm(t, p); err == nil || !strings.Contains(err.Error(), "proposal_already_decided") {
		t.Fatalf("a failed proposal can be confirmed again: %v", err)
	}
}

func TestConfirmReSignsASignedOrgLikeGrantAdd(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.21.0")
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.plainWF, "--alias", "summarize")
	p := f.propose(t, "growth", "grant.add", grantArgs)
	orgsigntest.Sign(t, f.root, "growth")
	s.calls = nil
	captureStderr(t, func() { f.mustRun(t, "proposal", "confirm", p["id"].(string), "--hash", p["hash"].(string)) })
	if st, _, err := orgsign.VerifyFile(f.root, "growth"); err != nil || !st.OK() || len(s.calls) == 0 {
		t.Fatalf("after the operator's confirm: sign calls %d, state %+v, %v", len(s.calls), st, err)
	}
}

func TestConfirmOutputIsOneJSONDocument(t *testing.T) {
	f := propFixture(t)
	p := f.propose(t, "growth", "grant.add", grantArgs)
	cmd := newOrgCmd(f.cfg)
	cmd.SetArgs([]string{"proposal", "confirm", p["id"].(string), "--hash", p["hash"].(string)})
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil || m["v"] != float64(1) {
		t.Fatalf("stdout is not one JSON document: %q %v", out, err)
	}
}
