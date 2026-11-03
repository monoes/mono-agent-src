package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/ops"
)

type strategyCall struct {
	profile string
	session int64
	op      string
	args    string
}

func installStrategy(t *testing.T, res BossStrategyResult, err error) *[]strategyCall {
	t.Helper()
	var calls []strategyCall
	SetBossStrategyCaller(func(_ context.Context, _ *sql.DB, _, profile string, session int64, op string, args json.RawMessage) (BossStrategyResult, error) {
		calls = append(calls, strategyCall{profile, session, op, string(args)})
		return res, err
	})
	t.Cleanup(func() { SetBossStrategyCaller(nil) })
	return &calls
}

var strategyToolNames = []string{"session_boss_strategy_propose", "session_boss_strategy_report"}

func TestBossStrategyToolNamesAreTheOpsPackagesAndTheOnlyAllowedOnes(t *testing.T) {
	if bossStrategyProposeOp != ops.OpStrategyPropose || bossStrategyReportOp != ops.OpStrategyReport {
		t.Fatal("the tools name operations the ops package does not")
	}
	if !ops.AllowedStrategy(bossStrategyProposeOp) || !ops.AllowedStrategy(bossStrategyReportOp) {
		t.Fatal("a tool names an operation the strategy allowlist refuses")
	}
	var callable []string
	for _, sp := range ops.StrategyOpSpecs() {
		if sp.SessionCallable {
			callable = append(callable, sp.Name)
		}
	}
	sort.Strings(callable)
	if strings.Join(callable, ",") != "strategy.propose,strategy.report" {
		t.Fatalf("the session-callable strategy operations are %v", callable)
	}
}

// The model-callable path cannot request, approve or begin a finish through strategy: the tools take no operation name,
// no session and no finish parameter; whatever the model writes, the operation handed on is the tool's own.
func TestNoStrategyToolCanRequestOrApproveAFinish(t *testing.T) {
	served := map[string]tool{}
	for _, tl := range allTools() {
		if strings.Contains(tl.name, "strategy") {
			served[tl.name] = tl
		}
	}
	if len(served) != len(strategyToolNames) {
		t.Fatalf("strategy tools = %d", len(served))
	}
	for _, n := range strategyToolNames {
		tl, ok := served[n]
		if !ok || !tl.mutating {
			t.Fatalf("%s: present %v mutating %v", n, ok, tl.mutating)
		}
		props, _ := tl.schema["properties"].(map[string]interface{})
		for p := range props {
			for _, bad := range []string{"op", "operation", "finish", "session", "caller", "approve", "limit", "budget", "charter", "generation"} {
				if strings.Contains(strings.ToLower(p), bad) {
					t.Errorf("%s takes a %q parameter", n, p)
				}
			}
		}
	}
	m, _ := newAskFixture(t)
	calls := installStrategy(t, BossStrategyResult{Accepted: true, DirectionID: "d-1"}, nil)
	t.Setenv(agenttokens.EnvMissionToken, "")
	for name, want := range map[string]string{"session_boss_strategy_propose": ops.OpStrategyPropose, "session_boss_strategy_report": ops.OpStrategyReport} {
		args := map[string]any{"op": "finish.begin", "operation": "finish.begin", "session": 1, "title": "t", "rationale": "r", "criteria": []string{"c0"}, "direction": "d", "state": "finished"}
		if _, err := m.as(t, m.tokOther, name, args); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := (*calls)[len(*calls)-1]
		if got.op != want || got.op == ops.OpFinishBegin || got.session == 0 || got.session == 1 {
			t.Fatalf("%s handed on %+v", name, got)
		}
	}
}

func TestBossStrategyToolsRefuseWithoutAnOpenRunAndForNonLeads(t *testing.T) {
	m, _ := newAskFixture(t)
	t.Setenv(agenttokens.EnvMissionToken, "")
	args := map[string]any{"title": "t", "rationale": "r", "criteria": []string{"c0"}}
	if out, err := m.as(t, m.tokOther, "session_boss_strategy_propose", args); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("no caller wired: %v %s", err, out)
	}
	calls := installStrategy(t, BossStrategyResult{}, coderhost.ErrDoomsdayOff)
	if out, err := m.as(t, m.tokOther, "session_boss_strategy_propose", args); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("no run: %v %s", err, out)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d", len(*calls))
	}
	before := len(*calls)
	for _, role := range []string{agenttokens.RoleWorker, agenttokens.RoleVerifier} {
		if _, err := m.asBound(t, role, "session_boss_strategy_propose", args); err == nil {
			t.Fatalf("a bound %s proposed", role)
		}
	}
	t.Setenv(agenttokens.EnvMissionToken, "")
	if _, err := m.as(t, "", "session_boss_strategy_report", map[string]any{"direction": "d", "state": "stalled"}); err == nil || !strings.Contains(err.Error(), "no_session") {
		t.Fatalf("no token: %v", err)
	}
	if len(*calls) != before {
		t.Fatalf("a refused caller reached the strategy caller: %v", *calls)
	}
}
