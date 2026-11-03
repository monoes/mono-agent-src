package store

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/storage"
)

func sumOf(t *testing.T, s *SQLStore, kind string) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(n),0) FROM doomsday_counter_deltas WHERE kind = ?`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func auditCount(t *testing.T, s *SQLStore) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM doomsday_audit`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunCapsAndAnswerClassesAreInTheHash(t *testing.T) {
	base := charter("p", 1).Hash()
	muts := map[string]func(c *Charter){
		"run spend": func(c *Charter) { c.PerAsk.RunSpendMicro++ },
		"run tasks": func(c *Charter) { c.PerAsk.RunTasks++ },
		"class add": func(c *Charter) { c.PerAsk.AnswerClasses = append(c.PerAsk.AnswerClasses, "extra") },
		"class del": func(c *Charter) { c.PerAsk.AnswerClasses = c.PerAsk.AnswerClasses[:1] },
	}
	for name, m := range muts {
		c := charter("p", 1)
		m(&c)
		if c.Hash() == base {
			t.Errorf("%s did not change the hash", name)
		}
	}
	a := charter("p", 1)
	a.PerAsk.AnswerClasses = []string{"clarify", "context"}
	if a.Hash() != base {
		t.Error("class order changed the hash")
	}
}

func TestRunCapAndClassValidation(t *testing.T) {
	cases := map[string]func(c *Charter){
		"declared without run spend":  func(c *Charter) { c.PerAsk.RunSpendMicro = 0 },
		"declared without run tasks":  func(c *Charter) { c.PerAsk.RunTasks = 0 },
		"run spend below per-ask":     func(c *Charter) { c.PerAsk.RunSpendMicro = c.PerAsk.SpendMicro - 1 },
		"run tasks below per-ask":     func(c *Charter) { c.PerAsk.RunTasks = int64(c.PerAsk.Tasks) - 1 },
		"empty class":                 func(c *Charter) { c.PerAsk.AnswerClasses = []string{""} },
		"duplicate class":             func(c *Charter) { c.PerAsk.AnswerClasses = []string{"a", "a"} },
		"caps without declaration":    func(c *Charter) { c.PerAsk = PerAsk{RunTasks: 5} },
		"classes without declaration": func(c *Charter) { c.PerAsk = PerAsk{AnswerClasses: []string{"a"}} },
	}
	for name, m := range cases {
		c := charter("p", 1)
		m(&c)
		if err := c.Validate(); !errors.Is(err, ErrInvalidCharter) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// No classes is valid: ask.answer stays off.
	c := charter("p", 1)
	c.PerAsk.AnswerClasses = nil
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
	// And the round trip through the store keeps them.
	s, _ := newStore(t)
	g := charter("p", 1)
	if _, err := s.Enter(ctx, g, g.Hash(), "op"); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadCharter(ctx, "p", 1)
	if err != nil || got.PerAsk.RunTasks != 50 || len(got.PerAsk.AnswerClasses) != 2 {
		t.Fatalf("%+v %v", got.PerAsk, err)
	}
}

func charges(spend, tasks, caps int64) []Charge {
	return []Charge{{"spend", spend, 10}, {"tasks", tasks, 5}, {"caps", caps, 3}}
}

func TestConsumeManyAllOrNothing(t *testing.T) {
	s, _ := newStore(t)
	enter(t, s, "p")
	a := AuditEntry{Kind: "approval.consume", Payload: "x"}
	if err := s.ConsumeMany(ctx, "p", 1, "ap1", charges(4, 2, 1), a); err != nil {
		t.Fatal(err)
	}
	if sumOf(t, s, "spend") != 4 || sumOf(t, s, "tasks") != 2 || sumOf(t, s, "caps") != 1 {
		t.Fatal("charges not recorded")
	}
	before := auditCount(t, s)
	// The last kind is over its cap: the earlier kinds must not stay charged and no audit row may appear.
	err := s.ConsumeMany(ctx, "p", 1, "ap2", charges(1, 1, 3), a)
	var ol *OverLimitError
	if !errors.As(err, &ol) || !errors.Is(err, ErrOverLimit) || len(ol.Kinds) != 1 || ol.Kinds[0] != "caps" {
		t.Fatalf("got %v", err)
	}
	if sumOf(t, s, "spend") != 4 || sumOf(t, s, "tasks") != 2 || sumOf(t, s, "caps") != 1 || auditCount(t, s) != before {
		t.Fatal("a later-kind failure over-counted an earlier kind or left an audit row")
	}
	// Every over kind is reported.
	err = s.ConsumeMany(ctx, "p", 1, "ap3", charges(7, 4, 1), a)
	if !errors.As(err, &ol) || len(ol.Kinds) != 2 {
		t.Fatalf("got %v", err)
	}
	// The same approval again is a replay and changes nothing.
	if err := s.ConsumeMany(ctx, "p", 1, "ap1", charges(1, 1, 1), a); !errors.Is(err, ErrAlreadyConsumed) {
		t.Fatalf("replay: %v", err)
	}
	if sumOf(t, s, "spend") != 4 || auditCount(t, s) != before {
		t.Fatal("replay changed state")
	}
	// The failed approval id was not burnt by the rollback: it can be used with smaller charges.
	if err := s.ConsumeMany(ctx, "p", 1, "ap2", charges(1, 1, 1), a); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	if err := s.VerifyAudit(ctx, "p", 1); err != nil {
		t.Fatal(err)
	}
}

func TestConsumeManyAuditFailureRollsBackCharges(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	if _, err := db.Exec(`CREATE TRIGGER boom BEFORE INSERT ON doomsday_audit WHEN NEW.kind = 'boom' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeMany(ctx, "p", 1, "ap1", charges(4, 2, 1), AuditEntry{Kind: "boom"}); err == nil {
		t.Fatal("audit failure not reported")
	}
	if sumOf(t, s, "spend")+sumOf(t, s, "tasks")+sumOf(t, s, "caps") != 0 {
		t.Fatal("charges survived a failed audit append")
	}
	if err := s.ConsumeMany(ctx, "p", 1, "ap1", charges(4, 2, 1), AuditEntry{Kind: "ok"}); err != nil {
		t.Fatalf("approval id burnt by the rollback: %v", err)
	}
}

func TestConsumeManyRefusals(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	a := AuditEntry{Kind: "k"}
	// N = 0, negative, empty, repeated kind: refused, nothing recorded.
	bad := map[string][]Charge{
		"zero":       {{"spend", 0, 10}},
		"zero mixed": {{"spend", 1, 10}, {"tasks", 0, 5}},
		"negative":   {{"spend", -1, 10}},
		"empty":      nil,
		"dup kind":   {{"spend", 1, 10}, {"spend", 1, 10}},
		"no kind":    {{"", 1, 10}},
		"neg limit":  {{"spend", 1, -1}},
	}
	for name, c := range bad {
		if err := s.ConsumeMany(ctx, "p", 1, "ap-"+name, c, a); !errors.Is(err, ErrOverLimit) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if sumOf(t, s, "spend")+sumOf(t, s, "tasks") != 0 || auditCount(t, s) != 1 {
		t.Fatal("a refused consume recorded something")
	}
	// ConsumeCumulative with N = 0 on the real store, too.
	if err := s.ConsumeCumulative(ctx, "p", 1, "spend", "z", 0, 10); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("N=0: %v", err)
	}
	// A stopped run takes no charge.
	walk(t, s, r, StateStopRequested)
	var ref *Refusal
	if err := s.ConsumeMany(ctx, "p", 1, "ap-stop", charges(1, 1, 1), a); !errors.As(err, &ref) {
		t.Fatalf("after stop: %v", err)
	}
	if sumOf(t, s, "spend") != 0 {
		t.Fatal("charged after stop")
	}
}

func TestConsumeManyContention(t *testing.T) {
	keyring.MockInit()
	st, err := storage.NewDatabase(t.TempDir() + "/c.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	if err := st.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	st.DB.SetMaxOpenConns(8)
	s := New(st.DB, testKeys())
	enter(t, s, "p")
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// tasks (cap 6) is the binding kind; spend (cap 100) must not leak charges of the losers.
			err := s.ConsumeMany(ctx, "p", 1, fmt.Sprint("ap", i), []Charge{{"spend", 1, 100}, {"tasks", 1, 6}}, AuditEntry{Kind: "c", Payload: fmt.Sprint(i)})
			if err == nil {
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	sp, tk := sumOf(t, s, "spend"), sumOf(t, s, "tasks")
	if tk > 6 || sp != tk || int64(ok.Load()) != tk {
		t.Fatalf("spend %d tasks %d successes %d", sp, tk, ok.Load())
	}
	if err := s.VerifyAudit(ctx, "p", 1); err != nil {
		t.Fatal(err)
	}
}
