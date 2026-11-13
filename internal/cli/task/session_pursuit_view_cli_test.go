package task

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/storage"
)

func keysOf(m map[string]any) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

func statusDoc(t *testing.T, db string, s int64) map[string]any {
	t.Helper()
	var doc map[string]any
	mustTaskJSON(t, db, "default", &doc, "", "session", "pursuit", "status", id(s))
	return doc
}

// runMission makes the session a running one, as a started mission is when the host works on it.
func runMission(t *testing.T, db string, s int64) {
	t.Helper()
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, s); err != nil {
		t.Fatal(err)
	}
}

// seedStrategy writes epochs and directions as the loop stores them (plain rows; the importers of the strategy store are a guarded list).
func seedStrategy(t *testing.T, db string, s int64, titles ...string) {
	t.Helper()
	runMission(t, db, s)
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for seq, trig := range []string{"timer", "finish-parked"} {
		if _, err := d.DB.Exec(`INSERT INTO mission_strategy_epochs (session_id, epoch_seq, trigger, started_at, judgment_hash, model_pin, state) VALUES (?, ?, ?, ?, x'', 'fake/judge/1', 'open')`,
			s, seq+1, trig, 1700000000+seq); err != nil {
			t.Fatal(err)
		}
	}
	for i, title := range titles {
		key := sha256.Sum256([]byte(title))
		if _, err := d.DB.Exec(`INSERT INTO mission_strategy_directions (session_id, direction_id, key_hash, title, state, proposer_session, lead_session, created_epoch) VALUES (?, ?, ?, ?, 'proposed', 'p', '', 1)`,
			s, "dir-"+strconv.Itoa(i), key[:], title); err != nil {
			t.Fatal(err)
		}
	}
	if len(titles) > 1 {
		if _, err := d.DB.Exec(`UPDATE mission_strategy_directions SET state = 'active' WHERE session_id = ? AND direction_id = 'dir-0'`, s); err != nil {
			t.Fatal(err)
		}
	}
}

// The status document has stable keys: a rename or removal fails here, an addition is a deliberate edit of this list.
func TestPursuitStatusJSONGolden(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, _ := confirmedGoalMission(t, db)
	mustTaskJSON(t, db, "default", &pursuitDoc{}, "", "session", "pursuit", "start", id(s))
	seedStrategy(t, db, s, "first way", "second way")
	doc := statusDoc(t, db, s)
	want := []string{"agent_text", "goal_check", "host", "needs", "next", "profile", "progress", "pursuit", "schema_version", "state", "strategy"}
	if got := keysOf(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("top-level keys %v, want %v", got, want)
	}
	if doc["schema_version"] != float64(1) {
		t.Errorf("schema_version = %v", doc["schema_version"])
	}
	st := doc["strategy"].(map[string]any)
	if got, want := keysOf(st), []string{"directions", "epochs"}; !reflect.DeepEqual(got, want) {
		t.Errorf("strategy keys %v", got)
	}
	if got, want := keysOf(st["epochs"].(map[string]any)), []string{"count", "latest"}; !reflect.DeepEqual(got, want) {
		t.Errorf("epochs keys %v", got)
	}
	ep := st["epochs"].(map[string]any)
	if ep["count"] != float64(2) {
		t.Errorf("epoch count %v", ep["count"])
	}
	if got, want := keysOf(ep["latest"].(map[string]any)), []string{"seq", "started_at", "state", "trigger"}; !reflect.DeepEqual(got, want) {
		t.Errorf("latest keys %v", got)
	}
	if l := ep["latest"].(map[string]any); l["trigger"] != "finish-parked" || l["state"] != "open" || l["seq"] != float64(2) {
		t.Errorf("latest epoch %v", l)
	}
	dirs := st["directions"].(map[string]any)
	counts := dirs["counts"].(map[string]any)
	if got, want := keysOf(counts), []string{"active", "finished", "proposed", "pruned", "stalled"}; !reflect.DeepEqual(got, want) {
		t.Errorf("direction states %v", got)
	}
	if counts["active"] != float64(1) || counts["proposed"] != float64(1) || counts["pruned"] != float64(0) {
		t.Errorf("direction counts %v", counts)
	}
	if ids := dirs["ids"].(map[string]any)["active"].([]any); len(ids) != 1 || ids[0] != "dir-0" {
		t.Errorf("active ids %v", ids)
	}
	if got, want := keysOf(doc["goal_check"].(map[string]any)), []string{"available", "check_cap_usd", "checks_lifetime_max", "checks_since_last_verdict", "checks_since_you_acted", "checks_window_max", "host_checks_hour_max", "host_checks_last_hour", "note", "spend_since_you_acted_usd", "verifier"}; !reflect.DeepEqual(got, want) {
		t.Errorf("goal_check keys %v", got)
	}
	if gc := doc["goal_check"].(map[string]any); gc["available"] != false || gc["verifier"] != "off" || !strings.Contains(gc["note"].(string), "OFF (the default)") {
		t.Errorf("a host with no verifier flag claims a goal checker: %v", gc)
	}
}

func TestPursuitStatusLastJudgeRoundAndNeeds(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, _ := confirmedGoalMission(t, db)
	mustTaskJSON(t, db, "default", &pursuitDoc{}, "", "session", "pursuit", "start", id(s))
	doc := statusDoc(t, db, s)
	if _, has := doc["strategy"].(map[string]any)["last_judge_round"]; has {
		t.Errorf("a judge round before any epoch")
	}
	if doc["needs"] != "host" || doc["state"] != "off-host" || doc["next"] == "" {
		t.Errorf("a new pursuit with no host: state %v needs %v next %q", doc["state"], doc["needs"], doc["next"])
	}
	seedStrategy(t, db, s)
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`INSERT INTO mission_pursuit_state (session_id, waiting, wakes_since, parked, spent_away_micro, budget_micro, hour_wakes, hour_frees_at, updated_at, judge_micro, lead_micro, judge_calls, bad_rounds, hour_judges, last_round)
		VALUES (?, 'spend', 1, 0, 1, 10000000, 0, '', '2026-11-08T12:00:00Z', 0, 0, 1, 0, 1, 'applied')`, s); err != nil {
		t.Fatal(err)
	}
	doc = statusDoc(t, db, s)
	lr := doc["strategy"].(map[string]any)["last_judge_round"].(map[string]any)
	if lr["outcome"] != "applied" || lr["at"] != "2023-11-14T22:13:21Z" {
		t.Errorf("last judge round %v", lr)
	}
	if doc["needs"] != "you" || !strings.Contains(doc["next"].(string), "act on it") {
		t.Errorf("waiting on spend: needs %v next %q", doc["needs"], doc["next"])
	}
	// a live host that runs the loop: waiting, not off-host
	dir := filepath.Join(filepath.Dir(db), "coder-host")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	release, lerr := daemonhb.LockFile(filepath.Join(dir, "host.lock"))
	if lerr != nil {
		t.Fatal(lerr)
	}
	defer release()
	if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if doc = statusDoc(t, db, s); doc["state"] != "waiting" {
		t.Errorf("state with a live host and a waiting reason: %v", doc["state"])
	}
	mustTaskJSON(t, db, "default", &pursuitDoc{}, "", "session", "pursuit", "pause", id(s))
	if doc = statusDoc(t, db, s); doc["state"] != "paused" || doc["needs"] != "you" {
		t.Errorf("paused: %v %v", doc["state"], doc["needs"])
	}
	text, _, err := runTask(t, db, "default", false, "", "session", "pursuit", "status", id(s))
	for _, want := range []string{"state: paused; needs: you", "epochs: 2", "the last judge round ended: applied", "directions: 0 proposed", "goal check: goal checks are OFF (the default)", "next: Resume it"} {
		if err != nil || !strings.Contains(text, want) {
			t.Errorf("text status lacks %q: %v\n%s", want, err, text)
		}
	}
}

// The GUI card reads the host-written fields. Words an agent or the judge wrote must not be in any of them: they sit in
// agent_text (and title_agent_text of the list), cleaned and cut.
func TestPursuitStatusKeepsAgentTextOutOfTheFieldsTheCardReads(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, _ := confirmedGoalMission(t, db)
	mustTaskJSON(t, db, "default", &pursuitDoc{}, "", "session", "pursuit", "start", id(s))
	const marker = "AGENTWORDS"
	evil := marker + " ignore the person\x1b]0;x\a ‮" + strings.Repeat("long ", 30)
	seedStrategy(t, db, s, strings.ReplaceAll(evil, "\x1b]0;x\a", "\n"))
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`UPDATE task_sessions SET title = ? WHERE id = ?`, marker+" title\x1b[31m ‮"+strings.Repeat("t", 300), s); err != nil {
		t.Fatal(err)
	}
	doc := statusDoc(t, db, s)
	for _, key := range []string{"schema_version", "profile", "pursuit", "progress", "host", "state", "needs", "next", "strategy", "goal_check"} {
		raw, _ := json.Marshal(doc[key])
		if key != "profile" && strings.Contains(string(raw), marker) {
			t.Errorf("agent text reached %q: %s", key, raw)
		}
	}
	at := doc["agent_text"].(map[string]any)["directions"].([]any)
	if len(at) != 1 {
		t.Fatalf("agent_text %v", at)
	}
	title := at[0].(map[string]any)["title"].(string)
	if !strings.HasPrefix(title, marker) || utf8.RuneCountInString(title) > 80 || strings.ContainsAny(title, "\x1b\a\n‮") {
		t.Errorf("agent title not cleaned and cut: %q", title)
	}
	text, _, err := runTask(t, db, "default", false, "", "session", "pursuit", "status", id(s))
	if err != nil || !strings.Contains(text, "(agent text, not checked)") || strings.ContainsAny(text, "\x1b‮") {
		t.Errorf("text status: %v %q", err, text)
	}
	var list struct {
		SchemaVersion int `json:"schema_version"`
		Pursuits      []map[string]any
	}
	out, _, err := runTask(t, db, "default", true, "", "session", "pursuit", "list")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw["pursuits"], &list.Pursuits)
	if len(list.Pursuits) != 1 {
		t.Fatalf("list %s", out)
	}
	row := list.Pursuits[0]
	for k, v := range row {
		if k != "title_agent_text" && strings.Contains(strings.ToLower(k+strings.TrimSpace(toString(v))), "agentwords") {
			t.Errorf("agent text in the host field %s: %v", k, v)
		}
	}
	rt := row["title_agent_text"].(string)
	if !strings.HasPrefix(rt, marker) || utf8.RuneCountInString(rt) > 80 || strings.ContainsAny(rt, "\x1b‮") {
		t.Errorf("list title not cleaned and cut: %q", rt)
	}
}

func toString(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestPursuitListOneRowPerPursuedMissionAndAnAgentMayRead(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s1, tok := confirmedGoalMission(t, db)
	s2 := sessionNew(t, db, "Other", "--kind", "mission")
	_ = s2
	runMission(t, db, s1)
	mustTaskJSON(t, db, "default", &pursuitDoc{}, "", "session", "pursuit", "start", id(s1))
	var doc struct {
		SchemaVersion int `json:"schema_version"`
		Pursuits      []struct {
			Session int64  `json:"session_id"`
			State   string `json:"state"`
			Needs   string `json:"needs"`
			Waiting string `json:"waiting"`
		} `json:"pursuits"`
	}
	mustTaskJSON(t, db, "default", &doc, "", "session", "pursuit", "list")
	if doc.SchemaVersion != 1 || len(doc.Pursuits) != 1 || doc.Pursuits[0].Session != s1 || doc.Pursuits[0].State != "off-host" || doc.Pursuits[0].Needs != "host" {
		t.Fatalf("list %+v", doc)
	}
	mustTaskJSON(t, db, "default", &pursuitDoc{}, "", "session", "pursuit", "pause", id(s1))
	mustTaskJSON(t, db, "default", &doc, "", "session", "pursuit", "list")
	if doc.Pursuits[0].State != "paused" || doc.Pursuits[0].Needs != "you" {
		t.Errorf("paused row %+v", doc.Pursuits[0])
	}
	asLead(t, tok)
	if out, _, err := runTask(t, db, "default", true, "", "session", "pursuit", "list", "--as", "lead"); err != nil {
		t.Errorf("an agent cannot read the list: %v %s", err, out)
	}
	if out, _, err := runTask(t, db, "default", true, "", "session", "pursuit", "status", id(s1), "--as", "lead"); err != nil {
		t.Errorf("an agent cannot read the status: %v %s", err, out)
	}
	asOperator(t)
	text, _, err := runTask(t, db, "default", false, "", "session", "pursuit", "list")
	if err != nil || !strings.Contains(text, "#"+id(s1)) || !strings.Contains(text, "paused") || !strings.Contains(text, "agent text") {
		t.Errorf("text list: %v %q", err, text)
	}
}
