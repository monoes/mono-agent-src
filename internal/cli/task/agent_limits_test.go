package task

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The lease is what --lease says, 30 minutes when it says nothing, and at most 24 hours (a --lease that
// asks for no time is refused: see the test after this one). A claim by id and a claim by next are the same.
func TestTaskClaimLeaseIsTheFlagDefaultsToThirtyMinutesAndStopsAtADay(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, c := range []struct {
		name  string
		flags []string
		lease time.Duration
	}{
		{"no --lease", nil, 30 * time.Minute},
		{"--lease 2h", []string{"--lease", "2h"}, 2 * time.Hour},
		{"--lease 90s", []string{"--lease", "90s"}, 90 * time.Second},
		{"--lease 24h", []string{"--lease", "24h"}, 24 * time.Hour},
		{"--lease 48h", []string{"--lease", "48h"}, 24 * time.Hour},
		{"--lease 100000h", []string{"--lease", "100000h"}, 24 * time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			byID := opsAdd(t, db, "by id: "+c.name, "--ready")
			before := time.Now()
			got := agentLease(t, db, append([]string{"claim", id(byID), "--as", "by-id"}, c.flags...)...)
			agentWithin(t, "claim "+c.name, got, before, time.Now(), c.lease)

			// The next ready task is the one just added, the others are all claimed.
			opsAdd(t, db, "by next: "+c.name, "--ready")
			before = time.Now()
			got = agentLease(t, db, append([]string{"next", "--claim", "--as", "by-next"}, c.flags...)...)
			agentWithin(t, "next --claim "+c.name, got, before, time.Now(), c.lease)
		})
	}
	// A comment renews a lease that is shorter than its own, and never shortens a longer one.
	short := opsAdd(t, db, "short lease", "--ready")
	long := opsAdd(t, db, "long lease", "--ready")
	agentLease(t, db, "claim", id(short), "--as", "bot", "--lease", "1m")
	longEnd := agentLease(t, db, "claim", id(long), "--as", "bot", "--lease", "3h")
	before := time.Now()
	agentRun(t, db, "comment", id(short), "still at it", "--as", "bot")
	agentRun(t, db, "comment", id(long), "still at it", "--as", "bot")
	var held struct {
		Task struct {
			Claim struct {
				Until time.Time `json:"until"`
			} `json:"claim"`
		} `json:"task"`
	}
	mustTaskJSON(t, db, "default", &held, "", "show", id(short))
	agentWithin(t, "a comment on a lease of a minute", held.Task.Claim.Until, before, time.Now(), 30*time.Minute)
	mustTaskJSON(t, db, "default", &held, "", "show", id(long))
	if !held.Task.Claim.Until.Equal(longEnd) {
		t.Errorf("a comment moved a lease of 3 hours from %s to %s", longEnd, held.Task.Claim.Until)
	}
}

// A --lease that was given means what it says. Zero or less is no time and is refused, where the store would
// read it as the default, which is for a caller that gave none (an agent that mistypes would learn nothing);
// and next takes a lease only to claim with, since a look at the next task has none to use. The refusals that
// were there before come first, and a refused call changes nothing.
func TestTaskLeaseThatIsGivenMustBeAPositiveTimeAndNextTakesItOnlyToClaim(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	waiting := id(opsAdd(t, db, "waiting", "--ready"))
	before := agentSnapshot(t, db)
	const positive = "--lease must be a positive time such as 30m; the default is 30m and the most is 24h"
	const onlyClaim = "--lease only applies with --claim: a look at the next task takes no lease"
	for _, c := range []struct {
		args []string
		want string // the words of the refusal: this one's, or of the one that comes before it
	}{
		{[]string{"claim", waiting, "--as", "bot", "--lease", "0"}, positive},
		{[]string{"claim", waiting, "--as", "bot", "--lease", "0s"}, positive},
		{[]string{"claim", waiting, "--as", "bot", "--lease", "-5m"}, positive},
		{[]string{"claim", waiting, "--as", "bot", "--lease=-1ns"}, positive},
		{[]string{"next", "--claim", "--as", "bot", "--lease", "0"}, positive},
		{[]string{"next", "--claim", "--as", "bot", "--lease", "-5m"}, positive},
		{[]string{"next", "--claim", "--as", "bot", "--lease=0s"}, positive},
		{[]string{"next", "--lease", "2h"}, onlyClaim},
		{[]string{"next", "--lease=30m"}, onlyClaim},
		{[]string{"next", "--lease", "2h", "--as", "bot"}, onlyClaim},
		// What was refused before is refused first.
		{[]string{"claim", waiting, "--lease", "0"}, "name yourself with --as NAME"},
		{[]string{"claim", waiting, "--as", "you", "--lease", "0"}, "reserved"},
		{[]string{"claim", "abc", "--as", "bot", "--lease", "0"}, "is not a task id"},
		{[]string{"claim", "--as", "bot", "--lease", "0"}, "task claim takes one task id"},
		{[]string{"claim", waiting, "2", "--as", "bot", "--lease", "0"}, "task claim takes one task id"},
		{[]string{"next", "--claim", "--lease", "0"}, "name yourself with --as NAME"},
		{[]string{"next", "--claim", "--as", "bot", "--lease", "0", "extra"}, "task next takes no arguments"},
		{[]string{"next", "--lease", "2h", "extra"}, "task next takes no arguments"},
	} {
		exit, code, msg := agentRefusal(t, db, c.args...)
		earlier := c.want != positive && c.want != onlyClaim
		if exit != 3 || code != "invalid_input" || !strings.Contains(msg, c.want) ||
			earlier && (strings.Contains(msg, positive) || strings.Contains(msg, onlyClaim)) {
			t.Errorf("task %s: exit %d, %q, %q; want exit 3, invalid_input and the words %q", strings.Join(c.args, " "), exit, code, msg, c.want)
		}
		if out, _, err := runTask(t, db, "default", false, "", c.args...); exitCode(err) != 3 || out != "" {
			t.Errorf("task %s as text: exit %d (%v), %q; want exit 3 and nothing printed", strings.Join(c.args, " "), exitCode(err), err, out)
		}
	}
	if after := agentSnapshot(t, db); after != before {
		t.Errorf("refused commands changed the board:\nbefore\n%s\nafter\n%s", before, after)
	}
	// The same lease is fine where it is used, and a look needs none.
	agentLease(t, db, "claim", waiting, "--as", "bot", "--lease", "1h")
	if out, _, err := runTask(t, db, "default", false, "", "next", "--as", "bot"); err != nil || !strings.Contains(out, "Nothing is ready") {
		t.Errorf("a look at the next task: %q, %v", out, err)
	}
}

// A claim is refused once a task holds 2,000 events, so that a loop of claims and releases cannot grow a
// history without bound. The agent is told to leave the task to the operator, and next goes on to the task
// behind it: a task that cannot be claimed must not stay at the head of the queue.
func TestTaskClaimOfATaskWithTwoThousandEventsIsLeftToTheOperator(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	full := opsAdd(t, db, "worn out", "--ready")
	next := opsAdd(t, db, "behind it", "--ready")
	agentSeedEvents(t, db, full, tasks.MaxEventsToClaim-1) // with its created event: 2,000
	before := agentSnapshot(t, db)
	exit, code, msg := agentRefusal(t, db, "claim", id(full), "--as", "bot")
	if exit != 3 || code != "limit" || !strings.Contains(msg, "leave it to the operator") {
		t.Errorf("a claim of a task with 2,000 events: exit %d, %q, %q; want 3, limit and the advice to leave it to the operator", exit, code, msg)
	}
	if after := agentSnapshot(t, db); after != before {
		t.Errorf("the refused claim changed the board")
	}
	for _, args := range [][]string{{"next", "--as", "looker"}, {"next", "--claim", "--as", "bot"}} {
		var got nextJSON
		mustTaskJSON(t, db, "default", &got, "", args...)
		if got.Task == nil || got.Task.ID != next {
			t.Errorf("task %s: %+v, want #%d, the task behind the one that cannot be claimed", strings.Join(args, " "), got.Task, next)
		}
	}
	// One event fewer and the task is claimed: the refusal is the limit and nothing else.
	almost := opsAdd(t, db, "almost worn out", "--ready")
	agentSeedEvents(t, db, almost, tasks.MaxEventsToClaim-2) // with its created event: 1,999
	if task := agentRun(t, db, "claim", id(almost), "--as", "bot"); task.Status != "in_progress" {
		t.Errorf("a claim of a task with 1,999 events: %+v", task)
	}
}

// A comment is refused once a task holds 500 events, with the code limit and the way out; a change
// of state is always recorded, so the agent that holds the task can still finish or release it.
func TestTaskCommentIsRefusedAtFiveHundredEventsButFinishAndReleaseStillWork(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	n := opsAdd(t, db, "chatty", "--ready")
	m := opsAdd(t, db, "chattier", "--ready")
	agentRun(t, db, "claim", id(n), "--as", "bot")
	agentRun(t, db, "claim", id(m), "--as", "bot")
	agentSeedEvents(t, db, n, tasks.MaxEventsPerTask-3) // created, claimed and these: 499
	agentSeedEvents(t, db, m, tasks.MaxEventsPerTask-2) // 500
	if task := agentRun(t, db, "comment", id(n), "the 500th event", "--as", "bot"); task.ID != n {
		t.Fatalf("the comment that makes 500 events: %+v", task)
	}
	for _, task := range []int64{n, m} {
		exit, code, msg := agentRefusal(t, db, "comment", id(task), "one too many", "--as", "bot")
		if exit != 3 || code != "limit" || !strings.Contains(msg, "finish or release") {
			t.Errorf("a comment on #%d with 500 events: exit %d, %q, %q; want 3, limit and the way out", task, exit, code, msg)
		}
	}
	if got := agentRun(t, db, "finish", id(n), "--as", "bot", "--result", "done"); got.Status != "review" {
		t.Errorf("finish at the limit: %+v", got)
	}
	if got := agentRun(t, db, "release", id(m), "--as", "bot"); got.Status != "ready" {
		t.Errorf("release at the limit: %+v", got)
	}
}

// Text that is empty once the store has cleaned it (spaces, control characters, hidden characters) is
// no comment and no result: it is refused, and the task stays held and as it was. The commands do not
// judge the text, so a flag that is given with only such text is the store's refusal.
func TestTaskCommentFinishWithTextThatIsEmptyOnceCleanedAreRefused(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	n := opsAdd(t, db, "work", "--ready")
	agentRun(t, db, "claim", id(n), "--as", "bot")
	before := agentSnapshot(t, db)
	for _, args := range [][]string{
		{"comment", id(n), "", "--as", "bot"},
		{"comment", id(n), "   ", "--as", "bot"},
		{"comment", id(n), "\x1b\x07", "--as", "bot"},
		{"comment", id(n), "\U0000202e\U000e0041", "--as", "bot"},
		{"finish", id(n), "--result", "   ", "--as", "bot"},
		{"finish", id(n), "--question", "\x1b\x07", "--as", "bot"},
		{"finish", id(n), "--result", "\U0000202e", "--as", "bot"},
		{"finish", id(n), "--result", "", "--as", "bot"},
	} {
		if exit, code, _ := agentRefusal(t, db, args...); exit != 3 || code != "invalid_input" {
			t.Errorf("task %q: exit %d, %q, want 3 and invalid_input", args, exit, code)
		}
	}
	if after := agentSnapshot(t, db); after != before {
		t.Errorf("refused texts changed the board:\nbefore\n%s\nafter\n%s", before, after)
	}
}

// The store cuts what an agent writes at 8 KiB, on a character, and says how long the text was; a
// comment, a result, a question and a note all go through that cut.
func TestTaskAgentTextIsCutAtEightKibibytesOnACharacter(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	long := strings.Repeat("\U000000e9", 6000) // 12,000 bytes
	for _, c := range []struct {
		kind string
		args func(n string) []string
	}{
		{"comment", func(n string) []string { return []string{"comment", n, long, "--as", "bot"} }},
		{"result", func(n string) []string { return []string{"finish", n, "--result", long, "--as", "bot"} }},
		{"question", func(n string) []string { return []string{"finish", n, "--question", long, "--as", "bot"} }},
		{"released", func(n string) []string { return []string{"release", n, "--note", long, "--as", "bot"} }},
	} {
		n := opsAdd(t, db, "wordy "+c.kind, "--ready")
		agentRun(t, db, "claim", id(n), "--as", "bot")
		agentRun(t, db, c.args(id(n))...)
		events := agentEvents(t, db, n)
		note := events[len(events)-1].Note
		if events[len(events)-1].Kind != c.kind || len(note) > tasks.MaxCommentBytes || !utf8.ValidString(note) ||
			!strings.HasSuffix(note, "[truncated: 6000 characters in the original]") {
			t.Errorf("a long %s: kind %q, %d bytes, valid %v, ends %q", c.kind, events[len(events)-1].Kind, len(note), utf8.ValidString(note), note[max(len(note)-60, 0):])
		}
	}
}
