package applypark

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/testdb"
)

var bg = context.Background()

const profile = "default"

func fixture(t *testing.T) (*sql.DB, Store, *applications.Store) {
	t.Helper()
	db := testdb.Open(t).DB
	return db, Store{DB: db, Profile: profile}, applications.NewStore(db)
}

func newApp(t *testing.T, apps *applications.Store, url string) string {
	t.Helper()
	app := &applications.Application{ProfileID: profile, Kind: applications.KindJob,
		Job: &applications.JobDetails{Title: "Engineer", Company: "Acme", URL: url}}
	if err := apps.Create(bg, app); err != nil {
		t.Fatal(err)
	}
	return app.ID
}

// parkedApp is an application at awaiting_submit with a fill record and its tab parked.
func parkedApp(t *testing.T, st Store, apps *applications.Store, tab int) string {
	t.Helper()
	id := newApp(t, apps, "https://jobs.example/1")
	if err := apps.RecordFill(bg, profile, id, applications.Fill{Status: applications.FillFilled}); err != nil {
		t.Fatal(err)
	}
	if err := apps.SetStatus(bg, profile, id, applications.StatusAwaitingSubmit, applications.ActorSystem, "fill"); err != nil {
		t.Fatal(err)
	}
	bindOK(t, st, id, tab)
	return id
}

func live(tab int, url string) LiveTab {
	return LiveTab{TabID: tab, GroupTitle: GroupPrefix + "Fill", URL: url}
}

func bindOK(t *testing.T, st Store, id string, tab int) {
	t.Helper()
	if err := st.RecordOpen(bg, "at-1", tab); err != nil {
		t.Fatal(err)
	}
	err := st.Bind(bg, BindRequest{ApplicationID: id, TaskID: "at-1", TabID: tab, Origin: "https://jobs.example",
		Live: []LiveTab{live(tab, "https://jobs.example/apply")}})
	if err != nil {
		t.Fatal(err)
	}
}

func appStatus(t *testing.T, apps *applications.Store, id string) applications.Status {
	t.Helper()
	a, err := apps.Get(bg, profile, id)
	if err != nil {
		t.Fatal(err)
	}
	return a.Status
}

func TestEnvTaskIDIsAitasks(t *testing.T) {
	if EnvTaskID != aitask.EnvTaskID {
		t.Fatalf("EnvTaskID %q drifted from aitask's %q", EnvTaskID, aitask.EnvTaskID)
	}
}

func TestBindValidatesAgainstTheBrowser(t *testing.T) {
	_, st, apps := fixture(t)
	id := newApp(t, apps, "https://jobs.example/1")
	other := newApp(t, apps, "https://jobs.example/2")
	for _, tab := range []int{7, 9, 11, 1 << 40} {
		if tab <= maxTabID {
			if err := st.RecordOpen(bg, "at-1", tab); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.RecordOpen(bg, "at-2", 9); err != nil {
		t.Fatal(err)
	}
	good := BindRequest{ApplicationID: id, TaskID: "at-1", TabID: 7, Origin: "https://jobs.example",
		Live: []LiveTab{live(7, "https://jobs.example/apply")}}
	bad := map[string]func(r *BindRequest){
		"tab id zero":          func(r *BindRequest) { r.TabID = 0 },
		"tab id negative":      func(r *BindRequest) { r.TabID = -4 },
		"tab id out of range":  func(r *BindRequest) { r.TabID = 1 << 40; r.Live[0].TabID = 1 << 40 },
		"no task":              func(r *BindRequest) { r.TaskID = " " },
		"origin not a url":     func(r *BindRequest) { r.Origin = "jobs.example" },
		"origin is javascript": func(r *BindRequest) { r.Origin = "javascript:alert(1)" },
		"origin has user info": func(r *BindRequest) { r.Origin = "https://a:b@jobs.example" },
		"tab is not open":      func(r *BindRequest) { r.Live = nil },
		"tab not in agent group": func(r *BindRequest) {
			r.Live = []LiveTab{{TabID: 7, GroupTitle: "Work", URL: "https://jobs.example/apply"}}
		},
		"tab is on another origin": func(r *BindRequest) { r.Live = []LiveTab{live(7, "https://evil.example/x")} },
		"tab is not a web page":    func(r *BindRequest) { r.Live = []LiveTab{live(7, "chrome://settings")} },
	}
	for name, mutate := range bad {
		r := good
		r.Live = append([]LiveTab(nil), good.Live...)
		mutate(&r)
		if err := st.Bind(bg, r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	if tab, _ := st.Get(bg, id); tab != nil {
		t.Fatalf("a refused claim bound a tab: %+v", tab)
	}
	if err := st.Bind(bg, good); err != nil {
		t.Fatal(err)
	}
	tab, _ := st.Get(bg, id)
	if tab == nil || tab.State != StateParked || tab.TabID != 7 || tab.Origin != "https://jobs.example" {
		t.Fatalf("binding = %+v", tab)
	}
	// One live tab belongs to one application.
	steal := good
	steal.ApplicationID = other
	if err := st.Bind(bg, steal); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a second application bound the same tab: %v", err)
	}
	// A redirect target: the claim equals the tab's own current origin.
	redirected := BindRequest{ApplicationID: other, TaskID: "at-2", TabID: 9, Origin: "https://ats.example:8443",
		Live: []LiveTab{live(9, "https://ats.example:8443/form")}}
	if err := st.Bind(bg, redirected); err != nil {
		t.Fatalf("a redirect target the browser reports is refused: %v", err)
	}
	// An application that does not exist (or is another profile's) binds nothing.
	ghost := good
	ghost.ApplicationID, ghost.TabID, ghost.Live = "nope", 11, []LiveTab{live(11, "https://jobs.example/a")}
	if err := st.Bind(bg, ghost); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown application: %v", err)
	}
}

func TestMaxParkedDefaultsAndClamps(t *testing.T) {
	db, _, _ := fixture(t)
	if got := MaxParked(db); got != 3 {
		t.Fatalf("default = %d", got)
	}
	for raw, want := range map[string]int{"5": 5, `"7"`: 7, "0": 1, "-3": 1, "99": 10, "many": 3, " 2 ": 2} {
		if _, err := db.Exec(`INSERT INTO configs (name, config_data) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data`,
			ConfigMaxParked, raw); err != nil {
			t.Fatal(err)
		}
		if got := MaxParked(db); got != want {
			t.Errorf("config %q = %d, want %d", raw, got, want)
		}
	}
}

func TestObserveOnlyRecordsAHintAndNeverSetsApplied(t *testing.T) {
	db, st, apps := fixture(t)
	id := parkedApp(t, st, apps, 7)
	// Wrong origin, wrong tab, bad input: nothing recorded.
	for _, o := range []Observation{{TabID: 8, Origin: "https://jobs.example"}, {TabID: 7, Origin: "https://evil.example"}} {
		if _, ok, err := Observe(bg, db, o); err != nil || ok {
			t.Fatalf("%+v matched or failed: %v %v", o, ok, err)
		}
	}
	if _, _, err := Observe(bg, db, Observation{TabID: 7, Origin: "javascript:1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad origin: %v", err)
	}
	if at, _ := ObservedAt(bg, db, id); at != "" {
		t.Fatalf("observed %q after refused observations", at)
	}
	got, ok, err := Observe(bg, db, Observation{TabID: 7, Origin: "HTTPS://Jobs.Example", URL: "https://jobs.example/thanks"})
	if err != nil || !ok || got != id {
		t.Fatalf("Observe = %q %v %v", got, ok, err)
	}
	if at, _ := ObservedAt(bg, db, id); at == "" {
		t.Fatal("no submit_observed_at after a matching observation")
	}
	// The revert-proof part: the status is unchanged and the fill is not "submitted".
	if s := appStatus(t, apps, id); s != applications.StatusAwaitingSubmit {
		t.Fatalf("an observed submit moved the application to %s", s)
	}
	if f, _ := apps.GetFill(bg, profile, id); f.Status == applications.FillSubmitted {
		t.Fatal("an observed submit marked the fill submitted")
	}
	// A new fill forgets it.
	if err := ClearObserved(bg, db, id); err != nil {
		t.Fatal(err)
	}
	if at, _ := ObservedAt(bg, db, id); at != "" {
		t.Fatal("ClearObserved left the hint")
	}
	// A closed tab no longer matches.
	if err := st.SetState(bg, id, StateClosed, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := Observe(bg, db, Observation{TabID: 7, Origin: "https://jobs.example"}); ok {
		t.Fatal("a closed tab matched")
	}
}

func TestObserveAmbiguousTabMatchesNothing(t *testing.T) {
	db, _, apps := fixture(t)
	if _, err := db.Exec(`INSERT INTO profiles (id, name) VALUES ('other', 'Other')`); err != nil {
		t.Skip("cannot add a second profile:", err)
	}
	a := newApp(t, apps, "https://jobs.example/1")
	b := &applications.Application{ProfileID: "other", Kind: applications.KindJob, Job: &applications.JobDetails{Title: "x", Company: "y", URL: "https://jobs.example/2"}}
	if err := apps.Create(bg, b); err != nil {
		t.Skip("cannot create in a second profile:", err)
	}
	for _, id := range []string{a, b.ID} {
		if err := apps.RecordFill(bg, map[string]string{a: profile, b.ID: "other"}[id], id, applications.Fill{Status: applications.FillFilled}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE applications SET status = 'awaiting_submit' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	// The unique index forbids two live bindings on one tab id, so the ambiguity
	// cannot arise; prove the index is what prevents it.
	if _, err := db.Exec(`INSERT INTO application_tabs VALUES (?, 't', 5, 'https://jobs.example', 'parked', 'x')`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO application_tabs VALUES (?, 't', 5, 'https://jobs.example', 'parked', 'x')`, b.ID); err == nil {
		t.Fatal("two live bindings share a tab id")
	}
}

func TestSweepMarksLostTabsAndClosesOverOnes(t *testing.T) {
	db, st, apps := fixture(t)
	kept := parkedApp(t, st, apps, 7)
	gone := parkedApp(t, st, apps, 8)
	done := parkedApp(t, st, apps, 9)
	if err := apps.SetStatus(bg, profile, done, applications.StatusApplied, applications.ActorUser, "sent"); err != nil {
		t.Fatal(err)
	}
	res, err := Sweep(bg, st, apps, []LiveTab{live(7, "https://jobs.example/a"), live(9, "https://jobs.example/c")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lost) != 1 || res.Lost[0] != gone || len(res.Closed) != 1 || res.Closed[0] != done {
		t.Fatalf("sweep = %+v", res)
	}
	if s := appStatus(t, apps, gone); s != applications.StatusNeedsYou {
		t.Fatalf("lost tab: status %s", s)
	}
	if f, _ := apps.GetFill(bg, profile, gone); !strings.Contains(f.Note, "Reopen and refill") || f.Status != applications.FillNeedsYou {
		t.Fatalf("lost tab fill = %+v", f)
	}
	if tab, _ := st.Get(bg, gone); tab.State != StateLost {
		t.Fatalf("state %s", tab.State)
	}
	if tab, _ := st.Get(bg, done); tab.State != StateClosed {
		t.Fatalf("state %s", tab.State)
	}
	if s := appStatus(t, apps, kept); s != applications.StatusAwaitingSubmit {
		t.Fatalf("kept tab: status %s", s)
	}
	if s := appStatus(t, apps, done); s != applications.StatusApplied {
		t.Fatalf("the sent application is %s", s)
	}
	// A lost tab frees its slot: only the kept one is occupied.
	if n, _ := st.Occupied(bg); n != 1 {
		t.Fatalf("occupied = %d", n)
	}
	_ = db
}

// A tab held `active` by a task that ended (the delete of its conversation cancels it) goes back to the person
// as parked; a task still going, and the placeholder a resume writes before its task exists, are left alone.
func TestSweepParksATabWhoseTaskEnded(t *testing.T) {
	db, st, apps := fixture(t)
	ended := parkedApp(t, st, apps, 7)
	running := parkedApp(t, st, apps, 8)
	pending := parkedApp(t, st, apps, 9)
	other := parkedApp(t, st, apps, 10)
	task := func(id, prof, status string) {
		if _, err := db.Exec(`INSERT INTO ai_tasks (id, profile_id, kind, conversation_id, status, goal, application_id, created_at, updated_at)
			VALUES (?, ?, 'apply', 'c1', ?, 'g', '', 'x', 'x')`, id, prof, status); err != nil {
			t.Fatal(err)
		}
	}
	task("t-ended", profile, "cancelled")
	task("t-running", profile, "running")
	task("t-other", "someone-else", "cancelled") // another profile's task id does not count
	for app, tk := range map[string]string{ended: "t-ended", running: "t-running", pending: "resume-pending", other: "t-other"} {
		if err := st.SetState(bg, app, StateActive, tk); err != nil {
			t.Fatal(err)
		}
	}
	tabs := []LiveTab{live(7, "https://jobs.example/a"), live(8, "https://jobs.example/b"), live(9, "https://jobs.example/c"), live(10, "https://jobs.example/d")}
	if _, err := Sweep(bg, st, apps, tabs); err != nil {
		t.Fatal(err)
	}
	for app, want := range map[string]State{ended: StateParked, running: StateActive, pending: StateActive, other: StateActive} {
		if tab, _ := st.Get(bg, app); tab.State != want {
			t.Errorf("%s: state %s, want %s", app, tab.State, want)
		}
	}
}

// A resume holds the tab under the id of the task it is about to create. One that died before creating it leaves
// a tab held under an id no task has; once the hold is older than any resume could take, the sweep gives it back.
// A task that exists is judged like any other, and a fresh hold is a resume in flight.
func TestSweepParksATabHeldForATaskThatNeverExisted(t *testing.T) {
	db, st, apps := fixture(t)
	dead := parkedApp(t, st, apps, 7)
	started := parkedApp(t, st, apps, 8)
	fresh := parkedApp(t, st, apps, 9)
	legacy := parkedApp(t, st, apps, 10)
	if _, err := db.Exec(`INSERT INTO ai_tasks (id, profile_id, kind, conversation_id, status, goal, application_id, created_at, updated_at)
		VALUES ('at-0000000000000002', ?, 'application-apply', 'c1', 'running', 'g', ?, 'x', 'x')`, profile, started); err != nil {
		t.Fatal(err)
	}
	for app, task := range map[string]string{dead: "at-0000000000000001", started: "at-0000000000000002", fresh: "at-0000000000000003", legacy: "resume-pending"} {
		if err := st.SetState(bg, app, StateActive, task); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().UTC().Add(-2 * ResumeHoldTTL).Format(time.RFC3339)
	for _, app := range []string{dead, started, legacy} {
		if _, err := db.Exec(`UPDATE application_tabs SET updated_at = ? WHERE application_id = ?`, old, app); err != nil {
			t.Fatal(err)
		}
	}
	tabs := []LiveTab{live(7, "https://jobs.example/a"), live(8, "https://jobs.example/b"), live(9, "https://jobs.example/c"), live(10, "https://jobs.example/d")}
	if _, err := Sweep(bg, st, apps, tabs); err != nil {
		t.Fatal(err)
	}
	for app, want := range map[string]State{dead: StateParked, started: StateActive, fresh: StateActive, legacy: StateParked} {
		if tab, _ := st.Get(bg, app); tab.State != want {
			t.Errorf("%s: state %s, want %s", app, tab.State, want)
		}
	}
	// The expired hold's id is cleared: a task that turns up late and finishes before its resume looks is not
	// taken for the owner of a tab the sweep gave back.
	if _, err := db.Exec(`INSERT INTO ai_tasks (id, profile_id, kind, conversation_id, status, goal, application_id, created_at, updated_at)
		VALUES ('at-0000000000000001', ?, 'application-apply', 'c1', 'complete', 'g', ?, 'x', 'x')`, profile, dead); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Holds(bg, dead, "at-0000000000000001"); ok {
		t.Fatal("a finished late task is read as the owner of a tab the sweep gave back")
	}
}

// The sweep read the tab under an id no task had; before the park the resume created its task, or the tab moved
// to another resume. The park must leave the tab alone.
func TestParkUnstartedResumeLeavesATabTheResumeMovedOn(t *testing.T) {
	db, st, apps := fixture(t)
	old := time.Now().UTC().Add(-2 * ResumeHoldTTL).Format(time.RFC3339)
	hold := func(tab int, task string) (string, Tab) {
		app := parkedApp(t, st, apps, tab)
		if err := st.SetState(bg, app, StateActive, task); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE application_tabs SET updated_at = ? WHERE application_id = ?`, old, app); err != nil {
			t.Fatal(err)
		}
		snap, _ := st.Get(bg, app)
		return app, *snap
	}
	created, snapCreated := hold(7, "at-0000000000000001")
	if _, err := db.Exec(`INSERT INTO ai_tasks (id, profile_id, kind, conversation_id, status, goal, application_id, created_at, updated_at)
		VALUES ('at-0000000000000001', ?, 'application-apply', 'c1', 'queued', 'g', ?, 'x', 'x')`, profile, created); err != nil {
		t.Fatal(err)
	}
	moved, snapMoved := hold(8, "at-0000000000000002")
	if err := st.SetState(bg, moved, StateActive, "at-0000000000000009"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE application_tabs SET updated_at = ? WHERE application_id = ?`, old, moved); err != nil {
		t.Fatal(err)
	}
	for _, snap := range []Tab{snapCreated, snapMoved} {
		if err := parkUnstartedResume(bg, st, snap); err != nil {
			t.Fatal(err)
		}
	}
	if tab, _ := st.Get(bg, created); tab.State != StateActive {
		t.Fatalf("a resume's task existed and its tab was parked: %+v", tab)
	}
	if tab, _ := st.Get(bg, moved); tab.State != StateActive || tab.TaskID != "at-0000000000000009" {
		t.Fatalf("the park took a tab another resume holds: %+v", tab)
	}
}

// A resume takes the parked tab it read, or nothing: two resumes cannot both hold it, and a stale read does not
// overwrite what the person did since.
func TestClaimTakesOnlyTheTabThatWasRead(t *testing.T) {
	_, st, apps := fixture(t)
	app := parkedApp(t, st, apps, 7)
	tab, _ := st.Get(bg, app)
	if held, err := st.Claim(bg, app, tab.TaskID, "at-1"); err != nil || !held {
		t.Fatalf("first claim: %v %v", held, err)
	}
	if held, _ := st.Claim(bg, app, tab.TaskID, "at-2"); held {
		t.Fatal("a second resume took a tab the first holds")
	}
	if got, _ := st.Get(bg, app); got.TaskID != "at-1" || got.State != StateActive {
		t.Fatalf("tab = %+v", got)
	}
	// The observed id is matched as written, not as a pattern.
	other := parkedApp(t, st, apps, 8)
	if err := st.SetState(bg, other, StateParked, "at-x"); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"%", "at-_", "at-%"} {
		if held, _ := st.Claim(bg, other, pattern, "at-9"); held {
			t.Fatalf("observed id %q matched as a pattern", pattern)
		}
	}
	// The person closed the tab after the resume read it as parked.
	if err := st.SetState(bg, app, StateClosed, ""); err != nil {
		t.Fatal(err)
	}
	if held, _ := st.Claim(bg, app, "at-1", "at-3"); held {
		t.Fatal("a stale read overwrote a closed tab")
	}
}

// A resume that failed gives the tab back only while it still holds it.
func TestReleaseOnlyWhileTheResumeHoldsTheTab(t *testing.T) {
	_, st, apps := fixture(t)
	app := parkedApp(t, st, apps, 7)
	prev, _ := st.Get(bg, app)
	if held, _ := st.Claim(bg, app, prev.TaskID, "at-1"); !held {
		t.Fatal("claim")
	}
	if err := st.SetState(bg, app, StateActive, "at-2"); err != nil { // someone else took it since
		t.Fatal(err)
	}
	if ok, err := st.Release(bg, app, "at-1", prev.TaskID); err != nil || ok {
		t.Fatalf("a late release took a newer owner's tab: %v %v", ok, err)
	}
	if ok, _ := st.Release(bg, app, "at-2", prev.TaskID); !ok {
		t.Fatal("the holder could not give the tab back")
	}
	if got, _ := st.Get(bg, app); got.State != StateParked || got.TaskID != prev.TaskID {
		t.Fatalf("tab = %+v", got)
	}
}

// Holds: the tab is the started task's while active under it; parked under it counts only when the task is over.
func TestHoldsKnowsAGivenBackTab(t *testing.T) {
	db, st, apps := fixture(t)
	app := parkedApp(t, st, apps, 7)
	if err := st.SetState(bg, app, StateActive, "at-1"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Holds(bg, app, "at-1"); !ok {
		t.Fatal("active under the task")
	}
	if err := st.SetState(bg, app, StateParked, ""); err != nil { // a sweep parked it before the task existed
		t.Fatal(err)
	}
	if ok, _ := st.Holds(bg, app, "at-1"); ok {
		t.Fatal("a tab the sweep gave back is held")
	}
	if _, err := db.Exec(`INSERT INTO ai_tasks (id, profile_id, kind, conversation_id, status, goal, application_id, created_at, updated_at)
		VALUES ('at-1', ?, 'application-apply', 'c1', 'complete', 'g', ?, 'x', 'x')`, profile, app); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Holds(bg, app, "at-1"); !ok {
		t.Fatal("the task finished and parked its own tab")
	}
}

// Sweep read the tab as active under an ended task; before the park a new task took the tab. The park must not
// land on the new task's tab, whatever state that task is in.
func TestParkIfTaskEndedLeavesATabANewTaskTook(t *testing.T) {
	for _, status := range []string{"running", "queued", "needs_user"} {
		t.Run(status, func(t *testing.T) {
			db, st, apps := fixture(t)
			app := parkedApp(t, st, apps, 7)
			ins := func(id, status string) {
				if _, err := db.Exec(`INSERT INTO ai_tasks (id, profile_id, kind, conversation_id, status, goal, application_id, created_at, updated_at)
					VALUES (?, ?, 'apply', 'c1', ?, 'g', '', 'x', 'x')`, id, profile, status); err != nil {
					t.Fatal(err)
				}
			}
			ins("t1", "cancelled")
			ins("t2", status)
			if err := st.SetState(bg, app, StateActive, "t1"); err != nil {
				t.Fatal(err)
			}
			snap, _ := st.Get(bg, app) // what the sweep's read saw
			if err := st.SetState(bg, app, StateActive, "t2"); err != nil {
				t.Fatal(err)
			}
			if err := parkIfTaskEnded(bg, st, *snap); err != nil {
				t.Fatal(err)
			}
			if tab, _ := st.Get(bg, app); tab.State != StateActive || tab.TaskID != "t2" {
				t.Fatalf("a %s task's tab was parked from a stale read: %+v", status, tab)
			}
		})
	}
}

// fakeRunner stands in for the application commands.
type fakeRunner struct {
	mu        sync.Mutex
	st        Store
	apps      *applications.Store
	started   []string
	running   int32
	maxSeen   int32
	needsYou  map[string]bool
	startErr  map[string]error
	gate      chan struct{} // when set, every Wait blocks until it is closed or fed
	nextTab   int32
	submitted chan string // an application the "person" submitted, freeing its slot
}

func (f *fakeRunner) Start(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.startErr[id]; err != nil {
		return "", err
	}
	f.started = append(f.started, id)
	n := atomic.AddInt32(&f.running, 1)
	if n > f.maxSeen {
		f.maxSeen = n
	}
	return "task-" + id, nil
}

func (f *fakeRunner) Wait(ctx context.Context, _ string) (string, error) {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "complete", nil
}

func (f *fakeRunner) Finalize(ctx context.Context, id string) error {
	defer atomic.AddInt32(&f.running, -1)
	if f.needsYou[id] {
		return f.apps.SetStatus(ctx, profile, id, applications.StatusNeedsYou, applications.ActorSystem, "login wall")
	}
	if err := f.apps.SetStatus(ctx, profile, id, applications.StatusAwaitingSubmit, applications.ActorSystem, "fill"); err != nil {
		return err
	}
	tab := int(atomic.AddInt32(&f.nextTab, 1)) + 100
	if err := f.st.RecordOpen(ctx, "task-"+id, tab); err != nil {
		return err
	}
	return f.st.Bind(ctx, BindRequest{ApplicationID: id, TaskID: "task-" + id, TabID: tab, Origin: "https://jobs.example",
		Live: []LiveTab{live(tab, "https://jobs.example/apply")}})
}

func (f *fakeRunner) Sweep(ctx context.Context) error {
	if f.submitted == nil {
		return nil
	}
	for {
		select {
		case id := <-f.submitted:
			if err := f.apps.SetStatus(ctx, profile, id, applications.StatusApplied, applications.ActorUser, "sent"); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

// person submits each of ids once it is parked (awaiting_submit).
func person(r *fakeRunner, ids []string) {
	for _, id := range ids {
		for i := 0; i < 4000; i++ {
			a, err := r.apps.Get(bg, profile, id)
			if err == nil && a.Status == applications.StatusAwaitingSubmit {
				r.submitted <- id
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func queueFixture(t *testing.T, n int) (*fakeRunner, Queue, []string) {
	t.Helper()
	_, st, apps := fixture(t)
	var ids []string
	for i := 0; i < n; i++ {
		ids = append(ids, newApp(t, apps, fmt.Sprintf("https://jobs.example/%d", i)))
	}
	r := &fakeRunner{st: st, apps: apps, needsYou: map[string]bool{}, startErr: map[string]error{}}
	q := Queue{Store: st, Slots: slotsFor(st, "queue-1", 1001), Max: 2, Runner: r, Tick: time.Millisecond,
		Status: func(ctx context.Context, id string) (string, error) {
			a, err := apps.Get(ctx, profile, id)
			return string(a.Status), err
		}}
	return r, q, ids
}

// waitStarted polls until n fills have started; a loaded runner can be slow to get there, so the deadline is
// generous and nothing here depends on how long it takes.
func waitStarted(t *testing.T, r *fakeRunner, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r.mu.Lock()
		got := len(r.started)
		r.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("started %d, want %d", got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// holdStarted gives the queue time to start more than it should and fails if it did.
func holdStarted(t *testing.T, r *fakeRunner, n int, what string) {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.started) != n {
		t.Fatalf("started %d %s", len(r.started), what)
	}
}

// waitJudged waits until the queue has read the status of n finished fills.
func waitJudged(t *testing.T, judged <-chan string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-judged:
		case <-time.After(30 * time.Second):
			t.Fatalf("only %d fills were judged, want %d", i, n)
		}
	}
}

func TestQueueNeverExceedsTheCapAndStartsTheNextAfterOneEnds(t *testing.T) {
	r, q, ids := queueFixture(t, 5)
	r.gate = make(chan struct{})
	r.submitted = make(chan string, 8)
	q.Max = 2
	// The person submits only once the queue has read a fill's status: a submit that lands earlier turns the
	// fill's outcome from parked into needs_you, which is the person's doing and not the queue's.
	judged := make(chan string, 8)
	status := q.Status
	q.Status = func(ctx context.Context, id string) (string, error) {
		st, err := status(ctx, id)
		judged <- id
		return st, err
	}
	done := make(chan []Outcome, 1)
	go func() {
		out, err := q.Run(bg, ids)
		if err != nil {
			t.Error(err)
		}
		done <- out
	}()
	// With every fill held, exactly Max start, never more.
	waitStarted(t, r, 2)
	holdStarted(t, r, 2, "before any ended, want the cap of 2")
	close(r.gate) // the two fills end and park: the queue waits for the person
	r.mu.Lock()
	first, second := r.started[0], r.started[1]
	r.mu.Unlock()
	waitJudged(t, judged, 2)
	holdStarted(t, r, 2, "with 2 tabs parked, want 2: parked tabs hold their slots")
	r.submitted <- first // the person submits one: its slot frees and the next starts
	waitStarted(t, r, 3)
	holdStarted(t, r, 3, "after a submit, want 3")
	r.submitted <- second
	go person(r, ids[2:])
	var out []Outcome
	select {
	case out = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the queue never finished")
	}
	if len(out) != 5 {
		t.Fatalf("outcomes = %+v", out)
	}
	for _, o := range out {
		if o.Result != ResultParked {
			t.Errorf("%s: %s (%s)", o.ApplicationID, o.Result, o.Error)
		}
	}
	if r.maxSeen > 2 {
		t.Fatalf("%d fills ran at once, cap 2", r.maxSeen)
	}
}

func TestQueueMovesOnFromNeedsYouAndFailures(t *testing.T) {
	r, q, ids := queueFixture(t, 4)
	q.Max = 1
	r.needsYou[ids[0]] = true
	r.startErr[ids[1]] = errors.New("coder is off")
	r.submitted = make(chan string, 8)
	// ids[0] ends needs_you (a login wall) and holds its slot as a tab only if one is bound:
	// none is, so the queue moves on at once; ids[1] fails to start; ids[2] parks.
	// With a cap of 1 ids[3] waits for ids[2]'s submit.
	done := make(chan []Outcome, 1)
	go func() { out, _ := q.Run(bg, ids); done <- out }()
	time.Sleep(80 * time.Millisecond)
	go person(r, ids[2:])
	var out []Outcome
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the queue never finished")
	}
	want := []string{ResultNeedsYou, ResultFailed, ResultParked, ResultParked}
	for i, o := range out {
		if o.Result != want[i] {
			t.Errorf("%s: %s (%s), want %s", o.ApplicationID, o.Result, o.Error, want[i])
		}
	}
	if out[1].Error != "coder is off" {
		t.Errorf("failure reason = %q", out[1].Error)
	}
}

func TestQueueStopsOnCancelAndReportsNotStarted(t *testing.T) {
	r, q, ids := queueFixture(t, 3)
	r.gate = make(chan struct{}) // never opened
	q.Max = 1
	ctx, cancel := context.WithCancel(bg)
	done := make(chan []Outcome, 1)
	go func() { out, _ := q.Run(ctx, ids); done <- out }()
	time.Sleep(40 * time.Millisecond)
	cancel()
	var out []Outcome
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the queue ignored cancellation")
	}
	if out[1].Result != ResultNotStarted || out[2].Result != ResultNotStarted {
		t.Fatalf("outcomes = %+v", out)
	}
}

// No path of this package may set applied: the only caller of SetStatus with
// that status is the person's `application send`.
func TestNothingInThePackageSetsApplied(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "StatusApplied") || strings.Contains(string(raw), "'applied'") || strings.Contains(string(raw), "FillSubmitted") {
			t.Errorf("%s mentions applied/submitted: applypark must never set it", f)
		}
	}
}

// Whatever the queue, sweep and observation do, applied stays unset.
func TestAppliedIsNeverSetByAnyPath(t *testing.T) {
	r, q, ids := queueFixture(t, 3)
	q.Max = 3
	out, err := q.Run(bg, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range out {
		if o.Result != ResultParked {
			t.Fatalf("%+v", o)
		}
	}
	db := r.st.DB
	for _, id := range ids {
		tab, _ := r.st.Get(bg, id)
		if _, _, err := Observe(bg, db, Observation{TabID: tab.TabID, Origin: tab.Origin}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Sweep(bg, r.st, r.apps, nil); err != nil { // every tab is gone
		t.Fatal(err)
	}
	for _, id := range ids {
		if s := appStatus(t, r.apps, id); s == applications.StatusApplied {
			t.Fatalf("%s became applied", id)
		}
		if f, _ := r.apps.GetFill(bg, profile, id); f.Status == applications.FillSubmitted {
			t.Fatalf("%s fill became submitted", id)
		}
	}
}

func slotsFor(st Store, owner string, pid int) Slots {
	return Slots{DB: st.DB, Profile: st.Profile, Owner: owner, Pid: pid, Alive: func(int) bool { return true }}
}

// A task may claim only a tab it opened: another task's tab, a tab opened by
// the person and a tab nobody recorded are all refused, and the owner's
// binding is untouched.
func TestATaskCannotClaimAnotherTasksTab(t *testing.T) {
	_, st, apps := fixture(t)
	a := newApp(t, apps, "https://jobs.example/1")
	b := newApp(t, apps, "https://jobs.example/2")
	if err := st.RecordOpen(bg, "at-B", 20); err != nil {
		t.Fatal(err)
	}
	claim := func(app, task string, tab int) error {
		return st.Bind(bg, BindRequest{ApplicationID: app, TaskID: task, TabID: tab, Origin: "https://jobs.example",
			Live: []LiveTab{live(tab, "https://jobs.example/apply")}})
	}
	if err := claim(b, "at-B", 20); err != nil {
		t.Fatal(err)
	}
	if err := claim(a, "at-A", 20); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not opened by task at-A") {
		t.Fatalf("A claimed B's tab: %v", err)
	}
	if err := claim(a, "at-A", 21); !errors.Is(err, ErrInvalid) {
		t.Fatalf("A claimed a tab nobody recorded: %v", err)
	}
	tab, _ := st.Get(bg, b)
	if tab == nil || tab.TaskID != "at-B" || tab.TabID != 20 || tab.State != StateParked {
		t.Fatalf("B's binding changed: %+v", tab)
	}
	if got, _ := st.Get(bg, a); got != nil {
		t.Fatalf("A got a binding: %+v", got)
	}
	// Even a task that opened the tab cannot take it from another application.
	if err := st.RecordOpen(bg, "at-A", 20); err != nil {
		t.Fatal(err)
	}
	if err := claim(a, "at-A", 20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bound tab was claimed: %v", err)
	}
}

// Two queue processes of one profile share the cap: with max-parked 1 only one
// fill starts, and when the holder dies its slot is released.
func TestTwoQueuesShareTheCapAndADeadOneReleasesIt(t *testing.T) {
	_, st, apps := fixture(t)
	x := newApp(t, apps, "https://jobs.example/x")
	y := newApp(t, apps, "https://jobs.example/y")
	alive := map[int]bool{101: true, 102: true}
	mk := func(owner string, pid int) Slots {
		s := slotsFor(st, owner, pid)
		s.Alive = func(p int) bool { return alive[p] }
		return s
	}
	q1, q2 := mk("q1", 101), mk("q2", 102)
	if ok, err := q1.Reserve(bg, x, 1); err != nil || !ok {
		t.Fatalf("first reserve: %v %v", ok, err)
	}
	if ok, err := q2.Reserve(bg, y, 1); err != nil || ok {
		t.Fatalf("second queue went past the cap: %v %v", ok, err)
	}
	if ok, _ := q2.Reserve(bg, x, 5); ok {
		t.Fatal("another queue took an application already in flight")
	}
	// A parked tab counts too: q1's fill parks, and still no room for y.
	if err := apps.SetStatus(bg, profile, x, applications.StatusAwaitingSubmit, applications.ActorSystem, "f"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordOpen(bg, "at-x", 30); err != nil {
		t.Fatal(err)
	}
	if err := st.Bind(bg, BindRequest{ApplicationID: x, TaskID: "at-x", TabID: 30, Origin: "https://jobs.example",
		Live: []LiveTab{live(30, "https://jobs.example/a")}}); err != nil {
		t.Fatal(err)
	}
	if err := q1.Release(bg, x); err != nil {
		t.Fatal(err)
	}
	if ok, _ := q2.Reserve(bg, y, 1); ok {
		t.Fatal("a parked tab did not hold its slot")
	}
	// q1 reserves again for another fill and dies: its slot goes with it.
	z := newApp(t, apps, "https://jobs.example/z")
	if ok, _ := q1.Reserve(bg, z, 2); !ok {
		t.Fatal("q1 could not reserve with room")
	}
	if ok, _ := q2.Reserve(bg, y, 2); ok {
		t.Fatal("cap 2: parked x plus in-flight z leave no room")
	}
	alive[101] = false
	if ok, err := q2.Reserve(bg, y, 2); err != nil || !ok {
		t.Fatalf("a dead queue's slot was not released: %v %v", ok, err)
	}
}

func TestAStaleLeaseExpires(t *testing.T) {
	_, st, apps := fixture(t)
	x := newApp(t, apps, "https://jobs.example/x")
	y := newApp(t, apps, "https://jobs.example/y")
	clock := time.Now()
	q1 := slotsFor(st, "q1", 1)
	q1.Now = func() time.Time { return clock }
	q1.Lease = time.Minute
	q2 := slotsFor(st, "q2", 2)
	q2.Now = func() time.Time { return clock }
	if ok, _ := q1.Reserve(bg, x, 1); !ok {
		t.Fatal("reserve")
	}
	clock = clock.Add(30 * time.Second)
	if ok, _ := q2.Reserve(bg, y, 1); ok {
		t.Fatal("a live lease was ignored")
	}
	if err := q1.Renew(bg); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(45 * time.Second) // 75 s after reserving, 45 after renewing
	if ok, _ := q2.Reserve(bg, y, 1); ok {
		t.Fatal("a renewed lease expired")
	}
	clock = clock.Add(2 * time.Minute)
	if ok, _ := q2.Reserve(bg, y, 1); !ok {
		t.Fatal("an expired lease still held its slot")
	}
}

// Two real queues over one database never run more fills than the cap.
func TestTwoQueueRunsNeverExceedTheCap(t *testing.T) {
	_, st, apps := fixture(t)
	var ids1, ids2 []string
	for i := 0; i < 3; i++ {
		ids1 = append(ids1, newApp(t, apps, fmt.Sprintf("https://jobs.example/a%d", i)))
		ids2 = append(ids2, newApp(t, apps, fmt.Sprintf("https://jobs.example/b%d", i)))
	}
	var inflight, peak int32
	mkRunner := func() *gateRunner {
		return &gateRunner{fakeRunner: fakeRunner{st: st, apps: apps, needsYou: map[string]bool{}, startErr: map[string]error{}}, inflight: &inflight, peak: &peak}
	}
	mkQueue := func(owner string, pid int, r *gateRunner) Queue {
		return Queue{Store: st, Slots: slotsFor(st, owner, pid), Max: 1, Runner: r, Tick: time.Millisecond,
			Status: func(ctx context.Context, id string) (string, error) {
				a, err := apps.Get(ctx, profile, id)
				return string(a.Status), err
			}}
	}
	var wg sync.WaitGroup
	for i, ids := range [][]string{ids1, ids2} {
		q := mkQueue(fmt.Sprintf("q%d", i), 500+i, mkRunner())
		wg.Add(1)
		go func(ids []string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(bg, 3*time.Second)
			defer cancel()
			_, _ = q.Run(ctx, ids)
		}(ids)
	}
	wg.Wait()
	if peak > 1 {
		t.Fatalf("%d fills ran at once with max-parked 1 across two queues", peak)
	}
	if peak == 0 {
		t.Fatal("nothing ran")
	}
}

// gateRunner counts fills in flight across runners.
type gateRunner struct {
	fakeRunner
	inflight, peak *int32
}

func (g *gateRunner) Start(ctx context.Context, id string) (string, error) {
	n := atomic.AddInt32(g.inflight, 1)
	for {
		p := atomic.LoadInt32(g.peak)
		if n <= p || atomic.CompareAndSwapInt32(g.peak, p, n) {
			break
		}
	}
	return g.fakeRunner.Start(ctx, id)
}

func (g *gateRunner) Finalize(ctx context.Context, id string) error {
	defer atomic.AddInt32(g.inflight, -1)
	return g.fakeRunner.Finalize(ctx, id)
}

// Many resumes read the same parked tab and claim it at once: exactly one holds it, and the tab is that one's alone.
func TestClaimByManyResumesAtOnceHasOneWinner(t *testing.T) {
	_, st, apps := fixture(t)
	app := parkedApp(t, st, apps, 7)
	read, _ := st.Get(bg, app)
	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	won := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			won[i], errs[i] = st.Claim(bg, app, read.TaskID, fmt.Sprintf("at-%d", i))
		}(i)
	}
	close(start)
	wg.Wait()
	winner := -1
	for i := range won {
		if errs[i] != nil {
			t.Fatalf("claim %d: %v", i, errs[i])
		}
		if won[i] {
			if winner >= 0 {
				t.Fatalf("claims %d and %d both won", winner, i)
			}
			winner = i
		}
	}
	if winner < 0 {
		t.Fatal("no claim won")
	}
	if tab, _ := st.Get(bg, app); tab.State != StateActive || tab.TaskID != fmt.Sprintf("at-%d", winner) {
		t.Fatalf("tab = %+v, want active under claim %d", tab, winner)
	}
}
