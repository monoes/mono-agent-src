package mcp

import (
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/tasks"
)

// missionToken mints a mission token as a conductor would and puts it in the test process's
// environment, where the server reads it.
func (f *taskFixture) missionToken(b agenttokens.MissionBinding, ttl time.Duration, at time.Time) (agenttokens.Token, string) {
	f.t.Helper()
	st := agenttokens.NewStore(f.Side.DB)
	if !at.IsZero() {
		st.SetClock(func() time.Time { return at })
	}
	tok, secret, err := st.CreateMission(opCtx, "default", b, ttl)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Setenv(agenttokens.EnvMissionToken, secret)
	return tok, secret
}

// managedSession is a running session a conversation manages, with one task per title.
func (f *taskFixture) managedSession(titles ...string) (tasks.Session, []tasks.Task) {
	f.t.Helper()
	se, err := f.Store.CreateSession(opCtx, "default", tasks.SessionInput{Title: "managed", Goal: "g", ConversationID: "conv-1"}, sessOperator)
	if err != nil {
		f.t.Fatal(err)
	}
	var ts []tasks.Task
	for _, title := range titles {
		tk, err := f.Store.AddSessionTask(opCtx, "default", se.ID, tasks.SessionTaskInput{Title: title, Acceptance: "done when " + title}, sessOperator)
		if err != nil {
			f.t.Fatal(err)
		}
		ts = append(ts, tk)
	}
	if se, err = f.Store.ApproveSession(opCtx, "default", se.ID, sessOperator); err != nil {
		f.t.Fatal(err)
	}
	return se, ts
}

// toReview has a worker take the task and hand it back.
func (f *taskFixture) toReview(se tasks.Session, tk tasks.Task, worker string) {
	f.t.Helper()
	w := tasks.Actor{Kind: tasks.Agent, Name: worker}
	if _, err := f.Store.SessionClaim(opCtx, "default", se.ID, tk.ID, w, 0); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.Store.SessionFinish(opCtx, "default", se.ID, tk.ID, tasks.Outcome{Result: "did it"}, w); err != nil {
		f.t.Fatal(err)
	}
}

func (f *taskFixture) refused(label, tool string, args map[string]any) {
	f.t.Helper()
	_, err := f.call(tool, args)
	if err == nil {
		f.t.Errorf("%s: %s %v was allowed", label, tool, args)
		return
	}
	f.t.Logf("%s: %s refused: %v", label, tool, err)
}

func TestWorkerTokenBindsTheServerToItsTask(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	a, ta := f.sessionWith(true, false, "mine", "sibling")
	b, tb := f.sessionWith(true, false, "other session")
	personal := f.add("default", "personal", true)
	f.missionToken(agenttokens.MissionBinding{Session: a.ID, Task: ta[0].ID, Role: agenttokens.RoleWorker, Actor: "m1-t1", Owner: "run-x"}, time.Hour, time.Time{})

	f.refused("sibling", "task_claim", map[string]any{"id": ta[1].ID})
	f.refused("next", "task_claim", map[string]any{"next": true, "session": a.ID})
	f.refused("other session", "task_claim", map[string]any{"id": tb[0].ID})
	f.refused("read other", "task_session_get", map[string]any{"id": b.ID})
	f.refused("personal", "task_get", map[string]any{"id": personal.ID})
	f.refused("add", "task_add", map[string]any{"title": "x", "session": a.ID, "acceptance": "y"})
	for _, id := range []int64{ta[1].ID, tb[0].ID} {
		if tk, _, _ := f.Store.Get(opCtx, "default", id); tk.Claim != nil || tk.Status != tasks.StatusReady {
			t.Fatalf("a refused call moved #%d: %+v", id, tk)
		}
	}
	// it acts under the name the token carries, not the server's own
	if _, err := f.call("task_claim", map[string]any{"id": ta[0].ID, "session": a.ID}); err != nil {
		t.Fatalf("its own task: %v", err)
	}
	if tk, _, _ := f.Store.Get(opCtx, "default", ta[0].ID); tk.Claim == nil || tk.Claim.By != "m1-t1" {
		t.Fatalf("claim: %+v", tk.Claim)
	}
}

// The labels a conductor leaves in the environment bind nothing: a process that carries only
// them is an ordinary agent, with no scope to lose or to keep.
func TestEnvironmentLabelsBindNothing(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	a, _ := f.sessionWith(true, false, "mine")
	_, tb := f.sessionWith(true, false, "other session")
	t.Setenv("MONOAGENT_TASK_SESSION", "1")
	t.Setenv("MONOAGENT_TASK_ID", "1")
	t.Setenv("MONOAGENT_TASK_ROLE", "verifier")
	_ = a
	// bound by a label it would be refused: the other session's task is outside session 1
	if _, err := f.call("task_get", map[string]any{"id": tb[0].ID}); err != nil {
		t.Fatalf("a label bound the server: %v", err)
	}
}

func TestMissionTokenThatDoesNotVerifyRefusesEveryAct(t *testing.T) {
	now := time.Now()
	cases := map[string]func(f *taskFixture, b agenttokens.MissionBinding){
		"garbled": func(f *taskFixture, b agenttokens.MissionBinding) { f.t.Setenv(agenttokens.EnvMissionToken, "garbled") },
		"forged": func(f *taskFixture, b agenttokens.MissionBinding) {
			f.t.Setenv(agenttokens.EnvMissionToken, agenttokens.Prefix+"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		},
		"expired": func(f *taskFixture, b agenttokens.MissionBinding) {
			f.missionToken(b, time.Hour, now.Add(-2*time.Hour))
		},
		"revoked": func(f *taskFixture, b agenttokens.MissionBinding) {
			tok, _ := f.missionToken(b, time.Hour, time.Time{})
			if _, err := agenttokens.NewStore(f.Side.DB).Revoke(opCtx, "default", tok.ID); err != nil {
				f.t.Fatal(err)
			}
		},
		"another kind of token": func(f *taskFixture, b agenttokens.MissionBinding) {
			_, secret, err := agenttokens.NewStore(f.Side.DB).Create(opCtx, "default", "plain", agenttokens.ScopeRead, time.Hour)
			if err != nil {
				f.t.Fatal(err)
			}
			f.t.Setenv(agenttokens.EnvMissionToken, secret)
		},
		"another profile": func(f *taskFixture, b agenttokens.MissionBinding) {
			_, secret, err := agenttokens.NewStore(f.Side.DB).CreateMission(opCtx, workProfileID, b, time.Hour)
			if err != nil {
				f.t.Fatal(err)
			}
			f.t.Setenv(agenttokens.EnvMissionToken, secret)
		},
	}
	for name, setToken := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTaskFixture(t, taskSetup{})
			a, ta := f.sessionWith(true, false, "mine")
			personal := f.add("default", "personal", true)
			setToken(f, agenttokens.MissionBinding{Session: a.ID, Task: ta[0].ID, Role: agenttokens.RoleWorker, Actor: "m1-t1", Owner: "run-x"})
			f.refused(name, "task_claim", map[string]any{"id": ta[0].ID, "session": a.ID})
			f.refused(name, "task_get", map[string]any{"id": personal.ID})
			f.refused(name, "task_list", map[string]any{})
			if tk, _, _ := f.Store.Get(opCtx, "default", ta[0].ID); tk.Claim != nil {
				t.Fatalf("%s: the task was claimed", name)
			}
		})
	}
}

// In a session a conversation manages, only the verifier bound to the task records a verdict:
// not an agent with no token, not the task's own worker, not a verifier of another task.
func TestOnlyTheBoundVerifierTokenVerifies(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	se, ts := f.managedSession("one", "two")
	f.toReview(se, ts[0], "m1-t1")
	verdict := map[string]any{"id": ts[0].ID, "pass": true, "note": "checked"}
	notVerified := func(label string) {
		t.Helper()
		if tk, _, _ := f.Store.Get(opCtx, "default", ts[0].ID); tk.Verified || tk.Status != tasks.StatusReview {
			t.Fatalf("%s: the task moved: %+v", label, tk)
		}
	}

	f.refused("no token", "task_verify", verdict)
	notVerified("no token")

	f.missionToken(agenttokens.MissionBinding{Session: se.ID, Task: ts[0].ID, Role: agenttokens.RoleWorker, Actor: "m1-t1", Owner: "run-x"}, time.Hour, time.Time{})
	f.refused("worker token", "task_verify", verdict)
	notVerified("worker token")

	f.missionToken(agenttokens.MissionBinding{Session: se.ID, Task: ts[1].ID, Role: agenttokens.RoleVerifier, Actor: "v1-t2", Owner: "run-x"}, time.Hour, time.Time{})
	f.refused("another task's verifier", "task_verify", verdict)
	notVerified("another task's verifier")

	f.missionToken(agenttokens.MissionBinding{Session: se.ID, Task: ts[0].ID, Role: agenttokens.RoleVerifier, Actor: "v1-t1", Owner: "run-x"}, time.Hour, time.Time{})
	f.refused("verifier claims", "task_claim", map[string]any{"id": ts[0].ID, "session": se.ID})
	f.refused("verifier finishes", "task_finish", map[string]any{"id": ts[0].ID, "session": se.ID, "result": "x"})
	notVerified("verifier misuse")
	if _, err := f.call("task_verify", verdict); err != nil {
		t.Fatalf("the bound verifier: %v", err)
	}
	if tk, _, _ := f.Store.Get(opCtx, "default", ts[0].ID); !tk.Verified {
		t.Fatalf("not verified: %+v", tk)
	}
}
