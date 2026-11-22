### Task 7: Concurrency proofs and `Watch`

**Files:**
- Create: `internal/tasks/watch.go`
- Test: `internal/tasks/race_test.go`, `internal/tasks/watch_test.go`

**Interfaces:**
- Consumes: Tasks 3 to 6 (`Next`, `Add`, `Rev`, `Counts`); `storage.NewDatabase(path)` (opens a file without migrating it) and `testdb.Path(t)` (a migrated database file).
- Produces: `Change{Rev int64; Counts Counts}` and `(*Store).Watch(ctx, profileID string, interval time.Duration, fn func(Change))` (blocks until `ctx` ends; reports once at the start and again whenever the revision moves). The race tests prove the claim and idempotency rules hold across goroutines and across processes.

- [ ] **Step 1: Write the concurrency tests**

Create `internal/tasks/race_test.go`:

```go
package tasks

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

func TestConcurrentClaimsNeverShareATask(t *testing.T) {
	s, _, _ := newTestStore(t)
	const nTasks, nAgents = 6, 12
	for i := 0; i < nTasks; i++ {
		mustAdd(t, s, "default", fmt.Sprintf("t%d", i), true)
	}
	var mu sync.Mutex
	got := map[int64]string{}
	var wg sync.WaitGroup
	for a := 0; a < nAgents; a++ {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			task, err := s.Next(bg, "default", bot(name), true, 0)
			if err != nil {
				t.Errorf("%s: %v", name, err)
				return
			}
			if task == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if prev, dup := got[task.ID]; dup {
				t.Errorf("task #%d was claimed by %s and by %s", task.ID, prev, name)
			}
			got[task.ID] = name
		}(fmt.Sprintf("agent-%d", a))
	}
	wg.Wait()
	if len(got) != nTasks {
		t.Fatalf("%d of %d tasks were claimed", len(got), nTasks)
	}
}

func TestConcurrentAddsWithOneClientIDMakeOneTask(t *testing.T) {
	s, db, _ := newTestStore(t)
	var created int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.Add(bg, "default",
				AddInput{Title: "once", ClientID: "shared-1", SourceKind: SourceChrome}, Actor{Kind: Capture, Name: SourceChrome})
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				atomic.AddInt32(&created, 1)
			}
		}()
	}
	wg.Wait()
	if n := countWhere(t, db, "tasks", "client_id = 'shared-1'"); n != 1 || created != 1 {
		t.Fatalf("%d rows and %d creations, want one of each", n, created)
	}
}

// TestHelperProcess is not a test: the tests below run it in other processes,
// so that several processes share one database file with this one.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("TASKS_HELPER") != "1" {
		return
	}
	db, err := storage.NewDatabase(os.Getenv("TASKS_HELPER_DB"))
	if err != nil {
		fmt.Println("ERROR", err)
		os.Exit(1)
	}
	defer db.Close()
	s := NewStore(db.DB)
	switch os.Getenv("TASKS_HELPER_MODE") {
	case "claim":
		for {
			task, err := s.Next(bg, "default", bot(os.Getenv("TASKS_HELPER_NAME")), true, 0)
			if err != nil {
				fmt.Println("ERROR", err)
				os.Exit(1)
			}
			if task == nil {
				return
			}
			fmt.Printf("RESULT %d\n", task.ID)
		}
	case "add":
		_, created, err := s.Add(bg, "default",
			AddInput{Title: "once", ClientID: "shared-1", SourceKind: SourceOS}, Actor{Kind: Capture, Name: SourceOS})
		if err != nil {
			fmt.Println("ERROR", err)
			os.Exit(1)
		}
		fmt.Printf("RESULT %v\n", created)
	}
}

// runHelpers runs n helper processes at once against the database file and
// returns the RESULT lines they printed.
func runHelpers(t *testing.T, path, mode string, n int) []string {
	t.Helper()
	outs := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
			cmd.Env = append(os.Environ(),
				"TASKS_HELPER=1", "TASKS_HELPER_DB="+path, "TASKS_HELPER_MODE="+mode,
				fmt.Sprintf("TASKS_HELPER_NAME=proc-%d", i),
				"GORACE=atexit_sleep_ms=0") // the race runtime would otherwise sleep a second at exit
			outs[i], errs[i] = cmd.Output()
		}(i)
	}
	wg.Wait()
	var lines []string
	for i := range outs {
		if errs[i] != nil {
			t.Fatalf("helper %d: %v\n%s", i, errs[i], outs[i])
		}
		for _, l := range strings.Split(string(outs[i]), "\n") {
			if rest, ok := strings.CutPrefix(l, "RESULT "); ok {
				lines = append(lines, rest)
			}
		}
	}
	return lines
}

func TestClaimsAcrossProcessesNeverShareATask(t *testing.T) {
	path := testdb.Path(t)
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(db.DB)
	const nTasks = 20
	for i := 0; i < nTasks; i++ {
		mustAdd(t, s, "default", fmt.Sprintf("t%d", i), true)
	}
	db.Close()

	lines := runHelpers(t, path, "claim", 4)
	seen := map[string]bool{}
	for _, l := range lines {
		if seen[l] {
			t.Fatalf("task #%s was claimed twice: %v", l, lines)
		}
		seen[l] = true
	}
	if len(seen) != nTasks {
		t.Fatalf("%d of %d tasks were claimed: %v", len(seen), nTasks, lines)
	}
}

func TestAddsWithOneClientIDAcrossProcessesMakeOneTask(t *testing.T) {
	path := testdb.Path(t)
	lines := runHelpers(t, path, "add", 5)
	created := 0
	for _, l := range lines {
		if l == "true" {
			created++
		}
	}
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := countWhere(t, db.DB, "tasks", "client_id = 'shared-1'"); n != 1 || created != 1 || len(lines) != 5 {
		t.Fatalf("%d rows, %d creations among %d answers: %v", n, created, len(lines), lines)
	}
}
```

- [ ] **Step 2: Run the concurrency tests**

Run: `go test ./internal/tasks/ -run 'TestConcurrent|TestClaimsAcross|TestAddsWithOne' -count=1`
Expected: PASS. They exercise code that already exists and prove that the rules of Tasks 3 and 6 hold under contention. If one fails, the bug is in `store.go` or `claims.go` (is `tx` really `BEGIN IMMEDIATE`?): fix it there, do not weaken the test.

- [ ] **Step 3: Write the failing test for `Watch`**

Create `internal/tasks/watch_test.go`:

```go
package tasks

import (
	"context"
	"testing"
	"time"
)

func waitChange(t *testing.T, ch <-chan Change) Change {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no change was reported within 5 seconds")
		return Change{}
	}
}

func TestWatchReportsTheStartAndEachChange(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	changes := make(chan Change, 10)
	done := make(chan struct{})
	go func() {
		s.Watch(ctx, "default", 5*time.Millisecond, func(c Change) { changes <- c })
		close(done)
	}()
	if first := waitChange(t, changes); first.Rev != 0 {
		t.Fatalf("the first report is the starting point: %+v", first)
	}
	mustAdd(t, s, "default", "x", false)
	if second := waitChange(t, changes); second.Rev != 1 || second.Counts.Inbox != 1 {
		t.Fatalf("after an add: %+v", second)
	}
	select {
	case c := <-changes:
		t.Fatalf("no write, no report: %+v", c)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not stop when its context ended")
	}
}
```

- [ ] **Step 4: Run it to see it fail**

Run: `go test ./internal/tasks/ -run TestWatch -count=1`
Expected: FAIL to compile (`s.Watch undefined`, `Change undefined`).

- [ ] **Step 5: Write `internal/tasks/watch.go`**

```go
package tasks

import (
	"context"
	"time"
)

// Change is what Watch reports: the board revision and the counts at it.
type Change struct {
	Rev    int64
	Counts Counts
}

// Watch calls fn once at the start and then each time the profile's board
// revision moves, polling every interval (two seconds when it is not
// positive), until ctx ends. A failed poll is skipped: the next one tries
// again. It blocks, so run it in a goroutine.
func (s *Store) Watch(ctx context.Context, profileID string, interval time.Duration, fn func(Change)) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	last := int64(-1)
	poll := func() {
		rev, err := s.Rev(ctx, profileID)
		if err != nil || rev == last {
			return
		}
		counts, err := s.Counts(ctx, profileID)
		if err != nil {
			return
		}
		last = rev
		fn(Change{Rev: rev, Counts: counts})
	}
	poll()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			poll()
		}
	}
}
```

- [ ] **Step 6: Run the whole package, also under the race detector**

Run: `gofmt -l internal/tasks` then `go vet ./internal/tasks/` then `go test ./internal/tasks/ -count=1` then `go test -race ./internal/tasks/ -count=1`
Expected: everything PASSES both ways. The race run takes a while (the migrated template database is built once; the property test makes 300 moves); do not shorten any test to hurry it.

- [ ] **Step 7: Commit**

```
git add internal/tasks/watch.go internal/tasks/race_test.go internal/tasks/watch_test.go
```
then
```
git commit -m "feat(tasks): Watch, and proofs that claims and client ids hold across goroutines and processes" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

