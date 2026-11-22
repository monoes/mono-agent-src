### Task 5: The operator's verbs: Edit, Move, Approve, Archive, Unarchive

**Files:**
- Create: `internal/tasks/ops.go`
- Test: `internal/tasks/ops_test.go`

**Interfaces:**
- Consumes: Task 3's `tx`, `getTx`, `event`, `bump`, `edgePosition`, `atTop`; Task 2's `Edit`, `Placement`, `cleanTitle`, `cleanText`, `cutBytes`.
- Produces (all refuse a non-`Human` actor with `ErrOperatorOnly`):
  - `(*Store).Edit(ctx, profileID string, id int64, e Edit, actor Actor) (Task, error)`
  - `(*Store).Move(ctx, profileID string, id int64, to Status, p Placement, actor Actor) (Task, error)`
  - `(*Store).Approve(ctx, profileID string, ids []int64, top bool, actor Actor) ([]Task, error)`
  - `(*Store).Archive(ctx, profileID string, ids []int64, actor Actor) ([]Task, error)`
  - `(*Store).ArchiveStatus(ctx, profileID string, status Status, actor Actor) (int, error)`
  - `(*Store).Unarchive(ctx, profileID string, ids []int64, actor Actor) ([]Task, error)`
  - unexported: `s.moveTx(ctx, x, profileID string, cur Task, to Status, p Placement, actor Actor, kind, note string) error`, `s.placeIn(ctx, x, profileID string, id int64, to Status, p Placement) (int64, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tasks/ops_test.go`:

```go
package tasks

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
)

func TestEditChangesTheTextForTheOperatorOnly(t *testing.T) {
	s, db, _ := newTestStore(t)
	task := mustAdd(t, s, "default", "old title", false)
	title, notes := "  new   title ", "some notes"
	got, err := s.Edit(bg, "default", task.ID, Edit{Title: &title, Notes: &notes}, human)
	if err != nil || got.Title != "new title" || got.Notes != "some notes" {
		t.Fatalf("edit: %+v, %v", got, err)
	}
	if got.LastEvent == nil || got.LastEvent.Kind != "edited" {
		t.Errorf("last event: %+v", got.LastEvent)
	}
	for _, a := range []Actor{bot("b"), {Kind: Capture, Name: SourceOS}} {
		if _, err := s.Edit(bg, "default", task.ID, Edit{Title: &title}, a); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%+v editing: %v, want ErrOperatorOnly", a, err)
		}
	}
	empty := " \x1b "
	if _, err := s.Edit(bg, "default", task.ID, Edit{Title: &empty}, human); !errors.Is(err, ErrInvalid) {
		t.Errorf("an empty title: %v, want ErrInvalid", err)
	}
	if _, err := s.Edit(bg, "default", task.ID, Edit{}, human); !errors.Is(err, ErrInvalid) {
		t.Errorf("nothing to change: %v, want ErrInvalid", err)
	}
	if n := countWhere(t, db, "task_events", "task_id = ? AND kind = 'edited'", task.ID); n != 1 {
		t.Errorf("%d edited events, want 1", n)
	}
}

func TestEditWithNothingNewWritesNothing(t *testing.T) {
	s, _, _ := newTestStore(t)
	task := mustAdd(t, s, "default", "same", false)
	before, _ := s.Rev(bg, "default")
	same := "same"
	if _, err := s.Edit(bg, "default", task.ID, Edit{Title: &same}, human); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.Rev(bg, "default"); after != before {
		t.Errorf("revision moved from %d to %d for an edit that changed nothing", before, after)
	}
}

// column lists the ids of a column, top to bottom.
func column(t *testing.T, s *Store, st Status) []int64 {
	t.Helper()
	ts, err := s.List(bg, "default", Filter{Statuses: []Status{st}}, human)
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for _, task := range ts {
		out = append(out, task.ID)
	}
	return out
}

func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMovePlacesCards(t *testing.T) {
	s, _, _ := newTestStore(t)
	a, b, c := mustAdd(t, s, "default", "a", true), mustAdd(t, s, "default", "b", true), mustAdd(t, s, "default", "c", true)
	move := func(id int64, p Placement) {
		t.Helper()
		if _, err := s.Move(bg, "default", id, StatusReady, p, human); err != nil {
			t.Fatalf("move #%d %+v: %v", id, p, err)
		}
	}
	steps := []struct {
		id   int64
		p    Placement
		want []int64
	}{
		{c.ID, Placement{Top: true}, []int64{c.ID, a.ID, b.ID}},
		{c.ID, Placement{Bottom: true}, []int64{a.ID, b.ID, c.ID}},
		{c.ID, Placement{Before: b.ID}, []int64{a.ID, c.ID, b.ID}},
		{a.ID, Placement{After: b.ID}, []int64{c.ID, b.ID, a.ID}},
	}
	for i, st := range steps {
		move(st.id, st.p)
		if got := column(t, s, StatusReady); !sameIDs(got, st.want) {
			t.Fatalf("step %d: %v, want %v", i, got, st.want)
		}
	}
}

func TestMoveToAnotherColumnPlacesByDefault(t *testing.T) {
	s, _, _ := newTestStore(t)
	a, b := mustAdd(t, s, "default", "a", false), mustAdd(t, s, "default", "b", false)
	for _, task := range []Task{a, b} { // a first, then b: a queue in Ready
		if _, err := s.Move(bg, "default", task.ID, StatusReady, Placement{}, human); err != nil {
			t.Fatal(err)
		}
	}
	if got := column(t, s, StatusReady); !sameIDs(got, []int64{a.ID, b.ID}) {
		t.Errorf("ready is a queue: %v", got)
	}
	for _, task := range []Task{a, b} { // a first, then b: newest first in Done
		if _, err := s.Move(bg, "default", task.ID, StatusDone, Placement{}, human); err != nil {
			t.Fatal(err)
		}
	}
	if got := column(t, s, StatusDone); !sameIDs(got, []int64{b.ID, a.ID}) {
		t.Errorf("done is newest first: %v", got)
	}
}

func TestMoveRenumbersAColumnWithNoRoom(t *testing.T) {
	s, db, _ := newTestStore(t)
	a, b, c := mustAdd(t, s, "default", "a", true), mustAdd(t, s, "default", "b", true), mustAdd(t, s, "default", "c", true)
	// Squeeze a and b to neighbouring positions: there is no integer between them.
	if _, err := db.Exec(`UPDATE tasks SET position = 10 WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET position = 11 WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Move(bg, "default", c.ID, StatusReady, Placement{After: a.ID}, human); err != nil {
		t.Fatal(err)
	}
	if got := column(t, s, StatusReady); !sameIDs(got, []int64{a.ID, c.ID, b.ID}) {
		t.Errorf("order after renumbering: %v", got)
	}
	ts, _ := s.List(bg, "default", Filter{Statuses: []Status{StatusReady}}, human)
	for i := 1; i < len(ts); i++ {
		if ts[i].Position <= ts[i-1].Position {
			t.Errorf("positions are not strictly increasing: %d then %d", ts[i-1].Position, ts[i].Position)
		}
	}
}

func TestMoveRefusals(t *testing.T) {
	s, _, _ := newTestStore(t)
	a, b := mustAdd(t, s, "default", "a", true), mustAdd(t, s, "default", "b", false)
	cases := []struct {
		name string
		to   Status
		p    Placement
		id   int64
		want error
	}{
		{"before itself", StatusReady, Placement{Before: a.ID}, a.ID, ErrInvalid},
		{"before a card of another column", StatusReady, Placement{Before: b.ID}, a.ID, ErrInvalid},
		{"two placements", StatusReady, Placement{Top: true, Bottom: true}, a.ID, ErrInvalid},
		{"archived is not a board column", StatusArchived, Placement{}, a.ID, ErrInvalid},
		{"unknown task", StatusReady, Placement{}, 99999, ErrNotFound},
	}
	for _, c := range cases {
		if _, err := s.Move(bg, "default", c.id, c.to, c.p, human); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

func TestAnyOrderOfMovesKeepsATotalOrder(t *testing.T) {
	s, _, _ := newTestStore(t)
	var all []Task
	for i := 0; i < 12; i++ {
		all = append(all, mustAdd(t, s, "default", fmt.Sprintf("t%d", i), i%2 == 0))
	}
	r := rand.New(rand.NewSource(7))
	for step := 0; step < 300; step++ {
		task := all[r.Intn(len(all))]
		to := BoardStatuses[r.Intn(len(BoardStatuses))]
		var p Placement
		switch r.Intn(4) {
		case 0:
			p.Top = true
		case 1:
			p.Bottom = true
		default:
			var others []int64
			for _, id := range column(t, s, to) {
				if id != task.ID {
					others = append(others, id)
				}
			}
			switch {
			case len(others) == 0:
				p.Top = true
			case r.Intn(2) == 0:
				p.Before = others[r.Intn(len(others))]
			default:
				p.After = others[r.Intn(len(others))]
			}
		}
		if _, err := s.Move(bg, "default", task.ID, to, p, human); err != nil {
			t.Fatalf("step %d: move #%d to %s %+v: %v", step, task.ID, to, p, err)
		}
		seen := 0
		for _, st := range BoardStatuses {
			ts, err := s.List(bg, "default", Filter{Statuses: []Status{st}}, human)
			if err != nil {
				t.Fatal(err)
			}
			seen += len(ts)
			for i := 1; i < len(ts); i++ {
				if ts[i].Position <= ts[i-1].Position {
					t.Fatalf("step %d: column %s has positions %d then %d", step, st, ts[i-1].Position, ts[i].Position)
				}
			}
		}
		if seen != len(all) {
			t.Fatalf("step %d: %d cards on the board, want %d", step, seen, len(all))
		}
	}
}

func TestApproveMovesInboxTasksToReadyAllOrNothing(t *testing.T) {
	s, _, _ := newTestStore(t)
	a, b := mustAdd(t, s, "default", "a", false), mustAdd(t, s, "default", "b", false)
	ready := mustAdd(t, s, "default", "already", true)
	got, err := s.Approve(bg, "default", []int64{a.ID, b.ID}, false, human)
	if err != nil || len(got) != 2 || got[0].Status != StatusReady || got[1].Status != StatusReady {
		t.Fatalf("approve: %+v, %v", got, err)
	}
	if ids := column(t, s, StatusReady); !sameIDs(ids, []int64{ready.ID, a.ID, b.ID}) {
		t.Errorf("approved cards go to the bottom of Ready: %v", ids)
	}
	c, d := mustAdd(t, s, "default", "c", false), mustAdd(t, s, "default", "d", false)
	if _, err := s.Approve(bg, "default", []int64{c.ID, ready.ID, d.ID}, false, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("approving a task that is not in Inbox: %v, want ErrInvalid", err)
	}
	if ids := column(t, s, StatusInbox); len(ids) != 2 {
		t.Errorf("an approval that failed must change nothing: inbox %v", ids)
	}
	top, err := s.Approve(bg, "default", []int64{c.ID}, true, human)
	if err != nil || column(t, s, StatusReady)[0] != top[0].ID {
		t.Errorf("--top: %+v, %v", top, err)
	}
	if _, err := s.Approve(bg, "default", nil, false, human); !errors.Is(err, ErrInvalid) {
		t.Errorf("no ids: %v", err)
	}
}

func TestArchiveAndUnarchiveRestoreTheColumn(t *testing.T) {
	s, _, _ := newTestStore(t)
	done := mustAdd(t, s, "default", "done one", false)
	if _, err := s.Move(bg, "default", done.ID, StatusDone, Placement{}, human); err != nil {
		t.Fatal(err)
	}
	arch, err := s.Archive(bg, "default", []int64{done.ID}, human)
	if err != nil || arch[0].Status != StatusArchived {
		t.Fatalf("archive: %+v, %v", arch, err)
	}
	if _, err := s.Archive(bg, "default", []int64{done.ID}, human); !errors.Is(err, ErrInvalid) {
		t.Errorf("archiving twice: %v, want ErrInvalid", err)
	}
	back, err := s.Unarchive(bg, "default", []int64{done.ID}, human)
	if err != nil || back[0].Status != StatusDone {
		t.Fatalf("unarchive restores the old column: %+v, %v", back, err)
	}
	if _, err := s.Unarchive(bg, "default", []int64{done.ID}, human); !errors.Is(err, ErrInvalid) {
		t.Errorf("unarchiving a task that is not archived: %v", err)
	}

	working := mustAdd(t, s, "default", "working", true)
	if _, err := s.Move(bg, "default", working.ID, StatusInProgress, Placement{}, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Archive(bg, "default", []int64{working.ID}, human); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Unarchive(bg, "default", []int64{working.ID}, human); err != nil || got[0].Status != StatusReady {
		t.Errorf("an in-progress task comes back as ready (nobody holds it): %+v, %v", got, err)
	}

	for i := 0; i < 3; i++ {
		x := mustAdd(t, s, "default", fmt.Sprintf("d%d", i), false)
		if _, err := s.Move(bg, "default", x.ID, StatusDone, Placement{}, human); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.ArchiveStatus(bg, "default", StatusDone, human)
	if err != nil || n != 4 { // the three new ones and the first one, restored to Done
		t.Errorf("archive all done: %d, %v", n, err)
	}
	if _, err := s.ArchiveStatus(bg, "default", StatusArchived, human); !errors.Is(err, ErrInvalid) {
		t.Errorf("archive-status of archived: %v", err)
	}
}

func TestOnlyTheOperatorMovesApprovesAndArchives(t *testing.T) {
	s, _, _ := newTestStore(t)
	task := mustAdd(t, s, "default", "a", false)
	for _, a := range []Actor{bot("b"), {Kind: Capture, Name: SourceChrome}} {
		if _, err := s.Move(bg, "default", task.ID, StatusReady, Placement{}, a); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%+v Move: %v", a, err)
		}
		if _, err := s.Approve(bg, "default", []int64{task.ID}, false, a); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%+v Approve: %v", a, err)
		}
		if _, err := s.Archive(bg, "default", []int64{task.ID}, a); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%+v Archive: %v", a, err)
		}
		if _, err := s.Unarchive(bg, "default", []int64{task.ID}, a); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%+v Unarchive: %v", a, err)
		}
		if _, err := s.ArchiveStatus(bg, "default", StatusDone, a); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%+v ArchiveStatus: %v", a, err)
		}
	}
	if got, _, _ := s.Get(bg, "default", task.ID); got.Status != StatusInbox {
		t.Errorf("a refused call moved the task to %s", got.Status)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/tasks/ -run 'TestEdit|TestMove|TestAnyOrder|TestApprove|TestArchive|TestOnlyTheOperator' -count=1`
Expected: FAIL to compile (`s.Edit undefined`, ...).

- [ ] **Step 3: Write `internal/tasks/ops.go`**

```go
package tasks

import (
	"context"
	"fmt"
	"strings"
)

// Edit changes a task's title and notes. Only the operator edits: what the
// operator approved is what an agent reads.
func (s *Store) Edit(ctx context.Context, profileID string, id int64, e Edit, actor Actor) (Task, error) {
	if actor.Kind != Human {
		return Task{}, operatorOnly("edit a task")
	}
	if e.Title == nil && e.Notes == nil {
		return Task{}, invalid("nothing to change: give a title or notes")
	}
	var out Task
	err := s.tx(ctx, func(x dbx) error {
		cur, err := s.getTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		title, notes := cur.Title, cur.Notes
		var changed []string
		if e.Title != nil {
			t := cleanTitle(*e.Title)
			if t == "" {
				return invalid("the title cannot be empty")
			}
			if t != title {
				title = t
				changed = append(changed, "title")
			}
		}
		if e.Notes != nil {
			n := cutBytes(cleanText(*e.Notes), MaxNotesBytes)
			if n != notes {
				notes = n
				changed = append(changed, "notes")
			}
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		stamp := s.stamp()
		if _, err := x.ExecContext(ctx, `UPDATE tasks SET title = ?, notes = ?, updated_at = ? WHERE id = ? AND profile_id = ?`,
			title, notes, stamp, id, profileID); err != nil {
			return fmt.Errorf("tasks: editing #%d: %w", id, err)
		}
		if err := s.event(ctx, x, id, stamp, actor.Label(), "edited", "", "", strings.Join(changed, ", ")); err != nil {
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

// placeIn returns the position that puts card id where p says in column `to`
// (the card itself is ignored). When two neighbours leave no integer between
// them the whole column is renumbered, in the caller's transaction.
func (s *Store) placeIn(ctx context.Context, x dbx, profileID string, id int64, to Status, p Placement) (int64, error) {
	if p.set() > 1 {
		return 0, invalid("use only one of before, after, top and bottom")
	}
	rows, err := x.QueryContext(ctx,
		`SELECT id, position FROM tasks WHERE profile_id = ? AND status = ? AND id <> ? ORDER BY position, id`,
		profileID, string(to), id)
	if err != nil {
		return 0, fmt.Errorf("tasks: reading a column: %w", err)
	}
	var ids, pos []int64
	for rows.Next() {
		var oid, op int64
		if err := rows.Scan(&oid, &op); err != nil {
			rows.Close()
			return 0, fmt.Errorf("tasks: reading a column: %w", err)
		}
		ids, pos = append(ids, oid), append(pos, op)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("tasks: reading a column: %w", err)
	}
	rows.Close()

	idx := len(ids)
	switch {
	case p.Before != 0 || p.After != 0:
		ref := p.Before
		if p.After != 0 {
			ref = p.After
		}
		idx = -1
		for i, oid := range ids {
			if oid == ref {
				idx = i
			}
		}
		if idx < 0 {
			return 0, invalid("task #%d is not in %s", ref, to)
		}
		if p.After != 0 {
			idx++
		}
	case p.Top:
		idx = 0
	case p.Bottom:
		idx = len(ids)
	case atTop(to):
		idx = 0
	}
	switch {
	case len(ids) == 0:
		return positionGap, nil
	case idx == 0:
		return pos[0] - positionGap, nil
	case idx == len(ids):
		return pos[len(pos)-1] + positionGap, nil
	case pos[idx]-pos[idx-1] >= 2:
		return pos[idx-1] + (pos[idx]-pos[idx-1])/2, nil
	}
	// No room between the neighbours: renumber the column, leaving a slot at idx.
	var mine int64
	slot := int64(0)
	for i, oid := range ids {
		if i == idx {
			slot++
			mine = slot * positionGap
		}
		slot++
		if _, err := x.ExecContext(ctx, `UPDATE tasks SET position = ? WHERE id = ? AND profile_id = ?`, slot*positionGap, oid, profileID); err != nil {
			return 0, fmt.Errorf("tasks: renumbering a column: %w", err)
		}
	}
	return mine, nil
}

// moveTx moves a card in the caller's transaction. Leaving In progress ends
// the agent's claim and says so; moving a held card within In progress keeps it.
func (s *Store) moveTx(ctx context.Context, x dbx, profileID string, cur Task, to Status, p Placement, actor Actor, kind, note string) error {
	pos, err := s.placeIn(ctx, x, profileID, cur.ID, to, p)
	if err != nil {
		return err
	}
	stamp := s.stamp()
	claimedBy, until := "", ""
	if to == StatusInProgress && cur.Status == StatusInProgress && cur.Claim != nil {
		claimedBy, until = cur.Claim.By, cur.Claim.Until.UTC().Format(timeFmt)
	}
	if cur.Claim != nil && claimedBy == "" {
		if err := s.event(ctx, x, cur.ID, stamp, actor.Label(), "released", string(cur.Status), string(to),
			"the claim of "+cur.Claim.By+" ended: the operator moved the card"); err != nil {
			return err
		}
	}
	if _, err := x.ExecContext(ctx,
		`UPDATE tasks SET status = ?, position = ?, claimed_by = ?, claim_until = ?, updated_at = ? WHERE id = ? AND profile_id = ?`,
		string(to), pos, claimedBy, until, stamp, cur.ID, profileID); err != nil {
		return fmt.Errorf("tasks: moving #%d: %w", cur.ID, err)
	}
	return s.event(ctx, x, cur.ID, stamp, actor.Label(), kind, string(cur.Status), string(to), note)
}

// Move puts a task in one of the five columns, where p says.
func (s *Store) Move(ctx context.Context, profileID string, id int64, to Status, p Placement, actor Actor) (Task, error) {
	if actor.Kind != Human {
		return Task{}, operatorOnly("move a task")
	}
	if !to.IsBoard() {
		return Task{}, invalid("move to one of inbox, ready, in_progress, review or done (archiving is its own command)")
	}
	var out Task
	err := s.tx(ctx, func(x dbx) error {
		cur, err := s.getTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if err := s.moveTx(ctx, x, profileID, cur, to, p, actor, "moved", ""); err != nil {
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

// each runs fn on every id in one transaction and returns the tasks as they are
// afterwards. Any failure undoes all of it.
func (s *Store) each(ctx context.Context, profileID string, ids []int64, fn func(x dbx, cur Task) error) ([]Task, error) {
	if len(ids) == 0 {
		return nil, invalid("name at least one task")
	}
	out := make([]Task, 0, len(ids))
	err := s.tx(ctx, func(x dbx) error {
		for _, id := range ids {
			cur, err := s.getTx(ctx, x, profileID, id)
			if err != nil {
				return err
			}
			if err := fn(x, cur); err != nil {
				return err
			}
		}
		if err := s.bump(ctx, x, profileID); err != nil {
			return err
		}
		for _, id := range ids {
			t, err := s.getTx(ctx, x, profileID, id)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Approve moves Inbox tasks to Ready, to the bottom of the queue or, with top, the
// top of it. A task that is not in Inbox refuses the whole call.
func (s *Store) Approve(ctx context.Context, profileID string, ids []int64, top bool, actor Actor) ([]Task, error) {
	if actor.Kind != Human {
		return nil, operatorOnly("approve a task")
	}
	return s.each(ctx, profileID, ids, func(x dbx, cur Task) error {
		if cur.Status != StatusInbox {
			return invalid("task #%d is %s, not inbox: only an inbox task is approved", cur.ID, cur.Status)
		}
		return s.moveTx(ctx, x, profileID, cur, StatusReady, Placement{Top: top, Bottom: !top}, actor, "moved", "approved")
	})
}

// Archive hides tasks from the board, keeping them.
func (s *Store) Archive(ctx context.Context, profileID string, ids []int64, actor Actor) ([]Task, error) {
	if actor.Kind != Human {
		return nil, operatorOnly("archive a task")
	}
	return s.each(ctx, profileID, ids, func(x dbx, cur Task) error {
		if cur.Status == StatusArchived {
			return invalid("task #%d is already archived", cur.ID)
		}
		return s.moveTx(ctx, x, profileID, cur, StatusArchived, Placement{Bottom: true}, actor, "archived", "")
	})
}

// ArchiveStatus archives every task of one column and returns how many.
func (s *Store) ArchiveStatus(ctx context.Context, profileID string, status Status, actor Actor) (int, error) {
	if actor.Kind != Human {
		return 0, operatorOnly("archive tasks")
	}
	if !status.IsBoard() {
		return 0, invalid("archive a whole column of inbox, ready, in_progress, review or done")
	}
	n := 0
	err := s.tx(ctx, func(x dbx) error {
		rows, err := x.QueryContext(ctx, `SELECT id FROM tasks WHERE profile_id = ? AND status = ? ORDER BY position, id`, profileID, string(status))
		if err != nil {
			return fmt.Errorf("tasks: reading a column: %w", err)
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("tasks: reading a column: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("tasks: reading a column: %w", err)
		}
		rows.Close()
		for _, id := range ids {
			cur, err := s.getTx(ctx, x, profileID, id)
			if err != nil {
				return err
			}
			if err := s.moveTx(ctx, x, profileID, cur, StatusArchived, Placement{Bottom: true}, actor, "archived", ""); err != nil {
				return err
			}
		}
		n = len(ids)
		if n == 0 {
			return nil
		}
		return s.bump(ctx, x, profileID)
	})
	return n, err
}

// Unarchive returns tasks to the column they were archived from; a task that
// was in progress comes back as ready, since nobody holds it now.
func (s *Store) Unarchive(ctx context.Context, profileID string, ids []int64, actor Actor) ([]Task, error) {
	if actor.Kind != Human {
		return nil, operatorOnly("unarchive a task")
	}
	return s.each(ctx, profileID, ids, func(x dbx, cur Task) error {
		if cur.Status != StatusArchived {
			return invalid("task #%d is %s, not archived", cur.ID, cur.Status)
		}
		to := StatusInbox
		var from string
		err := x.QueryRowContext(ctx,
			`SELECT from_status FROM task_events WHERE task_id = ? AND kind = 'archived' ORDER BY id DESC LIMIT 1`, cur.ID).Scan(&from)
		if err == nil {
			if st, perr := ParseStatus(from); perr == nil && st.IsBoard() {
				to = st
			}
		}
		if to == StatusInProgress {
			to = StatusReady
		}
		return s.moveTx(ctx, x, profileID, cur, to, Placement{}, actor, "unarchived", "")
	})
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `gofmt -l internal/tasks` then `go vet ./internal/tasks/` then `go test ./internal/tasks/ -count=1`
Expected: nothing from `gofmt`, vet clean, PASS (the property test runs 300 moves).

- [ ] **Step 5: Commit**

```
git add internal/tasks/ops.go internal/tasks/ops_test.go
```
then
```
git commit -m "feat(tasks): the operator's verbs: edit, move, approve, archive, unarchive" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

