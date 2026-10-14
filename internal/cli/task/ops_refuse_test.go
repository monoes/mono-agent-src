package task

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// The refusal of an agent comes first: before the arguments are read and before the
// database is opened. The store refuses an agent too, with the same code, so the code
// alone does not show who spoke: a database that cannot be opened does. A command that
// got as far as opening it fails with a plain error (exit 1), the refusal is exit 3.
func TestOperatorCommandsRefuseBeforeTheDatabaseIsOpened(t *testing.T) {
	t.Parallel()
	newTaskTestDB(t) // the operator's environment
	db := opsBrokenDB(t)
	for _, c := range operatorCommands {
		if _, _, err := runTask(t, db, "default", true, "", c...); exitCode(err) != 1 {
			t.Fatalf("the control, task %s as the operator on a database that cannot be opened: exit %d (%v), want the plain error 1",
				strings.Join(c, " "), exitCode(err), err)
		}
	}
	for name, setup := range opsAgentContexts {
		t.Run(name, func(t *testing.T) {
			extra := setup(t)
			for _, c := range operatorCommands {
				args := append(slices.Clone(c), extra...)
				doc := failedTaskJSON(t, db, "default", 3, args...)
				if msg, _ := doc["error"].(string); doc["code"] != "operator_only" || !strings.Contains(msg, "your own terminal") {
					t.Errorf("task %s: %v, want operator_only that says what to do", strings.Join(args, " "), doc)
				}
				// The same in text: no document, the error alone.
				out, _, err := runTask(t, db, "default", false, "", args...)
				if exitCode(err) != 3 || out != "" || !strings.Contains(errText(err), "only the operator can") {
					t.Errorf("task %s as text: exit %d, %q, %v", strings.Join(args, " "), exitCode(err), out, err)
				}
			}
		})
	}
}

// A mistake in the arguments is exit 3 with the JSON error document, like every other
// argument mistake of the group (cobra's own check would be exit 1 and no document), and
// it is found before the database is opened. An agent that makes one is refused as an
// agent first.
func TestOperatorCommandsRefuseTheWrongArgumentsWithoutOpeningTheDatabase(t *testing.T) {
	t.Parallel()
	newTaskTestDB(t)
	db := opsBrokenDB(t)
	for _, args := range [][]string{
		{"edit"},
		{"edit", "1", "2"},
		{"edit", "1", "a", "new", "title"},
		{"move"},
		{"move", "1"},
		{"move", "1", "ready", "extra"},
		{"approve"},
		{"unarchive"},
		{"archive"},
		{"archive", "1", "--status", "done"},
		{"archive", "--status", "done", "1", "2"},
		{"archive", "--status", ""},
		{"archive", "1", "--status", ""},
		{"archive", "--status", "bogus"},
		{"move", "1", "bogus"},
		{"edit", "abc", "--title", "x"},
		{"move", "#", "ready"},
		{"approve", "1", "x"},
		{"unarchive", "0"},
		{"archive", "0"},
		// A place that names no card is a mistake in the arguments too, and an agent that
		// makes it is refused as an agent first: the guard comes before the flags are read.
		{"move", "1", "ready", "--before", "0"},
		{"move", "1", "ready", "--after", ""},
	} {
		doc := failedTaskJSON(t, db, "default", 3, args...)
		if doc["code"] != "invalid_input" {
			t.Errorf("task %s: %v, want invalid_input", strings.Join(args, " "), doc)
		}
		agent := failedTaskJSON(t, db, "default", 3, append(slices.Clone(args), "--as", "bot")...)
		if agent["code"] != "operator_only" {
			t.Errorf("task %s --as bot: %v, want operator_only before the arguments are read", strings.Join(args, " "), agent)
		}
	}
}

// What the caller sent is repeated in a refusal only in part: a huge argument must not make
// a huge message, and the cut never splits a character (the second value is 2-byte
// characters). One row for each way a command takes an argument: a task id, a status, a
// place (--before, --after), the ids of approve, archive and unarchive, and --status.
func TestOperatorCommandsRepeatOnlyAShortStretchOfWhatTheyRefuse(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, huge := range []string{strings.Repeat("x", 10000), strings.Repeat("\xc3\xa9", 10000)} {
		for _, args := range [][]string{
			{"edit", huge, "--title", "x"},
			{"move", huge, "ready"},
			{"move", "1", huge},
			{"move", "1", "ready", "--before", huge},
			{"move", "1", "ready", "--after", huge},
			{"approve", huge},
			{"approve", "1", huge},
			{"archive", huge},
			{"archive", "1", huge},
			{"archive", "--status", huge},
			{"unarchive", huge},
			{"unarchive", "1", huge},
		} {
			out, _, err := runTask(t, db, "default", true, "", args...)
			var doc map[string]any
			if exitCode(err) != 3 || json.Unmarshal([]byte(out), &doc) != nil {
				t.Fatalf("task %s <%d bytes>: exit %d (%v), %.80q", args[0], len(huge), exitCode(err), err, out)
			}
			msg, _ := doc["error"].(string)
			if doc["code"] != "invalid_input" || len(msg) == 0 || len(err.Error()) >= 300 || len(out) > 400 || !utf8.ValidString(msg) {
				t.Errorf("task %s <%d bytes>: a refusal of %d bytes (document %d), code %v: %.120q", args[0], len(huge), len(err.Error()), len(out), doc["code"], msg)
			}
		}
	}
}

// What the operator's refusals say is what the spec gives: who is the agent, and what to
// do instead. They name the command's own words ("approve a task") too.
func TestOperatorRefusalsSayWhoIsTheAgentAndWhatToDo(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	message := func(args ...string) string {
		t.Helper()
		doc := failedTaskJSON(t, db, "default", 3, args...)
		s, _ := doc["error"].(string)
		return s
	}
	for _, c := range []struct {
		what string
		args []string
	}{
		{"edit a task", []string{"edit", "1", "--title", "x"}},
		{"move a task", []string{"move", "1", "ready"}},
		{"approve a task", []string{"approve", "1"}},
		{"archive tasks", []string{"archive", "1"}},
		{"unarchive a task", []string{"unarchive", "1"}},
	} {
		byName := message(append(slices.Clone(c.args), "--as", "bot")...)
		for _, want := range []string{"--as", "only the operator can " + c.what, "your own terminal"} {
			if !strings.Contains(byName, want) {
				t.Errorf("%s with --as: %q lacks %q", c.what, byName, want)
			}
		}
		blank := message(append(slices.Clone(c.args), "--as", "")...)
		if !strings.Contains(blank, "no name") {
			t.Errorf("%s with a blank --as: %q does not say that --as has no name", c.what, blank)
		}
	}
	setenv(t, "CLAUDECODE", "1")
	if msg := message("approve", "1"); !strings.Contains(msg, "CLAUDECODE") || !strings.Contains(msg, "your own terminal") {
		t.Errorf("under a marker: %q", msg)
	}
}

// A refused command leaves the board as it was.
func TestOperatorCommandsRefusedChangeNothing(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	inbox := opsAdd(t, db, "waiting")
	ready := opsAdd(t, db, "queued", "--ready")
	rev := opsRev(t, db)
	for name, setup := range opsAgentContexts {
		t.Run(name, func(t *testing.T) {
			extra := setup(t)
			for _, args := range [][]string{
				{"approve", id(inbox)},
				{"move", id(inbox), "done"},
				{"move", id(ready), "ready", "--top"},
				{"edit", id(inbox), "--title", "taken over"},
				{"archive", id(ready)},
				{"archive", "--status", "ready"},
				{"unarchive", id(ready)},
			} {
				failedTaskJSON(t, db, "default", 3, append(slices.Clone(args), extra...)...)
			}
		})
	}
	if got := opsRev(t, db); got != rev {
		t.Errorf("the board revision moved from %d to %d", rev, got)
	}
	if s := opsShow(t, db, inbox); s.Task.Status != "inbox" || s.Task.Title != "waiting" || s.kinds() != "created" {
		t.Errorf("the inbox task: %+v, events %s", s.Task, s.kinds())
	}
	if s := opsShow(t, db, ready); s.Task.Status != "ready" || s.kinds() != "created" {
		t.Errorf("the ready task: %+v, events %s", s.Task, s.kinds())
	}
}
