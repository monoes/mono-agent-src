package task

import (
	"strings"
	"testing"
)

// sessionDoc is the part of a session's document that the tests read.
type sessionDoc struct {
	Session struct {
		ID                int64   `json:"id"`
		State             string  `json:"state"`
		BudgetUSD         float64 `json:"budget_usd"`
		SpentUSD          float64 `json:"spent_usd"`
		MaxTasks          int     `json:"max_tasks"`
		AutoCloseVerified bool    `json:"auto_close_verified"`
		AccessLevel       string  `json:"access_level"`
		ApprovedBy        string  `json:"approved_by"`
		Archived          bool    `json:"archived"`
		Counts            struct {
			Ready, Review, Done, Inbox, Total, Blocked, Verified int
			InProgress                                           int `json:"in_progress"`
		} `json:"counts"`
	} `json:"session"`
}

type sessionTaskDoc struct {
	Task *struct {
		ID         int64   `json:"id"`
		Status     string  `json:"status"`
		SessionID  int64   `json:"session_id"`
		Verified   bool    `json:"verified"`
		Blocked    bool    `json:"blocked"`
		DependsOn  []int64 `json:"depends_on"`
		Acceptance string  `json:"acceptance"`
	} `json:"task"`
}

func sessionNew(t *testing.T, db string, args ...string) int64 {
	t.Helper()
	var d sessionDoc
	if len(args) == 0 || strings.HasPrefix(args[0], "--") {
		args = append([]string{"A", "session"}, args...)
	}
	mustTaskJSON(t, db, "default", &d, "", append([]string{"session", "new"}, args...)...)
	if d.Session.State != "draft" {
		t.Fatalf("a new session is a draft: %+v", d.Session)
	}
	return d.Session.ID
}

func sessionAdd(t *testing.T, db string, session int64, title string, extra ...string) int64 {
	t.Helper()
	var d sessionTaskDoc
	args := append([]string{"add", title, "--session", id(session), "--acceptance", "passes: " + title}, extra...)
	mustTaskJSON(t, db, "default", &d, "", args...)
	if d.Task == nil || d.Task.SessionID != session {
		t.Fatalf("add %q: %+v", title, d.Task)
	}
	return d.Task.ID
}

func sessionState(t *testing.T, db string, session int64) sessionDoc {
	t.Helper()
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "show", id(session))
	return d
}

func TestSessionLifecycleEndToEnd(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := sessionNew(t, db, "Ship", "the", "flag", "--goal", "add a flag")
	a := sessionAdd(t, db, s, "parse")
	b := sessionAdd(t, db, s, "wire", "--depends-on", id(a))
	c := sessionAdd(t, db, s, "docs", "--depends-on", id(a)+","+id(b))

	// a draft hands out nothing and the personal board shows nothing of it
	var next sessionTaskDoc
	mustTaskJSON(t, db, "default", &next, "", "next", "--session", id(s))
	if next.Task != nil {
		t.Fatalf("a draft hands out a task: %+v", next.Task)
	}
	var personal listJSON
	mustTaskJSON(t, db, "default", &personal, "", "list", "--status", "inbox,ready")
	if len(personal.Tasks) != 0 {
		t.Fatalf("the personal list shows %d session tasks", len(personal.Tasks))
	}
	mustTaskJSON(t, db, "default", &next, "", "next")
	if next.Task != nil {
		t.Fatal("personal next returns a session task")
	}

	var ap sessionDoc
	mustTaskJSON(t, db, "default", &ap, "", "session", "approve", id(s))
	if ap.Session.State != "running" || ap.Session.ApprovedBy != "you" || ap.Session.Counts.Ready != 3 {
		t.Fatalf("approved: %+v", ap.Session)
	}

	// next follows the dependencies: a, then (after a is Done) b, then c
	for _, want := range []int64{a, b, c} {
		var got sessionTaskDoc
		mustTaskJSON(t, db, "default", &got, "", "next", "--session", id(s), "--claim", "--as", "worker")
		if got.Task == nil || got.Task.ID != want || got.Task.Acceptance == "" {
			t.Fatalf("next --claim: want #%d, got %+v", want, got.Task)
		}
		mustTaskJSON(t, db, "default", &got, "", "artifact", id(want), "--kind", "commit", "--ref", "abc"+id(want), "--as", "worker")
		mustTaskJSON(t, db, "default", &got, "", "finish", id(want), "--result", "done it", "--as", "worker")
		if got.Task.Status != "review" {
			t.Fatalf("finish: %+v", got.Task)
		}
		mustTaskJSON(t, db, "default", &got, "", "verify", id(want), "--pass", "--note", "ok", "--as", "checker")
		if !got.Task.Verified || got.Task.Status != "review" {
			t.Fatalf("a pass marks verified and leaves it in review: %+v", got.Task)
		}
		// a verified task in Review settles its dependents: the next one is offered before the
		// operator closes it (auto-close is off), and nothing after the last
		var nextDoc sessionTaskDoc
		mustTaskJSON(t, db, "default", &nextDoc, "", "next", "--session", id(s))
		wantNext := map[int64]int64{a: b, b: c, c: 0}[want]
		if (nextDoc.Task == nil) != (wantNext == 0) || (nextDoc.Task != nil && nextDoc.Task.ID != wantNext) {
			t.Fatalf("after #%d is verified next offered %+v, want #%d", want, nextDoc.Task, wantNext)
		}
		mustTaskJSON(t, db, "default", &got, "", "move", id(want), "done")
	}
	st := sessionState(t, db, s)
	if st.Session.Counts.Done != 3 || st.Session.Counts.Verified != 3 {
		t.Fatalf("after the run: %+v", st.Session.Counts)
	}
	var shown struct {
		Artifacts []struct{ Kind, Ref string } `json:"artifacts"`
	}
	mustTaskJSON(t, db, "default", &shown, "", "show", id(a))
	if len(shown.Artifacts) != 1 || shown.Artifacts[0].Kind != "commit" {
		t.Fatalf("show: %+v", shown)
	}
	var stopped sessionDoc
	mustTaskJSON(t, db, "default", &stopped, "", "session", "stop", id(s))
	if stopped.Session.State != "stopped" {
		t.Fatalf("stop: %+v", stopped.Session)
	}
	var board struct {
		Session struct{ ID int64 } `json:"session"`
		Tasks   map[string][]any   `json:"tasks"`
	}
	mustTaskJSON(t, db, "default", &board, "", "board", "--session", id(s))
	if board.Session.ID != s || len(board.Tasks["done"]) != 3 {
		t.Fatalf("session board: %+v", board)
	}
	out, _, err := runTask(t, db, "default", false, "", "session", "plan", id(s))
	if err != nil || !strings.Contains(out, "done when:") || !strings.Contains(out, "(after #") {
		t.Fatalf("plan text: %v\n%s", err, out)
	}
}

// sessionOperatorCommands is every verb of the session group that only the operator may run.
var sessionOperatorCommands = [][]string{
	{"session", "approve", "1"},
	{"session", "resume", "1"},
	{"session", "archive", "1"},
	{"session", "budget", "1", "--usd", "5"},
	{"session", "budget", "1", "--auto-close", "on"},
	{"session", "budget", "1", "--max-tasks", "999"},
	{"session", "budget", "1", "--access", "full"},
	{"session", "budget", "1", "--spend", "1"},
	{"undepend", "1", "2"},
}

// Each of them refuses every kind of agent context, and the state it would have changed is untouched.
func TestSessionOperatorVerbsRefuseEveryAgentContext(t *testing.T) {
	t.Parallel()
	for name, setup := range opsAgentContexts {
		t.Run(name, func(t *testing.T) {
			db := newTaskTestDB(t)
			s := sessionNew(t, db)
			_ = s
			one := sessionAdd(t, db, 1, "t")
			_ = one
			extra := setup(t)
			for _, c := range sessionOperatorCommands {
				doc := failedTaskJSON(t, db, "default", 3, append(append([]string{}, c...), extra...)...)
				if doc["code"] != "operator_only" {
					t.Errorf("task %s: %v, want code operator_only", strings.Join(c, " "), doc)
				}
			}
			st := sessionState(t, db, 1)
			if st.Session.State != "draft" || st.Session.BudgetUSD != 0 || st.Session.SpentUSD != 0 || st.Session.MaxTasks != 200 ||
				st.Session.AutoCloseVerified || st.Session.AccessLevel != "scoped" || st.Session.Archived || st.Session.Counts.Inbox != 1 {
				t.Errorf("a refused verb changed the session: %+v", st.Session)
			}
		})
	}
}

func TestSessionAgentCanPlanButNotOpenIt(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "new", "plan", "--as", "lead", "--goal", "g")
	if d.Session.State != "draft" {
		t.Fatalf("%+v", d.Session)
	}
	for _, flag := range [][]string{{"--budget-usd", "9"}, {"--max-tasks", "5000"}, {"--access", "full"}} {
		args := append([]string{"session", "new", "x", "--as", "lead"}, flag...)
		if doc := failedTaskJSON(t, db, "default", 3, args...); doc["code"] != "operator_only" {
			t.Errorf("%v: %v", flag, doc)
		}
	}
	var add sessionTaskDoc
	mustTaskJSON(t, db, "default", &add, "", "add", "t", "--session", "1", "--acceptance", "x", "--as", "lead")
	if add.Task == nil || add.Task.Status != "inbox" {
		t.Fatalf("an agent's session task waits in the draft: %+v", add.Task)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "session", "approve", "1", "--as", "lead"); doc["code"] != "operator_only" {
		t.Fatalf("agent approve: %v", doc)
	}
	// pause and stop are open to an agent
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", "1")
	mustTaskJSON(t, db, "default", &d, "", "session", "pause", "1", "--as", "lead")
	if d.Session.State != "paused" {
		t.Fatalf("pause: %+v", d.Session)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "session", "resume", "1", "--as", "lead"); doc["code"] != "operator_only" {
		t.Fatalf("an agent resumes the operator's pause: %v", doc)
	}
	if st := sessionState(t, db, 1); st.Session.State != "paused" {
		t.Fatalf("state after the refused resume: %s", st.Session.State)
	}
}

func TestSessionFlagsMeanNothingOnThePersonalBoard(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, flag := range []string{"--acceptance", "--depends-on", "--parent", "--kind", "--executor"} {
		doc := failedTaskJSON(t, db, "default", 3, "add", "x", flag, "1")
		if doc["code"] != "invalid_input" || !strings.Contains(doc["error"].(string), "--session") {
			t.Errorf("%s: %v", flag, doc)
		}
	}
	if doc := failedTaskJSON(t, db, "default", 3, "add", "x", "--session", "1", "--ready", "--acceptance", "y"); doc["code"] != "invalid_input" {
		t.Errorf("--ready with --session: %v", doc)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "add", "x", "--session", "1"); doc["code"] == "" {
		t.Errorf("a session task with no criteria or session: %v", doc)
	}
	// nothing was added anywhere
	var l listJSON
	mustTaskJSON(t, db, "default", &l, "", "list", "--status", "inbox")
	if len(l.Tasks) != 0 {
		t.Fatalf("a refused add left %d tasks", len(l.Tasks))
	}
}

func TestSessionSplitDependArtifactVerifyCommands(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := sessionNew(t, db)
	a := sessionAdd(t, db, s, "a")
	b := sessionAdd(t, db, s, "b", "--depends-on", id(a))
	var parts struct {
		Tasks []struct {
			ID        int64   `json:"id"`
			DependsOn []int64 `json:"depends_on"`
		} `json:"tasks"`
	}
	mustTaskJSON(t, db, "default", &parts, "", "split", id(b), "--into", "b1|first passes", "--into", "b2|second passes")
	if len(parts.Tasks) != 2 || len(parts.Tasks[0].DependsOn) != 1 || parts.Tasks[0].DependsOn[0] != a {
		t.Fatalf("split keeps the dependencies: %+v", parts)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "split", id(a), "--into", "no bar"); doc["code"] != "invalid_input" {
		t.Errorf("split syntax: %v", doc)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "depend", id(a), "--on", id(parts.Tasks[0].ID)); doc["code"] != "invalid_input" || !strings.Contains(doc["error"].(string), "cycle") {
		t.Errorf("a dependency cycle: %v", doc)
	}
	var dep sessionTaskDoc
	mustTaskJSON(t, db, "default", &dep, "", "depend", id(parts.Tasks[1].ID), "--on", id(parts.Tasks[0].ID), "--as", "lead")
	if len(dep.Task.DependsOn) != 2 {
		t.Fatalf("depend: %+v", dep.Task)
	}
	mustTaskJSON(t, db, "default", &dep, "", "undepend", id(parts.Tasks[1].ID), id(parts.Tasks[0].ID))
	if len(dep.Task.DependsOn) != 1 {
		t.Fatalf("undepend: %+v", dep.Task)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "verify", id(a), "--pass", "--fail"); doc["code"] != "invalid_input" {
		t.Errorf("verify with both: %v", doc)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "verify", id(a), "--fail", "--as", "c"); doc["code"] != "invalid_input" {
		t.Errorf("verify a task that is not in review: %v", doc)
	}
	// a personal task is not a session's
	p := opsAdd(t, db, "mine")
	if doc := failedTaskJSON(t, db, "default", 3, "verify", id(p), "--pass", "--as", "c"); !strings.Contains(doc["error"].(string), "personal board") {
		t.Errorf("verify a personal task: %v", doc)
	}
}

func TestSessionBoundAgentEnvironment(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	one, two := sessionNew(t, db), sessionNew(t, db)
	t1 := sessionAdd(t, db, one, "one")
	t2 := sessionAdd(t, db, two, "two")
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", id(one))
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", id(two))
	personal := opsAdd(t, db, "personal")
	setenv(t, "MONOAGENT_TASK_SESSION", id(one))
	// bound to session one: it works there, and nowhere else
	var got sessionTaskDoc
	mustTaskJSON(t, db, "default", &got, "", "next", "--session", id(one), "--claim", "--as", "w")
	if got.Task == nil || got.Task.ID != t1 {
		t.Fatalf("own session: %+v", got.Task)
	}
	for _, args := range [][]string{
		{"next", "--session", id(two), "--claim", "--as", "w"},
		{"claim", id(t2), "--as", "w"},
		{"comment", id(t2), "hello", "--as", "w"},
		{"next", "--as", "w"},
		{"add", "x", "--as", "w"},
		{"session", "stop", id(two), "--as", "w"},
		{"session", "show", id(two), "--as", "w"},
		{"show", id(personal), "--as", "w"},
		{"show", id(t2), "--as", "w"},
	} {
		if _, _, err := runTask(t, db, "default", true, "", args...); err == nil {
			t.Errorf("task %s: a bound agent reached another session", strings.Join(args, " "))
		}
	}
	setenv(t, "MONOAGENT_TASK_SESSION", "")
	st := sessionState(t, db, two)
	if st.Session.State != "running" || st.Session.Counts.Ready != 1 || st.Session.Counts.InProgress != 0 {
		t.Fatalf("session two was touched: %+v", st.Session)
	}
}

func TestSessionTaskBoundAgentEnvironment(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	one := sessionNew(t, db)
	mine := sessionAdd(t, db, one, "mine")
	other := sessionAdd(t, db, one, "other")
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", id(one))
	setenv(t, "MONOAGENT_TASK_SESSION", id(one))
	setenv(t, "MONOAGENT_TASK_ID", id(mine))
	var got sessionTaskDoc
	mustTaskJSON(t, db, "default", &got, "", "claim", id(mine), "--as", "w")
	for _, args := range [][]string{
		{"claim", id(other), "--as", "w"},
		{"next", "--session", id(one), "--claim", "--as", "w"},
		{"add", "x", "--session", id(one), "--acceptance", "y", "--as", "w"},
		{"comment", id(other), "hello", "--as", "w"},
		{"finish", id(other), "--result", "stolen", "--as", "w"},
	} {
		if _, _, err := runTask(t, db, "default", true, "", args...); err == nil {
			t.Errorf("task %s: an agent bound to a task reached another", strings.Join(args, " "))
		}
	}
	setenv(t, "MONOAGENT_TASK_ID", "")
	setenv(t, "MONOAGENT_TASK_SESSION", "")
	st := sessionState(t, db, one)
	if st.Session.Counts.Total != 2 || st.Session.Counts.InProgress != 1 || st.Session.Counts.Ready != 1 {
		t.Fatalf("the session was touched beyond the bound task: %+v", st.Session.Counts)
	}
}

func TestSessionTaskVerifierEnvironment(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	one := sessionNew(t, db)
	mine := sessionAdd(t, db, one, "mine")
	other := sessionAdd(t, db, one, "other")
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", id(one))
	var got sessionTaskDoc
	mustTaskJSON(t, db, "default", &got, "", "claim", id(mine), "--as", "worker")
	mustTaskJSON(t, db, "default", &got, "", "finish", id(mine), "--result", "done", "--as", "worker")
	setenv(t, "MONOAGENT_TASK_SESSION", id(one))
	setenv(t, "MONOAGENT_TASK_ID", id(mine))
	setenv(t, "MONOAGENT_TASK_ROLE", "verifier")
	for _, args := range [][]string{
		{"claim", id(other), "--as", "v"},
		{"claim", id(mine), "--as", "v"},
		{"finish", id(mine), "--result", "mine now", "--as", "v"},
		{"verify", id(other), "--pass", "--as", "v"},
		{"comment", id(other), "hello", "--as", "v"},
	} {
		if _, _, err := runTask(t, db, "default", true, "", args...); err == nil {
			t.Errorf("task %s: a verifier did what only the executor does", strings.Join(args, " "))
		}
	}
	mustTaskJSON(t, db, "default", &got, "", "comment", id(mine), "ran the tests", "--as", "v")
	mustTaskJSON(t, db, "default", &got, "", "verify", id(mine), "--pass", "--note", "ok", "--as", "v")
	setenv(t, "MONOAGENT_TASK_ROLE", "")
	setenv(t, "MONOAGENT_TASK_ID", "")
	setenv(t, "MONOAGENT_TASK_SESSION", "")
	st := sessionState(t, db, one)
	if st.Session.Counts.Verified != 1 || st.Session.Counts.Ready != 1 || st.Session.Counts.InProgress != 0 {
		t.Fatalf("a verifier's binding did more or less than verify its task: %+v", st.Session.Counts)
	}
}
