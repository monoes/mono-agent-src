package task

import (
	"reflect"
	"strings"
	"testing"
)

// withChecker publishes a host whose loop and goal checker are on (the world's goal has command checks).
func withChecker(w beginWorld) {
	w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":2,"verifierPin":"claude/sonnet@4","verifierOn":true,"confinement":"bwrap"}}`)
}

func readinessDocOf(t *testing.T, w beginWorld) map[string]any {
	t.Helper()
	var doc map[string]any
	mustTaskJSON(t, w.db, "default", &doc, "", "session", "pursuit", "readiness", id(w.s))
	return doc
}

func readinessItems(doc map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, raw := range doc["items"].([]any) {
		it := raw.(map[string]any)
		out[it["id"].(string)] = it
	}
	return out
}

// The document has stable keys: a rename or removal fails here, an addition is a deliberate edit of this list.
func TestPursuitReadinessJSONGoldenAndWritesNothing(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	withChecker(w)
	before := w.state(t)
	doc := readinessDocOf(t, w)
	want := []string{"blockers", "items", "profile", "pursuit", "ready", "schema_version", "session_id"}
	if got := keysOf(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	if doc["schema_version"] != float64(1) || doc["ready"] != true || doc["session_id"] != float64(w.s) {
		t.Errorf("doc = %v", doc)
	}
	var ids []string
	for _, raw := range doc["items"].([]any) {
		it := raw.(map[string]any)
		if got, want := keysOf(it), []string{"detail", "id", "status"}; it["status"] == "ok" && !reflect.DeepEqual(got, want) && it["id"] != "away_limits" {
			t.Errorf("item keys %v", got)
		}
		ids = append(ids, it["id"].(string))
	}
	if want := []string{"host_loop", "judge_pin", "goal", "checker", "mission_budget", "lead_cap", "chat", "consent", "away_limits"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("items %v, want %v", ids, want)
	}
	items := readinessItems(doc)
	for _, id := range ids {
		if want := map[bool]string{true: "missing", false: "ok"}[id == "consent"]; items[id]["status"] != want {
			t.Errorf("%s = %v, want %s", id, items[id], want)
		}
	}
	if doc["pursuit"].(map[string]any)["started"] != false {
		t.Errorf("pursuit block %v", doc["pursuit"])
	}
	if after := w.state(t); after != before {
		t.Errorf("readiness wrote: %v -> %v", before, after)
	}
	asLead(t, w.tok) // an agent may read it
	var agent map[string]any
	mustTaskJSON(t, w.db, "default", &agent, "", "session", "pursuit", "readiness", id(w.s), "--as", "lead")
	if agent["ready"] != true {
		t.Errorf("an agent's readiness: %v", agent)
	}
}

// Its blockers are the ones preview refuses for, from the same function: the two never disagree.
func TestPursuitReadinessAgreesWithPreviewOnTheBlockers(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	w.exec(t, `UPDATE task_sessions SET budget_usd = 0 WHERE id = ?`, w.s)
	w.exec(t, `UPDATE coder_sessions SET budget_usd = 0 WHERE conversation_id = 'chat1'`)
	w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/opus","wakeCapUsd":2}}`)
	doc, p := readinessDocOf(t, w), w.preview(t)
	var viaPreview []string
	for _, b := range p.Blockers {
		viaPreview = append(viaPreview, b.Code)
	}
	var viaReadiness []string
	for _, b := range doc["blockers"].([]any) {
		viaReadiness = append(viaReadiness, b.(string))
	}
	if !reflect.DeepEqual(viaPreview, viaReadiness) || len(viaPreview) != 3 {
		t.Errorf("preview blockers %v, readiness blockers %v", viaPreview, viaReadiness)
	}
	items := readinessItems(doc)
	for _, id := range []string{"mission_budget", "lead_cap", "judge_pin"} {
		if items[id]["status"] != "missing" || items[id]["fix"] == "" {
			t.Errorf("%s = %v", id, items[id])
		}
	}
	if doc["ready"] != false {
		t.Error("ready with blockers")
	}
}

// A mission with no goal and no host still gets an answer (exit 0), not an error: that is the case the checklist is for.
func TestPursuitReadinessOfAMissionWithNoGoalAndNoHost(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, _ := goalMission(t, db, "mission")
	out, _, err := runTask(t, db, "default", false, "", "session", "pursuit", "readiness", id(s))
	if err != nil {
		t.Fatalf("readiness: %v\n%s", err, out)
	}
	for _, want := range []string{"NOT ready", "[missing] host_loop", "[missing] goal", "mission_goal_propose", "MONOAGENT_PURSUIT_JUDGE", "[missing] mission_budget", "[missing] lead_cap", "[missing] consent", "fix:"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain readiness lacks %q:\n%s", want, out)
		}
	}
}

func TestPursuitReadinessAfterBeginCarriesTheWaitingState(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	withChecker(w)
	mustTaskJSON(t, w.db, "default", &map[string]any{}, "", w.beginArgs(w.preview(t))...)
	doc := readinessDocOf(t, w)
	pu := doc["pursuit"].(map[string]any)
	if pu["started"] != true || pu["state"] == "" || pu["needs"] == "" || pu["next"] == "" {
		t.Errorf("pursuit block %v", pu)
	}
	if readinessItems(doc)["consent"]["status"] != "ok" || doc["ready"] != true {
		t.Errorf("after begin: %v", doc)
	}
}

func TestPursuitReadinessRefusesAnythingButAMission(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := sessionNew(t, db, "Plain")
	failedTaskJSON(t, db, "default", 3, "session", "pursuit", "readiness", id(s))
}

func TestPursuitStatusOfAMissionWithNoPursuitSaysWhatToDo(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, _ := confirmedGoalMission(t, db)
	doc := failedTaskJSON(t, db, "default", 2, "session", "pursuit", "status", id(s))
	msg, _ := doc["error"].(string)
	if msg == "" {
		msg, _ = doc["message"].(string)
	}
	if !strings.Contains(msg, "has no pursuit yet") || !strings.Contains(msg, "pursuit readiness") || strings.Contains(msg, "task not found") {
		t.Errorf("status of a mission with no pursuit: %v", doc)
	}
	_, stderr, err := runTask(t, db, "default", false, "", "session", "pursuit", "status", id(s))
	if err == nil || !strings.Contains(err.Error()+stderr, "pursuit readiness") || strings.Contains(err.Error()+stderr, "task not found") {
		t.Errorf("plain status error: %v %q", err, stderr)
	}
}
