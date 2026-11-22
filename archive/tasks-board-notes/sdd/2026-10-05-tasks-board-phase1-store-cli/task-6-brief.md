### Task 6: The agent's verbs: Next, Claim, Comment, Finish, Release

**Files:**
- Create: `internal/tasks/claims.go`
- Test: `internal/tasks/claims_test.go`

**Interfaces:**
- Consumes: Task 3's `tx`, `getTx`, `event`, `bump`, `edgePosition`; Task 5's `moveTx` is not used here; Task 2's `ClaimedError`, `needName` rules.
- Produces:
  - `(*Store).Next(ctx, profileID string, actor Actor, claim bool, lease time.Duration) (*Task, error)`: peek when `claim` is false (any actor), claim atomically when true (an `Agent` with a valid name); a nil task and a nil error mean nothing is available.
  - `(*Store).Claim(ctx, profileID string, id int64, actor Actor, lease time.Duration) (Task, error)`
  - `(*Store).Comment(ctx, profileID string, id int64, text string, actor Actor) (Task, error)` (a `Human` may comment on any task; an `Agent` only on a task it holds, and the comment renews its lease)
  - `(*Store).Finish(ctx, profileID string, id int64, o Outcome, actor Actor) (Task, error)`
  - `(*Store).Release(ctx, profileID string, id int64, note string, actor Actor) (Task, error)`
  - unexported: `needName(Actor) error`, `clampLease`, `later`, `s.pickNext`, `s.claimTx`, `s.heldBy`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tasks/claims_test.go`:

```go
package tasks

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNextPeeksTheTopReadyTask(t *testing.T) {
	s, _, _ := newTestStore(t)
	if got, err := s.Next(bg, "default", bot("b"), false, 0); err != nil || got != nil {
		t.Fatalf("an empty board: %+v, %v", got, err)
	}
	mustAdd(t, s, "default", "in the inbox", false) // never offered
	first := mustAdd(t, s, "default", "first", true)
	second := mustAdd(t, s, "default", "second", true)
	got, err := s.Next(bg, "default", bot("b"), false, 0)
	if err != nil || got == nil || got.ID != first.ID {
		t.Fatalf("next: %+v, %v, want #%d", got, err, first.ID)
	}
	if again, _ := s.Next(bg, "default", bot("b"), false, 0); again == nil || again.ID != first.ID || again.Status != StatusReady {
		t.Errorf("a peek claims nothing: %+v", again)
	}
	if _, err := s.Move(bg, "default", second.ID, StatusReady, Placement{Top: true}, human); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Next(bg, "default", bot("b"), false, 0); got == nil || got.ID != second.ID {
		t.Errorf("the top of the column is next: %+v", got)
	}
}

func TestNextClaimTakesItWithALease(t *testing.T) {
	s, _, c := newTestStore(t)
	task := mustAdd(t, s, "default", "work", true)
	got, err := s.Next(bg, "default", bot("claude-1"), true, 0)
	if err != nil || got == nil || got.ID != task.ID || got.Status != StatusInProgress {
		t.Fatalf("next --claim: %+v, %v", got, err)
	}
	if got.Claim == nil || got.Claim.By != "claude-1" || !got.Claim.Until.Equal(c.t.Add(DefaultLease)) || got.Claim.Stale {
		t.Errorf("claim: %+v", got.Claim)
	}
	if got.LastEvent == nil || got.LastEvent.Kind != "claimed" || got.LastEvent.Actor != "claude-1" {
		t.Errorf("last event: %+v", got.LastEvent)
	}
	if again, err := s.Next(bg, "default", bot("claude-2"), true, 0); err != nil || again != nil {
		t.Errorf("nothing is left to claim: %+v, %v", again, err)
	}
	if rev, _ := s.Rev(bg, "default"); rev != 2 {
		t.Errorf("revision %d, want 2 (the add and the claim)", rev)
	}
}

func TestClaimRefusals(t *testing.T) {
	s, _, _ := newTestStore(t)
	inbox, ready := mustAdd(t, s, "default", "inbox", false), mustAdd(t, s, "default", "ready", true)
	cases := []struct {
		name  string
		id    int64
		actor Actor
		want  error
	}{
		{"an inbox task", inbox.ID, bot("b"), ErrNotReady},
		{"an unknown task", 99999, bot("b"), ErrNotFound},
		{"the operator", ready.ID, human, ErrInvalid},
		{"an agent with no name", ready.ID, bot(""), ErrInvalid},
		{"an agent with a bad name", ready.ID, bot("bad name"), ErrInvalid},
		{"a capture", ready.ID, Actor{Kind: Capture, Name: SourceOS}, ErrInvalid},
	}
	for _, c := range cases {
		if _, err := s.Claim(bg, "default", c.id, c.actor, 0); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	held, err := s.Claim(bg, "default", ready.ID, bot("one"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Claim(bg, "default", ready.ID, bot("two"), 0)
	var ce *ClaimedError
	if !errors.Is(err, ErrClaimed) || !errors.As(err, &ce) || ce.By != "one" || !ce.Until.Equal(held.Claim.Until) {
		t.Errorf("a task another agent holds is never taken: %v", err)
	}
	manual := mustAdd(t, s, "default", "by hand", true)
	if _, err := s.Move(bg, "default", manual.ID, StatusInProgress, Placement{}, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(bg, "default", manual.ID, bot("one"), 0); !errors.Is(err, ErrNotReady) {
		t.Errorf("a task the operator works on: %v, want ErrNotReady", err)
	}
}

func TestSameNameClaimRenewsButNeverShortens(t *testing.T) {
	s, _, c := newTestStore(t)
	start := c.t
	task := mustAdd(t, s, "default", "long job", true)
	if _, err := s.Claim(bg, "default", task.ID, bot("one"), 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	c.advance(10 * time.Minute)
	again, err := s.Claim(bg, "default", task.ID, bot("one"), 0) // the default lease is shorter
	if err != nil || !again.Claim.Until.Equal(start.Add(4*time.Hour)) {
		t.Fatalf("claiming again must not shorten the lease: %+v, %v", again.Claim, err)
	}
	longer, err := s.Claim(bg, "default", task.ID, bot("one"), 6*time.Hour)
	if err != nil || !longer.Claim.Until.Equal(c.t.Add(6*time.Hour)) {
		t.Fatalf("a longer lease extends: %+v, %v", longer.Claim, err)
	}
	capped := mustAdd(t, s, "default", "capped", true)
	got, err := s.Claim(bg, "default", capped.ID, bot("one"), 48*time.Hour)
	if err != nil || !got.Claim.Until.Equal(c.t.Add(MaxLease)) {
		t.Errorf("a lease is at most 24 hours: %+v, %v", got.Claim, err)
	}
}

func TestStaleClaimsAreTakenOver(t *testing.T) {
	s, _, c := newTestStore(t)
	task := mustAdd(t, s, "default", "abandoned", true)
	if _, err := s.Claim(bg, "default", task.ID, bot("one"), 0); err != nil {
		t.Fatal(err)
	}
	c.advance(DefaultLease - time.Second)
	if _, err := s.Claim(bg, "default", task.ID, bot("two"), 0); !errors.Is(err, ErrClaimed) {
		t.Fatalf("a second before the lease ends the claim holds: %v", err)
	}
	if cn, _ := s.Counts(bg, "default"); cn.Stale != 0 {
		t.Errorf("stale claims a second before the end: %d", cn.Stale)
	}
	c.advance(time.Second) // exactly at the lease's end
	if cn, _ := s.Counts(bg, "default"); cn.Stale != 1 {
		t.Errorf("a claim that expires exactly now is stale: %d", cn.Stale)
	}
	ready := mustAdd(t, s, "default", "fresh", true)
	if got, _ := s.Next(bg, "default", bot("two"), false, 0); got == nil || got.ID != ready.ID {
		t.Fatalf("a ready task comes before a stale claim: %+v", got)
	}
	if _, err := s.Claim(bg, "default", ready.ID, bot("two"), 0); err != nil {
		t.Fatal(err)
	}
	took, err := s.Next(bg, "default", bot("three"), true, 0)
	if err != nil || took == nil || took.ID != task.ID || took.Claim.By != "three" {
		t.Fatalf("a stale claim is taken over by next: %+v, %v", took, err)
	}
	if took.LastEvent == nil || took.LastEvent.Kind != "reclaimed" {
		t.Errorf("last event: %+v", took.LastEvent)
	}
}

func TestCommentRenewsButNeverShortens(t *testing.T) {
	s, _, c := newTestStore(t)
	start := c.t
	long := mustAdd(t, s, "default", "long", true)
	if _, err := s.Claim(bg, "default", long.ID, bot("one"), 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	c.advance(10 * time.Minute)
	got, err := s.Comment(bg, "default", long.ID, "still going", bot("one"))
	if err != nil || !got.Claim.Until.Equal(start.Add(4*time.Hour)) {
		t.Fatalf("a comment must not shorten a four hour lease: %+v, %v", got.Claim, err)
	}
	short := mustAdd(t, s, "default", "short", true)
	if _, err := s.Claim(bg, "default", short.ID, bot("two"), 0); err != nil {
		t.Fatal(err)
	}
	c.advance(25 * time.Minute)
	got, err = s.Comment(bg, "default", short.ID, "progress", bot("two"))
	if err != nil || !got.Claim.Until.Equal(c.t.Add(DefaultLease)) {
		t.Fatalf("a comment renews the default lease: %+v, %v", got.Claim, err)
	}
}

func TestCommentRules(t *testing.T) {
	s, _, _ := newTestStore(t)
	held := mustAdd(t, s, "default", "held", true)
	inbox := mustAdd(t, s, "default", "inbox", false)
	ready := mustAdd(t, s, "default", "ready", true)
	if _, err := s.Claim(bg, "default", held.ID, bot("one"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Comment(bg, "default", held.ID, "not mine", bot("two")); !errors.Is(err, ErrNotClaimant) {
		t.Errorf("another agent: %v, want ErrNotClaimant", err)
	}
	if _, err := s.Comment(bg, "default", ready.ID, "not claimed", bot("one")); !errors.Is(err, ErrNotClaimant) {
		t.Errorf("a task nobody holds: %v, want ErrNotClaimant", err)
	}
	for _, id := range []int64{held.ID, inbox.ID} {
		if _, err := s.Comment(bg, "default", id, "from the operator", human); err != nil {
			t.Errorf("the operator comments on #%d: %v", id, err)
		}
	}
	if _, err := s.Comment(bg, "default", held.ID, " \x1b ", bot("one")); !errors.Is(err, ErrInvalid) {
		t.Errorf("an empty comment: %v", err)
	}
	if _, err := s.Comment(bg, "default", held.ID, "hi", Actor{Kind: Capture, Name: SourceOS}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a capture: %v", err)
	}
	if _, err := s.Comment(bg, "default", held.ID, "red \x1b[31mtext", bot("one")); err != nil {
		t.Fatal(err)
	}
	_, events, _ := s.Get(bg, "default", held.ID)
	last := events[len(events)-1]
	if last.Kind != "comment" || strings.ContainsRune(last.Note, 0x1b) || last.Actor != "one" {
		t.Errorf("last event: %+v", last)
	}
}

func TestEventCapRefusesCommentsOnly(t *testing.T) {
	s, db, _ := newTestStore(t)
	task := mustAdd(t, s, "default", "chatty", true)
	if _, err := s.Claim(bg, "default", task.ID, bot("one"), 0); err != nil {
		t.Fatal(err)
	}
	have := countWhere(t, db, "task_events", "task_id = ?", task.ID)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := have; i < MaxEventsPerTask; i++ {
		if _, err := tx.Exec(`INSERT INTO task_events (task_id, at, actor, kind) VALUES (?, ?, 'one', 'comment')`, task.ID, rowTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Comment(bg, "default", task.ID, "one more", bot("one")); !errors.Is(err, ErrLimit) {
		t.Fatalf("the 501st event as a comment: %v, want ErrLimit", err)
	}
	got, err := s.Finish(bg, "default", task.ID, Outcome{Result: "done anyway"}, bot("one"))
	if err != nil || got.Status != StatusReview {
		t.Fatalf("a change of state is always recorded: %+v, %v", got, err)
	}
	if n := countWhere(t, db, "task_events", "task_id = ?", task.ID); n != MaxEventsPerTask+1 {
		t.Errorf("%d events", n)
	}
}

func TestFinishHandsTheTaskBack(t *testing.T) {
	s, _, _ := newTestStore(t)
	older := mustAdd(t, s, "default", "older review", true)
	task := mustAdd(t, s, "default", "job", true)
	for _, id := range []int64{older.ID, task.ID} {
		if _, err := s.Claim(bg, "default", id, bot("one"), 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Finish(bg, "default", older.ID, Outcome{Result: "ok"}, bot("one")); err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]Outcome{"both": {Result: "r", Question: "q"}, "neither": {}, "blank": {Result: " \x1b "}} {
		if _, err := s.Finish(bg, "default", task.ID, o, bot("one")); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.Finish(bg, "default", task.ID, Outcome{Result: "r"}, bot("two")); !errors.Is(err, ErrNotClaimant) {
		t.Errorf("another agent: %v, want ErrNotClaimant", err)
	}
	if _, err := s.Finish(bg, "default", task.ID, Outcome{Result: "r"}, human); !errors.Is(err, ErrInvalid) {
		t.Errorf("the operator does not finish: %v", err)
	}
	got, err := s.Finish(bg, "default", task.ID, Outcome{Question: "which database?"}, bot("one"))
	if err != nil || got.Status != StatusReview || got.Claim != nil {
		t.Fatalf("finish with a question: %+v, %v", got, err)
	}
	if got.LastEvent == nil || got.LastEvent.Kind != "question" {
		t.Errorf("last event: %+v", got.LastEvent)
	}
	if ids := column(t, s, StatusReview); !sameIDs(ids, []int64{task.ID, older.ID}) {
		t.Errorf("review is newest first: %v", ids)
	}
	if _, err := s.Finish(bg, "default", task.ID, Outcome{Result: "again"}, bot("one")); !errors.Is(err, ErrNotClaimant) {
		t.Errorf("a task that is no longer in progress: %v", err)
	}
}

func TestReleaseGivesTheTaskBackBehindTheOthers(t *testing.T) {
	s, _, _ := newTestStore(t)
	task := mustAdd(t, s, "default", "needs the vpn", true)
	other := mustAdd(t, s, "default", "other", true)
	if _, err := s.Claim(bg, "default", task.ID, bot("one"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Release(bg, "default", task.ID, "x", bot("two")); !errors.Is(err, ErrNotClaimant) {
		t.Errorf("another agent: %v", err)
	}
	got, err := s.Release(bg, "default", task.ID, "needs the VPN", bot("one"))
	if err != nil || got.Status != StatusReady || got.Claim != nil {
		t.Fatalf("release: %+v, %v", got, err)
	}
	if got.LastEvent == nil || got.LastEvent.Kind != "released" {
		t.Errorf("last event: %+v", got.LastEvent)
	}
	if ids := column(t, s, StatusReady); !sameIDs(ids, []int64{other.ID, task.ID}) {
		t.Errorf("a released task goes behind the others, so the agent that gave it up is not offered it again: %v", ids)
	}
	if next, _ := s.Next(bg, "default", bot("one"), false, 0); next == nil || next.ID != other.ID {
		t.Errorf("next: %+v", next)
	}
}

func TestTheOperatorMovingAHeldCardEndsTheClaim(t *testing.T) {
	s, _, _ := newTestStore(t)
	task := mustAdd(t, s, "default", "held", true)
	if _, err := s.Claim(bg, "default", task.ID, bot("one"), 0); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Move(bg, "default", task.ID, StatusInProgress, Placement{Top: true}, human); err != nil || got.Claim == nil || got.Claim.By != "one" {
		t.Fatalf("reordering a held card keeps the claim: %+v, %v", got, err)
	}
	got, err := s.Move(bg, "default", task.ID, StatusReady, Placement{}, human)
	if err != nil || got.Status != StatusReady || got.Claim != nil {
		t.Fatalf("moving it out: %+v, %v", got, err)
	}
	_, events, _ := s.Get(bg, "default", task.ID)
	n := len(events)
	if events[n-2].Kind != "released" || events[n-1].Kind != "moved" {
		t.Errorf("the claim's end is recorded before the move: %+v", events[n-2:])
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/tasks/ -run 'TestNext|TestClaim|TestSameName|TestStale|TestComment|TestEventCap|TestFinish|TestRelease|TestTheOperatorMoving' -count=1`
Expected: FAIL to compile (`s.Next undefined`, ...).

- [ ] **Step 3: Write `internal/tasks/claims.go`**

```go
package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func clampLease(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return DefaultLease
	case d > MaxLease:
		return MaxLease
	}
	return d
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// needName refuses what is not an agent with a valid name: a claim is held
// under a name, the same one for the whole task.
func needName(a Actor) error {
	switch {
	case a.Kind != Agent:
		return invalid("only an agent claims, comments on or finishes a task; the operator moves the cards")
	case a.Name == "":
		return invalid("an agent must name itself: pass --as NAME, the same name for the whole task")
	case !nameRE.MatchString(a.Name):
		return invalid("an agent name is 1-%d characters of letters, digits and . _ # @ : -", MaxNameLen)
	}
	return nil
}

// pickNext is the id of the task Next would take: the top of Ready, else the
// stale claim whose lease ended first. 0 means there is none.
func (s *Store) pickNext(ctx context.Context, x dbx, profileID string) (int64, error) {
	var id int64
	err := x.QueryRowContext(ctx,
		`SELECT id FROM tasks WHERE profile_id = ? AND status = 'ready' ORDER BY position, id LIMIT 1`, profileID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("tasks: picking the next task: %w", err)
	}
	err = x.QueryRowContext(ctx,
		`SELECT id FROM tasks WHERE profile_id = ? AND status = 'in_progress' AND claimed_by <> '' AND claim_until <= ?
		 ORDER BY claim_until, id LIMIT 1`, profileID, s.stamp()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("tasks: picking the next task: %w", err)
	}
	return id, nil
}

// Next returns the task an agent should work next. Without claim it only looks
// (two agents that look may see the same task); with claim it picks and claims
// in one transaction, so two agents never get the same one. A nil task and a nil
// error mean there is nothing to do.
func (s *Store) Next(ctx context.Context, profileID string, actor Actor, claim bool, lease time.Duration) (*Task, error) {
	if !claim {
		id, err := s.pickNext(ctx, s.db, profileID)
		if err != nil || id == 0 {
			return nil, err
		}
		t, err := s.getTx(ctx, s.db, profileID, id)
		if err != nil {
			return nil, err
		}
		return &t, nil
	}
	if err := needName(actor); err != nil {
		return nil, err
	}
	var out *Task
	err := s.tx(ctx, func(x dbx) error {
		id, err := s.pickNext(ctx, x, profileID)
		if err != nil || id == 0 {
			return err
		}
		t, err := s.claimTx(ctx, x, profileID, id, actor, lease)
		if err != nil {
			return err
		}
		if err := s.bump(ctx, x, profileID); err != nil {
			return err
		}
		out = &t
		return nil
	})
	return out, err
}

// Claim takes a task for an agent, or renews the lease of one it already holds.
func (s *Store) Claim(ctx context.Context, profileID string, id int64, actor Actor, lease time.Duration) (Task, error) {
	if err := needName(actor); err != nil {
		return Task{}, err
	}
	var out Task
	err := s.tx(ctx, func(x dbx) error {
		t, err := s.claimTx(ctx, x, profileID, id, actor, lease)
		if err != nil {
			return err
		}
		if err := s.bump(ctx, x, profileID); err != nil {
			return err
		}
		out = t
		return nil
	})
	return out, err
}

// claimTx claims task id in the caller's write transaction: one conditional
// UPDATE that only holds while the task is as it was read.
func (s *Store) claimTx(ctx context.Context, x dbx, profileID string, id int64, actor Actor, lease time.Duration) (Task, error) {
	cur, err := s.getTx(ctx, x, profileID, id)
	if err != nil {
		return Task{}, err
	}
	now := s.now().UTC()
	name := actor.Label()
	until := now.Add(clampLease(lease))
	kind, note := "claimed", ""
	pos := cur.Position
	holder := "" // who holds the task now, which the UPDATE below must still find
	switch {
	case cur.Status == StatusReady:
		if pos, err = s.edgePosition(ctx, x, profileID, StatusInProgress, false, id); err != nil {
			return Task{}, err
		}
	case cur.Status == StatusInProgress && cur.Claim != nil && cur.Claim.By == name:
		note = "renewed"
		until = later(cur.Claim.Until, until)
		holder = name
	case cur.Status == StatusInProgress && cur.Claim != nil && cur.Claim.Stale:
		kind, note = "reclaimed", "the claim of "+cur.Claim.By+" had expired"
		holder = cur.Claim.By
	case cur.Status == StatusInProgress && cur.Claim != nil:
		return Task{}, &ClaimedError{By: cur.Claim.By, Until: cur.Claim.Until}
	case cur.Status == StatusInProgress:
		return Task{}, notReady("task #%d is being worked on by the operator", id)
	default:
		return Task{}, notReady("task #%d is %s: only a ready task can be claimed", id, cur.Status)
	}
	res, err := x.ExecContext(ctx,
		`UPDATE tasks SET status = 'in_progress', claimed_by = ?, claim_until = ?, position = ?, updated_at = ?
		 WHERE id = ? AND profile_id = ? AND status = ? AND claimed_by = ?`,
		name, until.Format(timeFmt), pos, now.Format(timeFmt), id, profileID, string(cur.Status), holder)
	if err != nil {
		return Task{}, fmt.Errorf("tasks: claiming #%d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Task{}, fmt.Errorf("tasks: claiming #%d: the task changed under the write lock", id)
	}
	if err := s.event(ctx, x, id, now.Format(timeFmt), name, kind, string(cur.Status), string(StatusInProgress), note); err != nil {
		return Task{}, err
	}
	return s.getTx(ctx, x, profileID, id)
}

// heldBy returns the task when the agent holds it (in progress, claimed under
// its name), else ErrNotClaimant.
func (s *Store) heldBy(ctx context.Context, x dbx, profileID string, id int64, actor Actor) (Task, error) {
	cur, err := s.getTx(ctx, x, profileID, id)
	if err != nil {
		return Task{}, err
	}
	if cur.Status != StatusInProgress || cur.Claim == nil || cur.Claim.By != actor.Label() {
		return Task{}, notClaimant("task #%d is not held by %s: claim it first", id, actor.Label())
	}
	return cur, nil
}

// Comment adds a note to a task's history. The operator comments on any task;
// an agent only on one it holds, and its comment renews the lease (never
// shortening it). A comment is refused once the task has MaxEventsPerTask events.
func (s *Store) Comment(ctx context.Context, profileID string, id int64, text string, actor Actor) (Task, error) {
	text = cutBytes(cleanText(text), MaxCommentBytes)
	if text == "" {
		return Task{}, invalid("a comment needs text")
	}
	switch actor.Kind {
	case Agent:
		if err := needName(actor); err != nil {
			return Task{}, err
		}
	case Human:
	default:
		return Task{}, invalid("only the operator and agents comment")
	}
	var out Task
	err := s.tx(ctx, func(x dbx) error {
		var cur Task
		var err error
		if actor.Kind == Agent {
			cur, err = s.heldBy(ctx, x, profileID, id, actor)
		} else {
			cur, err = s.getTx(ctx, x, profileID, id)
		}
		if err != nil {
			return err
		}
		var n int
		if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE task_id = ?`, id).Scan(&n); err != nil {
			return fmt.Errorf("tasks: counting events: %w", err)
		}
		if n >= MaxEventsPerTask {
			return limit("task #%d has %d events: finish or release it instead of commenting", id, n)
		}
		now := s.now().UTC()
		stamp := now.Format(timeFmt)
		if actor.Kind == Agent {
			until := later(cur.Claim.Until, now.Add(DefaultLease))
			_, err = x.ExecContext(ctx, `UPDATE tasks SET claim_until = ?, updated_at = ? WHERE id = ? AND profile_id = ?`,
				until.Format(timeFmt), stamp, id, profileID)
		} else {
			_, err = x.ExecContext(ctx, `UPDATE tasks SET updated_at = ? WHERE id = ? AND profile_id = ?`, stamp, id, profileID)
		}
		if err != nil {
			return fmt.Errorf("tasks: commenting on #%d: %w", id, err)
		}
		if err := s.event(ctx, x, id, stamp, actor.Label(), "comment", "", "", text); err != nil {
			return err
		}
		if err := s.bump(ctx, x, profileID); err != nil {
			return err
		}
		out, err = s.getTx(ctx, x, profileID, id)
		return err
	})
	return out, err
}

// Finish hands a held task back to the operator, in Review, with a result or a
// question (exactly one).
func (s *Store) Finish(ctx context.Context, profileID string, id int64, o Outcome, actor Actor) (Task, error) {
	if err := needName(actor); err != nil {
		return Task{}, err
	}
	result := cutBytes(cleanText(o.Result), MaxCommentBytes)
	question := cutBytes(cleanText(o.Question), MaxCommentBytes)
	if (result == "") == (question == "") {
		return Task{}, invalid("give exactly one of a result and a question")
	}
	kind, note := "result", result
	if question != "" {
		kind, note = "question", question
	}
	return s.handBack(ctx, profileID, id, actor, StatusReview, kind, note)
}

// Release gives a held task back to Ready, behind the others, so that the agent
// that gave it up is not offered it again at once.
func (s *Store) Release(ctx context.Context, profileID string, id int64, note string, actor Actor) (Task, error) {
	if err := needName(actor); err != nil {
		return Task{}, err
	}
	return s.handBack(ctx, profileID, id, actor, StatusReady, "released", cutBytes(cleanText(note), MaxCommentBytes))
}

// handBack ends the agent's claim: the task goes to `to` (Review at the top,
// Ready at the bottom) and the event says why.
func (s *Store) handBack(ctx context.Context, profileID string, id int64, actor Actor, to Status, kind, note string) (Task, error) {
	var out Task
	err := s.tx(ctx, func(x dbx) error {
		if _, err := s.heldBy(ctx, x, profileID, id, actor); err != nil {
			return err
		}
		pos, err := s.edgePosition(ctx, x, profileID, to, atTop(to), id)
		if err != nil {
			return err
		}
		stamp := s.stamp()
		if _, err := x.ExecContext(ctx,
			`UPDATE tasks SET status = ?, claimed_by = '', claim_until = '', position = ?, updated_at = ? WHERE id = ? AND profile_id = ?`,
			string(to), pos, stamp, id, profileID); err != nil {
			return fmt.Errorf("tasks: handing back #%d: %w", id, err)
		}
		if err := s.event(ctx, x, id, stamp, actor.Label(), kind, string(StatusInProgress), string(to), note); err != nil {
			return err
		}
		if err := s.bump(ctx, x, profileID); err != nil {
			return err
		}
		out, err = s.getTx(ctx, x, profileID, id)
		return err
	})
	return out, err
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `gofmt -l internal/tasks` then `go vet ./internal/tasks/` then `go test ./internal/tasks/ -count=1`
Expected: nothing from `gofmt`, vet clean, PASS.

- [ ] **Step 5: Commit**

```
git add internal/tasks/claims.go internal/tasks/claims_test.go
```
then
```
git commit -m "feat(tasks): the agent's verbs: next, claim, comment, finish, release, with leases" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

