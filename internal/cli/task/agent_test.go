package task

import (
	"strings"
	"testing"
)

type nextJSON struct {
	Task *taskJSON `json:"task"`
}

func TestAnAgentWorksATaskFromStartToFinish(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "fix the flaky test", "--ready")
	n := id(added.Task.ID)

	var peek nextJSON
	mustTaskJSON(t, db, "default", &peek, "", "next", "--as", "bot-1")
	if peek.Task == nil || peek.Task.ID != added.Task.ID || peek.Task.Status != "ready" || peek.Task.Claim != nil {
		t.Fatalf("peek: %+v", peek.Task)
	}
	var claimed nextJSON
	mustTaskJSON(t, db, "default", &claimed, "", "next", "--claim", "--as", "bot-1")
	if claimed.Task == nil || claimed.Task.Status != "in_progress" || claimed.Task.Claim == nil || claimed.Task.Claim.By != "bot-1" {
		t.Fatalf("claim: %+v", claimed.Task)
	}
	doc := failedTaskJSON(t, db, "default", 3, "claim", n, "--as", "bot-2")
	if doc["code"] != "claimed" || doc["claimed_by"] != "bot-1" || doc["claimed_until"] == nil {
		t.Errorf("a task another agent holds: %v", doc)
	}

	var one struct {
		Task taskJSON `json:"task"`
	}
	mustTaskJSON(t, db, "default", &one, "", "comment", n, "reproduced it", "locally", "--as", "bot-1")
	if one.Task.ID != added.Task.ID {
		t.Errorf("comment: %+v", one.Task)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "comment", n, "mine now", "--as", "bot-2"); doc["code"] != "not_claimant" {
		t.Errorf("a comment by another agent: %v", doc)
	}
	mustTaskJSON(t, db, "default", &one, "", "finish", n, "--as", "bot-1", "--result", "fixed in PR 41")
	if one.Task.Status != "review" || one.Task.Claim != nil {
		t.Errorf("finish: %+v", one.Task)
	}
	var none nextJSON
	mustTaskJSON(t, db, "default", &none, "", "next", "--claim", "--as", "bot-1")
	if none.Task != nil {
		t.Errorf("nothing is left to claim: %+v", none.Task)
	}
	out, _, err := runTask(t, db, "default", true, "", "next", "--as", "bot-1")
	if err != nil || !strings.Contains(out, `"task": null`) {
		t.Errorf("next with nothing to do must say so with a null task: %q, %v", out, err)
	}
}

func TestFinishAndReleaseHandTheTaskBack(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var a, b addedJSON
	mustTaskJSON(t, db, "default", &a, "", "add", "needs an answer", "--ready")
	mustTaskJSON(t, db, "default", &b, "", "add", "needs the vpn", "--ready")
	var claimed nextJSON
	mustTaskJSON(t, db, "default", &claimed, "", "claim", id(a.Task.ID), "--as", "bot")
	mustTaskJSON(t, db, "default", &claimed, "", "claim", id(b.Task.ID), "--as", "bot")

	if doc := failedTaskJSON(t, db, "default", 3, "finish", id(a.Task.ID), "--as", "bot"); doc["code"] != "invalid_input" {
		t.Errorf("finish with neither a result nor a question: %v", doc)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "finish", id(a.Task.ID), "--as", "bot", "--result", "r", "--question", "q"); doc["code"] != "invalid_input" {
		t.Errorf("finish with both: %v", doc)
	}
	var one struct {
		Task taskJSON `json:"task"`
	}
	mustTaskJSON(t, db, "default", &one, "", "finish", id(a.Task.ID), "--as", "bot", "--question", "which database?")
	if one.Task.Status != "review" {
		t.Errorf("a question goes to review: %+v", one.Task)
	}
	var shown struct {
		Events []struct {
			Kind string `json:"kind"`
			Note string `json:"note"`
		} `json:"events"`
	}
	mustTaskJSON(t, db, "default", &shown, "", "show", id(a.Task.ID))
	if last := shown.Events[len(shown.Events)-1]; last.Kind != "question" || last.Note != "which database?" {
		t.Errorf("last event: %+v", last)
	}
	mustTaskJSON(t, db, "default", &one, "", "release", id(b.Task.ID), "--as", "bot", "--note", "needs the VPN")
	if one.Task.Status != "ready" || one.Task.Claim != nil {
		t.Errorf("release: %+v", one.Task)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "release", id(b.Task.ID), "--as", "bot"); doc["code"] != "not_claimant" {
		t.Errorf("releasing a task one does not hold: %v", doc)
	}
}

func TestAgentCommandsNeedAName(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "go", "--ready")
	n := id(added.Task.ID)
	for _, args := range [][]string{
		{"next", "--claim"},
		{"claim", n},
		{"finish", n, "--result", "r"},
		{"release", n},
	} {
		doc := failedTaskJSON(t, db, "default", 3, args...)
		if doc["code"] != "invalid_input" || !strings.Contains(doc["error"].(string), "--as") {
			t.Errorf("task %s without a name: %v", strings.Join(args, " "), doc)
		}
	}
	setenv(t, "CLAUDECODE", "1") // an agent context with no name is no better
	doc := failedTaskJSON(t, db, "default", 3, "claim", n)
	if !strings.Contains(doc["error"].(string), "CLAUDECODE") {
		t.Errorf("the refusal should say why it thinks an agent is running: %v", doc)
	}
	setenv(t, "MONOAGENT_ACTOR", "bot") // the name from the environment is enough
	var claimed nextJSON
	mustTaskJSON(t, db, "default", &claimed, "", "claim", n)
	if claimed.Task == nil || claimed.Task.Claim == nil || claimed.Task.Claim.By != "bot" {
		t.Errorf("claim under MONOAGENT_ACTOR: %+v", claimed.Task)
	}
}

func TestTheOperatorsCommentIsNotAnAgentsComment(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "something")
	var one struct {
		Task taskJSON `json:"task"`
	}
	mustTaskJSON(t, db, "default", &one, "", "comment", id(added.Task.ID), "I will look at this tomorrow")
	var shown struct {
		Events []struct {
			Actor string `json:"actor"`
			Kind  string `json:"kind"`
		} `json:"events"`
	}
	mustTaskJSON(t, db, "default", &shown, "", "show", id(added.Task.ID))
	if last := shown.Events[len(shown.Events)-1]; last.Kind != "comment" || last.Actor != "you" {
		t.Errorf("last event: %+v", last)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "comment", id(added.Task.ID), "from an agent", "--as", "bot"); doc["code"] != "not_claimant" {
		t.Errorf("an agent cannot comment on a task it does not hold: %v", doc)
	}
}

func TestNextTellsAnAgentWhatToDoAndThatTheTextIsData(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "Fix the flaky test\nthe CI job is red on main", "add", "--stdin", "--source", "os")
	var moved struct {
		Tasks []taskJSON `json:"tasks"`
	}
	mustTaskJSON(t, db, "default", &moved, "", "approve", id(added.Task.ID))

	peek, _, err := runTask(t, db, "default", false, "", "next", "--as", "bot")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Next task: #", "Fix the flaky test", "untrusted", "--profile default", "next --claim --as"} {
		if !strings.Contains(peek, want) {
			t.Errorf("next lacks %q:\n%s", want, peek)
		}
	}
	claimed, _, err := runTask(t, db, "default", false, "", "next", "--claim", "--as", "bot")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Claimed #", "untrusted", "comment " + id(added.Task.ID) + " --as bot", "finish " + id(added.Task.ID) + " --as bot --result", "--question", "release", "--profile default"} {
		if !strings.Contains(claimed, want) {
			t.Errorf("next --claim lacks %q:\n%s", want, claimed)
		}
	}
}

func TestDigestIsSilentWithoutReadyTasksAndNeverFails(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	out, errOut, err := runTask(t, db, "default", false, "", "digest")
	if err != nil || out != "" || errOut != "" {
		t.Fatalf("an empty board: stdout %q stderr %q err %v", out, errOut, err)
	}
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "waiting", "--ready")
	out, _, err = runTask(t, db, "default", false, "", "digest")
	if err != nil || !strings.Contains(out, "1 ready") || !strings.Contains(out, "Next: #") || !strings.Contains(out, "--profile default task next --claim") {
		t.Errorf("digest: %q, %v", out, err)
	}
	var doc struct {
		Ready int `json:"ready"`
		Next  *struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"next"`
	}
	mustTaskJSON(t, db, "default", &doc, "", "digest")
	if doc.Ready != 1 || doc.Next == nil || doc.Next.Title != "waiting" {
		t.Errorf("digest as JSON: %+v", doc)
	}
	if _, _, err := runTask(t, db, "no-such-profile", false, "", "digest"); err != nil {
		t.Errorf("digest must exit 0 whatever happens, so a hook can call it: %v", err)
	}
}
