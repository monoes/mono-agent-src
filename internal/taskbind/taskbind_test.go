package taskbind

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

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

// A mission token bound to a process is no binding for a caller outside that process's tree:
// it resolves like a token that does not verify, which refuses every act.
func TestResolveRefusesACallerOutsideTheWorkersTree(t *testing.T) {
	if !agenttokens.MissionProcessSupported() {
		t.Skip("process ancestry is read on Linux only (documented fallback)")
	}
	db := testdb.Open(t).DB
	ctx := context.Background()
	st := agenttokens.NewStore(db)
	tok, secret, err := st.CreateMission(ctx, "default",
		agenttokens.MissionBinding{Session: 4, Task: 9, Role: agenttokens.RoleWorker, Actor: "w4-t9", Owner: "run"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if r := Resolve(ctx, db, "default", secret); r.State != Valid {
		t.Fatalf("unbound: %+v", r)
	}
	self := os.Getpid()
	if err := st.BindMissionProcess(ctx, "default", tok.ID, self, agenttokens.ProcessStamp(self)); err != nil {
		t.Fatal(err)
	}
	if r := Resolve(ctx, db, "default", secret); r.State != Valid {
		t.Fatalf("the worker's own process: %+v", r)
	}
	if err := st.BindMissionProcess(ctx, "default", tok.ID, self, "start:1"); err != nil { // a reused pid
		t.Fatal(err)
	}
	if r := Resolve(ctx, db, "default", secret); r.State != Invalid {
		t.Fatalf("a caller outside the tree resolved: %+v", r)
	} else if !r.Outside || r.Err == nil || !strings.Contains(r.Err.Error(), "process tree") {
		t.Fatalf("the refusal does not say the caller is outside the tree: %+v", r)
	}
}
