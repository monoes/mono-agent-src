package mcp

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
)

func installReady(t *testing.T, res BossFinishReadyResult, err error) *[]finishCall {
	t.Helper()
	var calls []finishCall
	SetBossFinishReadyReader(func(_ context.Context, _ *sql.DB, _, profile string, session int64) (BossFinishReadyResult, error) {
		calls = append(calls, finishCall{profile, session})
		return res, err
	})
	t.Cleanup(func() { SetBossFinishReadyReader(nil) })
	return &calls
}

// The reading is a read: whatever it answers, the finish beginner is never called by it.
func failIfFinishBegins(t *testing.T) {
	t.Helper()
	SetBossFinishBeginner(func(context.Context, *sql.DB, string, string, int64) (BossFinishResult, error) {
		t.Error("the finish-ready reading began a finish")
		return BossFinishResult{}, nil
	})
	t.Cleanup(func() { SetBossFinishBeginner(nil) })
}

func TestBossFinishReadyIsTheTopLeadsReadAndBeginsNothing(t *testing.T) {
	m, _ := newAskFixture(t)
	failIfFinishBegins(t)
	t.Setenv(agenttokens.EnvMissionToken, "")
	// a default build: no reader
	if out, err := m.as(t, m.tokOther, "session_boss_finish_ready", map[string]any{}); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("no reader: %v %s", err, out)
	}
	calls := installReady(t, BossFinishReadyResult{Ready: true, Reason: "all-criteria-plausibly-met", Epoch: 4, JudgmentAgeSeconds: 90, MarkerRecorded: true}, nil)
	if _, err := m.as(t, m.tokM, "session_boss_finish_ready", map[string]any{}); err == nil {
		t.Fatal("a lead below another lead read the finish-ready state")
	}
	for _, role := range []string{agenttokens.RoleWorker, agenttokens.RoleVerifier} {
		if _, err := m.asBound(t, role, "session_boss_finish_ready", map[string]any{}); err == nil {
			t.Fatalf("a bound %s read it", role)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused caller reached the reader: %v", *calls)
	}
	t.Setenv(agenttokens.EnvMissionToken, "")
	out, err := m.as(t, m.tokOther, "session_boss_finish_ready", map[string]any{"session": 999})
	if err != nil || !strings.Contains(out, `"ready": true`) || !strings.Contains(out, `"epoch": 4`) || !strings.Contains(out, `"markerRecorded": true`) {
		t.Fatalf("%v %s", err, out)
	}
	if len(*calls) != 1 || (*calls)[0].session == 0 || (*calls)[0].session == 999 {
		t.Fatalf("the session is the lead's own, never an argument: %+v", *calls)
	}
}

func TestBossFinishReadyMapsTheEnginesRefusals(t *testing.T) {
	m, _ := newAskFixture(t)
	t.Setenv(agenttokens.EnvMissionToken, "")
	installReady(t, BossFinishReadyResult{}, coderhost.ErrDoomsdayOff)
	if _, err := m.as(t, m.tokOther, "session_boss_finish_ready", map[string]any{}); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("%v", err)
	}
	installReady(t, BossFinishReadyResult{Ready: false, Reason: "latest-epoch-is-a-bad-judgment"}, nil)
	out, err := m.as(t, m.tokOther, "session_boss_finish_ready", map[string]any{})
	if err != nil || !strings.Contains(out, `"ready": false`) || !strings.Contains(out, "bad-judgment") {
		t.Fatalf("%v %s", err, out)
	}
}
