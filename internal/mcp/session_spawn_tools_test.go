package mcp

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
)

func TestSessionSpawnForALeadMakesANamedTaskSessionAndRefusesSmallWorkOnce(t *testing.T) {
	m := newMissionChildFixture(t, true)
	args := map[string]any{"title": "Docs direction", "kind": "direction", "reason": "parallel", "message": "write the docs"}
	// no tasks named: nothing to say the work is big, so the first call is advised against
	if _, err := m.as(t, m.tokM, "session_spawn", args); err == nil || !strings.HasPrefix(err.Error(), "advice:") || !strings.Contains(err.Error(), "doable in this session") {
		t.Fatalf("first call: %v", err)
	}
	out, err := m.as(t, m.tokM, "session_spawn", args)
	if err != nil || !strings.Contains(out, `"taskSessionId"`) || !strings.Contains(out, `"kind": "direction"`) || !strings.Contains(out, `"depth": 2`) {
		t.Fatalf("second call: %v %s", err, out)
	}
	var below int
	if err := m.f.Side.DB.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE parent_session_id = ? AND kind = 'direction' AND title = 'Docs direction'`, m.se.ID).Scan(&below); err != nil || below != 1 {
		t.Fatalf("task sessions below the mission = %d (%v)", below, err)
	}
	// the tasks named are read from the lead's own session, and a task of another mission reads as not yours
	if _, err := m.as(t, m.tokM, "session_spawn", map[string]any{"title": "Theirs", "kind": "task", "reason": "parallel", "task_ids": []int64{m.otherTask.ID}}); err == nil || !strings.Contains(err.Error(), "not_yours") {
		t.Fatalf("another mission's task: %v", err)
	}
}

func TestSessionSpawnRequiresTheLeadFieldsAndAKnownReason(t *testing.T) {
	m := newMissionChildFixture(t, true)
	for name, args := range map[string]map[string]any{
		"no fields (the plain spawn)": {},
		"no reason":                   {"title": "x", "kind": "task"},
		"no kind":                     {"title": "x", "reason": "parallel"},
		"unknown reason":              {"title": "x", "kind": "task", "reason": "i want to"},
		"a folder":                    {"title": "x", "kind": "task", "reason": "parallel", "cwd": "."},
	} {
		if out, err := m.as(t, m.tokM, "session_spawn", args); err == nil || !strings.Contains(err.Error(), "invalid_input") {
			t.Errorf("%s: %v %s", name, err, out)
		}
	}
	var n int
	m.f.Side.DB.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE parent_session_id IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d task sessions were made by refused calls", n)
	}
}

func TestSessionSpawnIsFencedFromBoundWorkersAndDrafts(t *testing.T) {
	for _, role := range []string{agenttokens.RoleWorker, agenttokens.RoleVerifier} {
		m := newMissionChildFixture(t, true)
		args := map[string]any{"title": "x", "kind": "task", "reason": "parallel", "task_ids": []int64{m.t1.ID}}
		if out, err := m.asBound(t, role, "session_spawn", args); err == nil || !strings.Contains(err.Error(), "not_yours") {
			t.Fatalf("%s: a bound process spawned: %v %s", role, err, out)
		}
	}
	t.Setenv(agenttokens.EnvMissionToken, "") // the bound calls above left their binding in the environment
	d := newMissionChildFixture(t, false)     // the plan is a draft: nothing is spawned before the operator approves
	if out, err := d.as(t, d.tokM, "session_spawn", map[string]any{"title": "x", "kind": "task", "reason": "parallel", "task_ids": []int64{d.t1.ID, d.t2.ID}}); err == nil || !strings.Contains(err.Error(), "invalid_input") {
		t.Fatalf("a draft spawned: %v %s", err, out)
	}
}

func TestSessionSpawnForAPlainMasterIsUnchangedAndTakesNoLeadFields(t *testing.T) {
	sf := newSessionFixture(t) // a master with no task session
	if out, err := sf.as(t, sf.tokM, "session_spawn", map[string]any{}); err != nil || !strings.Contains(out, "conversationId") {
		t.Fatalf("plain spawn = %v %s", err, out)
	}
	if out, err := sf.as(t, sf.tokM, "session_spawn", map[string]any{"title": "x", "reason": "parallel"}); err == nil || !strings.Contains(err.Error(), "invalid_input") {
		t.Fatalf("lead fields on a plain master: %v %s", err, out)
	}
}
