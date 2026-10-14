package task

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

type movedJSON struct {
	Task opsTask `json:"task"`
}

// A move to the column the card is in, with no place, changes nothing: no event, no
// revision, no new position. Any place (top, bottom, before, after) is a reorder, even one
// that changes no order: it writes a moved event and moves the revision.
func TestTaskMoveToTheColumnItIsInIsANoOpUnlessAPlaceIsGiven(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	a := opsAdd(t, db, "a", "--ready")
	b := opsAdd(t, db, "b", "--ready")
	c := opsAdd(t, db, "c", "--ready")
	before := opsShow(t, db, b)
	rev := opsRev(t, db)

	var one movedJSON
	mustTaskJSON(t, db, "default", &one, "", "move", id(b), "ready")
	after := opsShow(t, db, b)
	if one.Task.ID != b || one.Task.Status != "ready" || one.Task.Position != before.Task.Position || one.Task.UpdatedAt != before.Task.UpdatedAt {
		t.Errorf("a move to the column it is in changed the card: %+v, was %+v", one.Task, before.Task)
	}
	if after.kinds() != before.kinds() || opsRev(t, db) != rev || opsOrder(t, db, "ready") != "a,b,c" {
		t.Errorf("a move to the column it is in wrote something: events %q (was %q), revision %d (was %d), order %s",
			after.kinds(), before.kinds(), opsRev(t, db), rev, opsOrder(t, db, "ready"))
	}
	if out, _, err := runTask(t, db, "default", false, "", "move", id(b), "ready"); err != nil || !strings.Contains(out, "#"+id(b)) {
		t.Errorf("as text: %q, %v", out, err)
	}

	for _, place := range []struct {
		flags []string
		want  string
	}{
		{[]string{"--top"}, "b,a,c"},
		{[]string{"--bottom"}, "a,c,b"},
		{[]string{"--before", id(a)}, "b,a,c"},
		{[]string{"--after", "#" + id(c)}, "a,c,b"},
		{[]string{"--bottom"}, "a,c,b"}, // already there: still a reorder
	} {
		events, rev := len(opsShow(t, db, b).Events), opsRev(t, db)
		mustTaskJSON(t, db, "default", &one, "", append([]string{"move", id(b), "ready"}, place.flags...)...)
		shown := opsShow(t, db, b)
		if got := opsOrder(t, db, "ready"); got != place.want {
			t.Errorf("%v: order %s, want %s", place.flags, got, place.want)
		}
		if len(shown.Events) != events+1 || shown.Events[events].Kind != "moved" || opsRev(t, db) != rev+1 {
			t.Errorf("%v: events %q, revision %d (was %d): a place is a reorder, one moved event and one revision", place.flags, shown.kinds(), opsRev(t, db), rev)
		}
	}

	// The help says what the command does: a card already in the column stays where it is
	// unless a place is given (it does not go to the column's default end).
	move, _, err := NewCommand(testDeps(t, "", "", false)).Find([]string{"move"})
	if err != nil || !strings.Contains(strings.Join(strings.Fields(move.Long), " "), "A task already in that column stays where it is unless you give a place.") {
		t.Errorf("the help of move does not say that a task already in the column stays where it is: %v", err)
	}
}

// A column is named the way the store reads a status, whatever the case and the padding, and
// in-progress or progress mean in_progress: the command reads the name with ParseStatus and
// not with a spelling of its own. On a database that opens, a name the command passed on
// raw would be refused by the store, so each row shows the card in the column it names.
func TestTaskMoveReadsAColumnNameInAnySpelling(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	n := opsAdd(t, db, "x")
	var one movedJSON
	for _, c := range []struct{ name, want string }{
		{"in-progress", "in_progress"},
		{"READY", "ready"},
		{" progress ", "in_progress"},
		{" Review", "review"},
		{" In-Progress ", "in_progress"},
		{"DONE", "done"},
	} {
		mustTaskJSON(t, db, "default", &one, "", "move", id(n), c.name)
		if one.Task.Status != c.want {
			t.Errorf("move %q: the card is in %q, want %q", c.name, one.Task.Status, c.want)
		}
	}
}

// A place that names no card is refused, and never read as no place at all: the zero id is
// what the store takes for "not given", so the command itself must refuse a zero, a
// negative id, and a value that is given and empty (--before "$ID" with ID unset).
func TestTaskMoveRefusesAPlaceThatNamesNoCardAndMovesNothing(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	x := opsAdd(t, db, "x")
	other := opsAdd(t, db, "other")
	queued := opsAdd(t, db, "queued", "--ready")
	before, rev := opsShow(t, db, x), opsRev(t, db)
	for _, c := range []struct {
		flags []string
		words string // the message says which flag and what is wrong
	}{
		{[]string{"--before", "0"}, "--before"},
		{[]string{"--before=0"}, "--before"},
		{[]string{"--before=-3"}, "--before"},
		{[]string{"--before", ""}, "--before"},
		{[]string{"--before="}, "--before"},
		{[]string{"--before", "   "}, "--before"},
		{[]string{"--before", "abc"}, "--before"},
		{[]string{"--before", "#"}, "--before"},
		{[]string{"--after", "0"}, "--after"},
		{[]string{"--after=0"}, "--after"},
		{[]string{"--after=-3"}, "--after"},
		{[]string{"--after", ""}, "--after"},
		{[]string{"--after="}, "--after"},
		{[]string{"--after", "7x"}, "--after"},
		{[]string{"--before", "99999"}, "is not in ready"},      // no such card
		{[]string{"--after", id(other)}, "is not in ready"},     // a card of another column
		{[]string{"--before", id(x)}, "itself"},                 // a card is not its own neighbour
		{[]string{"--after", id(x)}, "itself"},                  // not after itself either
		{[]string{"--top", "--before", id(queued)}, "only one"}, // two places
	} {
		doc := failedTaskJSON(t, db, "default", 3, append([]string{"move", id(x), "ready"}, c.flags...)...)
		if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, c.words) {
			t.Errorf("move x ready %q: %v, want invalid_input that says %q", c.flags, doc, c.words)
		}
	}
	after := opsShow(t, db, x)
	if after.Task != before.Task || after.kinds() != before.kinds() || opsRev(t, db) != rev {
		t.Errorf("a refused move changed the card: %+v (was %+v), events %q, revision %d (was %d)", after.Task, before.Task, after.kinds(), opsRev(t, db), rev)
	}
}

// At most one place: the store refuses two, whatever they are, and nothing moves.
func TestTaskMoveRefusesTwoPlacesAndMovesNothing(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	x := opsAdd(t, db, "x")
	r1 := opsAdd(t, db, "r1", "--ready")
	r2 := opsAdd(t, db, "r2", "--ready")
	for _, flags := range [][]string{
		{"--top", "--bottom"},
		{"--before", id(r1), "--after", id(r2)},
		{"--top", "--after", id(r1)},
		{"--bottom", "--before", id(r1)},
		{"--top", "--bottom", "--before", id(r1), "--after", id(r2)},
	} {
		doc := failedTaskJSON(t, db, "default", 3, append([]string{"move", id(x), "ready"}, flags...)...)
		if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, "only one") {
			t.Errorf("move x ready %q: %v", flags, doc)
		}
	}
	if s := opsShow(t, db, x); s.Task.Status != "inbox" || s.kinds() != "created" {
		t.Errorf("a refused move changed the card: %+v, %s", s.Task, s.kinds())
	}
	// A place that is false is no place: --top=false with --bottom is one place.
	var one movedJSON
	mustTaskJSON(t, db, "default", &one, "", "move", id(x), "ready", "--top=false", "--bottom")
	if one.Task.Status != "ready" || opsOrder(t, db, "ready") != "r1,r2,x" {
		t.Errorf("--top=false --bottom: %+v, order %s", one.Task, opsOrder(t, db, "ready"))
	}
}

// Moving a card out of In progress ends an agent's claim and says so in a released event
// before the move's own; a move within In progress keeps the claim.
func TestTaskMoveOutOfInProgressEndsAnAgentsClaim(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	held := seedTaskRows(t, db, taskSeed{title: "held", status: "in_progress", holder: "bot", until: time.Now().Add(time.Hour)})[0]
	var one movedJSON
	mustTaskJSON(t, db, "default", &one, "", "move", id(held), "in_progress", "--top")
	if one.Task.Claim == nil || one.Task.Claim.By != "bot" {
		t.Errorf("a move within In progress ended the claim: %+v", one.Task)
	}
	mustTaskJSON(t, db, "default", &one, "", "move", id(held), "review")
	shown := opsShow(t, db, held)
	if one.Task.Status != "review" || one.Task.Claim != nil || !strings.HasSuffix(shown.kinds(), "released moved") {
		t.Errorf("a move to Review: %+v, events %q", one.Task, shown.kinds())
	}
}

// A column whose end card is so near the limit of an int64 that one more gap would wrap
// round can only be written by hand. The store refuses a move there with a plain error
// (exit 1): the message reaches the user, and nothing is written.
func TestTaskMoveReportsAColumnWhosePositionsAreOutOfRange(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	z := opsAdd(t, db, "z", "--ready")
	x := opsAdd(t, db, "x")
	raw, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.DB.Exec(`UPDATE tasks SET position = ? WHERE id = ?`, int64(math.MaxInt64-100), z); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	rev := opsRev(t, db)

	out, _, err := runTask(t, db, "default", true, "", "move", id(x), "ready", "--bottom")
	var doc map[string]any
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("no JSON error document: %v\n%s", jerr, out)
	}
	if msg, _ := doc["error"].(string); exitCode(err) != 1 || !strings.Contains(msg, "out of range") {
		t.Errorf("exit %d (%v), %v: want the plain error 1 that says the positions are out of range", exitCode(err), err, doc)
	}
	if code, has := doc["code"]; has {
		t.Errorf("a plain error has no code: %v", code)
	}
	if _, _, err := runTask(t, db, "default", false, "", "move", id(x), "ready"); exitCode(err) != 1 || !strings.Contains(errText(err), "out of range") {
		t.Errorf("as text: exit %d, %v", exitCode(err), err)
	}
	if s := opsShow(t, db, x); s.Task.Status != "inbox" || s.kinds() != "created" || opsRev(t, db) != rev {
		t.Errorf("the refused move wrote something: %+v, events %q", s.Task, s.kinds())
	}
	// The same column is no trap for a move to its other end: the top is a gap above z.
	var one movedJSON
	mustTaskJSON(t, db, "default", &one, "", "move", id(x), "ready", "--top")
	if one.Task.Status != "ready" || !slices.Equal(strings.Split(opsOrder(t, db, "ready"), ","), []string{"x", "z"}) {
		t.Errorf("a move to the top of the column: %+v, order %s", one.Task, opsOrder(t, db, "ready"))
	}
}
