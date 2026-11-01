package task

import (
	"strings"
	"testing"
)

// The coder-session token alone marks a process as an agent in callerFor, like the task binding,
// the task session and the task id; with none of them set the caller is the operator.
func TestSessionTokenAloneMakesTheCallerAnAgent(t *testing.T) {
	t.Parallel()
	d := testDeps(t, newTaskTestDB(t), "default", true)
	if c := d.callerFor(""); c.isAgent() {
		t.Fatalf("no token, yet an agent: %+v", c)
	}
	setenv(t, "MONOAGENT_SESSION_TOKEN", "tok")
	c := d.callerFor("")
	if !c.isAgent() || c.marker != "MONOAGENT_SESSION_TOKEN" || c.actor.Name != "" {
		t.Fatalf("the session token did not make an unnamed agent: %+v", c)
	}
	if c := d.callerFor("lead"); !c.isAgent() || c.actor.Name != "lead" {
		t.Fatalf("a token holder that names itself: %+v", c)
	}
}

// The operator branch of the three hybrid verbs is closed to a process holding only the session
// token; the refusal comes from the token marker, before any delegation is read.
func TestHybridVerbsRefuseATokenOnlyProcess(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	opsAdd(t, db, "captured")
	setenv(t, "MONOAGENT_SESSION_TOKEN", "tok")
	for _, args := range [][]string{{"session", "approve", "1"}, {"approve", "1"}, {"board"}} {
		doc := failedTaskJSON(t, db, "default", 3, args...)
		if doc["code"] != "operator_only" {
			t.Errorf("task %s: %v", strings.Join(args, " "), doc)
		}
	}
}

// A named agent that holds the session token keeps the delegated paths: with the operator's
// `agents allow --view --approve` it approves (recorded as delegated) and sees the board.
func TestANamedAgentHoldingTheSessionTokenKeepsTheDelegatedPaths(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	ids := []int64{opsAdd(t, db, "captured")}
	// The operator sets the delegation from its own terminal; the agent holds the token.
	agentsSet(t, db, "default", "allow", "--view")
	setenv(t, "MONOAGENT_SESSION_TOKEN", "tok")
	var board struct {
		Tasks map[string][]taskJSON `json:"tasks"`
	}
	mustTaskJSON(t, db, "default", &board, "", "board", "--as", "amy")
	if len(board.Tasks["inbox"]) != 1 {
		t.Fatalf("a delegated agent's board: %+v", board.Tasks)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "approve", id(ids[0]), "--as", "amy"); doc["code"] != "operator_only" {
		t.Fatalf("view is not approve: %v", doc)
	}
	setenv(t, "MONOAGENT_SESSION_TOKEN", "")
	agentsSet(t, db, "default", "allow", "--view", "--approve")
	setenv(t, "MONOAGENT_SESSION_TOKEN", "tok")
	mustTaskJSON(t, db, "default", &struct{}{}, "", "approve", id(ids[0]), "--as", "amy")
	shown := opsShow(t, db, ids[0])
	last := shown.Events[len(shown.Events)-1]
	if shown.Task.Status != "ready" || last.Actor != "amy" || !strings.Contains(last.Note, "delegated") {
		t.Fatalf("after the delegated approval: %s, %+v", shown.Task.Status, last)
	}
}

// What a token-holding process does without naming itself, pinned for the verbs that were open to
// it as the operator before: reads and plain adds keep working, and the verbs that need an agent's
// name now ask for it. (The lead's approval is TestLeadApprovesWithItsRealTokenAndGiantWorkWaitsForThePerson.)
func TestAnUnnamedTokenHolderStillReadsAndAddsButMustNameItselfToAct(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	opsAdd(t, db, "captured")
	setenv(t, "MONOAGENT_SESSION_TOKEN", "tok")
	for _, args := range [][]string{{"show", "1"}, {"next"}, {"session", "list"}, {"agents", "show"}, {"add", "another"}} {
		if _, _, err := runTask(t, db, "default", true, "", args...); err != nil {
			t.Errorf("task %s failed for a token holder: %v", strings.Join(args, " "), err)
		}
	}
	for _, args := range [][]string{{"claim", "1"}, {"comment", "1", "hi"}, {"session", "new", "t"}} {
		doc := failedTaskJSON(t, db, "default", 3, args...)
		if msg, _ := doc["error"].(string); !strings.Contains(msg, "name yourself") {
			t.Errorf("task %s: %v", strings.Join(args, " "), doc)
		}
	}
	out, _, err := runTask(t, db, "default", true, "", "list")
	if err != nil || strings.Contains(out, "captured") {
		t.Errorf("an unnamed token holder's default list shows the Inbox: %v\n%s", err, out)
	}
}
