package task

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// A look (next without --claim) is a read, for everyone who may read: the operator, a named agent,
// an agent that has not said its name, a blank --as. It never takes the task and it writes nothing:
// not a claim, not an event, not a revision of the board. (A --lease on a look is refused, which
// TestTaskLeaseThatIsGivenMustBeAPositiveTimeAndNextTakesItOnlyToClaim shows leaves the board as it
// was; the store's look ignores a lease it is given, as TestNextWithoutClaimIgnoresTheLeaseItIsGiven shows.)
func TestTaskNextPeekNeverClaims(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	n := opsAdd(t, db, "waiting", "--ready")
	env := func(name, value string) func(t *testing.T) { return func(t *testing.T) { setenv(t, name, value) } }
	for _, c := range []struct {
		name  string
		setup func(t *testing.T)
		args  []string
	}{
		{"the operator", nil, nil},
		{"a named agent", nil, []string{"--as", "bot"}},
		{"an agent named by MONOAGENT_ACTOR", env("MONOAGENT_ACTOR", "bot"), nil},
		{"an agent context and no name", env("CLAUDECODE", "1"), nil},
		{"a blank --as", nil, []string{"--as", ""}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.setup != nil {
				c.setup(t)
			}
			before := agentSnapshot(t, db)
			for _, asJSON := range []bool{false, true} {
				out, _, err := runTask(t, db, "default", asJSON, "", append([]string{"next"}, c.args...)...)
				if err != nil || !strings.Contains(out, "waiting") {
					t.Fatalf("task next %v (json %v): %q, %v", c.args, asJSON, out, err)
				}
			}
			if after := agentSnapshot(t, db); after != before {
				t.Errorf("a look changed the board:\nbefore\n%s\nafter\n%s", before, after)
			}
		})
	}
	if task := agentRun(t, db, "next", "--claim", "--as", "bot"); task.ID != n || task.Status != "in_progress" {
		t.Errorf("the task the looks left is the first claim's: %+v", task)
	}
}

// next takes the top of Ready, in the order of the column, and when no Ready task is left the stale
// claim whose lease ended first. It never offers the Inbox, a Review or Done or archived card, a task
// the operator works on, or a claim that has not run out. The look offers what the claim takes.
func TestTaskNextTakesTheTopOfReadyThenTheOldestStaleClaimAndNothingElse(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	now := time.Now()
	ids := seedTaskRows(t, db,
		taskSeed{title: "in the inbox"},
		taskSeed{title: "ready one", status: "ready"},
		taskSeed{title: "ready two", status: "ready"},
		taskSeed{title: "stale newer", status: "in_progress", holder: "old-2", until: now.Add(-time.Hour)},
		taskSeed{title: "stale older", status: "in_progress", holder: "old-1", until: now.Add(-3 * time.Hour)},
		taskSeed{title: "held", status: "in_progress", holder: "live", until: now.Add(time.Hour)},
		taskSeed{title: "the operator's own", status: "in_progress"},
		taskSeed{title: "in review", status: "review"},
		taskSeed{title: "done", status: "done"},
		taskSeed{title: "archived", status: "archived"},
	)
	for i, title := range []string{"ready one", "ready two", "stale older", "stale newer"} {
		name := fmt.Sprintf("bot-%d", i+1)
		var peek nextJSON
		mustTaskJSON(t, db, "default", &peek, "", "next", "--as", "looker")
		got := agentRun(t, db, "next", "--claim", "--as", name)
		if peek.Task == nil || peek.Task.Title != title || got.Title != title || got.Status != "in_progress" || got.Claim == nil || got.Claim.By != name || got.Claim.Stale {
			t.Errorf("claim %d: the look offered %+v and the claim took %+v, want %q", i+1, peek.Task, got, title)
		}
	}
	if got, want := agentKinds(agentEvents(t, db, ids[4])), "bot-3:reclaimed"; got != want {
		t.Errorf("the older stale claim was taken over with %q, want %q", got, want)
	}
	for _, args := range [][]string{{"next", "--as", "looker"}, {"next", "--claim", "--as", "bot-9"}} {
		var none nextJSON
		mustTaskJSON(t, db, "default", &none, "", args...)
		if none.Task != nil {
			t.Errorf("task %s: %+v, want nothing: what is left is not offered", strings.Join(args, " "), none.Task)
		}
	}
	for _, left := range []struct {
		id     int64
		status string
		holder string
	}{{ids[0], "inbox", ""}, {ids[5], "in_progress", "live"}, {ids[6], "in_progress", ""}, {ids[7], "review", ""}, {ids[8], "done", ""}, {ids[9], "archived", ""}} {
		s := opsShow(t, db, left.id)
		holder := ""
		if s.Task.Claim != nil {
			holder = s.Task.Claim.By
		}
		if s.Task.Status != left.status || holder != left.holder || len(s.Events) != 0 {
			t.Errorf("#%d was touched: %s held by %q with %d events", left.id, s.Task.Status, holder, len(s.Events))
		}
	}
}

// Two agents racing for the ready tasks get one each, and never the same one: the claim of next
// is one step, not a look and a claim after it.
func TestTaskAgentsRacingForTheReadyTasksGetOneEach(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	const ready, agents = 3, 6
	for i := 0; i < ready; i++ {
		opsAdd(t, db, fmt.Sprintf("task %d", i), "--ready")
	}
	type result struct {
		out string
		err error
	}
	results := make([]result, agents)
	var wg sync.WaitGroup
	for i := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _, err := runTask(t, db, "default", true, "", "next", "--claim", "--as", fmt.Sprintf("bot-%d", i))
			results[i] = result{out, err}
		}()
	}
	wg.Wait()
	taken := map[int64]string{}
	empty := 0
	for i, r := range results {
		var doc leasedDoc
		if r.err != nil || json.Unmarshal([]byte(r.out), &doc) != nil {
			t.Fatalf("agent %d: %v, %q", i, r.err, r.out)
		}
		if doc.Task == nil {
			empty++
			continue
		}
		if other, dup := taken[doc.Task.ID]; dup {
			t.Errorf("#%d went to bot-%d and to %s", doc.Task.ID, i, other)
		}
		taken[doc.Task.ID] = fmt.Sprintf("bot-%d", i)
		if doc.Task.Claim == nil || doc.Task.Claim.By != fmt.Sprintf("bot-%d", i) {
			t.Errorf("agent %d got a task it does not hold: %+v", i, doc.Task)
		}
	}
	if len(taken) != ready || empty != agents-ready {
		t.Errorf("%d tasks taken and %d agents sent away, want %d and %d", len(taken), empty, ready, agents-ready)
	}
}

// A claim says why it was refused, with the code and the exit of the refusal, and writes nothing.
func TestTaskClaimRefusalsSayWhyAndWriteNothing(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	addTaskProfile(t, db, "work-id", "Work")
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	ids := seedTaskRows(t, db,
		taskSeed{title: "in the inbox"},
		taskSeed{title: "in review", status: "review"},
		taskSeed{title: "done", status: "done"},
		taskSeed{title: "archived", status: "archived"},
		taskSeed{title: "the operator's own", status: "in_progress"},
		taskSeed{title: "held", status: "in_progress", holder: "bot-1", until: until},
		taskSeed{profile: "work-id", title: "another profile's", status: "ready"},
	)
	before := agentSnapshot(t, db)
	for _, c := range []struct {
		what   string
		id     int64
		exit   int
		code   string
		phrase string
	}{
		{"an inbox task", ids[0], 3, "not_ready", "only a ready task can be claimed"},
		{"a review task", ids[1], 3, "not_ready", "only a ready task can be claimed"},
		{"a done task", ids[2], 3, "not_ready", "only a ready task can be claimed"},
		{"an archived task", ids[3], 3, "not_ready", "only a ready task can be claimed"},
		{"a task the operator works on", ids[4], 3, "not_ready", "operator"},
		{"a task of another profile", ids[6], 2, "not_found", "not found"},
		{"a task that is not there", 99999, 2, "not_found", "not found"},
	} {
		exit, code, msg := agentRefusal(t, db, "claim", id(c.id), "--as", "bot-2")
		if exit != c.exit || code != c.code || !strings.Contains(msg, c.phrase) {
			t.Errorf("claiming %s: exit %d, %q, %q; want exit %d, %q and the words %q", c.what, exit, code, msg, c.exit, c.code, c.phrase)
		}
	}
	doc := failedTaskJSON(t, db, "default", 3, "claim", id(ids[5]), "--as", "bot-2")
	if doc["code"] != "claimed" || doc["claimed_by"] != "bot-1" || doc["claimed_until"] != until.UTC().Format(time.RFC3339) {
		t.Errorf("a task another agent holds names who holds it and until when: %v", doc)
	}
	if _, _, err := runTask(t, db, "default", false, "", "claim", id(ids[5]), "--as", "bot-2"); err == nil || !strings.Contains(err.Error(), "bot-1") {
		t.Errorf("the text refusal names the holder: %v", err)
	}
	if after := agentSnapshot(t, db); after != before {
		t.Errorf("refused claims changed the board:\nbefore\n%s\nafter\n%s", before, after)
	}
}

// A stale claim is anybody's to take, by its id as by next; a claim under the same name renews it, and
// a renewal never shortens the lease.
func TestTaskClaimTakesAStaleClaimAndRenewsItsOwnWithoutShorteningIt(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	stale := seedTaskRows(t, db, taskSeed{title: "left behind", status: "in_progress", holder: "old-bot", until: time.Now().Add(-time.Minute)})[0]
	task := agentRun(t, db, "claim", id(stale), "--as", "bot")
	if task.Claim == nil || task.Claim.By != "bot" || task.Claim.Stale || task.Status != "in_progress" {
		t.Fatalf("a stale claim taken over: %+v", task)
	}
	events := agentEvents(t, db, stale)
	if len(events) != 1 || events[0].Actor != "bot" || events[0].Kind != "reclaimed" || !strings.Contains(events[0].Note, "old-bot") {
		t.Errorf("the history of the takeover: %+v", events)
	}
	n := opsAdd(t, db, "mine", "--ready")
	first := agentLease(t, db, "claim", id(n), "--as", "bot", "--lease", "2h")
	if second := agentLease(t, db, "claim", id(n), "--as", "bot", "--lease", "5m"); second.Before(first) {
		t.Errorf("a renewal shortened the lease from %s to %s", first, second)
	}
	if third := agentLease(t, db, "claim", id(n), "--as", "bot", "--lease", "4h"); !third.After(first.Add(time.Hour)) {
		t.Errorf("a renewal with a longer lease did not lengthen it: %s after %s", third, first)
	}
	if got := agentKinds(agentEvents(t, db, n)); got != "you:created bot:claimed bot:claimed bot:claimed" {
		t.Errorf("history: %s", got)
	}
	if got := agentEvents(t, db, n)[2].Note; got != "renewed" {
		t.Errorf("a claim under the same name is a renewal, and says so: %q", got)
	}
}

// Finish, release and a comment are the claimant's alone: another agent's call, a call on a task nobody
// holds and the old holder's call after the task went back are all refused, and write nothing.
func TestTaskFinishReleaseAndCommentAreTheClaimantsAlone(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	a, b, c := opsAdd(t, db, "a", "--ready"), opsAdd(t, db, "b", "--ready"), opsAdd(t, db, "c", "--ready")
	agentRun(t, db, "claim", id(a), "--as", "bot-1")
	agentRun(t, db, "claim", id(b), "--as", "bot-1")
	notMine := func(why string, n int64, as string) {
		t.Helper()
		before := agentSnapshot(t, db)
		for _, args := range [][]string{
			{"finish", id(n), "--as", as, "--result", "stolen"},
			{"finish", id(n), "--as", as, "--question", "stolen?"},
			{"release", id(n), "--as", as},
			{"comment", id(n), "stolen", "--as", as},
		} {
			if exit, code, msg := agentRefusal(t, db, args...); exit != 3 || code != "not_claimant" || !strings.Contains(msg, "claim it first") {
				t.Errorf("%s: task %s: exit %d, %q, %q; want not_claimant", why, strings.Join(args, " "), exit, code, msg)
			}
		}
		if after := agentSnapshot(t, db); after != before {
			t.Errorf("%s: refused calls changed the board:\nbefore\n%s\nafter\n%s", why, before, after)
		}
	}
	notMine("another agent's task", a, "bot-2")
	notMine("a task nobody holds", c, "bot-1")
	agentRun(t, db, "finish", id(a), "--as", "bot-1", "--result", "done")
	notMine("a task that was finished", a, "bot-1")
	agentRun(t, db, "release", id(b), "--as", "bot-1")
	notMine("a task that was released", b, "bot-1")
	// A claim that has run out is still its holder's until somebody takes it, and not after.
	stale := seedTaskRows(t, db, taskSeed{title: "stale", status: "in_progress", holder: "bot-3", until: time.Now().Add(-time.Hour)})[0]
	agentRun(t, db, "claim", id(stale), "--as", "bot-4")
	notMine("a stale claim somebody took", stale, "bot-3")
	late := seedTaskRows(t, db, taskSeed{title: "late", status: "in_progress", holder: "bot-5", until: time.Now().Add(-time.Hour)})[0]
	if task := agentRun(t, db, "finish", id(late), "--as", "bot-5", "--result", "late but done"); task.Status != "review" {
		t.Errorf("the holder of a stale claim that nobody took may still finish it: %+v", task)
	}
}

// What an agent says reaches the history as it said it, under its own name, and finish and release
// hand the task to the column the operator reads or the queue the next agent takes from.
func TestTaskCommentFinishAndReleaseWriteWhatTheAgentSaid(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	n := opsAdd(t, db, "work", "--ready")
	agentRun(t, db, "claim", id(n), "--as", "bot")
	agentRun(t, db, "comment", id(n), "reproduced", "it", "locally", "--as", "bot")
	if task := agentRun(t, db, "release", id(n), "--as", "bot", "--note", "needs the VPN"); task.Status != "ready" || task.Claim != nil {
		t.Fatalf("release: %+v", task)
	}
	agentRun(t, db, "claim", id(n), "--as", "bot")
	if task := agentRun(t, db, "finish", id(n), "--as", "bot", "--question", "which database?"); task.Status != "review" || task.Claim != nil {
		t.Fatalf("finish with a question: %+v", task)
	}
	if exit, code, _ := agentRefusal(t, db, "claim", id(n), "--as", "bot"); exit != 3 || code != "not_ready" {
		t.Errorf("a task in Review is not claimed: exit %d, %q", exit, code)
	}
	var got []string
	for _, e := range agentEvents(t, db, n) {
		got = append(got, e.Actor+":"+e.Kind+":"+e.From+">"+e.To+":"+e.Note)
	}
	want := []string{
		"you:created:>ready:",
		"bot:claimed:ready>in_progress:",
		"bot:comment:>:reproduced it locally",
		"bot:released:in_progress>ready:needs the VPN",
		"bot:claimed:ready>in_progress:",
		"bot:question:in_progress>review:which database?",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("history:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A result goes to the history as a result, and a release with no note writes none.
	m := opsAdd(t, db, "more work", "--ready")
	agentRun(t, db, "claim", id(m), "--as", "bot")
	agentRun(t, db, "finish", id(m), "--as", "bot", "--result", "fixed in PR 41")
	p := opsAdd(t, db, "other work", "--ready")
	agentRun(t, db, "claim", id(p), "--as", "bot")
	agentRun(t, db, "release", id(p), "--as", "bot")
	if last := agentEvents(t, db, m); last[len(last)-1] != (agentEvent{"bot", "result", "in_progress", "review", "fixed in PR 41"}) {
		t.Errorf("the result: %+v", last[len(last)-1])
	}
	if last := agentEvents(t, db, p); last[len(last)-1] != (agentEvent{"bot", "released", "in_progress", "ready", ""}) {
		t.Errorf("a release with no note: %+v", last[len(last)-1])
	}
}

// Every agent command works on the profile it was given: the ids of another profile are not found,
// its ready tasks are not offered, and a profile asked for by its name is acted on and named by its id.
func TestTaskAgentCommandsStayInTheirProfile(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	addTaskProfile(t, db, "work-id", "Work")
	ids := seedTaskRows(t, db,
		taskSeed{profile: "work-id", title: "work ready", status: "ready"},
		taskSeed{profile: "work-id", title: "work held", status: "in_progress", holder: "bot", until: time.Now().Add(time.Hour)},
	)
	before := agentSnapshot(t, db)
	for _, args := range [][]string{
		{"claim", id(ids[0]), "--as", "bot"},
		{"comment", id(ids[1]), "x", "--as", "bot"},
		{"comment", id(ids[0]), "from the operator"},
		{"finish", id(ids[1]), "--as", "bot", "--result", "r"},
		{"release", id(ids[1]), "--as", "bot"},
	} {
		if exit, code, _ := agentRefusal(t, db, args...); exit != 2 || code != "not_found" {
			t.Errorf("task %s from another profile: exit %d, %q, want 2 and not_found", strings.Join(args, " "), exit, code)
		}
	}
	for _, args := range [][]string{{"next", "--as", "bot"}, {"next", "--claim", "--as", "bot-2"}} {
		var none nextJSON
		mustTaskJSON(t, db, "default", &none, "", args...)
		if none.Task != nil {
			t.Errorf("task %s offered another profile's task: %+v", strings.Join(args, " "), none.Task)
		}
	}
	if out, errOut, err := runTask(t, db, "default", false, "", "digest"); err != nil || out != "" || errOut != "" {
		t.Errorf("a digest of a profile with nothing ready: %q, %q, %v", out, errOut, err)
	}
	if after := agentSnapshot(t, db); after != before {
		t.Errorf("another profile's commands changed the board:\nbefore\n%s\nafter\n%s", before, after)
	}
	out, _, err := runTask(t, db, "Work", false, "", "next", "--claim", "--as", "bot-2")
	if err != nil || !strings.HasPrefix(out, "Profile: Work\nClaimed #"+id(ids[0])+" ") || !strings.Contains(out, "monoagentcli --profile work-id task finish "+id(ids[0])+" --as bot-2 ") {
		t.Errorf("a claim in the profile asked for by its name: %q, %v", out, err)
	}
}
