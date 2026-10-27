package summary

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/capture"
)

func TestInboxSections(t *testing.T) {
	db := testDB(t)
	exec(t, db,
		`INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','A',1,'default'), ('wx','X',1,'other')`,
		`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, created_at, profile_id) VALUES
		 ('h1','e1','w1','n','N','pending','2026-09-26 08:00:00','default'),
		 ('h2','e1','w1','n','N','approved','2026-09-26 07:00:00','default'),
		 ('h3','e9','wx','n','N','pending','2026-09-26 06:00:00','other')`,
		`INSERT INTO people (id, profile_id, platform, platform_username, full_name, category, created_at) VALUES
		 ('p1','default','LINKEDIN','a','A','pending_approval','2026-09-25 10:00:00'),
		 ('p2','default','LINKEDIN','b','B','',                '2026-09-01 10:00:00'),
		 ('p3','default','LINKEDIN','c','C','',                '2026-09-24 10:00:00.5 +0000 UTC')`,
		`INSERT INTO social_lists (id, list_type, name, profile_id) VALUES ('sl1','people','L','default')`,
		`INSERT INTO person_messages (id, person_id, source, external_id, direction, status, profile_id, created_at) VALUES
		 ('m1','p2','linkedin','x1','outbound','draft','default','2026-09-26 09:00:00'),
		 ('m2','p2','linkedin','x2','inbound','sent','default','2026-09-25 09:00:00'),
		 ('m4','p2','linkedin','x4','outbound','sent','default','2026-09-25 09:30:00'),
		 ('m3','p2','linkedin','x3','inbound','sent','default','2026-08-01 09:00:00')`,
		`INSERT INTO person_links (id, profile_id, person_a, person_b, relation, status) VALUES
		 ('l1','default','p2','p3','same','suggested'), ('l2','default','p1','p3','same','confirmed')`,
		`INSERT INTO applications (id, profile_id, kind, status, created_at, updated_at) VALUES
		 ('a1','default','job','pending','2026-09-25T10:00:00Z','2026-09-25T10:00:00Z'),
		 ('a3','default','job','pending','2026-09-02T10:00:00Z','2026-09-02T10:00:00Z'),
		 ('a2','default','job','applied','2026-09-01T10:00:00Z','2026-09-02T10:00:00Z')`,
		`INSERT INTO application_evaluations (id, application_id, runtime, eligibility_pass, language_pass, location_pass, verdict, created_at)
		 VALUES ('ev1','a3','claude',1,1,1,'fit','2026-09-03T10:00:00Z')`,
		`INSERT INTO vault_documents (id, seq, path, filename, profile_id, created_at, indexed, index_error, capture_dir) VALUES
		 ('d1',1,'/a','a.md','default','2026-09-25 10:00:00',1,NULL,'/cap/running'),
		 ('d2',2,'/b','b.md','default','2026-09-25 10:00:00',0,'too big','/cap/error'),
		 ('d3',3,'/c','c.md','default','2026-09-25 10:00:00',1,NULL,NULL)`,
	)
	caps := []capture.Entry{{CapturedAt: "2026-09-25T10:00:00Z"}, {CapturedAt: "2026-08-01T10:00:00Z"}}
	states := map[string]string{"/cap/running": "running", "/cap/error": "error"}
	s := Build(context.Background(), Options{DB: db.DB, ProfileID: "default", Now: now,
		Captures:     func() ([]capture.Entry, error) { return caps, nil },
		SummaryState: func(dir string) string { return states[dir] },
		Sections:     map[string]bool{"hil": true, "people": true, "activity": true, "applications": true}})

	h := s.HIL
	if h.Error != "" || h.WorkflowPending != 1 || h.PeopleReview != 1 || h.Drafts != 1 || h.LinkSuggestions != 1 || h.Total != 4 {
		t.Fatalf("hil = %+v", h)
	}
	if h.OldestWaitingSince != "2026-09-26T08:00:00Z" {
		t.Fatalf("oldest_waiting_since = %q", h.OldestWaitingSince)
	}
	if p := s.People; p.Error != "" || p.Total != 3 || p.Added7d != 2 || p.Lists != 1 {
		t.Fatalf("people = %+v", p)
	}
	a := s.Activity
	// m2 and m3 are inbound and unread (new rows start unread; migration 050).
	if a.Error != "" || a.Captures7d != 1 || a.CapturesTotal != 2 || a.MessagesIn7d != 1 || a.MessagesOut7d != 1 || a.MessagesUnread != 2 {
		t.Fatalf("activity = %+v", a)
	}
	if d := a.Documents; d.Total != 3 || d.Indexed != 2 || d.IndexErrors != 1 || d.Summarising != 1 || d.SummaryErrors != 1 {
		t.Fatalf("documents = %+v", d)
	}
	ap := s.Applications
	if ap.Error != "" || ap.ByStatus["pending"] != 2 || ap.ByStatus["applied"] != 1 || ap.ByStatus["rejected"] != 0 ||
		ap.Evaluated != 1 || ap.UnevaluatedPending != 1 || ap.Added7d != 1 {
		t.Fatalf("applications = %+v", ap)
	}
}

// HIL counts use the row's own profile (like `hil list`) and leave
// org-triggered rows to `org summary`.
func TestHILScopeAndOrgRows(t *testing.T) {
	db := testDB(t)
	exec(t, db,
		`INSERT INTO workflows (id, name, profile_id) VALUES ('wf','W','default')`,
		`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id) VALUES
		 ('e-org','wf','WAITING','org_tool','default'), ('e-man','wf','WAITING','manual','default')`,
		`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, profile_id) VALUES
		 ('h1','e-man','wf-no-sql-row','n','N','pending','default'),
		 ('h2','e-missing','wf','n','N','pending','default'),
		 ('h3','e-org','wf','n','N','pending','default'),
		 ('h4','e-man','wf','n','N','pending','other')`)
	h := Build(context.Background(), Options{DB: db.DB, ProfileID: "default", Now: now, Sections: map[string]bool{"hil": true}}).HIL
	if h.Error != "" || h.WorkflowPending != 2 {
		t.Fatalf("hil = %+v (want h1 and h2 only)", h)
	}
}

// An item counts while the person has to act on it: its question is open, the person is its answerer
// now (no live master) and its session is not stopped. Per kind, ids only, never text.
func TestHILCountsCoderItemsTheCurrentAnswererIsThePerson(t *testing.T) {
	db := testDB(t)
	exec(t, db,
		`INSERT INTO coder_sessions (profile_id, conversation_id, state, role, parent_conversation_id, created_at, updated_at) VALUES
		 ('default','solo','waiting_user','solo','','t','t'), ('default','orphan','waiting_user','child','','t','t'),
		 ('default','stopped','stopped','solo','','t','t'), ('default','boss','idle','master','','t','t'),
		 ('default','kid','waiting_user','child','boss','t','t'), ('default','dead','waiting_user','child','boss2','t','t')`,
		`INSERT INTO coder_session_questions (id, profile_id, conversation_id, class, question, status, created_at) VALUES
		 (1,'default','solo','risky_action','q1','open','t'), (2,'default','solo','risky_action','q2','answered','t'),
		 (3,'default','orphan','missing_credential','q3','open','t'), (4,'other','solo2','risky_action','q4','open','t'),
		 (5,'default','kid','risky_action','q5','open','t'), (6,'default','stopped','risky_action','q6','open','t'),
		 (7,'default','dead','risky_action','q7','open','t')`,
		`INSERT INTO coder_session_person_items (id, profile_id, conversation_id, kind, ref_id, created_at) VALUES
		 (11,'default','solo','parked_question',1,'t'), (12,'default','solo','parked_question',2,'t'),
		 (13,'default','orphan','parked_question',3,'t'), (14,'other','solo2','parked_question',4,'t'),
		 (15,'default','kid','parked_question',5,'t'), (16,'default','stopped','parked_question',6,'t'),
		 (17,'default','dead','parked_question',7,'t')`,
	)
	h := Build(context.Background(), Options{DB: db.DB, ProfileID: "default", Now: now, Sections: map[string]bool{"hil": true}}).HIL
	// solo (1), the orphaned child (3) and the child of a master that is gone (7) are the person's;
	// the answered one, the other profile's, the live master's child and the stopped session are not.
	if h.Error != "" || h.CoderItems["parked_question"] != 3 || h.Total != 3 || len(h.CoderItemIDs) != 3 || h.CoderItemIDs[0] != 11 {
		t.Fatalf("hil = %+v", h)
	}
}
