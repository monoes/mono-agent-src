### Task 4: The agent's verbs: `task_claim`, `task_comment`, `task_finish`, `task_release`, `task_add`

**Files:**
- Create: `internal/mcp/task_write.go`
- Modify: `internal/mcp/task_tools.go` (`taskTools` gains the verbs)
- Test: `internal/mcp/task_write_test.go`

**Interfaces:**
- Consumes: Task 3's `taskBoardIntro`, `(*Server).taskBoard`, the fixture (`newTaskFixture`, `taskSetup`, `(f).server`, `(f).call`, `(f).doc`, `(f).add`, `taskOf`) and the existing `toolsListNames`, `toolDefinitions`, `mustCall`, `callAPITool`, `workProfileID`, `workProfileName`; Task 2's `taskView`, `viewOf`, `viewPtr`, `listView`, `taskResult`, `untrustedNote`, `taskToolErr`, `invalidArgs`, `decodeTaskArgs`, `taskIDArg`, `numberArg`; P1's `Store.Next`, `Store.Claim`, `Store.Comment`, `Store.Finish`, `Store.Release`, `Store.Add`, `Store.Get`, `tasks.Outcome`, `tasks.AddInput`, `tasks.AgentTasksPerHour`.
- Produces: `taskWriteTools() []tool` (every one `mutating: true`); handlers `toolTaskClaim`, `toolTaskComment`, `toolTaskFinish`, `toolTaskRelease`, `toolTaskAdd`; `taskClaimResult{Profile, Task *taskView, NextSteps []string, Note}`, `taskAddResult{Profile, Created bool, Task taskView, Note}`; `const maxLeaseMinutes = 1440`; `leaseOf(minutes int64) time.Duration`; `nextSteps(*tasks.Task) []string`; the fixture method `(f).count(query string, args ...any) int`.

- [ ] **Step 1: Write the failing tests**

Create `internal/mcp/task_write_test.go`:

```go
package mcp

// An agent's verbs over the task tools (spec 5.1 and 8): it claims a ready task, reports
// progress and hands the task back; it adds to Inbox only, at most 20 an hour; it never does the
// operator's part; and nothing it sends changes who it is or which board it works.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// count runs a COUNT query on the fixture's database.
func (f *taskFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.Side.DB.QueryRow(query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestAnAgentClaimsWorksAndHandsBackATask(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	first := f.add("default", "first", true)
	second := f.add("default", "second", true)
	me := f.Server.taskActor().Name

	d := f.doc("task_claim", map[string]any{"next": true})
	tk := d["task"].(map[string]any)
	if taskOf(d) != first.ID || tk["status"] != "in_progress" || tk["claim"].(map[string]any)["by"] != me {
		t.Fatalf("task_claim next: %v", d)
	}
	steps := fmt.Sprint(d["next_steps"])
	for _, want := range []string{"task_comment", "task_finish", `"question"`, "task_release", fmt.Sprint(first.ID)} {
		if !strings.Contains(steps, want) {
			t.Errorf("next_steps do not say %s: %s", want, steps)
		}
	}
	d = f.doc("task_comment", map[string]any{"id": first.ID, "text": "halfway"})
	if le := d["task"].(map[string]any)["last_event"].(map[string]any); le["kind"] != "comment" || le["actor"] != me {
		t.Errorf("task_comment: %v", d)
	}
	d = f.doc("task_finish", map[string]any{"id": first.ID, "result": "sent the fix"})
	if tk := d["task"].(map[string]any); tk["status"] != "review" || tk["claim"] != nil {
		t.Errorf("task_finish: %v", d)
	}
	_, events, err := f.Store.Get(context.Background(), "default", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
		if e.Kind != "created" && e.Actor != me {
			t.Errorf("%s was written by %s, want %s", e.Kind, e.Actor, me)
		}
	}
	if got := strings.Join(kinds, ","); got != "created,claimed,comment,result" {
		t.Errorf("history %s", got)
	}

	f.doc("task_claim", map[string]any{"id": second.ID})
	d = f.doc("task_release", map[string]any{"id": second.ID, "note": "needs the VPN"})
	if tk := d["task"].(map[string]any); tk["status"] != "ready" || tk["claim"] != nil {
		t.Errorf("task_release: %v", d)
	}
	if d := f.doc("task_claim", map[string]any{"next": true}); taskOf(d) != second.ID {
		t.Fatalf("the released task is ready again: %v", d)
	}
	f.doc("task_finish", map[string]any{"id": second.ID, "question": "which VPN?"})
	d = f.doc("task_claim", map[string]any{"next": true})
	if d["task"] != nil || !strings.HasPrefix(fmt.Sprint(d["next_steps"]), "[Nothing is ready") {
		t.Errorf("nothing to take: %v", d)
	}
}

func TestTaskClaimTakesIDOrNextAndBoundsTheLease(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	for _, args := range []map[string]any{{}, {"id": 1, "next": true}, {"next": false}} {
		if _, err := f.call("task_claim", args); err == nil || !strings.HasPrefix(err.Error(), "invalid_input: give id") {
			t.Errorf("%v: %v, want id or next, one of the two", args, err)
		}
	}
	for minutes, want := range map[int64]time.Duration{
		0:         30 * time.Minute,
		-5:        30 * time.Minute,
		1440:      24 * time.Hour,
		1441:      24 * time.Hour, // the store clamps this one too: without the bound only the next row fails
		307445735: 24 * time.Hour, // multiplied by a minute in nanoseconds this wraps to 26 seconds
	} {
		tk := f.add("default", fmt.Sprintf("lease %d", minutes), true)
		args := map[string]any{"id": tk.ID}
		if minutes != 0 {
			args["lease_minutes"] = minutes
		}
		f.doc("task_claim", args)
		got, _, err := f.Store.Get(context.Background(), "default", tk.ID)
		if err != nil || got.Claim == nil {
			t.Fatalf("lease %d: %+v, %v", minutes, got, err)
		}
		if off := time.Until(got.Claim.Until) - want; off < -time.Minute || off > time.Minute {
			t.Errorf("lease_minutes %d: the claim ends in %s, want %s", minutes, time.Until(got.Claim.Until).Round(time.Second), want)
		}
	}
	tk := f.add("default", "lease as text", true)
	f.doc("task_claim", map[string]any{"id": tk.ID, "lease_minutes": "60"})
	if got, _, err := f.Store.Get(context.Background(), "default", tk.ID); err != nil || got.Claim == nil ||
		time.Until(got.Claim.Until) < 59*time.Minute || time.Until(got.Claim.Until) > 61*time.Minute {
		t.Errorf(`lease_minutes "60": %+v, %v; want a claim of an hour`, got.Claim, err)
	}
}

// task_claim gives the task whole (the agent is about to work it); the progress verbs give it with
// its notes cut, as task_list does (Ruling 2).
func TestTheProgressVerbsCutLongNotesAndClaimDoesNot(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	notes := strings.Repeat("n", 1500)
	tk, _, err := f.Store.Add(context.Background(), "default", tasks.AddInput{Title: "long", Notes: notes, Ready: true}, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		t.Fatal(err)
	}
	notesOf := func(d map[string]any) string { return d["task"].(map[string]any)["notes_untrusted"].(string) }
	if got := notesOf(f.doc("task_claim", map[string]any{"id": tk.ID})); got != notes {
		t.Errorf("task_claim cut the notes of the task it gives: %d characters", len(got))
	}
	if got := notesOf(f.doc("task_comment", map[string]any{"id": tk.ID, "text": "on it"})); !strings.Contains(got, "[cut at 1000 characters") {
		t.Errorf("task_comment returned the notes whole: %d characters", len(got))
	}
}

// A claim on a task whose history is full (2,000 events: the lead's P1 rule) is refused with limit,
// and the tool tells the agent to leave the task to the operator, or to finish or release it if it
// holds it (the lead's ruling Q3).
func TestAClaimOnAFullHistoryIsLeftToTheOperator(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	full := f.add("default", "claimed and released too often", true)
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	tx, err := f.Side.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 2000; i++ { // with its created event: 2,000
		if _, err := tx.Exec(`INSERT INTO task_events (task_id, at, actor, kind, from_status, to_status, note) VALUES (?, ?, 'bot', 'released', 'in_progress', 'ready', '')`,
			full.ID, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call("task_claim", map[string]any{"id": full.ID}); err == nil || !strings.HasPrefix(err.Error(), "limit: ") {
		t.Errorf("a claim on a full history: %v, want limit", err)
	}
	for _, tl := range taskTools() {
		if tl.name != "task_claim" {
			continue
		}
		for _, want := range []string{"leave the task to the operator", "finish or release it"} {
			if !strings.Contains(tl.description, want) {
				t.Errorf("task_claim's description does not say %q about limit", want)
			}
		}
	}
}

// A server whose profile was deleted since it started refuses with invalid_input "unknown profile"
// (Ruling 8): the board went with the profile.
func TestAServerWhoseProfileWasDeletedRefuses(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	work := f.server(taskSetup{profile: workProfileName}, "bbbb")
	f.add(workProfileID, "theirs", true)
	mustCall(t, work, "task_list", nil) // the server resolves its profile at its first call
	if _, err := f.Side.DB.Exec(`DELETE FROM profiles WHERE id = ?`, workProfileID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		args map[string]any
	}{{"task_list", nil}, {"task_claim", map[string]any{"next": true}}} {
		_, err := callAPITool(t, work, c.name, c.args)
		if err == nil || !strings.HasPrefix(err.Error(), "invalid_input: ") || !strings.Contains(err.Error(), "unknown profile") {
			t.Errorf("%s after the profile was deleted: %v", c.name, err)
		}
	}
}

// Every task tool acts as an agent (spec 5.1). Each check below fails if the tools acted as the
// operator: the operator's tasks are cli tasks with no hourly limit, and the operator may comment
// on any task.
func TestTheTaskToolsActAsAnAgent(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	ready := f.add("default", "approved", true)
	inbox := f.add("default", "not yet read", false)

	// What an agent sends cannot make it someone else, put a task elsewhere than Inbox or reach
	// another profile: no tool takes such an argument.
	for _, args := range []map[string]any{
		{"title": "x", "ready": true}, {"title": "x", "status": "ready"}, {"title": "x", "source": "os"},
		{"title": "x", "profile": workProfileID}, {"title": "x", "as": "you"},
	} {
		if _, err := f.call("task_add", args); err == nil || !strings.HasPrefix(err.Error(), "invalid_input: ") {
			t.Errorf("task_add %v: %v, want it refused", args, err)
		}
	}
	if n := f.count(`SELECT COUNT(*) FROM tasks`); n != 2 {
		t.Fatalf("refused calls added tasks: %d on the board", n)
	}

	d := f.doc("task_add", map[string]any{"title": "Found a bug", "notes": "in the parser"})
	if tk := d["task"].(map[string]any); tk["status"] != "inbox" || tk["source_kind"] != "agent" || tk["profile_id"] != "default" || d["created"] != true {
		t.Errorf("an agent's task: %v", d)
	}
	for i := 2; i <= tasks.AgentTasksPerHour; i++ {
		f.doc("task_add", map[string]any{"title": fmt.Sprintf("finding %d", i)})
	}
	if _, err := f.call("task_add", map[string]any{"title": "one too many"}); err == nil || !strings.HasPrefix(err.Error(), "limit: ") {
		t.Errorf("task %d of the hour: %v, want limit", tasks.AgentTasksPerHour+1, err)
	}

	for _, c := range []struct {
		name string
		args map[string]any
		code string
	}{
		{"task_claim", map[string]any{"id": inbox.ID}, "not_ready: "},
		{"task_comment", map[string]any{"id": ready.ID, "text": "a note"}, "not_claimant: "},
		{"task_finish", map[string]any{"id": ready.ID, "result": "done"}, "not_claimant: "},
		{"task_release", map[string]any{"id": ready.ID}, "not_claimant: "},
	} {
		if _, err := f.call(c.name, c.args); err == nil || !strings.HasPrefix(err.Error(), c.code) {
			t.Errorf("%s %v: %v, want %s", c.name, c.args, err, c.code)
		}
	}

	// Another session of the same client is another claimant.
	f.doc("task_claim", map[string]any{"id": ready.ID})
	other := f.server(taskSetup{}, "bbbb")
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"task_comment", map[string]any{"id": ready.ID, "text": "mine now"}},
		{"task_finish", map[string]any{"id": ready.ID, "result": "done"}},
		{"task_release", map[string]any{"id": ready.ID}},
	} {
		if _, err := callAPITool(t, other, c.name, c.args); err == nil || !strings.HasPrefix(err.Error(), "not_claimant: ") {
			t.Errorf("another session's %s: %v, want not_claimant", c.name, err)
		}
	}
}

func TestTheTaskVerbsNeedAllowMutations(t *testing.T) {
	f := newTaskFixture(t, taskSetup{readOnly: true})
	ready := f.add("default", "approved", true)
	verbs := []string{"task_claim", "task_comment", "task_finish", "task_release", "task_add"}
	for _, name := range verbs {
		if _, err := f.call(name, map[string]any{"id": ready.ID}); err == nil || !strings.Contains(err.Error(), "--allow-mutations") {
			t.Errorf("%s on a read-only server: %v", name, err)
		}
	}
	if tk, _, err := f.Store.Get(context.Background(), "default", ready.ID); err != nil || tk.Status != tasks.StatusReady {
		t.Errorf("a refused call changed the task: %+v, %v", tk, err)
	}
	// None deletes anything, so a host need not ask as it does for a destructive call.
	for _, name := range verbs {
		for _, def := range toolDefinitions(true) {
			if def["name"] == name {
				if a, _ := def["annotations"].(map[string]bool); a["readOnlyHint"] || a["destructiveHint"] || len(a) != 2 {
					t.Errorf("%s: annotations %v, want readOnlyHint and destructiveHint false", name, def["annotations"])
				}
			}
		}
	}
	names := toolsListNames(t, f.Server) // last: it closes this server's database
	for _, name := range verbs {
		if names[name] {
			t.Errorf("a read-only server lists %s", name)
		}
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/mcp/ -run 'TestAnAgentClaims|TestTaskClaimTakes|TestTheProgressVerbsCut|TestAClaimOnAFullHistory|TestAServerWhoseProfileWasDeleted|TestTheTaskToolsActAsAnAgent|TestTheTaskVerbsNeed' -count=1`
Expected: FAIL: `unknown tool "task_claim"` (and the same for the other verbs).

- [ ] **Step 3: Write `internal/mcp/task_write.go`**

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// maxLeaseMinutes is the longest claim, a day, as the store's MaxLease.
const maxLeaseMinutes = 1440

// taskClaimResult is what task_claim returns: the task (null when next found
// nothing to take) and what to do next, which names tools and ids and never a
// task's text.
type taskClaimResult struct {
	Profile   tasks.Profile `json:"profile"`
	Task      *taskView     `json:"task"`
	NextSteps []string      `json:"next_steps"`
	Note      string        `json:"note"`
}

// taskAddResult is what task_add returns, as `task add --json` has it.
type taskAddResult struct {
	Profile tasks.Profile `json:"profile"`
	Created bool          `json:"created"`
	Task    taskView      `json:"task"`
	Note    string        `json:"note"`
}

// taskWriteTools change the board as an agent may (spec 5.1); they follow
// --allow-mutations like every mutating tool. None approves, edits, moves or
// archives a task: that is the operator's.
func taskWriteTools() []tool {
	write := map[string]bool{"readOnlyHint": false, "destructiveHint": false} // none deletes anything
	return []tool{
		{
			name: "task_claim",
			description: taskBoardIntro +
				"Takes a ready task for you, or renews your hold on a task you hold: {profile, task, next_steps, note}. " +
				"Give id (a task's number, from task_list or task_next) or next: true, which takes the top of Ready, else a claim whose lease has run out, " +
				"in one step, so two agents never get the same task; with nothing to take, task is null. " +
				"lease_minutes: how long you hold it (default 30, at most 1440: more is cut to 1440). " +
				"A task_comment extends your claim to 30 minutes from the comment, if that is later, and never shortens it: it does not add lease_minutes. " +
				"To hold a long lease past its end, comment once less than 30 minutes of it are left, before it runs out, " +
				"or call task_claim again with this task's id and lease_minutes (not next: true), which extends the claim to that lease counted from now, if that is later. " +
				"This server names you (agent:<client>#<4 hex digits>), the same for every call of this session: no argument names you. " +
				"A claim belongs to this server process: after it restarts you are a new claimant, and a task you held frees itself when its lease ends. " +
				"Refusals: not_ready (the task is not in Ready, or the operator is working on it), claimed (another agent holds it, and until when), " +
				"limit (the task's history is full: if you hold the task, finish or release it; otherwise leave the task to the operator and tell the user), not_found, invalid_input.",
			schema: objSchema(map[string]interface{}{
				"id":            intParam("A ready task's number (give id or next, not both)"),
				"next":          boolParam("true: take the top of Ready (give id or next, not both)"),
				"lease_minutes": intParam("How long you hold it, in minutes (default 30, at most 1440)"),
			}),
			annotations: write,
			mutating:    true,
			handler:     toolTaskClaim,
		},
		{
			name: "task_comment",
			description: taskBoardIntro +
				"Adds a progress note to a task you hold and extends your claim to 30 minutes from now, if that is later (it never shortens it, and it does not add the lease_minutes you claimed with): {profile, task, note}, " +
				"the task's notes cut at 1000 characters as in task_list. " +
				"id: the task's number; text: what you did or found (at most 8 KiB; longer is cut). " +
				"Refusals: not_claimant (you do not hold the task: claim it first), limit (the task has 500 events: finish or release it), not_found, invalid_input (no text).",
			schema: objSchema(map[string]interface{}{
				"id":   intParam("The number of a task you hold"),
				"text": strParam("Your progress note"),
			}, "id", "text"),
			annotations: write,
			mutating:    true,
			handler:     toolTaskComment,
		},
		{
			name: "task_finish",
			description: taskBoardIntro +
				"Hands a task you hold back to the operator, in Review, with a result or a question (exactly one): {profile, task, note}, " +
				"the task's notes cut at 1000 characters as in task_list. " +
				"result: what you did. question: what you need to know before you can go on; the operator answers and puts the task back in Ready. " +
				"Your claim ends either way. Refusals: not_claimant, not_found, invalid_input (neither or both).",
			schema: objSchema(map[string]interface{}{
				"id":       intParam("The number of a task you hold"),
				"result":   strParam("What you did (give result or question)"),
				"question": strParam("What you need to know before you can go on (give result or question)"),
			}, "id"),
			annotations: write,
			mutating:    true,
			handler:     toolTaskFinish,
		},
		{
			name: "task_release",
			description: taskBoardIntro +
				"Gives a task you hold back to Ready, behind the other ready tasks, when you cannot do it: {profile, task, note}, " +
				"the task's notes cut at 1000 characters as in task_list. " +
				"note: why (optional). Your claim ends. Refusals: not_claimant, not_found.",
			schema: objSchema(map[string]interface{}{
				"id":   intParam("The number of a task you hold"),
				"note": strParam("Why you give it back (optional)"),
			}, "id"),
			annotations: write,
			mutating:    true,
			handler:     toolTaskRelease,
		},
		{
			name: "task_add",
			description: taskBoardIntro +
				"Adds a task to this profile's Inbox, where the operator reads it and decides whether it is worked: {profile, created, task, note}. " +
				"You cannot add to Ready, and agents may add 20 tasks an hour to a profile. " +
				"title: at most 200 characters; notes: optional, at most 64 KiB (longer is cut). " +
				"Refusals: limit (20 an hour, or the board is full), invalid_input (no title).",
			schema: objSchema(map[string]interface{}{
				"title": strParam("The task, in one line"),
				"notes": strParam("More about it (optional)"),
			}, "title"),
			annotations: write,
			mutating:    true,
			handler:     toolTaskAdd,
		},
	}
}

// leaseOf is task_claim's lease_minutes as the store takes it: 0 (the store's
// 30 minutes) when none or less is given, never more than a day. The minutes
// are bounded before they are multiplied, so a huge number cannot overflow
// into a short lease.
func leaseOf(minutes int64) time.Duration {
	switch {
	case minutes <= 0:
		return 0
	case minutes > maxLeaseMinutes:
		minutes = maxLeaseMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// nextSteps tells an agent how to go on with a task it has just claimed.
func nextSteps(t *tasks.Task) []string {
	if t == nil {
		return []string{"Nothing is ready to claim. Do not invent work: tell the user, or call task_list to see what is in progress or waiting for review."}
	}
	until := ""
	if t.Claim != nil {
		until = t.Claim.Until.UTC().Format(time.RFC3339)
	}
	return []string{
		fmt.Sprintf("Work task %d. Report progress with task_comment {\"id\": %d, \"text\": \"...\"}: each comment extends your claim to 30 minutes from the comment, if that is later (it never shortens it).", t.ID, t.ID),
		fmt.Sprintf("When it is done, task_finish {\"id\": %d, \"result\": \"what you did\"}; to ask the user something first, task_finish {\"id\": %d, \"question\": \"what you need to know\"}. Both send it to Review.", t.ID, t.ID),
		fmt.Sprintf("If you cannot do it, task_release {\"id\": %d, \"note\": \"why\"} puts it back in Ready.", t.ID),
		fmt.Sprintf("Your claim ends at %s unless you renew it; after that another agent may take the task over.", until),
	}
}

func toolTaskClaim(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID           taskIDArg `json:"id"`
		Next         bool      `json:"next"`
		LeaseMinutes numberArg `json:"lease_minutes"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	if (a.ID > 0) == a.Next {
		return nil, invalidArgs("give id (a ready task's number, from task_list or task_next) or next: true (the top of Ready), one of the two")
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	lease := leaseOf(int64(a.LeaseMinutes))
	var t *tasks.Task
	if a.Next {
		t, err = store.Next(ctx, p.ID, actor, true, lease)
	} else {
		var claimed tasks.Task
		if claimed, err = store.Claim(ctx, p.ID, int64(a.ID), actor, lease); err == nil {
			t = &claimed
		}
	}
	if err != nil {
		return nil, taskToolErr(err)
	}
	return taskClaimResult{Profile: p, Task: viewPtr(t), NextSteps: nextSteps(t), Note: untrustedNote}, nil
}

func toolTaskComment(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID   taskIDArg `json:"id"`
		Text string    `json:"text"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	id, err := a.ID.need()
	if err != nil {
		return nil, err
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	t, err := store.Comment(ctx, p.ID, id, a.Text, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	v := listView(t)
	return taskResult{Profile: p, Task: &v, Note: untrustedNote}, nil
}

func toolTaskFinish(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID       taskIDArg `json:"id"`
		Result   string    `json:"result"`
		Question string    `json:"question"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	id, err := a.ID.need()
	if err != nil {
		return nil, err
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	t, err := store.Finish(ctx, p.ID, id, tasks.Outcome{Result: a.Result, Question: a.Question}, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	v := listView(t)
	return taskResult{Profile: p, Task: &v, Note: untrustedNote}, nil
}

func toolTaskRelease(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID   taskIDArg `json:"id"`
		Note string    `json:"note"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	id, err := a.ID.need()
	if err != nil {
		return nil, err
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	t, err := store.Release(ctx, p.ID, id, a.Note, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	v := listView(t)
	return taskResult{Profile: p, Task: &v, Note: untrustedNote}, nil
}

func toolTaskAdd(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Title string `json:"title"`
		Notes string `json:"notes"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	t, created, err := store.Add(ctx, p.ID, tasks.AddInput{Title: a.Title, Notes: a.Notes}, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	return taskAddResult{Profile: p, Created: created, Task: viewOf(t), Note: untrustedNote}, nil
}
```

- [ ] **Step 4: Serve the verbs**

In `internal/mcp/task_tools.go`, replace

```go
func taskTools() []tool {
	return taskReadTools()
}
```

with

```go
func taskTools() []tool {
	return append(taskReadTools(), taskWriteTools()...)
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `gofmt -l internal/mcp` then `go vet ./internal/mcp/` then `go test ./internal/mcp/ -run 'TestAnAgentClaims|TestTaskClaimTakes|TestTheProgressVerbsCut|TestAClaimOnAFullHistory|TestAServerWhoseProfileWasDeleted|TestTheTaskToolsActAsAnAgent|TestTheTaskVerbsNeed|TestTaskList|TestTaskGet|TestTaskNext|TestAServerKeeps|TestTheTaskReadTools|TestServerMutatingToolRefusedWithoutFlag' -count=1`
Expected: nothing from `gofmt`, vet clean, PASS.

- [ ] **Step 6: Commit**

```
git add internal/mcp/task_write.go internal/mcp/task_write_test.go internal/mcp/task_tools.go
```
then
```
git commit -m "feat(tasks): MCP verbs task_claim, task_comment, task_finish, task_release and task_add, as an agent" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

