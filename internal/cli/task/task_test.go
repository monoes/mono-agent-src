package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// taskJSON is the part of a task document the tests read.
type taskJSON struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Notes  string `json:"notes"`
	Status string `json:"status"`
	Source struct {
		Kind  string `json:"kind"`
		URL   string `json:"url"`
		Title string `json:"title"`
		App   string `json:"app"`
	} `json:"source"`
	Claim *struct {
		By string `json:"by"`
	} `json:"claim"`
}

// mustTaskJSON runs a command with --json, expects success and decodes stdout.
func mustTaskJSON(t *testing.T, dbPath, profile string, v any, stdin string, args ...string) {
	t.Helper()
	out, _, err := runTask(t, dbPath, profile, true, stdin, args...)
	if err != nil {
		t.Fatalf("task %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("task %s: stdout is not JSON: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// failedTaskJSON runs a command with --json, expects it to fail with the exit
// code wantExit, and returns the {"error","code"} document it printed.
func failedTaskJSON(t *testing.T, dbPath, profile string, wantExit int, args ...string) map[string]any {
	t.Helper()
	out, _, err := runTask(t, dbPath, profile, true, "", args...)
	if exitCode(err) != wantExit {
		t.Fatalf("task %s: exit %d (%v), want %d\n%s", strings.Join(args, " "), exitCode(err), err, wantExit, out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("task %s: no JSON error document: %v\n%s", strings.Join(args, " "), err, out)
	}
	return doc
}

// addedJSON is `task add --json`.
type addedJSON struct {
	Created bool `json:"created"`
	Profile struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"profile"`
	Task taskJSON `json:"task"`
}

func TestTaskAddWithATitleGoesToTheInbox(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "Fix", "the", "flaky", "test")
	if !added.Created || added.Task.ID == 0 || added.Task.Title != "Fix the flaky test" || added.Task.Status != "inbox" ||
		added.Task.Source.Kind != "cli" || added.Profile.ID != "default" || added.Profile.Name == "" {
		t.Errorf("add: %+v", added)
	}
	out, _, err := runTask(t, db, "default", false, "", "add", "A second one")
	if err != nil || !strings.Contains(out, "Added #") || !strings.Contains(out, "Inbox") {
		t.Errorf("text output: %q, %v", out, err)
	}
}

func TestTaskAddFromStandardInputDerivesTheTitle(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "Reply to Sam\nabout the invoice", "add", "--stdin",
		"--url", "https://user:pw@example.com/mail", "--source-title", "Mail")
	if added.Task.Title != "Reply to Sam" || added.Task.Notes != "Reply to Sam\nabout the invoice" {
		t.Errorf("title %q notes %q", added.Task.Title, added.Task.Notes)
	}
	if added.Task.Source.URL != "https://example.com/mail" {
		t.Errorf("url %q: the user-info must be dropped", added.Task.Source.URL)
	}
	if added.Task.Source.Title != "Mail" || added.Task.Source.App != "" {
		t.Errorf("--source-title: title %q, app %q", added.Task.Source.Title, added.Task.Source.App)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "add", "--stdin"); doc["code"] != "invalid_input" {
		t.Errorf("empty standard input: %v", doc)
	}
}

// Notes and standard-input text are two ways to give a task its words, and the
// store refuses both at once rather than drop one (spec 4.6).
func TestTaskAddRefusesNotesTogetherWithStandardInput(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	out, _, err := runTask(t, db, "default", true, "text from standard input", "add", "T", "--notes", "N", "--stdin")
	if exitCode(err) != 3 {
		t.Fatalf("exit %d (%v), want 3\n%s", exitCode(err), err, out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("no JSON error document: %v\n%s", err, out)
	}
	if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, "not both") {
		t.Errorf("notes with standard input: %v", doc)
	}
	raw, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var rows int
	if err := raw.DB.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("%d tasks stored by a refused add", rows)
	}
}

func TestTaskAddWithAClientIDIsIdempotent(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var first, second addedJSON
	mustTaskJSON(t, db, "default", &first, "", "add", "once", "--client-id", "c-1")
	mustTaskJSON(t, db, "default", &second, "", "add", "once", "--client-id", "c-1")
	if !first.Created || second.Created || first.Task.ID != second.Task.ID {
		t.Errorf("first %+v second %+v", first, second)
	}
}

func TestTaskAddFromTheOSMenuIsACapture(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "selected text", "add", "--stdin", "--source", "os", "--app", "Safari")
	if added.Task.Source.Kind != "os" || added.Task.Status != "inbox" {
		t.Errorf("os capture: %+v", added.Task)
	}
	if added.Task.Source.App != "Safari" || added.Task.Source.Title != "" {
		t.Errorf("--app: app %q, title %q", added.Task.Source.App, added.Task.Source.Title)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "add", "--stdin", "--source", "os", "--ready"); doc["code"] != "operator_only" {
		t.Errorf("a capture cannot go straight to Ready: %v", doc)
	}
}

func TestAnAgentCannotUseTheOSSourceOrReadyToSkipTheGate(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	if doc := failedTaskJSON(t, db, "default", 3, "add", "x", "--as", "bot", "--source", "os"); doc["code"] != "invalid_input" {
		t.Errorf("an agent claiming to be a capture: %v", doc)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "add", "x", "--as", "bot", "--ready"); doc["code"] != "operator_only" {
		t.Errorf("an agent adding to Ready: %v", doc)
	}
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "from an agent", "--as", "bot")
	if added.Task.Source.Kind != "agent" || added.Task.Status != "inbox" {
		t.Errorf("an agent's task: %+v", added.Task)
	}
	setenv(t, "CLAUDECODE", "1") // the same under an agent-context marker, with no --as at all
	if doc := failedTaskJSON(t, db, "default", 3, "add", "x", "--ready"); doc["code"] != "operator_only" {
		t.Errorf("an agent context adding to Ready: %v", doc)
	}
}

// The store refuses Ready to an agent and to a capture too, with the same code
// and its own words, so the code alone does not show that the CLI's own refusal
// spoke. Its words are the ones the spec gives: run it in your own terminal.
func TestTaskAddReadyRefusalsSayWhoRefusedAndWhatToDo(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	message := func(doc map[string]any) string { s, _ := doc["error"].(string); return s }
	byName := message(failedTaskJSON(t, db, "default", 3, "add", "x", "--as", "bot", "--ready"))
	if !strings.Contains(byName, "--as") || !strings.Contains(byName, "your own terminal") {
		t.Errorf("an agent named by --as: %q", byName)
	}
	capture := message(failedTaskJSON(t, db, "default", 3, "add", "--stdin", "--source", "os", "--ready"))
	if !strings.Contains(capture, "Inbox") {
		t.Errorf("a capture: %q", capture)
	}
	setenv(t, "CLAUDECODE", "1")
	byMarker := message(failedTaskJSON(t, db, "default", 3, "add", "x", "--ready"))
	if !strings.Contains(byMarker, "CLAUDECODE") || !strings.Contains(byMarker, "your own terminal") {
		t.Errorf("an agent context: %q", byMarker)
	}
}

// An agent context is an agent: it has no hourly allowance (past the twentieth
// task it still adds), whatever its name, and --source os is not open to it.
func TestTaskAddByAnAgentIsNotLimitedPerHourWhateverItsName(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	raw, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	store := tasks.NewStore(raw.DB)
	for i := 1; i <= 20; i++ {
		in := tasks.AddInput{Title: fmt.Sprintf("agent task %d", i)}
		if _, _, err := store.Add(context.Background(), "default", in, tasks.Actor{Kind: tasks.Agent, Name: "seed"}); err != nil {
			t.Fatalf("agent task %d: %v", i, err)
		}
	}
	raw.Close()
	setenv(t, "CLAUDECODE", "1") // an agent context, with no --as
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "one more")
	if !added.Created || added.Task.Source.Kind != "agent" {
		t.Errorf("an agent context's 21st task of the hour: %+v", added)
	}
	mustTaskJSON(t, db, "default", &added, "", "add", "and another", "--as", "someone-else")
	if !added.Created || added.Task.Source.Kind != "agent" {
		t.Errorf("another name's task: %+v", added)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "add", "one more", "--source", "os"); doc["code"] != "invalid_input" {
		t.Errorf("--source os is not open to an agent: %v", doc)
	}
	setenv(t, "CLAUDECODE", "")
	mustTaskJSON(t, db, "default", &added, "", "add", "the operator")
	if !added.Created || added.Task.Source.Kind != "cli" {
		t.Errorf("the operator's task: %+v", added)
	}
}

func TestTaskAddAsTheOperatorMayGoStraightToReady(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "urgent", "--ready")
	if added.Task.Status != "ready" {
		t.Errorf("status %q", added.Task.Status)
	}
}

func TestTaskCommandsActOnTheProfileNamedByTheGlobalFlag(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	raw, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.DB.Exec(`INSERT INTO profiles (id, name) VALUES ('work-id', 'Work')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	var added addedJSON
	mustTaskJSON(t, db, "Work", &added, "", "add", "by name")
	if added.Profile.ID != "work-id" || added.Profile.Name != "Work" {
		t.Errorf("a profile may be named by its name: %+v", added.Profile)
	}
	if doc := failedTaskJSON(t, db, "no-such-profile", 3, "add", "x"); doc["code"] != "invalid_input" {
		t.Errorf("an unknown profile: %v", doc)
	}
}

// taskErr is the one place that turns a refusal of the store into an exit code
// and the code of the --json error document.
func TestTaskErrGivesEveryStoreRefusalItsExitCodeAndCode(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err  error
		exit int
		code string
	}{
		{fmt.Errorf("%w: #9", tasks.ErrNotFound), 2, "not_found"},
		{fmt.Errorf("%w: add a task straight to Ready", tasks.ErrOperatorOnly), 3, "operator_only"},
		{fmt.Errorf("%w: #9 is in the Inbox", tasks.ErrNotReady), 3, "not_ready"},
		{fmt.Errorf("%w: #9", tasks.ErrNotClaimant), 3, "not_claimant"},
		{fmt.Errorf("%w: twenty an hour", tasks.ErrLimit), 3, "limit"},
		{fmt.Errorf("%w: a task needs a title", tasks.ErrInvalid), 3, "invalid_input"},
		{&tasks.ClaimedError{By: "bot", Until: time.Date(2026, 10, 6, 12, 30, 0, 0, time.UTC)}, 3, "claimed"},
	} {
		err := td.taskErr(c.err)
		var code any
		var fields jsonErrorFields
		if errors.As(err, &fields) {
			code = fields.JSONErrorFields()["code"]
		}
		if exitCode(err) != c.exit || code != c.code {
			t.Errorf("%v: exit %d, code %v; want exit %d, code %q", c.err, exitCode(err), code, c.exit, c.code)
		}
	}
	var fields jsonErrorFields
	claimed := td.taskErr(&tasks.ClaimedError{By: "bot", Until: time.Date(2026, 10, 6, 12, 30, 0, 0, time.UTC)})
	if !errors.As(claimed, &fields) || fields.JSONErrorFields()["claimed_by"] != "bot" ||
		fields.JSONErrorFields()["claimed_until"] != "2026-10-06T12:30:00Z" {
		t.Errorf("a claimed task names who holds it and until when: %v", claimed)
	}
	plain := errors.New("disk full")
	if td.taskErr(plain) != plain || td.taskErr(nil) != nil {
		t.Error("an error that is not a refusal of the store passes through, and nil stays nil")
	}
}

// An agent is whoever names itself (--as, else MONOAGENT_ACTOR) or runs under an
// agent-context marker; the operator is everyone else.
func TestTaskCallerIsAnAgentByNameOrByMarker(t *testing.T) {
	t.Parallel()
	newTaskTestDB(t) // the operator's environment: no marker, no MONOAGENT_ACTOR
	op := testDeps(t, "", "", false).callerFor("")
	if _, err := op.operator("do it"); op.isAgent() || err != nil {
		t.Errorf("no name and no marker: agent %v, %v", op.isAgent(), err)
	}
	if _, err := op.agent(); exitCode(err) != 3 {
		t.Errorf("the operator asked for an agent: %v", err)
	}
	if a, err := testDeps(t, "", "", false).callerFor(" bot ").agent(); err != nil || a.Kind != tasks.Agent || a.Name != "bot" {
		t.Errorf("--as: %+v, %v", a, err)
	}
	setenv(t, "MONOAGENT_ACTOR", "from-env")
	if a, err := testDeps(t, "", "", false).callerFor("").agent(); err != nil || a.Name != "from-env" {
		t.Errorf("MONOAGENT_ACTOR: %+v, %v", a, err)
	}
	if a, err := testDeps(t, "", "", false).callerFor("flag").agent(); err != nil || a.Name != "flag" {
		t.Errorf("--as must win over MONOAGENT_ACTOR: %+v, %v", a, err)
	}
	setenv(t, "MONOAGENT_ACTOR", "")
	setenv(t, "CLAUDECODE", "1")
	m := testDeps(t, "", "", false).callerFor("")
	if !m.isAgent() || m.actor.Name != "" || m.marker != "CLAUDECODE" {
		t.Errorf("a marker and no name: %+v", m)
	}
	if _, err := m.agent(); exitCode(err) != 3 || !strings.Contains(err.Error(), "--as NAME") {
		t.Errorf("an agent without a name must be told to choose one: %v", err)
	}
	_, err := m.operator("approve a task")
	var fields jsonErrorFields
	if exitCode(err) != 3 || !errors.As(err, &fields) || fields.JSONErrorFields()["code"] != "operator_only" {
		t.Errorf("an agent asked to act as the operator: %v", err)
	}
}

func TestTaskIDsAndColumnLabels(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int64{"42": 42, "#42": 42, " #7 ": 7} {
		if got, err := td.parseTaskID(in); err != nil || got != want {
			t.Errorf("td.parseTaskID(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "#", "0", "-3", "4x", "#-1"} {
		if _, err := td.parseTaskID(in); exitCode(err) != 3 {
			t.Errorf("td.parseTaskID(%q): %v, want exit 3", in, err)
		}
	}
	if ids, err := td.parseTaskIDs([]string{"1", "#2"}); err != nil || len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Errorf("parseTaskIDs: %v, %v", ids, err)
	}
	if _, err := td.parseTaskIDs([]string{"1", "x"}); exitCode(err) != 3 {
		t.Errorf("parseTaskIDs with a bad id: %v, want exit 3", err)
	}
	// A bad argument is repeated in the message only in part: whole up to 64 runes,
	// and beyond that its first 63 runes and an ellipsis, 64 runes exactly. A cut
	// never splits a character (the last argument has 2 bytes each: %q would show
	// half of one as a \x escape, and the comparison would fail).
	ellipsis := "\U00002026"
	for _, c := range []struct{ in, want string }{
		{strings.Repeat("9", 64), strings.Repeat("9", 64)},
		{strings.Repeat("9", 65), strings.Repeat("9", 63) + ellipsis},
		{strings.Repeat("9", 10<<10), strings.Repeat("9", 63) + ellipsis},
		{strings.Repeat("\xc3\xa9", 5<<10), strings.Repeat("\xc3\xa9", 63) + ellipsis},
	} {
		_, one := td.parseTaskID(c.in)
		_, many := td.parseTaskIDs([]string{"1", c.in})
		for _, err := range []error{one, many} {
			quoted, _, _ := strings.Cut(errText(err), " is not a task id")
			got, uerr := strconv.Unquote(quoted)
			if exitCode(err) != 3 || uerr != nil || got != c.want {
				t.Errorf("a %d-byte argument: exit %d, the message repeats %d runes (unquote: %v), want %d runes: %.100q",
					len(c.in), exitCode(err), utf8.RuneCountInString(got), uerr, utf8.RuneCountInString(c.want), errText(err))
			}
		}
	}
	for st, want := range map[tasks.Status]string{
		tasks.StatusInbox: "Inbox", tasks.StatusReady: "Ready", tasks.StatusInProgress: "In progress",
		tasks.StatusReview: "Review", tasks.StatusDone: "Done", tasks.StatusArchived: "Archived",
	} {
		if got := columnLabel(st); got != want {
			t.Errorf("columnLabel(%q) = %q, want %q", st, got, want)
		}
	}
}
