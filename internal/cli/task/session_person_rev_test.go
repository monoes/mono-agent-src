package task

import (
	"strings"
	"testing"
)

// The inbox view carries what the person read: the session revision and a digest of the plan.
type revView struct {
	Items []struct {
		SessionID int64  `json:"sessionId"`
		Rev       int64  `json:"rev"`
		Digest    string `json:"digest"`
	} `json:"items"`
}

func inboxRev(t *testing.T, db string) (rev int64, digest string) {
	t.Helper()
	var v revView
	mustTaskJSON(t, db, "default", &v, "", "session", "person", "inbox")
	if len(v.Items) != 1 || v.Items[0].Rev == 0 || v.Items[0].Digest == "" {
		t.Fatalf("the inbox must carry rev and digest: %+v", v.Items)
	}
	return v.Items[0].Rev, v.Items[0].Digest
}

func TestPersonApproveRefusesWithoutRev(t *testing.T) {
	t.Parallel()
	db, _, big := giantWithItem(t)
	doc := failedTaskJSON(t, db, "default", 3, "session", "person", "approve", id(big))
	if msg, _ := doc["error"].(string); !strings.Contains(msg, "--rev") {
		t.Fatalf("approve without --rev must say why: %v", doc)
	}
	if st := sessionState(t, db, big); st.Session.State != "draft" {
		t.Fatalf("approved without a rev: %+v", st.Session)
	}
}

// A lead adds a task after the person read the plan: the approval of the old revision is refused.
func TestPersonApproveRefusesAPlanThatMovedSinceItWasRead(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(t *testing.T, db string, big, task int64){
		"a task added": func(t *testing.T, db string, big, _ int64) { sessionAdd(t, db, big, "swapped in") },
		"a comment": func(t *testing.T, db string, big, task int64) {
			mustTaskJSON(t, db, "default", &struct{}{}, "", "comment", id(task), "also run the other thing")
		},
	} {
		t.Run(name, func(t *testing.T) {
			db, _, big := giantWithItem(t)
			extra := sessionAdd(t, db, big, "extra")
			rev, digest := inboxRev(t, db)
			change(t, db, big, extra)
			doc := failedTaskJSON(t, db, "default", 3, "session", "person", "approve", id(big), "--rev", id(rev), "--digest", digest)
			if msg, _ := doc["error"].(string); !strings.Contains(msg, "changed") || !strings.Contains(msg, "inbox") {
				t.Fatalf("the refusal must tell the person to re-read: %v", doc)
			}
			if st := sessionState(t, db, big); st.Session.State != "draft" {
				t.Fatalf("approved a plan that moved: %+v", st.Session)
			}
		})
	}
}

func TestPersonApproveWithTheRevItShowedWorks(t *testing.T) {
	t.Parallel()
	db, _, big := giantWithItem(t)
	rev, digest := inboxRev(t, db)
	var out map[string]map[string]any
	mustTaskJSON(t, db, "default", &out, "", "session", "person", "approve", id(big), "--rev", id(rev), "--digest", digest)
	if se := out["session"]; se["state"] != "running" || se["approval_basis"] != "person" {
		t.Fatalf("session = %+v", se)
	}
}

// The operator's own `session approve` needs no rev, and records the operator even when an item is open:
// only `person approve` records the person.
func TestOperatorSessionApproveNeedsNoRevAndRecordsTheOperator(t *testing.T) {
	t.Parallel()
	db, _, big := giantWithItem(t)
	var out map[string]map[string]any
	mustTaskJSON(t, db, "default", &out, "", "session", "approve", id(big))
	if se := out["session"]; se["state"] != "running" || se["approval_basis"] != "operator" {
		t.Fatalf("session = %+v", se)
	}
}

// deny and ask need no rev.
func TestPersonDenyAndAskNeedNoRev(t *testing.T) {
	t.Parallel()
	db, _, big := giantWithItem(t)
	mustTaskJSON(t, db, "default", &struct{}{}, "", "session", "person", "ask", id(big), "--text", "why")
	mustTaskJSON(t, db, "default", &struct{}{}, "", "session", "person", "deny", id(big), "--note", "no")
}
