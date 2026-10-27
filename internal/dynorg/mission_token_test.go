package dynorg

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The conductor mints a token for a worker's run, binds it to the session, the task, the role and
// the name, passes it in the process's environment and revokes it when the run ends.
func TestConductorMintsAWorkerTokenAndRevokesItWhenTheRunEnds(t *testing.T) {
	r := newRig(t, rigOpts{timeout: 10 * time.Minute})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "a running", func() bool { return r.ex.runCount(a.ID) == 1 })
	r.ex.mu.Lock()
	secret := r.ex.envs[a.ID][agenttokens.EnvMissionToken]
	r.ex.mu.Unlock()
	if secret == "" {
		t.Fatal("the worker was handed no token")
	}
	st := agenttokens.NewStore(r.db)
	tok, err := st.VerifyMission(context.Background(), "default", secret)
	if err != nil {
		t.Fatalf("the token does not verify while the run lives: %v", err)
	}
	want := agenttokens.MissionBinding{Session: r.sid, Task: a.ID, Role: agenttokens.RoleWorker, Actor: missionWorkerName(r.sid, a.ID)}
	got := *tok.Mission
	got.Owner = ""
	if got != want {
		t.Fatalf("binding %+v, want %+v", got, want)
	}
	if tok.Mission.Owner == "" || tok.ExpiresAt == nil || !tok.ExpiresAt.After(time.Now().Add(10*time.Minute)) || tok.ExpiresAt.After(time.Now().Add(30*time.Minute)) {
		t.Fatalf("the owner and the expiry (a little over the task timeout): %+v", tok)
	}
	if _, err := st.Verify(context.Background(), secret); !errors.Is(err, agenttokens.ErrInvalid) {
		t.Fatalf("the HTTP API accepts a mission token: %v", err)
	}

	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the run to end", func() bool { return r.c.taskWorker(a.ID) == nil })
	if _, err := st.VerifyMission(context.Background(), "default", secret); !errors.Is(err, agenttokens.ErrInvalid) {
		t.Fatalf("the token outlived the run: %v", err)
	}
}

func TestConductorMintsAVerifierTokenAndRevokesIt(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	var secret string
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	inner := r.ex.verify
	r.ex.verify = func(ctx context.Context, id int64, o monomind.ExecOptions) (*monomind.TurnResult, error) {
		secret = o.Env[agenttokens.EnvMissionToken]
		return inner(ctx, id, o)
	}
	r.toReview(a.ID)
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the verdict", func() bool { return r.task(a.ID).Verified })
	eventually(t, "the verifier to end", func() bool { return r.c.taskWorker(a.ID) == nil })
	st := agenttokens.NewStore(r.db)
	var m agenttokens.MissionBinding
	if err := r.db.QueryRow(`SELECT mission_session, mission_task, mission_role, mission_actor FROM agent_tokens WHERE mission_role = ?`, agenttokens.RoleVerifier).
		Scan(&m.Session, &m.Task, &m.Role, &m.Actor); err != nil {
		t.Fatal(err)
	}
	if m.Session != r.sid || m.Task != a.ID || m.Actor != missionVerifierName(r.sid, a.ID) {
		t.Fatalf("verifier binding %+v", m)
	}
	if secret == "" {
		t.Fatal("the verifier was handed no token")
	}
	if _, err := st.VerifyMission(context.Background(), "default", secret); !errors.Is(err, agenttokens.ErrInvalid) {
		t.Fatalf("the verifier's token outlived its run: %v", err)
	}
}

// A run that fails still ends its token.
func TestConductorRevokesTheTokenOfAFailedRun(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.ex.fail[a.ID] = 1
	r.next(r.c)
	eventually(t, "the failed run to end", func() bool { return r.ex.runCount(a.ID) >= 1 && r.c.taskWorker(a.ID) == nil })
	var live int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM agent_tokens WHERE mission_role != '' AND revoked_at IS NULL`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d mission tokens are live after the run failed", live)
	}
}

func tokenValid(t *testing.T, r *missionRig, secret string) bool {
	t.Helper()
	_, err := agenttokens.NewStore(r.db).VerifyMission(context.Background(), "default", secret)
	return err == nil
}

// A conductor that is dropped (a crash: nothing revokes its tokens) leaves a live token. A new
// conductor of the same conversation, named by the same owner, revokes it when it starts, and
// the tokens it mints itself afterwards survive.
func TestANewConductorRevokesTheTokensOfACrashedRun(t *testing.T) {
	r := newRig(t, rigOpts{timeout: 10 * time.Minute})
	r.mc.Owner = TurnOwner("default", "conv-1", "turn-1")
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "a running", func() bool { return r.ex.runCount(a.ID) == 1 })
	r.ex.mu.Lock()
	old := r.ex.envs[a.ID][agenttokens.EnvMissionToken]
	r.ex.mu.Unlock()
	if !tokenValid(t, r, old) {
		t.Fatal("the first run's token is not live")
	}
	w := r.c.taskWorker(a.ID)

	// The turn is restarted: a new conductor over the same store and conversation.
	mc2 := &MissionRun{Store: r.store, ProfileID: "default", Session: r.sid, Tokens: agenttokens.NewStore(r.db), Owner: TurnOwner("default", "conv-1", "turn-2"),
		Poll: 10 * time.Millisecond, TaskTimeout: 10 * time.Minute}
	c2 := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 20, MaxConcurrent: 5}, Staffer: &Staffer{Roster: []Model{opus, haiku}, Lead: opus},
		ReadAccess: true, Exec: r.ex.exec, Emit: r.em, Missions: mc2})
	t.Cleanup(c2.Close)
	env := map[string]string{}
	unbind, err := c2.bindMission(tbg, w, env) // its first mint sweeps first
	if err != nil {
		t.Fatal(err)
	}
	if tokenValid(t, r, old) {
		t.Fatal("the crashed run's token is still valid after a new conductor started")
	}
	mine := env[agenttokens.EnvMissionToken]
	if !tokenValid(t, r, mine) {
		t.Fatal("the new conductor's own token was swept")
	}
	c2.sweepMissionTokens(mc2) // once per conductor: a second call must not take its own tokens
	if !tokenValid(t, r, mine) {
		t.Fatal("a later sweep revoked the conductor's own token")
	}
	unbind()
	if tokenValid(t, r, mine) {
		t.Fatal("the run's token outlived its run")
	}
}

// A token another conversation's conductor holds is not swept.
func TestTheSweepLeavesAnotherConversationsTokensAlone(t *testing.T) {
	r := newRig(t, rigOpts{timeout: 10 * time.Minute})
	r.mc.Owner = TurnOwner("default", "conv-1", "turn-1")
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "a running", func() bool { return r.ex.runCount(a.ID) == 1 })
	r.ex.mu.Lock()
	theirs := r.ex.envs[a.ID][agenttokens.EnvMissionToken]
	r.ex.mu.Unlock()
	mc2 := &MissionRun{Store: r.store, ProfileID: "default", Tokens: agenttokens.NewStore(r.db), Owner: TurnOwner("default", "conv-2", "turn-1")}
	c2 := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 2, MaxConcurrent: 1}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		Exec: r.ex.exec, Emit: r.em, Missions: mc2})
	t.Cleanup(c2.Close)
	c2.sweepMissionTokens(mc2)
	if !tokenValid(t, r, theirs) {
		t.Fatal("a sweep of one conversation revoked another's token")
	}
}

// A revoke that fails is retried and then surfaced; the token is left to expire.
func TestAFailedRevokeIsSurfaced(t *testing.T) {
	r := newRig(t, rigOpts{timeout: 10 * time.Minute})
	r.mc.revokeFn = func(context.Context, string) error { return errors.New("database is locked") }
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "a running", func() bool { return r.ex.runCount(a.ID) == 1 })
	r.ex.mu.Lock()
	secret := r.ex.envs[a.ID][agenttokens.EnvMissionToken]
	r.ex.mu.Unlock()
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the failure to be journalled", func() bool { return countNotices(r.em, "mission_token_revoke_failed") == 1 })
	if !tokenValid(t, r, secret) {
		t.Fatal("the stub failed the revoke, so the token should still be live (and end by expiry)")
	}
}

// A second conductor of the same conversation, while the first one's turn still runs, does not
// end the first one's workers; once the first turn is over, the next conductor's sweep does.
func TestASecondConductorOfARunningConversationLeavesItsTokens(t *testing.T) {
	r := newRig(t, rigOpts{timeout: 10 * time.Minute})
	r.mc.Owner = TurnOwner("default", "conv-1", "turn-1")
	running := map[string]bool{r.mc.Owner: true}
	r.mc.Live = func(_ context.Context, o string) bool { return running[o] }
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "a running", func() bool { return r.ex.runCount(a.ID) == 1 })
	r.ex.mu.Lock()
	first := r.ex.envs[a.ID][agenttokens.EnvMissionToken]
	r.ex.mu.Unlock()
	w := r.c.taskWorker(a.ID)

	mc2 := &MissionRun{Store: r.store, ProfileID: "default", Session: r.sid, Tokens: agenttokens.NewStore(r.db), Owner: TurnOwner("default", "conv-1", "turn-2"),
		Live: r.mc.Live, Poll: 10 * time.Millisecond, TaskTimeout: 10 * time.Minute}
	c2 := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 20, MaxConcurrent: 5}, Staffer: &Staffer{Roster: []Model{opus, haiku}, Lead: opus},
		ReadAccess: true, Exec: r.ex.exec, Emit: r.em, Missions: mc2})
	t.Cleanup(c2.Close)
	env := map[string]string{}
	unbind, err := c2.bindMission(tbg, w, env)
	if err != nil {
		t.Fatal(err)
	}
	defer unbind()
	if !tokenValid(t, r, first) {
		t.Fatal("a second conductor revoked the token of a worker whose turn is still running")
	}
	if !tokenValid(t, r, env[agenttokens.EnvMissionToken]) {
		t.Fatal("the second conductor's own token is not live")
	}

	// The first turn ends (a crash, reconciled): a third conductor's sweep takes its token.
	running[r.mc.Owner] = false
	mc3 := &MissionRun{Store: r.store, ProfileID: "default", Tokens: agenttokens.NewStore(r.db), Owner: TurnOwner("default", "conv-1", "turn-3"), Live: r.mc.Live}
	c3 := New(tbg, Config{Cwd: "/w", Exec: r.ex.exec, Emit: r.em, Missions: mc3})
	t.Cleanup(c3.Close)
	c3.sweepMissionTokens(mc3)
	if tokenValid(t, r, first) {
		t.Fatal("the token of a turn that ended survived the next sweep")
	}
}

// A token the sweep already purged is not a failure: one attempt, nothing journalled.
func TestRevokingAPurgedTokenIsNotAFailure(t *testing.T) {
	r := newRig(t, rigOpts{timeout: 10 * time.Minute})
	var calls atomic.Int32
	r.mc.revokeFn = func(context.Context, string) error {
		calls.Add(1)
		return fmt.Errorf("revoking: %w", agenttokens.ErrNotFound)
	}
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "a running", func() bool { return r.ex.runCount(a.ID) == 1 })
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the revoke", func() bool { return calls.Load() >= 1 })
	time.Sleep(400 * time.Millisecond) // the retries, if it wrongly made them, back off 100 and 200 ms
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d revoke attempts for a token that was already gone", n)
	}
	if n := countNotices(r.em, "mission_token_revoke_failed"); n != 0 {
		t.Fatalf("%d spurious failure notices", n)
	}
}
