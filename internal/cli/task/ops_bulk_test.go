package task

import (
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

type manyJSON struct {
	Tasks []opsTask `json:"tasks"`
}

func (m manyJSON) ids() []int64 {
	out := make([]int64, 0, len(m.Tasks))
	for _, t := range m.Tasks {
		out = append(out, t.ID)
	}
	return out
}

// first is the first task of the answer, or the zero task when it holds none: a test that
// reads it fails on its own assertion, and not on a panic that takes the other tests with it.
func (m manyJSON) first() opsTask {
	if len(m.Tasks) == 0 {
		return opsTask{}
	}
	return m.Tasks[0]
}

// approve ID... --top keeps the order the ids are given: the first one ends on the very
// top, above what was there. Without --top they go to the bottom, in the same order.
func TestTaskApproveKeepsTheOrderTheIdsAreGiven(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	opsAdd(t, db, "z", "--ready")
	a := opsAdd(t, db, "a")
	b := opsAdd(t, db, "b")
	c := opsAdd(t, db, "c")
	d := opsAdd(t, db, "d")
	var many manyJSON
	mustTaskJSON(t, db, "default", &many, "", "approve", id(c), "#"+id(a), "--top")
	if got := opsOrder(t, db, "ready"); got != "c,a,z" {
		t.Errorf("--top: ready is %s, want c,a,z", got)
	}
	if ids := many.ids(); len(ids) != 2 || ids[0] != c || ids[1] != a || many.first().Status != "ready" {
		t.Errorf("--top: the answer is %v, want the tasks in the order given", ids)
	}
	mustTaskJSON(t, db, "default", &many, "", "approve", id(d), id(b))
	if got := opsOrder(t, db, "ready"); got != "c,a,z,d,b" {
		t.Errorf("no --top: ready is %s, want c,a,z,d,b", got)
	}
	if ids := many.ids(); len(ids) != 2 || ids[0] != d || ids[1] != b {
		t.Errorf("no --top: the answer is %v, want the tasks in the order given", ids)
	}
	if s := opsShow(t, db, b); s.kinds() != "created moved" || s.Events[1].Note != "approved" || s.Events[1].Actor != "you" {
		t.Errorf("an approved task's history: %+v", s.Events)
	}
}

// An approval is all or nothing, and what refuses it is named: the first task, in the
// order given, that is not in the Inbox, with its status; or one that is not there.
func TestTaskApproveNamesWhatRefusesItAndWritesNothing(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	a := opsAdd(t, db, "a")
	b := opsAdd(t, db, "b")
	r := opsAdd(t, db, "r")
	var one movedJSON
	mustTaskJSON(t, db, "default", &one, "", "move", id(r), "review")
	rev := opsRev(t, db)

	doc := failedTaskJSON(t, db, "default", 3, "approve", id(a), id(r), id(b))
	if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, "#"+id(r)) || !strings.Contains(msg, "review") {
		t.Errorf("a task in Review: %v, want invalid_input that names the task and its column", doc)
	}
	doc = failedTaskJSON(t, db, "default", 2, "approve", id(a), "99999", id(b))
	if doc["code"] != "not_found" {
		t.Errorf("an unknown task: %v", doc)
	}
	if got := opsOrder(t, db, "ready"); got != "" || opsRev(t, db) != rev {
		t.Errorf("a refused approval wrote something: ready is %q, revision %d (was %d)", got, opsRev(t, db), rev)
	}
	// The same id twice is refused, however it is written, and the tasks stay where they are.
	for _, args := range [][]string{
		{id(a), id(a)},
		{id(a), "#" + id(a)},
		{id(a), id(b), " " + id(a) + " "},
	} {
		doc := failedTaskJSON(t, db, "default", 3, append([]string{"approve"}, args...)...)
		if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, "#"+id(a)+" is named twice") {
			t.Errorf("approve %q: %v, want invalid_input: task #%d is named twice", args, doc, a)
		}
	}
	if got := opsOrder(t, db, "ready"); got != "" || opsRev(t, db) != rev {
		t.Errorf("a refused approval wrote something: ready is %q, revision %d (was %d)", got, opsRev(t, db), rev)
	}
	var many manyJSON
	mustTaskJSON(t, db, "default", &many, "", "approve", id(a), id(b))
	if got := opsOrder(t, db, "ready"); got != "a,b" {
		t.Errorf("approve a b: ready is %s", got)
	}
}

func TestTaskArchiveHidesTasksAndUnarchiveBringsThemBackToTheirColumns(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	i := opsAdd(t, db, "i")
	r := opsAdd(t, db, "r", "--ready")
	d := opsAdd(t, db, "d")
	var one movedJSON
	mustTaskJSON(t, db, "default", &one, "", "move", id(d), "done")
	n := opsAdd(t, db, "n")

	var many manyJSON
	mustTaskJSON(t, db, "default", &many, "", "archive", id(i), id(r), "#"+id(d))
	if ids := many.ids(); len(ids) != 3 || ids[0] != i || ids[1] != r || ids[2] != d {
		t.Fatalf("archive: %v, want the tasks in the order given", ids)
	}
	for _, task := range many.Tasks {
		if task.Status != "archived" {
			t.Errorf("archive: %+v", task)
		}
	}
	var seen listJSON
	mustTaskJSON(t, db, "default", &seen, "", "list")
	if got := titlesOf(seen); len(got) != 1 || got[0] != "n" {
		t.Errorf("the board after the archive: %v", got)
	}
	if got := opsOrder(t, db, "archived"); len(strings.Split(got, ",")) != 3 {
		t.Errorf("the archive: %s", got)
	}
	// Archiving what is archived refuses the whole call.
	doc := failedTaskJSON(t, db, "default", 3, "archive", id(n), id(i))
	if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, "#"+id(i)) || !strings.Contains(msg, "already archived") {
		t.Errorf("archiving an archived task: %v", doc)
	}
	if s := opsShow(t, db, n); s.Task.Status != "inbox" {
		t.Errorf("a refused archive archived the other task too: %+v", s.Task)
	}
	// Unarchiving brings each task back to the column it left; one that is not archived refuses the call.
	if doc := failedTaskJSON(t, db, "default", 3, "unarchive", id(i), id(n)); doc["code"] != "invalid_input" {
		t.Errorf("unarchiving a task that is not archived: %v", doc)
	}
	mustTaskJSON(t, db, "default", &many, "", "unarchive", id(d), id(i), id(r))
	want := []string{"done", "inbox", "ready"}
	for k, task := range many.Tasks {
		if task.Status != want[k] {
			t.Errorf("unarchive #%d: %s, want %s", task.ID, task.Status, want[k])
		}
	}
	if s := opsShow(t, db, i); !strings.HasSuffix(s.kinds(), "archived unarchived") {
		t.Errorf("history of an unarchived task: %s", s.kinds())
	}
}

// archive --status takes the one column of this profile, and only that.
func TestTaskArchiveByStatusTakesOnlyThatColumnOfThisProfile(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	addTaskProfile(t, db, "work-id", "Work")
	var one movedJSON
	for _, title := range []string{"d1", "d2"} {
		mustTaskJSON(t, db, "default", &one, "", "move", id(opsAdd(t, db, title)), "done")
	}
	keep := opsAdd(t, db, "kept", "--ready")
	var added addedJSON
	mustTaskJSON(t, db, "work-id", &added, "", "add", "work done")
	mustTaskJSON(t, db, "work-id", &one, "", "move", id(added.Task.ID), "done")

	var res struct {
		Archived int `json:"archived"`
	}
	mustTaskJSON(t, db, "default", &res, "", "archive", "--status", "done")
	if res.Archived != 2 {
		t.Errorf("archived %d, want 2", res.Archived)
	}
	if s := opsShow(t, db, keep); s.Task.Status != "ready" {
		t.Errorf("another column was archived: %+v", s.Task)
	}
	var work struct {
		Task taskJSON `json:"task"`
	}
	mustTaskJSON(t, db, "work-id", &work, "", "show", id(added.Task.ID))
	if work.Task.Status != "done" {
		t.Errorf("another profile's card was archived: %+v", work.Task)
	}
	rev := opsRev(t, db)
	mustTaskJSON(t, db, "default", &res, "", "archive", "--status", "done")
	if res.Archived != 0 || opsRev(t, db) != rev {
		t.Errorf("an empty column: archived %d, revision %d (was %d)", res.Archived, opsRev(t, db), rev)
	}
	for _, status := range []string{"archived", "bogus", ""} {
		if doc := failedTaskJSON(t, db, "default", 3, "archive", "--status", status); doc["code"] != "invalid_input" {
			t.Errorf("--status %q: %v", status, doc)
		}
	}
	// A --status that is given and empty is no reason to archive the ids that came with it.
	if doc := failedTaskJSON(t, db, "default", 3, "archive", id(keep), "--status", ""); doc["code"] != "invalid_input" {
		t.Errorf("an id with an empty --status: %v", doc)
	}
	if s := opsShow(t, db, keep); s.Task.Status != "ready" {
		t.Errorf("an empty --status was dropped and the id archived: %+v", s.Task)
	}
}

// A column is named the way the store reads a status: in-progress and progress mean
// in_progress, and the case and the padding do not matter. The command reads the name with
// ParseStatus and not with a spelling of its own, which a database that opens shows: the
// store refuses a name that is passed on raw, so each row has to archive the card it names.
func TestTaskArchiveByStatusReadsAColumnNameInAnySpelling(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var one movedJSON
	var res struct {
		Archived int `json:"archived"`
	}
	for _, name := range []string{"In-Progress", " progress ", "IN_PROGRESS"} {
		mustTaskJSON(t, db, "default", &one, "", "move", id(opsAdd(t, db, "held")), "in_progress")
		mustTaskJSON(t, db, "default", &res, "", "archive", "--status", name)
		if res.Archived != 1 {
			t.Errorf("archive --status %q: archived %d, want 1", name, res.Archived)
		}
	}
}

// Archiving a card an agent holds ends the claim and the store writes a released event
// before the archived one: the card the command prints has no claim, whether it archives
// by id or a whole column, and in text the line of the task is there all the same.
func TestTaskArchiveOfAHeldCardClearsTheClaim(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	until := time.Now().Add(time.Hour)
	ids := seedTaskRows(t, db,
		taskSeed{title: "held one", status: "in_progress", holder: "bot", until: until},
		taskSeed{title: "held two", status: "in_progress", holder: "bot", until: until},
		taskSeed{title: "held three", status: "in_progress", holder: "other", until: until})
	var many manyJSON
	mustTaskJSON(t, db, "default", &many, "", "archive", id(ids[0]))
	if many.first().Status != "archived" || many.first().Claim != nil {
		t.Errorf("an archived card still held: %+v", many.first())
	}
	if s := opsShow(t, db, ids[0]); s.kinds() != "released archived" || s.Events[0].Actor != "you" {
		t.Errorf("history: %s", s.kinds())
	}
	text, _, err := runTask(t, db, "default", false, "", "archive", id(ids[1]))
	if err != nil || !strings.Contains(text, "Archived #"+id(ids[1])) {
		t.Errorf("as text: %q, %v", text, err)
	}
	var res struct {
		Archived int `json:"archived"`
	}
	mustTaskJSON(t, db, "default", &res, "", "archive", "--status", "in_progress")
	if s := opsShow(t, db, ids[2]); res.Archived != 1 || s.Task.Status != "archived" || s.Task.Claim != nil || s.kinds() != "released archived" {
		t.Errorf("a column of held cards: archived %d, %+v, %s", res.Archived, s.Task, s.kinds())
	}
}

// A card that leaves the archive for a board column counts against the limit of open
// tasks, as an added one does: unarchive and move both answer limit, and write nothing.
func TestTaskUnarchiveAndMoveOutOfTheArchiveRespectTheOpenTaskLimit(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	rows := make([]taskSeed, 0, tasks.MaxOpenTasks+2)
	for i := 0; i < tasks.MaxOpenTasks; i++ {
		rows = append(rows, taskSeed{title: "open " + id(int64(i))})
	}
	rows = append(rows, taskSeed{title: "archived one", status: "archived"}, taskSeed{title: "archived two", status: "archived"})
	ids := seedTaskRows(t, db, rows...)
	open, one, two := ids[0], ids[len(ids)-2], ids[len(ids)-1]
	rev := opsRev(t, db)
	for _, args := range [][]string{
		{"unarchive", id(one)},
		{"unarchive", id(two), id(one)},
		{"move", id(one), "ready"},
		{"move", id(one), "inbox", "--top"},
	} {
		if doc := failedTaskJSON(t, db, "default", 3, args...); doc["code"] != "limit" {
			t.Errorf("task %s with %d open tasks: %v, want limit", strings.Join(args, " "), tasks.MaxOpenTasks, doc)
		}
	}
	if s := opsShow(t, db, one); s.Task.Status != "archived" || opsRev(t, db) != rev {
		t.Errorf("a refused call wrote something: %+v, revision %d (was %d)", s.Task, opsRev(t, db), rev)
	}
	// One slot is freed: one card comes back, and the next is refused again.
	var many manyJSON
	mustTaskJSON(t, db, "default", &many, "", "archive", id(open))
	mustTaskJSON(t, db, "default", &many, "", "unarchive", id(one))
	if many.first().Status != "inbox" {
		t.Errorf("unarchive with a free slot: %+v", many.first())
	}
	if doc := failedTaskJSON(t, db, "default", 3, "move", id(two), "done"); doc["code"] != "limit" {
		t.Errorf("the slot is taken again: %v", doc)
	}
	// Archiving is never refused for the limit: it is how room is made.
	mustTaskJSON(t, db, "default", &many, "", "archive", id(one))
	if many.first().Status != "archived" {
		t.Errorf("archive at the limit: %+v", many.first())
	}
}

// Every task id belongs to one profile: another profile's task is not found, by every
// command, and nothing of it changes.
func TestTaskOperatorCommandsOnlyReachTheirOwnProfilesTasks(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	addTaskProfile(t, db, "work-id", "Work")
	var added addedJSON
	mustTaskJSON(t, db, "work-id", &added, "", "add", "work only")
	w := id(added.Task.ID)
	mine := opsAdd(t, db, "mine", "--ready")
	for _, args := range [][]string{
		{"edit", w, "--title", "x"},
		{"move", w, "ready"},
		{"approve", w},
		{"approve", w, id(mine)},
		{"archive", w},
		{"unarchive", w},
		{"edit", "#" + w, "--notes", "x"},
	} {
		if doc := failedTaskJSON(t, db, "default", 2, args...); doc["code"] != "not_found" {
			t.Errorf("task %s in another profile: %v", strings.Join(args, " "), doc)
		}
	}
	var shown opsShown
	mustTaskJSON(t, db, "work-id", &shown, "", "show", w)
	if shown.Task.Status != "inbox" || shown.Task.Title != "work only" || shown.kinds() != "created" {
		t.Errorf("another profile's task changed: %+v, %s", shown.Task, shown.kinds())
	}
	if s := opsShow(t, db, mine); s.Task.Status != "ready" || s.kinds() != "created" {
		t.Errorf("a refused call changed this profile's task: %+v, %s", s.Task, s.kinds())
	}
	// A place in another profile's column is no place.
	if doc := failedTaskJSON(t, db, "default", 3, "move", id(opsAdd(t, db, "x")), "ready", "--before", w); doc["code"] != "invalid_input" {
		t.Errorf("a card of another profile as a neighbour: %v", doc)
	}
}
