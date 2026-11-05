package evaluation

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// Record reads a column and writes the merge back. A writer that commits between the two must not make it fail
// with SQLITE_BUSY_SNAPSHOT (517, which busy_timeout does not wait for): the write lock is taken first (#1459).
// The competing writer runs inside Record's own gap on a connection that does not wait (busy_timeout 0): where
// Record holds no lock yet it commits and Record's write fails; where Record holds the lock it is refused at once
// and is applied after Record, so the outcome is the same on every run without a sleep.
func TestRecordSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	path := testdb.Path(t)
	d, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })

	ctx := context.Background()
	s := NewStore(d.DB, "p")
	run, _ := s.CreateRun(ctx, "wf", nil)
	c := &Case{}
	if err := s.AddCase(ctx, run.ID, c); err != nil {
		t.Fatal(err)
	}
	competing := func() error {
		_, err := other.ExecContext(ctx, `UPDATE evaluation_results SET metrics = json_set(metrics, '$.y', 2) WHERE id = ?`, c.ID)
		return err
	}
	var refused error
	afterRecordRead = func() { refused = competing() }
	t.Cleanup(func() { afterRecordRead = nil })

	if err := Record(ctx, d.DB, run.ID, c.ID, KindMetrics, map[string]interface{}{"x": 1.0}); err != nil {
		t.Fatalf("Record failed when a writer committed between its read and its write: %v", err)
	}
	afterRecordRead = nil
	if refused != nil {
		if err := competing(); err != nil { // refused while Record held the lock: now it is Record's turn to have finished
			t.Fatal(err)
		}
	}
	if err := s.LoadCase(ctx, c); err != nil {
		t.Fatal(err)
	}
	if c.Metrics["x"] != 1.0 || c.Metrics["y"] != 2.0 {
		t.Errorf("metrics %v, want x and y both kept", c.Metrics)
	}
}

// Two Record calls on different keys of one case, at once, keep both keys and neither fails.
func TestRecordConcurrentCallsOnOneCaseKeepEveryKey(t *testing.T) {
	db := testdb.Open(t).DB
	ctx := context.Background()
	s := NewStore(db, "p")
	run, _ := s.CreateRun(ctx, "wf", nil)
	c := &Case{}
	if err := s.AddCase(ctx, run.ID, c); err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Record(ctx, db, run.ID, c.ID, KindMetrics, map[string]interface{}{string(rune('a' + i)): float64(i)})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
	if err := s.LoadCase(ctx, c); err != nil {
		t.Fatal(err)
	}
	if len(c.Metrics) != n {
		t.Errorf("metrics %v, want %d keys", c.Metrics, n)
	}
}
