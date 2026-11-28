package budget

import (
	"context"
	"fmt"
	"math"
	"testing"
)

// resumed journals what a claude turn of one runtime session reports: the session it ran in, the session's running cost
// and its running token counters (here the same n for all four).
func (r *rig) resumed(conv, turn string, cost float64, n int) {
	r.t.Helper()
	r.usage(conv, turn, "", cost)
	for _, e := range [][2]string{
		{"session.bound", `{"runtime":"claude","sessionId":"s1"}`},
		{"notice", fmt.Sprintf(`{"code":"coder.usage","severity":"info","coderUsage":{"models":{"m":{"input":%[1]d,"output":%[1]d,"cacheRead":%[1]d,"cacheCreation":%[1]d}}}}`, n)},
	} {
		r.turns++
		if _, err := r.l.DB.Exec(`INSERT INTO ai_chat_events (profile_id, conversation_id, turn_id, seq, created_at, version, type, payload) VALUES ('p1',?,?,?,'x',1,?,?)`,
			conv, turn, r.turns, e[0], e[1]); err != nil {
			r.t.Fatal(err)
		}
	}
}

// A task whose follow-up turns resume one claude session has spent what the session cost, not the sum of the session's
// running totals (#1744). Workers' figures are their own and are added as journaled.
func TestAResumedClaudeSessionIsCountedOnce(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 0)
	if err := r.l.SetCap(ScopeGlobal, "", 1, "operator"); err != nil {
		t.Fatal(err)
	}
	a := r.task("c1", "generic", 1)
	if _, err := r.l.Admit(ctx, a); err != nil {
		t.Fatal(err)
	}
	r.resumed("c1", "t1", 0.30, 100)
	r.resumed("c1", "t2", 0.40, 200)
	r.usage("c1", "t2", "w1", 0.05)
	st, err := r.l.TaskStatus(ctx, a.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(st.SpentUSD-0.45) > 1e-9 {
		t.Fatalf("spent = %v, want the session's 0.40 and the worker's 0.05", st.SpentUSD)
	}
	grant, err := r.l.AdmitTurn(ctx, a, 0)
	if err != nil || math.Abs(grant-0.55) > 1e-9 {
		t.Fatalf("the next turn's grant = %v %v, want 0.55", grant, err)
	}
}
