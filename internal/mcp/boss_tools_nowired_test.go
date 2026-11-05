package mcp

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
)

// With the mode not running (no installer wired, which is a build without the engine and a build with it before any run
// exists alike) every one of the boss tools answers the "mode off" refusal to the verified lead, and writes nothing:
// no row of any table changes. The five of the first generation and the two strategy tools are all listed here, by the same
// names TestNoToolTouchesTheOpenEndedMode allows.
func TestEveryBossToolWithNothingWiredRefusesAsOffAndWritesNothing(t *testing.T) {
	SetBossRouteInstaller(nil)
	SetBossFinishBeginner(nil)
	SetBossStrategyCaller(nil)
	SetBossFinishReadyReader(nil)
	m, _ := newAskFixture(t)
	id := askCritical(t, m)
	args := map[string]map[string]any{
		"session_boss_answer_ask":       {"ask_id": id, "answer": "yes"},
		"session_boss_approve_plan":     {"session": m.se.ID, "rev": 1, "digest": "d"},
		"session_boss_grant_budget":     {"request_id": 1, "amount_usd": 1},
		"session_boss_finish_begin":     {},
		"session_boss_finish_ready":     {},
		"session_boss_strategy_propose": {"title": "t", "rationale": "r", "criteria": []string{"c0"}},
		"session_boss_strategy_report":  {"direction": "d", "state": "stalled"},
	}
	if len(args) != len(bossToolNames) {
		t.Fatalf("this table covers %d tools, the mode has %d", len(args), len(bossToolNames))
	}
	before := mcpTableCounts(t, m)
	for _, name := range bossToolNames {
		a, ok := args[name]
		if !ok {
			t.Fatalf("no arguments for %s", name)
		}
		t.Setenv(agenttokens.EnvMissionToken, "")
		out, err := m.as(t, m.tokOther, name, a)
		if err == nil || !strings.Contains(err.Error(), "not running") {
			t.Errorf("%s: %v %s", name, err, out)
		}
	}
	after := mcpTableCounts(t, m)
	for n, b := range before {
		if after[n] != b {
			t.Errorf("a refused boss tool changed the rows of %s: %d -> %d", n, b, after[n])
		}
	}
}

func mcpTableCounts(t *testing.T, m *missionChildFixture) map[string]int {
	t.Helper()
	rows, err := m.f.Side.DB.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	out := map[string]int{}
	for _, n := range names {
		var c int
		if err := m.f.Side.DB.QueryRow(`SELECT COUNT(*) FROM "` + n + `"`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[n] = c
	}
	return out
}
