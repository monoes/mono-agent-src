package task

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// An unmarked process the turn-aware check places inside a coder turn is an
// agent, not the operator: the store's Human-only checks must never see it as
// tasks.Human. Without a refusal it stays the operator; an unwired check
// refuses (clicore.OperatorRefusal), so it is an agent too.
func TestCallerForClassesAnUnmarkedTurnProcessAsAnAgent(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	d := testDeps(t, db, "default", true)
	if c := d.callerFor(""); c.actor.Kind != tasks.Human || c.isAgent() {
		t.Fatalf("no refusal: %+v", c.actor)
	}
	setenv(t, notOperatorEnv, "this command runs inside a coder session's turn")
	c := d.callerFor("")
	if !c.isAgent() || c.actor.Kind == tasks.Human {
		t.Fatalf("in a turn: %+v", c.actor)
	}
	if _, err := c.operator("approve"); err == nil || !strings.Contains(err.Error(), "coder session's turn") {
		t.Errorf("operator(): %v", err)
	}
	d.NotOperator = nil
	if c := d.callerFor(""); c.actor.Kind == tasks.Human || !c.isAgent() {
		t.Errorf("unwired check must fail closed: %+v", c.actor)
	}
}

// Moving to Review or Done, approving a task and approving a session's plan
// from a cleared-marker turn are refused, and nothing changes.
func TestTurnProcessCannotMoveApproveOrApproveASession(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	ready := opsAdd(t, db, "work", "--ready")
	inbox := opsAdd(t, db, "waiting")
	s := sessionNew(t, db, "draft")
	sessionAdd(t, db, s, "a")
	const why = "this command runs inside a coder session's turn"
	setenv(t, notOperatorEnv, why)
	for _, args := range [][]string{
		{"move", id(ready), "review"}, {"move", id(ready), "done"},
		{"approve", id(inbox)}, {"session", "approve", id(s)},
	} {
		doc := failedTaskJSON(t, db, "default", 3, args...)
		if msg, _ := doc["error"].(string); doc["code"] != "operator_only" || !strings.Contains(msg, why) {
			t.Errorf("task %s: %v", strings.Join(args, " "), doc)
		}
	}
	setenv(t, notOperatorEnv, "")
	if st := opsShow(t, db, ready).Task.Status; st != "ready" {
		t.Errorf("moved: %s", st)
	}
	if st := opsShow(t, db, inbox).Task.Status; st != "inbox" {
		t.Errorf("approved: %s", st)
	}
	if st := sessionState(t, db, s); st.Session.State != "draft" {
		t.Errorf("session: %+v", st.Session)
	}
}
