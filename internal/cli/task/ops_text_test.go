package task

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// Each command names the profile it acted on, in its first line (the active profile can
// change under a caller, and archive --status takes a whole column), then says what it did
// with one line for each task: the verb, the id, the column the task is in and its title.
func TestTaskOperatorCommandsNameTheProfileAndSayWhatTheyDid(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "first")
	a, b := added.Task.ID, opsAdd(t, db, "second")
	header := "Profile: " + added.Profile.Name + "\n"
	line := func(verb string, n int64, column, title string) string {
		return verb + " #" + id(n) + " (" + column + "): " + title + "\n"
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"approve", id(a), id(b)}, header + line("Approved", a, "Ready", "first") + line("Approved", b, "Ready", "second")},
		{[]string{"move", id(a), "in_progress"}, header + line("Moved", a, "In progress", "first")},
		{[]string{"edit", id(a), "--title", "first, edited"}, header + line("Edited", a, "In progress", "first, edited")},
		{[]string{"archive", id(a), id(b)}, header + line("Archived", a, "Archived", "first, edited") + line("Archived", b, "Archived", "second")},
		{[]string{"unarchive", id(b), "#" + id(a)}, header + line("Restored", b, "Ready", "second") + line("Restored", a, "Ready", "first, edited")},
		{[]string{"archive", "--status", "ready"}, header + "Archived 2 tasks from Ready.\n"},
		{[]string{"archive", "--status", "ready"}, header + "Archived 0 tasks from Ready.\n"},
	} {
		out, errOut, err := runTask(t, db, "default", false, "", c.args...)
		if err != nil || errOut != "" || out != c.want {
			t.Errorf("task %s:\n%q (%v, %q)\nwant\n%q", strings.Join(c.args, " "), out, err, errOut, c.want)
		}
	}
	// One task is "1 task".
	opsAdd(t, db, "alone", "--ready")
	if out, _, err := runTask(t, db, "default", false, "", "archive", "--status", "ready"); err != nil || out != header+"Archived 1 task from Ready.\n" {
		t.Errorf("one task: %q, %v", out, err)
	}
}

// The profile named is the one the command acted on, by whatever way it was named.
func TestTaskOperatorCommandsNameTheProfileTheyActedOn(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	addTaskProfile(t, db, "work-id", "Work")
	var added addedJSON
	mustTaskJSON(t, db, "Work", &added, "", "add", "for work")
	for _, args := range [][]string{
		{"approve", id(added.Task.ID)},
		{"edit", id(added.Task.ID), "--title", "for work, edited"},
		{"move", id(added.Task.ID), "done"},
		{"archive", "--status", "done"},
	} {
		out, _, err := runTask(t, db, "work-id", false, "", args...)
		if err != nil || !strings.HasPrefix(out, "Profile: Work\n") {
			t.Errorf("task %s: %q, %v, want the profile Work named first", strings.Join(args, " "), out, err)
		}
	}
}

// The text of a task is cut in the line that names it, as the board cuts it, and cut
// on a character. The JSON keeps all of it.
func TestTaskOperatorCommandsCutALongTitleInTheirLine(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	long := strings.Repeat("\U000000e9", 150)
	n := opsAdd(t, db, long)
	out, _, err := runTask(t, db, "default", false, "", "approve", id(n))
	if err != nil {
		t.Fatal(err)
	}
	shown, ok := strings.CutPrefix(out, "Profile: ")
	_, rest, _ := strings.Cut(shown, "\n")
	title, ok2 := strings.CutPrefix(rest, "Approved #"+id(n)+" (Ready): ")
	title = strings.TrimSuffix(title, "\n")
	if !ok || !ok2 || utf8.RuneCountInString(title) != 70 || !strings.HasSuffix(title, ellipsis) || !strings.HasPrefix(title, strings.Repeat("\U000000e9", 69)) {
		t.Errorf("the line of a 150-character title: %q", out)
	}
	var many manyJSON
	mustTaskJSON(t, db, "default", &many, "", "archive", id(n))
	if many.first().Title != long {
		t.Errorf("the JSON cut the title to %d characters", utf8.RuneCountInString(many.first().Title))
	}
}

// The documents the operator's commands print: one task is {profile, task}, several are
// {profile, tasks} with an array, and archive --status is {profile, archived}.
func TestTaskOperatorCommandsPrintTheDocumentsOfTheSpec(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	a, b := opsAdd(t, db, "a"), opsAdd(t, db, "b")
	for _, c := range []struct {
		args []string
		keys string
	}{
		{[]string{"edit", id(a), "--title", "a2"}, "profile,task"},
		{[]string{"move", id(a), "ready"}, "profile,task"},
		{[]string{"move", id(a), "ready"}, "profile,task"}, // the no-op has the same shape
		{[]string{"approve", id(b)}, "profile,tasks"},
		{[]string{"archive", id(a), id(b)}, "profile,tasks"},
		{[]string{"unarchive", id(a)}, "profile,tasks"},
		{[]string{"archive", "--status", "ready"}, "archived,profile"},
		{[]string{"archive", "--status", "done"}, "archived,profile"}, // an empty column too
	} {
		out, _, err := runTask(t, db, "default", true, "", c.args...)
		if err != nil {
			t.Fatalf("task %s: %v", strings.Join(c.args, " "), err)
		}
		if keys := opsKeys(t, out); keys != c.keys {
			t.Errorf("task %s prints {%s}, want {%s}\n%s", strings.Join(c.args, " "), keys, c.keys, out)
			continue
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatal(err)
		}
		var profile struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(doc["profile"], &profile); err != nil || profile.ID != "default" || profile.Name == "" {
			t.Errorf("task %s: profile %s (%v)", strings.Join(c.args, " "), doc["profile"], err)
		}
		if raw, many := doc["tasks"]; many && !bytes.HasPrefix(raw, []byte("[")) {
			t.Errorf("task %s: tasks is %s, want an array", strings.Join(c.args, " "), raw)
		}
		if raw, num := doc["archived"]; num && bytes.HasPrefix(raw, []byte(`"`)) {
			t.Errorf("task %s: archived is %s, want a number", strings.Join(c.args, " "), raw)
		}
	}
}

func TestTaskEditChangesTheTitleAndTheNotesAndNothingElse(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "old title", "--notes", "some notes")
	n := added.Task.ID
	var one movedJSON
	edit := func(args ...string) opsTask {
		t.Helper()
		mustTaskJSON(t, db, "default", &one, "", append([]string{"edit", id(n)}, args...)...)
		return one.Task
	}
	if task := edit("--notes", ""); task.Notes != "" || task.Title != "old title" {
		t.Errorf("an empty --notes clears the notes and leaves the title: %+v", task)
	}
	if task := edit("--title", "new   title"); task.Title != "new title" || task.Notes != "" {
		t.Errorf("--title keeps the notes and cleans the title: %+v", task)
	}
	if task := edit("--title", "t2", "--notes", "line one\nline two"); task.Title != "t2" || task.Notes != "line one\nline two" || task.Status != "inbox" {
		t.Errorf("both: %+v", task)
	}
	shown := opsShow(t, db, n)
	if shown.kinds() != "created edited edited edited" || shown.Events[1].Note != "notes" || shown.Events[2].Note != "title" || shown.Events[3].Note != "title, notes" {
		t.Errorf("history: %+v", shown.Events)
	}
	// The text it already has is no change: no event.
	before := opsRev(t, db)
	edit("--title", "t2", "--notes", "line one\nline two")
	if opsShow(t, db, n).kinds() != shown.kinds() || opsRev(t, db) != before {
		t.Errorf("an edit that changes nothing wrote something")
	}
	// What is refused changes nothing: no flag at all, and a title that is empty once cleaned.
	for _, args := range [][]string{
		{"edit", id(n)},
		{"edit", id(n), "--title", ""},
		{"edit", id(n), "--title", "   "},
		{"edit", id(n), "--title", "\x1b\x07"},
		{"edit", id(n), "--title", "", "--notes", "changed"}, // the empty title is not dropped, to change the notes alone
	} {
		if doc := failedTaskJSON(t, db, "default", 3, args...); doc["code"] != "invalid_input" {
			t.Errorf("task %q: %v", args, doc)
		}
	}
	if s := opsShow(t, db, n); s.Task.Title != "t2" || s.Task.Notes != "line one\nline two" || s.kinds() != shown.kinds() {
		t.Errorf("a refused edit changed the task: %+v, %s", s.Task, s.kinds())
	}
}

// An edit of text with terminal escapes or hidden characters stores and prints it clean.
func TestTaskEditPrintsNoEscapeOrHiddenCharacter(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	n := opsAdd(t, db, "plain")
	out, _, err := runTask(t, db, "default", false, "", "edit", id(n), "--title", "red\x1b[31m\U0000202eflag\U000e0041")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out {
		if badTerminalRune(r) {
			t.Errorf("the output holds %U: %q", r, out)
		}
	}
	if !strings.Contains(out, "red[31mflag") {
		t.Errorf("the cleaned title is missing: %q", out)
	}
}
