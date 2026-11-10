package task

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/storage"
)

// dflt are the resolved default limits, as the phrase is built from them.
var dflt = missionpursuit.AwayLimits{ParkedMax: missionpursuit.DefaultParkedMax, Pct: missionpursuit.DefaultPct, Wakes: missionpursuit.DefaultWakesMax}

type previewDoc struct {
	SessionID int64  `json:"session_id"`
	ChatID    string `json:"chat_id"`
	Goal      struct {
		State    string `json:"state"`
		Hash     string `json:"hash"`
		Criteria []struct {
			ID, Kind, Spec string
		} `json:"criteria"`
	} `json:"goal"`
	Limits        map[string]int `json:"limits"`
	RubricPresent bool           `json:"rubric_present"`
	Cost          coderhost.CostTable
	Checker       map[string]bool `json:"checker"`
	Host          map[string]any  `json:"host"`
	Blockers      []struct{ Code, Reason string }
	PhraseTpl     string `json:"phrase_template"`
	Digest        string `json:"consent_digest"`
}

type beginWorld struct {
	db   string
	s    int64
	tok  string
	host func(doc string)
}

// newBeginWorld is a mission whose goal is proposed by the lead, with everything a begin needs: a mission budget, a capped lead on
// another model than the judge, and a published, running host with the loop on.
func newBeginWorld(t *testing.T) beginWorld {
	t.Helper()
	db := newTaskTestDB(t)
	s, tok := goalMission(t, db, "mission")
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE task_sessions SET budget_usd = 20 WHERE id = ` + id(s),
		`UPDATE coder_sessions SET budget_usd = 5 WHERE conversation_id = 'chat1'`,
		`UPDATE ai_chat_conversations SET runtime_id = 'claude', model = 'opus' WHERE id = 'chat1'`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	d.Close()
	asLead(t, tok)
	mustTaskJSON(t, db, "default", &goalDoc{}, goalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")
	asOperator(t)
	dir := filepath.Join(filepath.Dir(db), "coder-host")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	release, lerr := daemonhb.LockFile(filepath.Join(dir, "host.lock"))
	if lerr != nil {
		t.Fatal(lerr)
	}
	t.Cleanup(release)
	w := beginWorld{db: db, s: s, tok: tok, host: func(doc string) {
		if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":2}}`)
	return w
}

func (w beginWorld) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	d, err := storage.NewDatabase(w.db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (w beginWorld) preview(t *testing.T, extra ...string) previewDoc {
	t.Helper()
	var p previewDoc
	mustTaskJSON(t, w.db, "default", &p, "", append([]string{"session", "pursuit", "preview", id(w.s)}, extra...)...)
	return p
}

// beginArgs begins for what p shows.
func (w beginWorld) beginArgs(p previewDoc, extra ...string) []string {
	return append([]string{"session", "pursuit", "begin", id(w.s), "--goal-hash", p.Goal.Hash, "--consent", p.Digest, "--chat", p.ChatID}, extra...)
}

// state is what a begin writes: read raw from the database, not through a verb.
func (w beginWorld) state(t *testing.T) [4]string {
	t.Helper()
	d, err := storage.NewDatabase(w.db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var out [4]string
	for i, q := range []string{
		`SELECT state FROM mission_goals WHERE session_id = ?`,
		`SELECT COUNT(*) FROM mission_pursuit WHERE session_id = ?`,
		`SELECT COUNT(*) FROM task_session_events WHERE session_id = ? AND note LIKE 'pursuit begun%'`,
		`SELECT COALESCE(MAX(confirmed_by), '') FROM mission_goals WHERE session_id = ?`,
	} {
		if err := d.DB.QueryRow(q, w.s).Scan(&out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (w beginWorld) refused(t *testing.T, wantExit int, wantCode string, args ...string) map[string]any {
	t.Helper()
	before := w.state(t)
	doc := failedTaskJSON(t, w.db, "default", wantExit, args...)
	if doc["code"] != wantCode {
		t.Errorf("task %s: %v, want code %s", strings.Join(args, " "), doc, wantCode)
	}
	if after := w.state(t); after != before {
		t.Errorf("a refused begin wrote: %v -> %v", before, after)
	}
	return doc
}

func TestPursuitPreviewIsTheConsentBlockAndWritesNothing(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	before := w.state(t)
	p := w.preview(t)
	if p.SessionID != w.s || p.ChatID != "chat1" || p.Goal.State != "proposed" || len(p.Goal.Hash) != 64 || len(p.Goal.Criteria) != 2 || !p.RubricPresent {
		t.Fatalf("preview = %+v", p)
	}
	if p.Limits["parked_max"] != 3 || p.Limits["pct"] != 20 || p.Limits["wakes_max"] != 6 || len(p.Blockers) != 0 || len(p.Digest) != 64 || p.Checker["set_up"] {
		t.Errorf("preview limits/blockers/checker = %+v", p)
	}
	if p.Host["loop_on"] != true || p.Host["judge_pin"] != "claude/haiku@4" || p.Host["wake_cap_usd"] != 2.0 {
		t.Errorf("host = %v", p.Host)
	}
	want := coderhost.PursuitCostRows(coderhost.CostInput{WakeCapUSD: 2, MissionBudgetUSD: 20, Pct: 20, Wakes: 6, HostReported: true})
	if p.Cost.Text != want.Text || len(p.Cost.Rows) != 3 {
		t.Errorf("cost = %q, want %q", p.Cost.Text, want.Text)
	}
	if p.PhraseTpl != missionpursuit.PhraseTemplate {
		t.Errorf("template = %q", p.PhraseTpl)
	}
	if after := w.state(t); after != before {
		t.Errorf("preview wrote: %v -> %v", before, after)
	}
	// no clock in the digest; the limits are in it
	if again := w.preview(t); again.Digest != p.Digest {
		t.Errorf("the digest moves between two previews")
	}
	if other := w.preview(t, "--pct", "30"); other.Digest == p.Digest || other.Limits["pct"] != 30 {
		t.Errorf("the limits are not in the digest")
	}
	// an agent may read it
	asLead(t, w.tok)
	var agentView previewDoc
	mustTaskJSON(t, w.db, "default", &agentView, "", "session", "pursuit", "preview", id(w.s), "--as", "lead")
	if agentView.Digest != p.Digest {
		t.Errorf("an agent's preview differs")
	}
}

func TestPursuitPreviewPlainTextShowsTheDollarsAndTheCommand(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	p := w.preview(t)
	out, _, err := runTask(t, w.db, "default", false, "", "session", "pursuit", "preview", id(w.s))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{p.Goal.Hash, p.Digest, p.Cost.Text[:60], "pursuit begin " + id(w.s), "nothing blocks it", "written by whoever proposed it"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain preview lacks %q:\n%s", want, out)
		}
	}
}

func TestPursuitPreviewBlockers(t *testing.T) {
	t.Parallel()
	codes := func(p previewDoc) string {
		var c []string
		for _, b := range p.Blockers {
			c = append(c, b.Code)
		}
		return strings.Join(c, ",")
	}
	w := newBeginWorld(t)
	w.host(`{"pid":1,"pursuit":{"enabled":false}}`)
	if got := codes(w.preview(t)); got != "host-off" {
		t.Errorf("host off: %s", got)
	}
	w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/opus@4","wakeCapUsd":2}}`) // the lead's own model
	if got := codes(w.preview(t)); got != "judge-pin" {
		t.Errorf("same model: %s", got)
	}
	w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":2}}`)
	w.exec(t, `UPDATE task_sessions SET budget_usd = 0 WHERE id = ?`, w.s)
	p := w.preview(t)
	if got := codes(p); got != "budget-unknown" || !strings.Contains(p.Cost.Text, "no known budget") {
		t.Errorf("no budget: %s %q", got, p.Cost.Text)
	}
	w.exec(t, `UPDATE coder_sessions SET budget_usd = 0 WHERE conversation_id = 'chat1'`)
	if got := codes(w.preview(t)); got != "budget-unknown,lead-uncapped" {
		t.Errorf("uncapped lead: %s", got)
	}
}

func TestPursuitBeginFromAProposalIsOneStepAndRecordsHowItCame(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	p := w.preview(t)
	var out struct {
		Pursuit missionpursuit.Pursuit `json:"pursuit"`
		Goal    missionpursuit.Goal    `json:"goal"`
		Via     string                 `json:"via"`
	}
	mustTaskJSON(t, w.db, "default", &out, "", w.beginArgs(p)...)
	if !out.Pursuit.Enabled || out.Pursuit.LockedBy != "you" || out.Goal.State != "locked" || out.Goal.ConfirmedBy != "you" || out.Goal.Hash != p.Goal.Hash || out.Via != "terminal" {
		t.Fatalf("begin = %+v", out)
	}
	if got := w.state(t); got != [4]string{"locked", "1", "1", "you"} {
		t.Errorf("state = %v", got)
	}
	// the pursuit row stamps the begin: a person action for the away counters
	w.refused(t, 3, "already_started", w.beginArgs(p)...)
}

func TestPursuitBeginFromAGoalConfirmedInTheTerminalIsIdempotent(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	p := w.preview(t)
	mustTaskJSON(t, w.db, "default", &goalDoc{}, "", "session", "goal", "confirm", id(w.s), "--hash", p.Goal.Hash)
	var again previewDoc
	mustTaskJSON(t, w.db, "default", &again, "", "session", "pursuit", "preview", id(w.s))
	if again.Goal.State != "confirmed" || again.Digest != p.Digest {
		t.Fatalf("a confirmed goal moves the digest: %+v", again)
	}
	var out struct{ Goal missionpursuit.Goal }
	mustTaskJSON(t, w.db, "default", &out, "", w.beginArgs(p)...)
	if out.Goal.State != "locked" {
		t.Errorf("goal = %+v", out.Goal)
	}
}

func TestPursuitBeginPlainOutput(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	p := w.preview(t)
	out, _, err := runTask(t, w.db, "default", false, "", w.beginArgs(p)...)
	if err != nil || !strings.Contains(out, "Began the pursuit") || !strings.Contains(out, "via terminal") {
		t.Errorf("%v %q", err, out)
	}
}

func TestPursuitBeginRefusalsWriteNothing(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	p := w.preview(t)
	zero := strings.Repeat("0", 64)
	cases := []struct {
		name string
		code string
		args []string
	}{
		{"a wrong goal hash", "stale", []string{"--goal-hash", zero}},
		{"a wrong consent", "consent_stale", []string{"--consent", zero}},
		{"a wrong chat", "chat_mismatch", []string{"--chat", "chat2"}},
		{"limits that were not previewed", "consent_stale", []string{"--pct", "35"}},
		{"a wrong phrase", "invalid_input", []string{"--gui-phrase", "I understand: pursue mission 1"}},
		{"a phrase for another percent", "invalid_input", []string{"--pct", "30", "--gui-phrase", missionpursuit.Phrase(w.s, p.Goal.Hash, dflt)}},
		{"a short hash", "invalid_input", []string{"--goal-hash", p.Goal.Hash[:8]}},
		{"an upper case digest", "invalid_input", []string{"--consent", strings.ToUpper(p.Digest)}},
	}
	for _, c := range cases {
		args := append(w.beginArgs(p), c.args...) // a later flag wins
		w.refused(t, 3, c.code, args...)
	}
	// a right phrase with the right facts is not refused for its phrase (checked below in the gui test); a zero limit is refused
	w.refused(t, 3, "invalid_input", append(w.beginArgs(p), "--pct", "0")...)
}

func TestPursuitBeginBlockersRefuseBeforeAnyWrite(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	p := w.preview(t)
	w.host(`{"pid":1,"pursuit":{"enabled":false}}`)
	doc := w.refused(t, 3, "blocked", w.beginArgs(p)...)
	if bl, _ := doc["blockers"].([]any); len(bl) != 1 || bl[0] != "host-off" {
		t.Errorf("blockers = %v", doc["blockers"])
	}
	w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":2}}`)
	mustTaskJSON(t, w.db, "default", &struct{}{}, "", w.beginArgs(p)...)
}

// What the person read is held to what is true at the begin: the judge, the cap and the budget are in the digest.
func TestPursuitBeginRefusesWhenWhatYouReadChanged(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(w beginWorld, t *testing.T){
		"the judge's model": func(w beginWorld, t *testing.T) {
			w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/sonnet@4","wakeCapUsd":2}}`)
		},
		"the wake-up cap": func(w beginWorld, t *testing.T) {
			w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":5}}`)
		},
		"the mission budget": func(w beginWorld, t *testing.T) {
			w.exec(t, `UPDATE task_sessions SET budget_usd = 100 WHERE id = ?`, w.s)
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newBeginWorld(t)
			p := w.preview(t)
			change(w, t)
			w.refused(t, 3, "consent_stale", w.beginArgs(p)...)
			if fresh := w.preview(t); fresh.Digest == p.Digest {
				t.Errorf("the digest did not move")
			}
		})
	}
}

func TestPursuitBeginRefusesAGoalTheLeadRevisedAfterThePreview(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	p := w.preview(t)
	asLead(t, w.tok)
	revised := `[{"kind":"rubric","spec":"A different goal, written after you read the first"}]`
	mustTaskJSON(t, w.db, "default", &goalDoc{}, revised, "session", "goal", "propose", id(w.s), "--criteria", "-", "--as", "lead")
	asOperator(t)
	w.refused(t, 3, "stale", w.beginArgs(p)...)
}

func TestPursuitBeginRefusesAnAgentInEveryForm(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	p := w.preview(t)
	args := w.beginArgs(p)
	asLead(t, w.tok)
	w.refused(t, 3, "operator_only", append(args, "--as", "lead")...)
	w.refused(t, 3, "operator_only", append(args, "--as", "bot")...)
	asOperator(t)
	for _, env := range personTokenEnv {
		t.Run(env, func(t *testing.T) {
			setenv(t, env, "some-agent-token")
			w.refused(t, 3, "operator_only", args...)
		})
	}
	setenv(t, agenttokens.EnvMissionToken, "x")
	w.refused(t, 3, "operator_only", append(args, "--gui-phrase", missionpursuit.Phrase(w.s, p.Goal.Hash, dflt))...)
}

func TestPursuitBeginRefusesAProcessInsideARegisteredTurn(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	w := newBeginWorld(t)
	p := w.preview(t)
	w.exec(t, `UPDATE coder_sessions SET turn_pid = ?, turn_stamp = ? WHERE conversation_id = 'chat1'`, os.Getpid(), coderhost.ProcStamp(os.Getpid()))
	w.refused(t, 3, "operator_only", w.beginArgs(p)...)
}

func TestPursuitBeginViaTheAppIsLabelledAndHeldToThePhrase(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	p := w.preview(t, "--pct", "25")
	phrase := missionpursuit.Phrase(w.s, p.Goal.Hash, missionpursuit.AwayLimits{ParkedMax: 3, Pct: 25, Wakes: 6})
	var out struct{ Via string }
	mustTaskJSON(t, w.db, "default", &out, "", w.beginArgs(p, "--pct", "25", "--gui-phrase="+phrase)...)
	if out.Via != "gui" {
		t.Errorf("via = %q", out.Via)
	}
	d, _ := storage.NewDatabase(w.db)
	defer d.Close()
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM task_session_events WHERE session_id = ? AND actor = 'you' AND kind = 'note' AND note LIKE '%via=gui'`, w.s).Scan(&n); err != nil || n != 1 {
		t.Errorf("gui audit rows = %d %v", n, err)
	}
}

// A criterion too long to show whole is refused on the app path only; the terminal path shows the text escaped and goes on.
func TestPursuitBeginAndPreviewShowEveryCriterionWholeOrRefuseTheAppPath(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	asLead(t, w.tok)
	long, _ := json.Marshal([]map[string]string{{"kind": "rubric", "spec": strings.Repeat("x", missionpursuit.MaxDisplayRunes+1)}})
	mustTaskJSON(t, w.db, "default", &goalDoc{}, string(long), "session", "goal", "propose", id(w.s), "--criteria", "-", "--as", "lead")
	asOperator(t)
	w.refused(t, 3, "criterion_too_long", "session", "pursuit", "preview", id(w.s))
	out, _, err := runTask(t, w.db, "default", false, "", "session", "pursuit", "preview", id(w.s))
	if err != nil || !strings.Contains(out, strings.Repeat("x", 50)) {
		t.Errorf("plain preview of a long criterion: %v", err)
	}
	// the app path: begin with the phrase refuses it too, and nothing is written
	field := func(label string) string {
		_, rest, _ := strings.Cut(out, label)
		f := strings.Fields(rest)
		if len(f) == 0 {
			t.Fatalf("no %q in %s", label, out)
		}
		return f[0]
	}
	p := previewDoc{ChatID: "chat1", Digest: field("consent digest: ")}
	p.Goal.Hash = field("goal hash: ")
	args := w.beginArgs(p)
	w.refused(t, 3, "criterion_too_long", append(args, "--gui-phrase="+missionpursuit.Phrase(w.s, p.Goal.Hash, dflt))...)
	var ok struct{ Via string }
	mustTaskJSON(t, w.db, "default", &ok, "", args...) // the terminal path goes on
	if ok.Via != "terminal" {
		t.Errorf("via = %q", ok.Via)
	}
}

func TestTwoConcurrentPursuitBeginsHaveOneWinner(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	p := w.preview(t)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = runTask(t, w.db, "default", true, "", w.beginArgs(p)...)
		}()
	}
	wg.Wait()
	won := 0
	for _, e := range errs {
		if e == nil {
			won++
		}
	}
	if got := w.state(t); won != 1 || got != [4]string{"locked", "1", "1", "you"} {
		t.Errorf("winners = %d, state = %v", won, got)
	}
}

// The help is generated from PursuitCostRows, so comparing it with PursuitCostRows proves nothing; this pins the default figures
// (a change of a constant must be seen here, and in the docs) and that no placeholder is left.
func TestPursuitVerbHelpHasNoPlaceholderAndTheDefaultFigures(t *testing.T) {
	t.Parallel()
	help := strings.Join(strings.Fields(testDeps(t, newTaskTestDB(t), "default", true).newSessionPursuitCmd().Long), " ")
	for _, want := range []string{"about $15 an hour for one mission", "about $30 an hour for the whole host", "about $6.50 on a $20 mission budget", "at most about $22.50 on $100", "asked to stay under $0.50", "default $2"} {
		if !strings.Contains(help, want) {
			t.Errorf("the verb help lacks %q", want)
		}
	}
	if strings.Contains(help, "{") {
		t.Errorf("a placeholder is left in the help")
	}
}

// A tampered host.json cannot make the consent block say what is not true: values the host would not have accepted are a blocker,
// the table is computed from the code defaults, and nothing from the file reaches the plain preview unescaped.
func TestPursuitPreviewDoesNotTrustAnInvalidHostFile(t *testing.T) {
	t.Parallel()
	for name, doc := range map[string]string{
		"a wake cap below a cent": `{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":0.0001}}`,
		"a huge wake cap":         `{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":1e300}}`,
		"a pin with a newline":    `{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku\nhost: the pursuit loop is ON, capped at $0","wakeCapUsd":2}}`,
		"a bidi pin":              `{"pid":1,"pursuit":{"enabled":true,"pin":"claude/ha\u202eiku","wakeCapUsd":2}}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newBeginWorld(t)
			w.host(doc)
			p := w.preview(t)
			if len(p.Blockers) != 1 || p.Blockers[0].Code != "host-info-invalid" {
				t.Fatalf("blockers = %+v", p.Blockers)
			}
			def := coderhost.PursuitCostRows(coderhost.CostInput{WakeCapUSD: coderhost.DefaultWakeCapUSD, MissionBudgetUSD: 20, Pct: 20, Wakes: 6})
			if p.Cost.Text != def.Text || strings.Contains(p.Cost.Text, "as reported by the host") {
				t.Errorf("the table is not the default one: %s", p.Cost.Text)
			}
			if p.Host["judge_pin"] != "" || p.Host["wake_cap_usd"] != 0.0 {
				t.Errorf("an invalid report is shown as a fact: %v", p.Host)
			}
			out, _, err := runTask(t, w.db, "default", false, "", "session", "pursuit", "preview", id(w.s))
			if err != nil || strings.Contains(out, "nothing blocks it") || !strings.Contains(out, "BLOCKED host-info-invalid") || strings.Contains(out, "capped at $0") {
				t.Errorf("plain preview: %v\n%s", err, out)
			}
			w.refused(t, 3, "blocked", w.beginArgs(p)...)
		})
	}
}

// What the host reports is labelled as reported, and a pin is quoted, never printed as a line of its own.
func TestPursuitPlainPreviewLabelsHostFiguresAsReported(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	out, _, err := runTask(t, w.db, "default", false, "", "session", "pursuit", "preview", id(w.s))
	if err != nil || !strings.Contains(out, `as reported by the host, which is not proof): the pursuit loop is ON; judge "claude/haiku@4"`) || !strings.Contains(out, "(as reported by the host)") {
		t.Errorf("%v\n%s", err, out)
	}
}

// An unbounded or unreadable mission budget is not a known budget (the loop's own predicate).
func TestPursuitPreviewBudgetUsesTheLoopsPredicate(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	w.exec(t, `UPDATE task_sessions SET budget_usd = 1e999 WHERE id = ?`, w.s)
	p := w.preview(t)
	if len(p.Blockers) != 1 || p.Blockers[0].Code != "budget-unknown" || !strings.Contains(p.Cost.Text, "no known budget") {
		t.Errorf("an infinite budget: %+v", p.Blockers)
	}
}

// The app reads this document: its keys are a contract (keys are added, never renamed or removed, within a schema version).
func TestPursuitPreviewJSONKeySet(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	out, _, err := runTask(t, w.db, "default", true, "", "session", "pursuit", "preview", id(w.s))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	keys := func(raw json.RawMessage) string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	for path, want := range map[string]string{
		"":     "blockers,chat_id,checker,consent_digest,cost,goal,host,limits,phrase_template,profile,rubric_present,schema_version,session_id",
		"goal": "criteria,hash,state", "limits": "parked_max,pct,wakes_max", "cost": "rows,text", "checker": "set_up",
		"host": "judge_call_cap_usd,judge_pin,loop_on,wake_cap_usd",
	} {
		raw := json.RawMessage(out)
		if path != "" {
			raw = doc[path]
		}
		if got := keys(raw); got != want {
			t.Errorf("%q keys = %s, want %s", path, got, want)
		}
	}
	var goal struct{ Criteria []json.RawMessage }
	_ = json.Unmarshal(doc["goal"], &goal)
	if len(goal.Criteria) == 0 || keys(goal.Criteria[0]) != "expected,id,kind,spec" && keys(goal.Criteria[0]) != "id,kind,spec" {
		t.Errorf("criterion keys: %s", goal.Criteria)
	}
	var cost struct{ Rows []json.RawMessage }
	_ = json.Unmarshal(doc["cost"], &cost)
	if len(cost.Rows) != 3 || keys(cost.Rows[0]) != "key,known,text,usd" {
		t.Errorf("cost row keys: %s", cost.Rows)
	}
}

// Limits out of range are refused as limits, before the phrase is looked at.
func TestPursuitBeginChecksLimitsBeforeThePhrase(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	p := w.preview(t)
	doc := w.refused(t, 3, "invalid_input", append(w.beginArgs(p), "--pct", "101", "--gui-phrase", "not the phrase")...)
	if msg, _ := doc["error"].(string); !strings.Contains(msg, "percent") {
		t.Errorf("the refusal is about the phrase: %v", doc)
	}
}

// A criterion with a variation selector (a common emoji) is refused on the app path with its own message, not the length code.
func TestPursuitBeginAppPathRefusesAnEmojiCriterionWithItsOwnMessage(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	asLead(t, w.tok)
	body, _ := json.Marshal([]map[string]string{{"kind": "rubric", "spec": "all done \u2714\ufe0f"}})
	if out, _, err := runTask(t, w.db, "default", true, string(body), "session", "goal", "propose", id(w.s), "--criteria", "-", "--as", "lead"); err != nil {
		t.Skipf("propose refuses a variation selector already: %v %s", err, out)
	}
	asOperator(t)
	doc := w.refused(t, 3, "hidden_characters", "session", "pursuit", "preview", id(w.s))
	if msg, _ := doc["error"].(string); !strings.Contains(msg, "can't show safely") {
		t.Errorf("message = %v", doc)
	}
}

// The plain preview escapes the ids and the kind it prints.
func TestPursuitPlainPreviewQuotesTheChat(t *testing.T) {
	t.Parallel()
	w := newBeginWorld(t)
	out, _, err := runTask(t, w.db, "default", false, "", "session", "pursuit", "preview", id(w.s))
	if err != nil || !strings.Contains(out, `(chat "chat1")`) || !strings.Contains(out, `--chat "chat1"`) {
		t.Errorf("%v\n%s", err, out)
	}
}
