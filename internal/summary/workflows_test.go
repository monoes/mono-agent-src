package summary

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
)

type fakeWorkflows struct{ wfs []workflow.Workflow }

func (f fakeWorkflows) ListWorkflows(context.Context, string) ([]workflow.Workflow, error) {
	return f.wfs, nil
}

func (f fakeWorkflows) GetWorkflow(_ context.Context, id string) (*workflow.Workflow, error) {
	for i := range f.wfs {
		if f.wfs[i].ID == id {
			return &f.wfs[i], nil
		}
	}
	return nil, nil
}

func testDB(t *testing.T) *storage.Database {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, db.DB)
	t.Cleanup(func() { db.Close() })
	return db
}

func exec(t *testing.T, db *storage.Database, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
}

var now = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func TestWorkflowSections(t *testing.T) {
	db := testDB(t)
	exec(t, db,
		`INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','Scraper',1,'default'), ('w2','Digest',0,'default'), ('wx','Other',1,'other')`,
		// the three stored timestamp shapes, on purpose
		`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, created_at, error_message) VALUES
		 ('e1','w1','SUCCESS','manual','default','2026-09-26 11:00:00', NULL),
		 ('e2','w1','FAILED','schedule','default','2026-09-26T10:00:00Z', 'boom'),
		 ('e3','w1','RUNNING','manual','default','2026-09-26 11:59:00.5 +0000 UTC', NULL),
		 ('e4','w2','SUCCESS','manual','default','2026-09-20 09:00:00', NULL),
		 ('e5','wx','QUEUED','manual','other','2026-09-26 11:00:00', NULL)`,
	)
	src := fakeWorkflows{wfs: []workflow.Workflow{
		{ID: "w1", Name: "Scraper", IsActive: true, ProfileID: "default", Nodes: []workflow.WorkflowNode{
			{ID: "n1", Type: "trigger.schedule", Config: map[string]interface{}{"cron": "0 0 14 * * *", "timezone": "UTC"}},
			{ID: "n9", Type: "trigger.schedule", Disabled: true, Config: map[string]interface{}{"cron": "0 0 13 * * *"}},
		}},
		{ID: "w2", Name: "Digest", IsActive: false, ProfileID: "default", Nodes: []workflow.WorkflowNode{
			{ID: "n2", Type: "trigger.schedule", Config: map[string]interface{}{"cron": "0 0 * * * *"}},
		}},
	}}
	s := Build(context.Background(), Options{
		DB: db.DB, ProfileID: "default", Now: now, Workflows: src,
		DaemonRunning: func() bool { return true },
		Sections:      map[string]bool{"workflows": true, "executions": true, "schedules": true},
	})
	if s.Workflows == nil || s.Workflows.Total != 2 || s.Workflows.Active != 1 {
		t.Fatalf("workflows = %+v", s.Workflows)
	}
	e := s.Executions
	if e == nil || e.Error != "" || e.Running != 1 || e.Queued != 0 || e.Last24h.Total != 3 || e.Last24h.Success != 1 || e.Last24h.Failed != 1 {
		t.Fatalf("executions = %+v", e)
	}
	if len(e.Recent) != 4 || e.Recent[0].ID != "e3" || e.Recent[0].WorkflowName != "Scraper" {
		t.Fatalf("recent = %+v", e.Recent)
	}
	if e.Recent[1].ID != "e1" || e.Recent[2].Error != "boom" || e.Recent[2].CreatedAt != "2026-09-26T10:00:00Z" {
		t.Fatalf("recent order/fields = %+v", e.Recent)
	}
	sc := s.Schedules
	if sc == nil || !sc.DaemonRunning || len(sc.Upcoming) != 1 || len(sc.Invalid) != 0 {
		t.Fatalf("schedules = %+v", sc)
	}
	if got := sc.Upcoming[0].NextRun; got != "2026-09-26T14:00:00Z" {
		t.Fatalf("next_run = %s", got)
	}
	if s.HIL != nil {
		t.Fatal("unselected section must be omitted")
	}
}

func TestScheduleTimezoneAndInvalidSpecs(t *testing.T) {
	db := testDB(t)
	src := fakeWorkflows{wfs: []workflow.Workflow{{ID: "w1", Name: "Mixed", IsActive: true, ProfileID: "default", Nodes: []workflow.WorkflowNode{
		{ID: "bad", Type: "trigger.schedule", Config: map[string]interface{}{"cron": "every tuesday"}},
		{ID: "five", Type: "trigger.schedule", Config: map[string]interface{}{"cron": "0 14 * * *"}}, // scheduler needs seconds
		{ID: "berlin", Type: "trigger.schedule", Config: map[string]interface{}{"cron": "0 0 15 * * *", "timezone": "Europe/Berlin"}},
		{ID: "every", Type: "trigger.schedule", Config: map[string]interface{}{"cron": "@every 5m"}},
	}}}}
	s := Build(context.Background(), Options{DB: db.DB, ProfileID: "default", Now: now, Workflows: src,
		DaemonRunning: func() bool { return false }, Sections: map[string]bool{"schedules": true}})
	sc := s.Schedules
	if sc.DaemonRunning || sc.Error != "" || len(sc.Invalid) != 2 {
		t.Fatalf("schedules = %+v", sc)
	}
	if sc.Invalid[0].NodeID != "bad" || sc.Invalid[1].NodeID != "five" {
		t.Fatalf("invalid = %+v", sc.Invalid)
	}
	// 15:00 Berlin (CEST, UTC+2) = 13:00 UTC; @every 5m = 12:05 UTC, soonest first.
	if len(sc.Upcoming) != 2 || sc.Upcoming[0].NodeID != "every" || sc.Upcoming[1].NextRun != "2026-09-26T13:00:00Z" {
		t.Fatalf("upcoming = %+v", sc.Upcoming)
	}
}

func TestNoDatabaseIsASectionError(t *testing.T) {
	s := Build(context.Background(), Options{ProfileID: "default", Now: now, Sections: map[string]bool{"executions": true, "hil": true}})
	if s.Executions.Error == "" || s.HIL.Error == "" {
		t.Fatalf("want section errors, got %+v %+v", s.Executions, s.HIL)
	}
}

// The file store lists every profile's workflows; the summary counts only
// this profile's (and profile-less legacy ones).
func TestWorkflowsAreProfileScoped(t *testing.T) {
	db := testDB(t)
	sched := func(id string) []workflow.WorkflowNode {
		return []workflow.WorkflowNode{{ID: id, Type: "trigger.schedule", Config: map[string]interface{}{"cron": "@every 5m"}}}
	}
	src := fakeWorkflows{wfs: []workflow.Workflow{
		{ID: "mine", Name: "Mine", IsActive: true, ProfileID: "default", Nodes: sched("a")},
		{ID: "legacy", Name: "Legacy", IsActive: true, Nodes: sched("b")},
		{ID: "theirs", Name: "Theirs", IsActive: true, ProfileID: "other", Nodes: sched("c")},
	}}
	s := Build(context.Background(), Options{DB: db.DB, ProfileID: "default", Now: now, Workflows: src,
		Sections: map[string]bool{"workflows": true, "schedules": true}})
	if s.Workflows.Total != 1 || len(s.Schedules.Upcoming) != 1 {
		t.Fatalf("workflows = %+v, upcoming = %+v", s.Workflows, s.Schedules.Upcoming)
	}
	for _, u := range s.Schedules.Upcoming {
		if u.WorkflowID == "theirs" {
			t.Fatal("another profile's schedule leaked in")
		}
		if u.Every != "5m" {
			t.Fatalf("@every row should carry its interval: %+v", u)
		}
	}
}

// With the daemon running, its scheduler's own next fire time wins; a stale
// (past) one, or no daemon, falls back to the computed time.
func TestDaemonSchedulesPreferred(t *testing.T) {
	db := testDB(t)
	src := fakeWorkflows{wfs: []workflow.Workflow{{ID: "w1", Name: "A", IsActive: true, ProfileID: "default", Nodes: []workflow.WorkflowNode{
		{ID: "every", Type: "trigger.schedule", Config: map[string]interface{}{"cron": "@every 1h"}},
		{ID: "daily", Type: "trigger.schedule", Config: map[string]interface{}{"cron": "0 0 14 * * *"}},
	}}}}
	daemon := map[string]time.Time{"w1/every": now.Add(17 * time.Minute), "w1/daily": now.Add(-time.Minute)}
	run := func(running bool) map[string]ScheduleRow {
		s := Build(context.Background(), Options{DB: db.DB, ProfileID: "default", Now: now, Workflows: src,
			DaemonRunning: func() bool { return running }, DaemonSchedules: func() map[string]time.Time { return daemon },
			Sections: map[string]bool{"schedules": true}})
		out := map[string]ScheduleRow{}
		for _, u := range s.Schedules.Upcoming {
			out[u.NodeID] = u
		}
		return out
	}
	up := run(true)
	if up["every"].Source != "daemon" || up["every"].NextRun != "2026-09-26T12:17:00Z" {
		t.Fatalf("every = %+v", up["every"])
	}
	if up["daily"].Source != "computed" || up["daily"].NextRun != "2026-09-26T14:00:00Z" {
		t.Fatalf("a past daemon time must not be used: %+v", up["daily"])
	}
	if up := run(false); up["every"].Source != "computed" {
		t.Fatalf("no daemon: %+v", up["every"])
	}
}

// A workflow with no profile id is an error state: it is owned by no
// profile, so it is skipped everywhere, the default profile included.
func TestOwnWorkflowsSkipsOwnerless(t *testing.T) {
	src := fakeWorkflows{wfs: []workflow.Workflow{
		{ID: "legacy", IsActive: true},
		{ID: "work", IsActive: true, ProfileID: "p-work"},
	}}
	for profile, want := range map[string]string{"default": "", "p-work": "work"} {
		got, err := ownWorkflows(context.Background(), Options{ProfileID: profile, Workflows: src})
		if err != nil {
			t.Fatalf("%s: %v", profile, err)
		}
		if want == "" && len(got) != 0 || want != "" && (len(got) != 1 || got[0].ID != want) {
			t.Errorf("%s: got %+v; want %q", profile, got, want)
		}
	}
}
