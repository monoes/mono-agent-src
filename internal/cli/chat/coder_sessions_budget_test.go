package chat

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// budgetRequestOf makes a running mission whose lead has a $5 budget and has asked for $2 more.
func budgetRequestOf(t *testing.T, m masterTurn) coderhost.BudgetRequest {
	t.Helper()
	sid, _ := missionOfMaster(t, m, 1, true)
	db, err := storage.NewDatabase(m.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cs := coderhost.NewStore(db.DB)
	if err := cs.SetBudget("default", m.master, 5); err != nil {
		t.Fatal(err)
	}
	lead := coderhost.NewLeadScope(coderhost.Caller{ProfileID: "default", ConversationID: m.master}, coderhost.BindingNone)
	b, err := cs.RequestBudget(lead, "default", sid, coderhost.TargetLead, 0, 2, "need more")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func budgetOfConv(t *testing.T, dbPath, conv string) float64 {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, err := coderhost.NewStore(db.DB).Budget("default", conv)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBudgetAnswersAreTheOperatorsAlone(t *testing.T) {
	m := newMasterTurn(t) // an agent's process: it holds the session token
	b := budgetRequestOf(t, m)
	m.cfg.RequireOperator = func(string) error { return nil } // even a check that would pass cannot open it for a token holder
	id := "1"
	for _, args := range [][]string{{"budget", "grant", id}, {"budget", "grant", id, "--usd", "1"}, {"budget", "decline", id}, {"budget", "list"}} {
		if out, code := runSessionsCLI(t, m.cfg, args...); code != 3 {
			t.Errorf("%v: exit %d %s, want the operator refusal (3)", args, code, out)
		}
	}
	t.Setenv(coderhost.SessionTokenEnv, "")
	t.Setenv(agenttokens.EnvMissionToken, "some-token") // a worker of a mission
	if _, code := runSessionsCLI(t, m.cfg, "budget", "grant", id); code != 3 {
		t.Fatalf("a bound process: exit %d", code)
	}
	if got := budgetOfConv(t, m.dbPath, m.master); got != 5 {
		t.Fatalf("budget = %v after refused answers (request %d)", got, b.ID)
	}
	m.cfg.RequireOperator = func(string) error { return clicore.OperatorOnly("not the operator terminal") } // not the operator's terminal
	t.Setenv(agenttokens.EnvMissionToken, "")
	if _, code := runSessionsCLI(t, m.cfg, "budget", "grant", id); code != 3 {
		t.Fatalf("a failing operator check: exit %d", code)
	}
}

func TestBudgetGrantFromTheOperatorsTerminalIsOnce(t *testing.T) {
	m := newMasterTurn(t)
	b := budgetRequestOf(t, m)
	t.Setenv(coderhost.SessionTokenEnv, "")
	m.cfg.RequireOperator = func(string) error { return nil }
	out, code := runSessionsCLI(t, m.cfg, "budget", "list", "--open")
	if code != 0 || !strings.Contains(out, `"amountUsd":2`) || !strings.Contains(out, `"open":true`) {
		t.Fatalf("list: %d %s", code, out)
	}
	if out, code := runSessionsCLI(t, m.cfg, "budget", "grant", "1", "--usd", "9"); code == 0 {
		t.Fatalf("a grant above the ask succeeded: %s", out)
	}
	if out, code := runSessionsCLI(t, m.cfg, "budget", "grant", "1", "--usd", "1.5"); code != 0 || !strings.Contains(out, `"grantedUsd":1.5`) {
		t.Fatalf("grant: %d %s", code, out)
	}
	if got := budgetOfConv(t, m.dbPath, m.master); got != 6.5 {
		t.Fatalf("budget = %v, want 6.5 (request %d)", got, b.ID)
	}
	if _, code := runSessionsCLI(t, m.cfg, "budget", "grant", "1"); code == 0 {
		t.Fatal("a second grant succeeded")
	}
	if _, code := runSessionsCLI(t, m.cfg, "budget", "grant", "abc"); code == 0 {
		t.Fatal("a bad id was accepted")
	}
	if out, _ := runSessionsCLI(t, m.cfg, "budget", "list", "--open"); strings.Contains(out, `"id":1`) {
		t.Fatalf("an answered request is still listed as open: %s", out)
	}
}
