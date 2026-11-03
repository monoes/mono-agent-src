package mcp

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
)

type finishCall struct {
	profile string
	session int64
}

// installFinish stands in for the engine's installer: the beginner the build with the mode sets. It records what it was
// called with and answers res, err.
func installFinish(t *testing.T, res BossFinishResult, err error) *[]finishCall {
	t.Helper()
	var calls []finishCall
	SetBossFinishBeginner(func(_ context.Context, _ *sql.DB, _, profile string, session int64) (BossFinishResult, error) {
		calls = append(calls, finishCall{profile, session})
		return res, err
	})
	t.Cleanup(func() { SetBossFinishBeginner(nil) })
	return &calls
}

func TestBossFinishBeginIsServedMutatingAndRefusesWithoutAnOpenRun(t *testing.T) {
	var tl *tool
	for _, x := range allTools() {
		if x.name == "session_boss_finish_begin" {
			x := x
			tl = &x
		}
	}
	if tl == nil || !tl.mutating {
		t.Fatalf("tool: %+v", tl)
	}
	m, _ := newAskFixture(t)
	t.Setenv(agenttokens.EnvMissionToken, "")
	// no beginner (a default build)
	if out, err := m.as(t, m.tokOther, "session_boss_finish_begin", map[string]any{}); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("no beginner: %v %s", err, out)
	}
	// a beginner, and the run is not running: the installer's refusal is the mode not running
	calls := installFinish(t, BossFinishResult{}, coderhost.ErrDoomsdayOff)
	if out, err := m.as(t, m.tokOther, "session_boss_finish_begin", map[string]any{}); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("no run: %v %s", err, out)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d", len(*calls))
	}
}

// Only the verified lead reaches the beginner; the session it gets is the lead's own, never an argument.
func TestBossFinishBeginIsTheVerifiedLeadsAndTakesNoSessionArgument(t *testing.T) {
	m, _ := newAskFixture(t)
	calls := installFinish(t, BossFinishResult{Generation: 3, Accepted: true, State: "quiescent"}, nil)
	t.Setenv(agenttokens.EnvMissionToken, "")
	if _, err := m.as(t, m.tokM, "session_boss_finish_begin", map[string]any{}); err == nil {
		t.Fatal("a lead below another lead began the finish of the run")
	}
	for _, role := range []string{agenttokens.RoleWorker, agenttokens.RoleVerifier} {
		if _, err := m.asBound(t, role, "session_boss_finish_begin", map[string]any{}); err == nil {
			t.Fatalf("a bound %s began the finish", role)
		}
	}
	t.Setenv(agenttokens.EnvMissionToken, "")
	if _, err := m.as(t, "", "session_boss_finish_begin", map[string]any{}); err == nil || !strings.Contains(err.Error(), "no_session") {
		t.Fatalf("no token: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused caller reached the beginner: %v", *calls)
	}
	out, err := m.as(t, m.tokOther, "session_boss_finish_begin", map[string]any{"session": 999})
	if err != nil || !strings.Contains(out, `"accepted": true`) || !strings.Contains(out, `"generation": 3`) {
		t.Fatalf("%v %s", err, out)
	}
	if len(*calls) != 1 || (*calls)[0].profile != m.profile || (*calls)[0].session == 0 || (*calls)[0].session == 999 {
		t.Fatalf("%+v", *calls)
	}
}

// A refusal of the finish's own preconditions comes back as data with its reason, not as an error and not as accepted.
func TestBossFinishBeginReportsARefusalOfThePreconditions(t *testing.T) {
	m, _ := newAskFixture(t)
	installFinish(t, BossFinishResult{Generation: 3, Refused: "1 person item(s) are open"}, nil)
	t.Setenv(agenttokens.EnvMissionToken, "")
	out, err := m.as(t, m.tokOther, "session_boss_finish_begin", map[string]any{})
	if err != nil || !strings.Contains(out, `"accepted": false`) || !strings.Contains(out, "person item") {
		t.Fatalf("%v %s", err, out)
	}
}
