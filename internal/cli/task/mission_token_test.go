package task

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/storage"
)

// mintToken issues a mission token as a conductor does, against the test's database.
func mintToken(t *testing.T, dbPath, profile string, b agenttokens.MissionBinding, ttl time.Duration, at time.Time) (agenttokens.Token, string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := agenttokens.NewStore(db.DB)
	if !at.IsZero() {
		st.SetClock(func() time.Time { return at })
	}
	if b.Owner == "" {
		b.Owner = "run-test"
	}
	tok, secret, err := st.CreateMission(context.Background(), profile, b, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return tok, secret
}

// bindProcess hands the test's process a live mission token, as a conductor hands a worker its own.
func bindProcess(t *testing.T, dbPath string, b agenttokens.MissionBinding) agenttokens.Token {
	t.Helper()
	tok, secret := mintToken(t, dbPath, "default", b, time.Hour, time.Time{})
	setenv(t, agenttokens.EnvMissionToken, secret)
	return tok
}

// The labels a conductor leaves in the environment bind nothing: carrying them is being an
// ordinary agent, which neither gains nor keeps a scope by them.
func TestLabelsInTheEnvironmentBindNothing(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	one, two := sessionNew(t, db), sessionNew(t, db)
	sessionAdd(t, db, one, "one")
	elsewhere := sessionAdd(t, db, two, "elsewhere")
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", id(one))
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", id(two))
	setenv(t, "MONOAGENT_TASK_SESSION", id(one))
	setenv(t, "MONOAGENT_TASK_ID", "1")
	setenv(t, "MONOAGENT_TASK_ROLE", "verifier")
	// session one would not let it near session two's task
	var got sessionTaskDoc
	mustTaskJSON(t, db, "default", &got, "", "claim", id(elsewhere), "--as", "w")
	if got.Task == nil || got.Task.ID != elsewhere {
		t.Fatalf("a label bound the process: %+v", got.Task)
	}
	// and it is still an agent, never the operator
	if _, _, err := runTask(t, db, "default", true, "", "session", "approve", id(two)); err == nil {
		t.Fatal("labels made the process the operator")
	}
}

// A token binds the session, the task, the role and the name together; none of them is
// the process's to choose, and what does not verify binds to nothing that exists.
func TestMissionTokenCases(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	one := sessionNew(t, db, "T", "--conversation", "conv-1")
	mine := sessionAdd(t, db, one, "mine")
	other := sessionAdd(t, db, one, "other")
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", id(one))
	var got sessionTaskDoc
	// the task is worked by its bound worker (an unbound agent cannot work a managed session)
	_, wsecret := mintToken(t, db, "default", agenttokens.MissionBinding{Session: one, Task: mine, Role: agenttokens.RoleWorker, Actor: "m1-t1"}, time.Hour, time.Time{})
	setenv(t, agenttokens.EnvMissionToken, wsecret)
	mustTaskJSON(t, db, "default", &got, "", "claim", id(mine), "--as", "m1-t1")
	mustTaskJSON(t, db, "default", &got, "", "finish", id(mine), "--result", "done", "--as", "m1-t1")
	setenv(t, agenttokens.EnvMissionToken, "")
	b := agenttokens.MissionBinding{Session: one, Task: mine, Role: agenttokens.RoleVerifier, Actor: "v1-t1"}

	refused := func(label string, args ...string) {
		t.Helper()
		if _, _, err := runTask(t, db, "default", true, "", args...); err == nil {
			t.Errorf("%s: task %s was allowed", label, strings.Join(args, " "))
		}
		cur := getenv(t)(agenttokens.EnvMissionToken)
		setenv(t, agenttokens.EnvMissionToken, "") // read the state as an unbound process
		st := sessionState(t, db, one)
		setenv(t, agenttokens.EnvMissionToken, cur)
		if st.Session.Counts.Verified != 0 {
			t.Fatalf("%s: a verdict was recorded", label)
		}
	}
	verdict := []string{"verify", id(mine), "--pass", "--note", "ok"}

	// no token: an agent with no binding does not judge a session a conversation manages
	setenv(t, "MONOAGENT_ACTOR", "someone")
	refused("no token", append(verdict, "--as", "someone")...)
	// a worker's token never judges
	tok, secret := mintToken(t, db, "default", agenttokens.MissionBinding{Session: one, Task: mine, Role: agenttokens.RoleWorker, Actor: "m1-t1"}, time.Hour, time.Time{})
	setenv(t, agenttokens.EnvMissionToken, secret)
	refused("worker token", append(verdict, "--as", "v1-t1")...)
	// a verifier's token of the other task does not judge this one
	_, secret = mintToken(t, db, "default", agenttokens.MissionBinding{Session: one, Task: other, Role: agenttokens.RoleVerifier, Actor: "v1-t2"}, time.Hour, time.Time{})
	setenv(t, agenttokens.EnvMissionToken, secret)
	refused("another task's verifier", append(verdict, "--as", "v1-t1")...)
	// expired
	_, secret = mintToken(t, db, "default", b, time.Hour, time.Now().Add(-2*time.Hour))
	setenv(t, agenttokens.EnvMissionToken, secret)
	refused("expired", append(verdict, "--as", "v1-t1")...)
	// another profile's
	_, secret = mintToken(t, db, "p-other", b, time.Hour, time.Time{})
	setenv(t, agenttokens.EnvMissionToken, secret)
	refused("another profile", append(verdict, "--as", "v1-t1")...)
	// revoked
	vtok, secret := mintToken(t, db, "default", b, time.Hour, time.Time{})
	revoke(t, db, vtok.ID)
	setenv(t, agenttokens.EnvMissionToken, secret)
	refused("revoked", append(verdict, "--as", "v1-t1")...)
	_ = tok

	// the live verifier token judges, under the token's own name whatever --as says
	_, secret = mintToken(t, db, "default", b, time.Hour, time.Time{})
	setenv(t, agenttokens.EnvMissionToken, secret)
	mustTaskJSON(t, db, "default", &got, "", append(verdict, "--as", "someone-else")...)
	var shown struct {
		Events []struct {
			Kind  string `json:"kind"`
			Actor string `json:"actor"`
		} `json:"events"`
	}
	mustTaskJSON(t, db, "default", &shown, "", "show", id(mine), "--as", "v1-t1")
	for _, e := range shown.Events {
		if e.Kind == "verified" && e.Actor != "v1-t1" {
			t.Fatalf("the verdict was recorded as %q, not under the token's name", e.Actor)
		}
	}
}

func revoke(t *testing.T, dbPath, tokenID string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := agenttokens.NewStore(db.DB).Revoke(context.Background(), "default", tokenID); err != nil {
		t.Fatal(err)
	}
}
