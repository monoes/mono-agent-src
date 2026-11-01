package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/applyfill"
	"github.com/monoes/mono-agent/internal/applypark"
	"github.com/monoes/mono-agent/internal/storage"
)

// browser is the fake browser and the fake `aitask submit` in one: each
// submitted fill is a task that is already complete, and left a tab open.
type browser struct {
	mu      sync.Mutex
	dbPath  string
	tabs    []applypark.LiveTab
	next    int
	started []string
	running int
	maxSeen int
	status  string // status of the tasks it makes
}

func (b *browser) install(t *testing.T, dbPath string) {
	t.Helper()
	b.dbPath, b.next, b.status = dbPath, 200, aitask.StatusComplete
	oldSubmit, oldOp, oldLive, oldPark, oldPoll := submitTask, FillStartWiring.RequireOperator, liveTabs, applyfill.Parking, pollEvery
	t.Cleanup(func() {
		submitTask, FillStartWiring.RequireOperator, liveTabs, applyfill.Parking, pollEvery = oldSubmit, oldOp, oldLive, oldPark, oldPoll
	})
	FillStartWiring.RequireOperator = func(string) error { return nil }
	pollEvery = time.Millisecond
	read := func(context.Context, string) ([]applypark.LiveTab, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		return append([]applypark.LiveTab(nil), b.tabs...), nil
	}
	applyfill.Parking = &applyfill.ParkingConfig{Live: read}
	liveTabs = func(ctx context.Context, d Deps) ([]applypark.LiveTab, error) { return read(ctx, d.ProfileID()) }
	submitTask = func(_ context.Context, args []string) ([]byte, error) {
		app, want := "", aitask.NewID()
		for i, a := range args {
			if a == "--application" {
				app = args[i+1]
			}
			if a == "--task-id" {
				want = args[i+1]
			}
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.next++
		tab := b.next
		b.started = append(b.started, app)
		b.running++
		if b.running > b.maxSeen {
			b.maxSeen = b.running
		}
		b.tabs = append(b.tabs, applypark.LiveTab{TabID: tab, GroupTitle: "Coder · Fill", URL: "https://acme.example/apply"})
		db, err := storage.NewDatabase(b.dbPath)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		result := fmt.Sprintf(`{"status":"filled","fields":[{"label":"Email","value":"a@b.example","source":"Facts"}],"tabId":%d,"origin":"https://acme.example"}`, tab)
		task, err := (&aitask.Store{DB: db.DB, Profile: "default"}).Insert(aitask.Task{ID: want, Kind: applyfill.Kind,
			ConversationID: fmt.Sprintf("conv-%d", tab), Status: b.status, Goal: "g", ApplicationID: app, Access: aitask.AccessWorkspace, BudgetUSD: 1})
		if err != nil {
			return nil, err
		}
		if err := (applypark.Store{DB: db.DB, Profile: "default"}).RecordOpen(context.Background(), task.ID, tab); err != nil {
			return nil, err
		}
		if b.status == aitask.StatusComplete {
			if _, err := db.DB.Exec(`UPDATE ai_tasks SET result_json = ? WHERE id = ?`, result, task.ID); err != nil {
				return nil, err
			}
		}
		b.running-- // the fill is over as soon as it is submitted
		return []byte(fmt.Sprintf(`{"taskId":"%s","kind":"application-apply","conversationId":"conv-%d","status":"%s"}`, task.ID, tab, b.status)), nil
	}
}

func (b *browser) closeTab(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.tabs[:0]
	for _, t := range b.tabs {
		if t.TabID != id {
			out = append(out, t)
		}
	}
	b.tabs = out
}

type queueOut struct {
	Max      int `json:"max"`
	Outcomes []struct {
		ApplicationID string `json:"applicationId"`
		TaskID        string `json:"taskId"`
		Result        string `json:"result"`
		Error         string `json:"error"`
	} `json:"outcomes"`
}

func jobs(t *testing.T, dbPath string, n int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		out, err := run(t, dbPath, true, "add", "--kind", "job", "--title", fmt.Sprintf("Engineer %d", i), "--company", "Acme", "--url", fmt.Sprintf("https://acme.example/%d", i))
		if err != nil {
			t.Fatal(err)
		}
		var r struct{ ID string }
		_ = json.Unmarshal([]byte(out), &r)
		ids = append(ids, r.ID)
	}
	return ids
}

func statusOf(t *testing.T, db *sql.DB, id string) applications.Status {
	t.Helper()
	a, err := applications.NewStore(db).Get(context.Background(), "default", id)
	if err != nil {
		t.Fatal(err)
	}
	return a.Status
}

func TestFillQueueParksEachAndHoldsTheCap(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 3)
	b := &browser{}
	b.install(t, dbPath)

	// The person submits the first application once two are parked: its slot frees for the third.
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The person submits only what is parked: wait until both fills are
		// parked (awaiting_submit), not just started, or the submit lands while
		// the queue is still recording the first fill and the queue parks nothing.
		parkedBoth := func() bool {
			ok := true
			withDB(t, dbPath, func(db *sql.DB) {
				for _, id := range ids[:2] {
					if a, err := applications.NewStore(db).Get(context.Background(), "default", id); err != nil || a.Status != applications.StatusAwaitingSubmit {
						ok = false
					}
				}
			})
			return ok
		}
		for deadline := time.Now().Add(20 * time.Second); !parkedBoth(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Error("two fills were not parked within 20s")
				break
			}
		}
		time.Sleep(100 * time.Millisecond) // the third must not have started yet
		b.mu.Lock()
		if len(b.started) != 2 {
			t.Errorf("%d fills started with two tabs parked and a cap of 2", len(b.started))
		}
		b.mu.Unlock()
		withDB(t, dbPath, func(db *sql.DB) {
			if err := applications.NewStore(db).SetStatus(context.Background(), "default", ids[0], applications.StatusApplied, applications.ActorUser, "sent"); err != nil {
				t.Error(err)
			}
		})
	}()
	// The queue is a foreground loop that waits for the person for as long as
	// the cap is full, so a slot that is never freed would hang it. The wait
	// is bounded: a stuck queue fails here in seconds, not at the package timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := NewCommand(testDeps(dbPath, true))
	cmd.SetArgs(append([]string{"fill", "queue", "--max-parked", "2"}, ids...))
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.ExecuteContext(ctx)
	out := stdout.String()
	<-done
	if ctx.Err() != nil {
		t.Fatalf("the queue was still waiting for a slot after 30s (the first application was never submitted, or its slot was never freed): %v\n%s", err, out)
	}
	if err != nil {
		t.Fatalf("queue: %v (%s)", err, out)
	}
	var got queueOut
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Max != 2 || len(got.Outcomes) != 3 {
		t.Fatalf("output %q: %v", out, err)
	}
	for _, o := range got.Outcomes {
		if o.Result != applypark.ResultParked {
			t.Errorf("%s: %s %s", o.ApplicationID, o.Result, o.Error)
		}
	}
	withDB(t, dbPath, func(db *sql.DB) {
		for _, id := range ids[1:] {
			if s := statusOf(t, db, id); s != applications.StatusAwaitingSubmit {
				t.Errorf("%s: %s", id, s)
			}
			tab, _ := applypark.Store{DB: db, Profile: "default"}.Get(context.Background(), id)
			if tab == nil || tab.State != applypark.StateParked {
				t.Errorf("%s: tab %+v", id, tab)
			}
		}
		// The sent one is the person's, and the queue never made it.
		if s := statusOf(t, db, ids[0]); s != applications.StatusApplied {
			t.Errorf("first: %s", s)
		}
	})
}

func TestFillQueueMovesOnFromATaskThatNeedsYou(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 2)
	b := &browser{}
	b.install(t, dbPath)
	b.status = aitask.StatusNeedsUser // the conversation ended with no result
	out, err := run(t, dbPath, true, append([]string{"fill", "queue", "--max-parked", "1"}, ids...)...)
	if err != nil {
		t.Fatalf("queue: %v (%s)", err, out)
	}
	var got queueOut
	_ = json.Unmarshal([]byte(out), &got)
	if len(got.Outcomes) != 2 || got.Outcomes[0].Result != applypark.ResultNeedsYou || got.Outcomes[1].Result != applypark.ResultNeedsYou {
		t.Fatalf("outcomes = %s", out)
	}
	withDB(t, dbPath, func(db *sql.DB) {
		for _, id := range ids {
			if s := statusOf(t, db, id); s != applications.StatusNeedsYou {
				t.Errorf("%s: %s", id, s)
			}
			f, _ := applications.NewStore(db).GetFill(context.Background(), "default", id)
			if f.Status != applications.FillNeedsYou || f.Note == "" {
				t.Errorf("%s fill = %+v", id, f)
			}
		}
	})
}

func TestFillQueueRefusals(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 1)
	b := &browser{}
	b.install(t, dbPath)
	for name, args := range map[string][]string{
		"cap too high":  {"fill", "queue", "--max-parked", "11", ids[0]},
		"cap too low":   {"fill", "queue", "--max-parked", "-1", ids[0]},
		"nothing named": {"fill", "queue"},
		"both":          {"fill", "queue", "--pending", ids[0]},
		"unknown id":    {"fill", "queue", "nope"},
	} {
		if out, err := run(t, dbPath, true, args...); err == nil {
			t.Errorf("%s: accepted (%s)", name, out)
		}
	}
	withDB(t, dbPath, func(db *sql.DB) {
		if err := applications.NewStore(db).SetStatus(context.Background(), "default", ids[0], applications.StatusAwaitingSubmit, applications.ActorSystem, "x"); err != nil {
			t.Fatal(err)
		}
	})
	if out, err := run(t, dbPath, true, "fill", "queue", ids[0]); err == nil || !strings.Contains(err.Error(), "only a pending or needs_you") {
		t.Fatalf("an awaiting_submit application was queued: %v (%s)", err, out)
	}
	if len(b.started) != 0 {
		t.Fatalf("a refused queue started %d fills", len(b.started))
	}
	// Operator-only, like fill start.
	FillStartWiring.RequireOperator = func(what string) error { return errors.New("only the user can " + what) }
	for _, sub := range [][]string{{"fill", "queue", ids[0]}, {"fill", "resume", ids[0]}, {"fill", "sweep"}} {
		if _, err := run(t, dbPath, true, sub...); err == nil || !strings.Contains(err.Error(), "only the user") {
			t.Errorf("%v by a non-operator: %v", sub, err)
		}
	}
	FillStartWiring.RequireOperator = nil
	if _, err := run(t, dbPath, true, "fill", "queue", ids[0]); err == nil {
		t.Error("an unwired operator check let queue through")
	}
}

func TestFillSweepAndTabAndResume(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 2)
	b := &browser{}
	b.install(t, dbPath)
	if out, err := run(t, dbPath, true, append([]string{"fill", "queue", "--max-parked", "2"}, ids...)...); err != nil {
		t.Fatalf("queue: %v (%s)", err, out)
	}
	tabOf := func(id string) (tab *applypark.Tab, observed string) {
		out, err := run(t, dbPath, true, "fill", "tab", id)
		if err != nil {
			t.Fatal(err)
		}
		var r struct {
			Tab              *applypark.Tab `json:"tab"`
			SubmitObservedAt string         `json:"submitObservedAt"`
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(err)
		}
		return r.Tab, r.SubmitObservedAt
	}
	first, _ := tabOf(ids[0])
	second, _ := tabOf(ids[1])
	if first == nil || second == nil || first.TabID == second.TabID {
		t.Fatalf("tabs = %+v %+v", first, second)
	}

	// A submit the extension saw is only a hint.
	withDB(t, dbPath, func(db *sql.DB) {
		if _, ok, err := applypark.Observe(context.Background(), db, applypark.Observation{TabID: first.TabID, Origin: first.Origin}); err != nil || !ok {
			t.Fatalf("observe: %v %v", ok, err)
		}
		if s := statusOf(t, db, ids[0]); s != applications.StatusAwaitingSubmit {
			t.Fatalf("an observed submit moved the status to %s", s)
		}
	})
	if _, at := tabOf(ids[0]); at == "" {
		t.Fatal("tab shows no observed submit")
	}

	// The person closes the second tab: sweep turns it into "Reopen and refill".
	b.closeTab(second.TabID)
	out, err := run(t, dbPath, true, "fill", "sweep")
	if err != nil {
		t.Fatalf("sweep: %v (%s)", err, out)
	}
	var sw applypark.SweepResult
	if err := json.Unmarshal([]byte(out), &sw); err != nil || len(sw.Lost) != 1 || sw.Lost[0] != ids[1] {
		t.Fatalf("sweep = %s (%v)", out, err)
	}
	withDB(t, dbPath, func(db *sql.DB) {
		if s := statusOf(t, db, ids[1]); s != applications.StatusNeedsYou {
			t.Errorf("lost tab: %s", s)
		}
		if f, _ := applications.NewStore(db).GetFill(context.Background(), "default", ids[1]); !strings.Contains(f.Note, "Reopen and refill") {
			t.Errorf("note = %q", f.Note)
		}
	})

	// Resume: the parked tab is bound to the new task alone; the lost one gets a fresh fill.
	if out, err := run(t, dbPath, true, "fill", "resume", ids[0]); err != nil {
		t.Fatalf("resume: %v (%s)", err, out)
	}
	tab, at := tabOf(ids[0])
	if tab.State != applypark.StateParked && tab.State != applypark.StateActive {
		t.Fatalf("tab = %+v", tab)
	}
	if at != "" {
		t.Errorf("a resumed fill kept the old observed submit")
	}
	if _, err := run(t, dbPath, true, "fill", "resume", "nope"); err == nil {
		t.Error("resume of an unknown application")
	}
}

func TestFillResumeNeedsAParkedOrLostTab(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 1)
	b := &browser{}
	b.install(t, dbPath)
	if out, err := run(t, dbPath, true, "fill", "resume", ids[0]); err == nil || !strings.Contains(err.Error(), "no parked or lost tab") {
		t.Fatalf("resume with no tab: %v (%s)", err, out)
	}
	if len(b.started) != 0 {
		t.Fatal("a refused resume started a task")
	}
}

// A resume holds the tab under the id its task will take before the task exists, and the task takes that id. If
// the hold goes stale and the tab is given back first, the resume cancels its task and says so; if the submit
// fails, the tab goes back to parked.
func TestFillResumeHoldsTheTabUnderItsTaskId(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 1)
	b := &browser{}
	b.install(t, dbPath)
	if out, err := run(t, dbPath, true, "fill", "queue", ids[0]); err != nil {
		t.Fatalf("queue: %v (%s)", err, out)
	}
	tabState := func() (st applypark.Tab) {
		withDB(t, dbPath, func(db *sql.DB) {
			tab, err := (applypark.Store{DB: db, Profile: "default"}).Get(context.Background(), ids[0])
			if err != nil || tab == nil {
				t.Fatalf("tab: %v %v", tab, err)
			}
			st = *tab
		})
		return st
	}
	before := tabState()
	realSubmit := submitTask

	// The submit fails: the tab is parked again, under its old task.
	submitTask = func(ctx context.Context, args []string) ([]byte, error) {
		if got := tabState(); got.State != applypark.StateActive || got.TaskID != flagValues(args, "--task-id")[0] {
			t.Errorf("during the submit the tab is %+v, want active under the task id %v", got, flagValues(args, "--task-id"))
		}
		return nil, fmt.Errorf("submit exploded")
	}
	if _, err := run(t, dbPath, true, "fill", "resume", ids[0]); err == nil || !strings.Contains(err.Error(), "submit exploded") {
		t.Fatalf("resume = %v", err)
	}
	if got := tabState(); got.State != applypark.StateParked || got.TaskID != before.TaskID {
		t.Fatalf("after a failed resume the tab is %+v, want parked under %s", got, before.TaskID)
	}

	// The hold went stale: a sweep parked the tab while the submit ran. The task is cancelled, the tab stays with
	// whoever has it.
	var taskID string
	submitTask = func(ctx context.Context, args []string) ([]byte, error) {
		taskID = flagValues(args, "--task-id")[0]
		withDB(t, dbPath, func(db *sql.DB) {
			if err := (applypark.Store{DB: db, Profile: "default"}).SetState(ctx, ids[0], applypark.StateParked, ""); err != nil {
				t.Fatal(err)
			}
		})
		return realSubmit(ctx, args)
	}
	b.status = aitask.StatusRunning
	if _, err := run(t, dbPath, true, "fill", "resume", ids[0]); err == nil || !strings.Contains(err.Error(), "given back") {
		t.Fatalf("resume = %v", err)
	}
	withDB(t, dbPath, func(db *sql.DB) {
		task, err := (&aitask.Store{DB: db, Profile: "default"}).Get(taskID)
		if err != nil || task.Status != aitask.StatusCancelled {
			t.Fatalf("the task of a resume that lost its tab: %+v %v", task, err)
		}
	})
	if got := tabState(); got.State != applypark.StateParked {
		t.Fatalf("tab = %+v", got)
	}
}

// giveBack cancels only the tasks it is told this resume's own submit answered with: a task that merely shares the
// requested id (a refused submit answered with none) keeps running, whatever its age.
func TestGiveBackCancelsOnlyTheTasksItIsGiven(t *testing.T) {
	dbPath := homeDB(t)
	withDB(t, dbPath, func(db *sql.DB) {
		tasks := &aitask.Store{DB: db, Profile: "default"}
		for _, id := range []string{"at-00000000000000a1", "at-00000000000000b2"} {
			if _, err := tasks.Insert(aitask.Task{ID: id, Kind: applyfill.Kind, ConversationID: "c-" + id, Status: aitask.StatusRunning, Goal: "g", Access: aitask.AccessWorkspace, BudgetUSD: 1}); err != nil {
				t.Fatal(err)
			}
		}
		status := func(id string) string {
			tk, err := tasks.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			return tk.Status
		}
		d := Deps{ProfileID: func() string { return "default" }}
		tabs := applypark.Store{DB: db, Profile: "default"}
		cause := errors.New("boom")
		if err := d.giveBack(context.Background(), db, tabs, "app", "at-00000000000000a1", "", nil, cause); !errors.Is(err, cause) {
			t.Errorf("the cause was dropped: %v", err)
		}
		if s := status("at-00000000000000a1"); s != aitask.StatusRunning {
			t.Errorf("a task nobody was told is the resume's was %s", s)
		}
		_ = d.giveBack(context.Background(), db, tabs, "app", "at-00000000000000a1", "", []string{"at-00000000000000b2"}, cause)
		if s := status("at-00000000000000b2"); s != aitask.StatusCancelled {
			t.Errorf("the task the submit answered with is %s", s)
		}
		if s := status("at-00000000000000a1"); s != aitask.StatusRunning {
			t.Errorf("the requested id's task was %s though the submit did not answer with it", s)
		}
	})
}

// A resume of a lost tab (no hold of its own) whose submit answers with another task than the one asked for cancels
// that task too: it must not keep running unlinked.
func TestFillResumeOfALostTabCancelsAWrongIdAnswer(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 1)
	b := &browser{}
	b.install(t, dbPath)
	if out, err := run(t, dbPath, true, "fill", "queue", ids[0]); err != nil {
		t.Fatalf("queue: %v (%s)", err, out)
	}
	var tabID int
	withDB(t, dbPath, func(db *sql.DB) {
		tab, err := (applypark.Store{DB: db, Profile: "default"}).Get(context.Background(), ids[0])
		if err != nil || tab == nil {
			t.Fatalf("tab: %v %v", tab, err)
		}
		tabID = tab.TabID
	})
	b.closeTab(tabID)
	if out, err := run(t, dbPath, true, "fill", "sweep"); err != nil {
		t.Fatalf("sweep: %v (%s)", err, out)
	}
	realSubmit := submitTask
	var answered string
	submitTask = func(ctx context.Context, args []string) ([]byte, error) {
		var kept []string // the submit ignores the id it was asked for
		for i := 0; i < len(args); i++ {
			if args[i] == "--task-id" {
				i++
				continue
			}
			kept = append(kept, args[i])
		}
		out, err := realSubmit(ctx, kept)
		var task struct {
			ID string `json:"taskId"`
		}
		_ = json.Unmarshal(out, &task)
		answered = task.ID
		return out, err
	}
	b.status = aitask.StatusRunning
	if _, err := run(t, dbPath, true, "fill", "resume", ids[0]); err == nil || !strings.Contains(err.Error(), "not the") {
		t.Fatalf("resume = %v", err)
	}
	withDB(t, dbPath, func(db *sql.DB) {
		task, err := (&aitask.Store{DB: db, Profile: "default"}).Get(answered)
		if err != nil || task.Status != aitask.StatusCancelled {
			t.Fatalf("the task a wrong-id answer made: %+v %v", task, err)
		}
	})
}

// Submit made the task but linking it to the application failed: the resume gives the tab back and cancels that
// task, which would otherwise run unlinked.
func TestFillResumeCancelsATaskItCouldNotLink(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 1)
	b := &browser{}
	b.install(t, dbPath)
	if out, err := run(t, dbPath, true, "fill", "queue", ids[0]); err != nil {
		t.Fatalf("queue: %v (%s)", err, out)
	}
	withDB(t, dbPath, func(db *sql.DB) {
		if _, err := db.Exec(`CREATE TRIGGER no_links BEFORE INSERT ON application_links BEGIN SELECT RAISE(ABORT, 'link refused'); END`); err != nil {
			t.Fatal(err)
		}
	})
	realSubmit := submitTask
	var made string
	submitTask = func(ctx context.Context, args []string) ([]byte, error) {
		made = flagValues(args, "--task-id")[0]
		return realSubmit(ctx, args)
	}
	b.status = aitask.StatusRunning
	if _, err := run(t, dbPath, true, "fill", "resume", ids[0]); err == nil || !strings.Contains(err.Error(), "could not be linked") {
		t.Fatalf("resume = %v", err)
	}
	withDB(t, dbPath, func(db *sql.DB) {
		task, err := (&aitask.Store{DB: db, Profile: "default"}).Get(made)
		if err != nil || task.Status != aitask.StatusCancelled {
			t.Fatalf("the task that could not be linked: %+v %v", task, err)
		}
		tab, _ := (applypark.Store{DB: db, Profile: "default"}).Get(context.Background(), ids[0])
		if tab == nil || tab.State != applypark.StateParked {
			t.Fatalf("tab = %+v", tab)
		}
	})
}

// The fill started and linked, then clearing the old submit observation failed: the resume reports failure, so it
// gives the tab back and cancels the task instead of leaving it filling.
func TestFillResumeCancelsATaskWhenClearingTheObservationFails(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 1)
	b := &browser{}
	b.install(t, dbPath)
	if out, err := run(t, dbPath, true, "fill", "queue", ids[0]); err != nil {
		t.Fatalf("queue: %v (%s)", err, out)
	}
	withDB(t, dbPath, func(db *sql.DB) {
		if _, err := db.Exec(`CREATE TRIGGER no_clear BEFORE UPDATE OF submit_observed_at ON application_fills BEGIN SELECT RAISE(ABORT, 'clear refused'); END`); err != nil {
			t.Fatal(err)
		}
	})
	realSubmit := submitTask
	var made string
	submitTask = func(ctx context.Context, args []string) ([]byte, error) {
		made = flagValues(args, "--task-id")[0]
		return realSubmit(ctx, args)
	}
	b.status = aitask.StatusRunning
	if _, err := run(t, dbPath, true, "fill", "resume", ids[0]); err == nil || !strings.Contains(err.Error(), "clear refused") {
		t.Fatalf("resume = %v", err)
	}
	withDB(t, dbPath, func(db *sql.DB) {
		task, err := (&aitask.Store{DB: db, Profile: "default"}).Get(made)
		if err != nil || task.Status != aitask.StatusCancelled {
			t.Fatalf("the task of a resume reported as failed: %+v %v", task, err)
		}
		tab, _ := (applypark.Store{DB: db, Profile: "default"}).Get(context.Background(), ids[0])
		if tab == nil || tab.State != applypark.StateParked {
			t.Fatalf("tab = %+v", tab)
		}
	})
}

// Two resumes of one parked tab at once: exactly one starts a task and holds the tab; the other changes nothing.
func TestTwoResumesOfOneTabAtOnceHaveOneWinner(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 1)
	b := &browser{}
	b.install(t, dbPath)
	if out, err := run(t, dbPath, true, "fill", "queue", ids[0]); err != nil {
		t.Fatalf("queue: %v (%s)", err, out)
	}
	b.mu.Lock()
	b.started = nil
	b.mu.Unlock()
	b.status = aitask.StatusRunning
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = runWith(t, testDeps(dbPath, true), "fill", "resume", ids[0])
		}(i)
	}
	close(start)
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("exactly one resume must win: %v / %v", errs[0], errs[1])
	}
	b.mu.Lock()
	started := len(b.started)
	b.mu.Unlock()
	if started != 1 {
		t.Fatalf("%d tasks started, want 1", started)
	}
	withDB(t, dbPath, func(db *sql.DB) {
		tab, _ := (applypark.Store{DB: db, Profile: "default"}).Get(context.Background(), ids[0])
		var running int
		_ = db.QueryRow(`SELECT COUNT(*) FROM ai_tasks WHERE id = ? AND status = ?`, tab.TaskID, aitask.StatusRunning).Scan(&running)
		if tab.State != applypark.StateActive || running != 1 {
			t.Fatalf("tab = %+v, running tasks under it = %d", tab, running)
		}
	})
}

// A real database error while cancelling is joined into what giveBack returns, with the cause: the failure to stop
// the task is never dropped.
func TestGiveBackJoinsADatabaseErrorOfTheCancel(t *testing.T) {
	dbPath := homeDB(t)
	var tabs applypark.Store
	withDB(t, dbPath, func(db *sql.DB) { tabs = applypark.Store{DB: db, Profile: "default"} })
	closed, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	d := Deps{ProfileID: func() string { return "default" }}
	cause := errors.New("boom")
	err = d.giveBack(context.Background(), closed.DB, tabs, "app", "at-00000000000000a1", "", []string{"at-00000000000000a1"}, cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "could not be cancelled") {
		t.Fatalf("err = %v, want the cause joined with the cancel failure", err)
	}
}

// The resume committed (task started, linked, tab bound) and only writing its report failed: the error reaches the
// caller, but nothing is undone.
func TestResumeWhoseReportCannotBeWrittenStands(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 1)
	b := &browser{}
	b.install(t, dbPath)
	if out, err := run(t, dbPath, true, "fill", "queue", ids[0]); err != nil {
		t.Fatalf("queue: %v (%s)", err, out)
	}
	b.status = aitask.StatusRunning
	cmd := NewCommand(testDeps(dbPath, true))
	cmd.SetArgs([]string{"fill", "resume", ids[0]})
	cmd.SetOut(failingWriter{})
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("a failed report write was not reported")
	}
	withDB(t, dbPath, func(db *sql.DB) {
		tab, _ := (applypark.Store{DB: db, Profile: "default"}).Get(context.Background(), ids[0])
		var running int
		_ = db.QueryRow(`SELECT COUNT(*) FROM ai_tasks WHERE id = ? AND status = ?`, tab.TaskID, aitask.StatusRunning).Scan(&running)
		if tab.State != applypark.StateActive || running != 1 {
			t.Fatalf("the committed resume was undone: tab = %+v, running = %d", tab, running)
		}
	})
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write refused") }

// A submit that dies after its task started (no answer read): the resume reports the task that is there and does
// not undo it, rather than reporting a failed resume.
func TestResumeWhoseSubmitDiedAfterTheTaskStartedReportsTheTask(t *testing.T) {
	dbPath := homeDB(t)
	ids := jobs(t, dbPath, 1)
	b := &browser{}
	b.install(t, dbPath)
	if out, err := run(t, dbPath, true, "fill", "queue", ids[0]); err != nil {
		t.Fatalf("queue: %v (%s)", err, out)
	}
	realSubmit := submitTask
	var taskID string
	submitTask = func(ctx context.Context, args []string) ([]byte, error) {
		taskID = flagValues(args, "--task-id")[0]
		if _, err := realSubmit(ctx, args); err != nil {
			t.Fatal(err)
		}
		return nil, errors.New("signal: killed")
	}
	b.status = aitask.StatusRunning
	_, err := run(t, dbPath, true, "fill", "resume", ids[0])
	if err == nil || !strings.Contains(err.Error(), taskID) || !strings.Contains(err.Error(), aitask.StatusRunning) {
		t.Fatalf("resume = %v, want it to name task %s as running", err, taskID)
	}
	withDB(t, dbPath, func(db *sql.DB) {
		task, gerr := (&aitask.Store{DB: db, Profile: "default"}).Get(taskID)
		tab, _ := (applypark.Store{DB: db, Profile: "default"}).Get(context.Background(), ids[0])
		if gerr != nil || task.Status != aitask.StatusRunning || tab.State != applypark.StateActive || tab.TaskID != taskID {
			t.Fatalf("task = %+v %v, tab = %+v: the started task was undone", task, gerr, tab)
		}
	})
}

// The command says that a resume stands when only its report fails.
func TestResumeHelpSaysItStandsWhenItsReportFails(t *testing.T) {
	for _, c := range NewCommand(testDeps("", true)).Commands() {
		if c.Name() != "fill" {
			continue
		}
		for _, r := range c.Commands() {
			if r.Name() == "resume" && !strings.Contains(r.Long, "nothing is undone") {
				t.Fatalf("resume help = %q", r.Long)
			}
		}
	}
}
