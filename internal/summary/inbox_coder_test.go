package summary

import (
	"context"
	"testing"
)

// When the coder items cannot be read the rest of "needs you" is still counted, and the error shows.
func TestHILTotalSurvivesUnreadableCoderItems(t *testing.T) {
	db := testDB(t)
	exec(t, db,
		`INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','A',1,'default')`,
		`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, created_at, profile_id) VALUES ('h1','e1','w1','n','N','pending','2026-09-26 08:00:00','default')`,
		`DROP TABLE coder_session_person_items`,
	)
	h := Build(context.Background(), Options{DB: db.DB, ProfileID: "default", Now: now, Sections: map[string]bool{"hil": true}}).HIL
	if h.Error == "" || h.WorkflowPending != 1 || h.Total != 1 {
		t.Fatalf("hil = %+v", h)
	}
}
