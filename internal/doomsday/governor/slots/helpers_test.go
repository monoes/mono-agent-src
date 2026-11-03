package slots

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

var ctx = context.Background()

func TestMain(m *testing.M) { testhome.Main(m) }

const (
	capProcess = 4
	capTree    = 2
)

// registry answers governor.RunView from the real doomsday store.
type registry struct {
	st  *store.SQLStore
	err error
}

func (r registry) Running(c context.Context, profile string, gen int64) (bool, error) {
	runs, err := r.st.OpenRuns(c, profile)
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		if run.Profile == profile && run.Generation == gen {
			return run.State == store.StateRunning, nil
		}
	}
	return false, nil
}

func (r registry) LiveMemberIDs(c context.Context, profile string, gen int64) (map[string]bool, error) {
	if r.err != nil {
		return nil, r.err
	}
	ms, err := r.st.LiveMembers(c, profile, gen)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, m := range ms {
		out[m.ID] = true
	}
	return out, nil
}

type env struct {
	db  *sql.DB
	st  *store.SQLStore
	s   *Store
	l   governor.Limits
	run store.Run
}

func migrate(t *testing.T, path string) {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	db.DB.Close()
}

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db.DB
}

func testCharter(profile string, gen int64) store.Charter {
	return store.Charter{
		Profile: profile, Generation: gen, Goal: "ship it",
		Criteria:    []store.Criterion{{Kind: "command", Spec: "go test ./...", Expected: "ok"}},
		Limits:      store.Limits{TreeDepth: 4, LivePerTree: capTree, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: capProcess, MaxTotal: 100, BudgetMicro: 5_000_000},
		Lines:       []string{store.LineSpending, store.LineInstall},
		BossPin:     store.Pin{Provider: "anthropic", Model: "m1", Version: "v1", PurposePolicy: "judging-v1"},
		VerifierPin: store.Pin{Provider: "anthropic", Model: "m2", Version: "v1", PurposePolicy: "judging-v1"},
		PerAsk:      store.PerAsk{Declared: true, SpendMicro: 1_000_000, Tasks: 5, Access: "edit", Depth: 2, RunSpendMicro: 10_000_000, RunTasks: 50, AnswerClasses: []string{"context", "clarify"}},
	}
}

// newEnv migrates a fresh database file, enters a running run and opens the slots store on it.
func newEnv(t *testing.T) *env {
	t.Helper()
	path := t.TempDir() + "/dd.db"
	migrate(t, path)
	return openEnv(t, path, true)
}

func openEnv(t *testing.T, path string, enter bool) *env {
	t.Helper()
	db := openDB(t, path)
	st := store.New(db, store.NewHMACKeyer([]byte("test-vault-derived-key")))
	e := &env{db: db, st: st, s: New(db, registry{st: st})}
	if enter {
		g, err := st.NextGeneration(ctx, "p")
		if err != nil {
			t.Fatal(err)
		}
		c := testCharter("p", g)
		if e.run, err = st.Enter(ctx, c, c.Hash(), "operator"); err != nil {
			t.Fatal(err)
		}
	}
	e.l = governor.Limits{Profile: "p", Generation: 1, MaxConcurrent: capProcess, LivePerTree: capTree}
	return e
}

func (e *env) register(t *testing.T, id string) {
	t.Helper()
	if err := e.st.Register(ctx, store.Member{Profile: "p", Generation: 1, ID: id, Kind: "session", Pid: 1, Stamp: "s"}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) live(t *testing.T, k governor.Kind, tree string) int {
	t.Helper()
	n, err := e.s.Live(ctx, "p", 1, k, tree)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func proc(i int) governor.Request {
	return governor.Request{Kind: governor.KindProcess, MemberID: fmt.Sprintf("proc-%d", i)}
}

func sess(tree string, i int) governor.Request {
	return governor.Request{Kind: governor.KindSession, Tree: tree, MemberID: fmt.Sprintf("sess-%s-%d", tree, i)}
}
