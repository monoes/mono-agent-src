package sstore

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

var ctx = context.Background()

func TestMain(m *testing.M) { testhome.Main(m) }

// chainSink writes REAL rows into the store's doomsday_audit chain inside the caller's transaction, with the store's
// own MAC scheme, so store.VerifyAudit accepts them. fail makes it refuse after writing, to prove the rollback.
type chainSink struct {
	keys  store.Keyer
	mu    sync.Mutex
	fail  bool
	kinds []string
}

func (c *chainSink) Append(ctx context.Context, tx Tx, profile string, gen int64, kind, payload string) error {
	var seq int64
	var prev []byte
	err := tx.QueryRowContext(ctx, `SELECT seq, mac FROM doomsday_audit WHERE profile = ? AND generation = ? ORDER BY seq DESC LIMIT 1`, profile, gen).Scan(&seq, &prev)
	if errors.Is(err, sql.ErrNoRows) {
		seq, prev = 0, nil
	} else if err != nil {
		return err
	}
	seq++
	mac, err := c.keys.MAC("audit", []byte(profile), []byte(strconv.FormatInt(gen, 10)), []byte(strconv.FormatInt(seq, 10)), prev, []byte(kind), []byte(payload))
	if err != nil {
		return err
	}
	if prev == nil {
		prev = []byte{}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO doomsday_audit(profile, generation, seq, kind, payload, prev, mac) VALUES (?,?,?,?,?,?,?)`, profile, gen, seq, kind, payload, prev, mac); err != nil {
		return err
	}
	c.mu.Lock()
	c.kinds = append(c.kinds, kind)
	fail := c.fail
	c.mu.Unlock()
	if fail {
		return errors.New("sink refused")
	}
	return nil
}

type env struct {
	s    *Store
	db   *sql.DB
	core *store.SQLStore
	sink *chainSink
	prof string
	gen  int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	keyring.MockInit()
	d, err := storage.NewDatabase(t.TempDir() + "/dd.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.DB.Close() })
	d.DB.SetMaxOpenConns(1)
	keys := store.NewHMACKeyer([]byte("test-vault-derived-key"))
	core := store.New(d.DB, keys)
	e := &env{db: d.DB, core: core, sink: &chainSink{keys: keys}, prof: "p1"}
	e.s = New(WrapDB(d.DB), keys, e.sink)
	g, err := core.NextGeneration(ctx, e.prof)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Charter{
		Profile: e.prof, Generation: g, Goal: "ship it",
		Criteria: []store.Criterion{{Kind: "command", Spec: "go test ./...", Expected: "ok"}},
		Limits:   store.Limits{TreeDepth: 4, LivePerTree: 30, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: 30, MaxTotal: 100, BudgetMicro: 5_000_000},
		Lines:    []string{store.LineSpending, store.LineInstall},
		BossPin:  store.Pin{Provider: "anthropic", Model: "m1", Version: "v1", PurposePolicy: "judging-v1"}, VerifierPin: store.Pin{Provider: "anthropic", Model: "m2", Version: "v1", PurposePolicy: "judging-v1"},
		PerAsk: store.PerAsk{Declared: true, SpendMicro: 1_000_000, Tasks: 5, Access: "edit", Depth: 2, RunSpendMicro: 10_000_000, RunTasks: 50, AnswerClasses: []string{"context", "clarify"}},
	}
	if _, err := core.Enter(ctx, c, c.Hash(), "operator"); err != nil {
		t.Fatal(err)
	}
	e.gen = g
	return e
}

func (e *env) stop(t *testing.T) {
	t.Helper()
	if _, err := e.core.Advance(ctx, e.prof, e.gen, store.StateStopRequested, "operator", "test"); err != nil {
		t.Fatal(err)
	}
}

func (e *env) dir(id, title string) types.Direction {
	return types.Direction{Profile: e.prof, Generation: e.gen, ID: id, KeyHash: types.KeyHash(title), Title: title, State: types.DirProposed, ProposerSession: "s-plan"}
}

func (e *env) accept(t *testing.T, id, title string) types.Direction {
	t.Helper()
	d := e.dir(id, title)
	if err := e.s.AcceptProposal(ctx, d, types.NoveltyRow{Outcome: types.NoveltyAccepted}); err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *env) epoch(seq int64, trig types.Trigger) types.Epoch {
	return types.Epoch{Profile: e.prof, Generation: e.gen, Seq: seq, Trigger: trig, StartedAt: 1000 + seq, ModelPin: "anthropic/m1/v1", State: types.EpochOpen}
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) auditOK(t *testing.T) {
	t.Helper()
	if err := e.core.VerifyAudit(ctx, e.prof, e.gen); err != nil {
		t.Fatalf("audit chain: %v", err)
	}
}
