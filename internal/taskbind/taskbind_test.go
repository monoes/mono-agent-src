package taskbind

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

func TestResolve(t *testing.T) {
	db := testdb.Open(t).DB
	ctx := context.Background()
	st := agenttokens.NewStore(db)
	b := agenttokens.MissionBinding{Session: 4, Task: 9, Role: agenttokens.RoleVerifier, Actor: "v4-t9", Owner: "run"}
	_, secret, err := st.CreateMission(ctx, "default", b, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := tasks.Actor{Kind: tasks.Agent, Name: "claimed-name"}

	if got := Resolve(ctx, db, "default", "").Apply(agent); got != agent {
		t.Errorf("no token changed the actor: %+v", got)
	}
	got := Resolve(ctx, db, "default", secret).Apply(agent)
	if want := (tasks.Actor{Kind: tasks.Agent, Name: "v4-t9", Session: 4, Task: 9, Verifier: true}); got != want {
		t.Errorf("a live token: %+v, want %+v", got, want)
	}
	for name, token := range map[string]string{"forged": "mat-nope", "garbled": "x"} {
		if r := Resolve(ctx, db, "default", token); r.State != Invalid {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if r := Resolve(ctx, db, "elsewhere", secret); r.State != Invalid {
		t.Errorf("another profile: %+v", r)
	}
	if got := Resolve(ctx, db, "default", "x").Apply(agent); got.Session != -1 || got.Task != -1 || got.Verifier {
		t.Errorf("an invalid token must bind to nothing that exists: %+v", got)
	}
}
