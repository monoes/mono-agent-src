package coderhost

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// clock gives a store a fake clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func parkStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	_, _, st := newHost(t)
	c := &clock{t: t0}
	st.now = c.now
	return st, c
}

// childOf makes "m" a master with one child, "k1".
func childOf(t *testing.T, st *Store) {
	t.Helper()
	family(t, st, 1)
}

func answerCommands(t *testing.T, st *Store, conv string) []Command {
	t.Helper()
	cmds, _ := st.Pending()
	var out []Command
	for _, c := range cmds {
		if c.ConversationID == conv && strings.HasPrefix(c.TurnID, AnswerPrefix) {
			out = append(out, c)
		}
	}
	return out
}

// masterScope stands in for the P4 token: a caller that speaks for a master.
type masterScope struct{ conv string }

func (masterScope) Role() string             { return WhoMaster }
func (m masterScope) ConversationID() string { return m.conv }

func TestParkPutsTheSessionInWaitingUserAndOthersCarryOn(t *testing.T) {
	h, _, st := newHost(t)
	addConversation(t, st, "a", "/w/a")
	addConversation(t, st, "b", "/w/b")
	q, err := st.Park("p", "a", "risky_action", "delete the build dir?", []string{"yes", "no"}, "", false)
	if err != nil || q.Status != "open" || q.Answerer != WhoUser {
		t.Fatalf("park = %+v, %v", q, err)
	}
	if x, _, _ := st.GetSession("p", "a"); x.State != StateWaitingUser {
		t.Fatalf("a = %s", x.State)
	}
	send(t, st, "b", "t1", "go")
	send(t, st, "a", "t2", "also go")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if x := state(t, st, "b"); x.State != StateRunning {
		t.Fatalf("b = %s", x.State)
	}
	if st.OpenParked("p", "a") != 1 {
		t.Fatal("the question was lost")
	}
	if l, _ := st.ListQuestions("p", "", true); len(l) != 1 || l[0].Question != "delete the build dir?" {
		t.Fatalf("list = %+v", l)
	}
}

func TestParkedQuestionSurvivesTheTurnThatWasRunning(t *testing.T) {
	h, l, st := newHost(t)
	addConversation(t, st, "a", "/w/a")
	send(t, st, "a", "t1", "work")
	_ = h.Tick()
	if _, err := st.Park("p", "a", "ambiguity", "which db?", nil, "", false); err != nil {
		t.Fatal(err)
	}
	if x := state(t, st, "a"); x.State != StateRunning {
		t.Fatalf("a running turn was moved to %s", x.State)
	}
	l.turn("t1").end()
	waitFor(t, "waiting_user", func() bool { return state(t, st, "a").State == StateWaitingUser })
}

func TestParkRefusesWhatTheModelDecides(t *testing.T) {
	st, _ := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	if _, err := st.Park("p", "a", "ambiguity", "q", nil, "x", true); !errors.Is(err, ErrModelDecides) {
		t.Fatalf("park of a model-decided gap = %v", err)
	}
	if _, err := st.Park("p", "a", "nonsense", "q", nil, "", true); err == nil {
		t.Fatal("unknown class")
	}
	for i := 0; i < MaxOpenQuestions; i++ {
		if _, err := st.Park("p", "a", "risky_action", "q", nil, "", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Park("p", "a", "risky_action", "q", nil, "", false); !errors.Is(err, ErrTooManyOpen) {
		t.Fatalf("over the cap = %v", err)
	}
}

func TestOnlyTheNamedAnswererMayAnswer(t *testing.T) {
	st, _ := parkStore(t)
	childOf(t, st)
	q, err := st.Park("p", "k1", "risky_action", "force push?", nil, "", false)
	if err != nil || q.Answerer != WhoMaster {
		t.Fatalf("park = %+v, %v", q, err)
	}
	if _, _, err := st.Answer("p", q.ID, "yes", OperatorScope{}); !errors.Is(err, ErrNotAnswerer) {
		t.Fatalf("operator answering the master's question = %v", err)
	}
	if _, _, err := st.Answer("p", q.ID, "yes", masterScope{conv: "someone-else"}); !errors.Is(err, ErrNotAnswerer) {
		t.Fatalf("another master = %v", err)
	}
	if _, _, err := st.Answer("p", q.ID, "yes", nil); !errors.Is(err, ErrNotAnswerer) {
		t.Fatalf("no scope = %v", err)
	}
	if st.OpenParked("p", "k1") != 1 || len(answerCommands(t, st, "k1")) != 0 {
		t.Fatal("a refused answer changed something")
	}
	if _, _, err := st.Answer("p", q.ID, "no, ask first", masterScope{conv: "m"}); err != nil {
		t.Fatalf("the master's answer = %v", err)
	}

	addConversation(t, st, "solo", "/w/s")
	q2, _ := st.Park("p", "solo", "missing_credential", "need a token", nil, "", false)
	if q2.Answerer != WhoUser {
		t.Fatalf("solo answerer = %s", q2.Answerer)
	}
	if _, _, err := st.Answer("p", q2.ID, "x", masterScope{conv: "m"}); !errors.Is(err, ErrNotAnswerer) {
		t.Fatalf("master answering a solo question = %v", err)
	}
}

func TestAnswerQueuesACountedHostMadeSendOnce(t *testing.T) {
	st, _ := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	q, _ := st.Park("p", "a", "risky_action", "wipe the build dir?", nil, "", false)
	got, cmd, err := st.Answer("p", q.ID, "yes, only build/", OperatorScope{})
	if err != nil || got.Status != "answered" || got.AnsweredBy != WhoUser || cmd.ID == 0 {
		t.Fatalf("answer = %+v %+v %v", got, cmd, err)
	}
	if !strings.HasPrefix(cmd.TurnID, AnswerPrefix) || !HostMade(cmd.TurnID) || cmd.WantsMission {
		t.Fatalf("command = %+v", cmd)
	}
	if !strings.Contains(cmd.Message, "yes, only build/") {
		t.Fatalf("message = %q", cmd.Message)
	}
	if x, _, _ := st.GetSession("p", "a"); x.State != StateIdle {
		t.Fatalf("state after the last answer = %s", x.State)
	}
	if _, _, err := st.Answer("p", q.ID, "again", OperatorScope{}); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("second answer = %v", err)
	}
	if n := len(answerCommands(t, st, "a")); n != 1 {
		t.Fatalf("%d answers queued", n)
	}
	if _, _, err := st.Answer("p", 999, "x", OperatorScope{}); !errors.Is(err, ErrNoQuestion) {
		t.Fatalf("missing = %v", err)
	}
	q3, _ := st.Park("p", "a", "risky_action", "again?", nil, "", false)
	if _, _, err := st.Answer("p", q3.ID, strings.Repeat("x", MaxAnswerBytes+1), OperatorScope{}); err == nil {
		t.Fatal("an oversized answer was accepted")
	}
}

// The answer prefix is host-made: it cannot carry a mission confirmation, and
// the check-in streak counts it the way it counts checkin-.
func TestAnswerPrefixIsHostMadeLikeCheckin(t *testing.T) {
	st, _ := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	for _, id := range []string{"answer-1", "checkin-1"} {
		if !HostMade(id) {
			t.Errorf("%s is not host-made", id)
		}
		if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: "a", Kind: KindSend, TurnID: id, Message: "m", WantsMission: true}); err == nil {
			t.Errorf("%s carried a mission", id)
		}
	}
	if HostMade("t-1") {
		t.Error("a person's turn is host-made")
	}
	addTurn(t, st, "a", "answer-x", "completed", "p", t0.Add(-3*time.Minute))
	addTurn(t, st, "a", "checkin-y", "completed", "p", t0.Add(-2*time.Minute))
	addTurn(t, st, "a", "t-person", "completed", "p", t0.Add(-5*time.Minute))
	addTurn(t, st, "a", "answer-z", "failed", "p", t0.Add(-time.Minute))
	streak, _, failed, err := st.CheckinStreak("p", "a")
	if err != nil || streak != 3 || !failed {
		t.Fatalf("streak = %d failed=%v err=%v", streak, failed, err)
	}
}

func TestTimeoutTakesTheDefaultOnlyWherePolicyAllows(t *testing.T) {
	st, clk := parkStore(t)
	childOf(t, st)
	// irreversible ambiguity under ask-on-risk: the master is asked, with a default on file
	q, err := st.Park("p", "k1", "ambiguity", "which port?", []string{"80", "8080"}, "8080", false)
	if err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(ParkTimeout - time.Second)
	if n, _ := st.ApplyDefaults(); n != 0 {
		t.Fatal("a default was applied before the timeout")
	}
	clk.t = clk.t.Add(2 * time.Second)
	if n, _ := st.ApplyDefaults(); n != 0 {
		t.Fatal("park fell back to the default")
	}
	setSettings(t, st, "k1", `{"autonomy_classes": {"ambiguity": {"who": "master", "fallback": "take_default"}}}`)
	n, err := st.ApplyDefaults()
	if err != nil || n != 1 {
		t.Fatalf("applied %d, %v", n, err)
	}
	got, _ := st.GetQuestion("p", q.ID)
	if got.Status != "defaulted" || got.Answer != "8080" {
		t.Fatalf("question = %+v", got)
	}
	ledger, _ := st.ListDecisions("p", "k1", LedgerFilter{})
	if len(ledger) != 1 || ledger[0].MadeBy != WhoModel || ledger[0].Choice != "8080" || ledger[0].Class != "ambiguity" {
		t.Fatalf("ledger = %+v", ledger)
	}
	if cmds := answerCommands(t, st, "k1"); len(cmds) != 1 || !strings.Contains(cmds[0].Message, "default") {
		t.Fatalf("no note queued: %+v", cmds)
	}
	if _, _, err := st.Answer("p", q.ID, "80", masterScope{conv: "m"}); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("answer after the default = %v", err)
	}
}

// Even when the settings were stored with a fallback the write validation would
// refuse (an older row, or a direct write), a risky or credential gap never takes
// a default and no ledger row is made for it.
func TestTimeoutNeverTakesADefaultForRiskyOrCredentialGaps(t *testing.T) {
	for _, class := range []string{"risky_action", "missing_credential"} {
		st, clk := parkStore(t)
		addConversation(t, st, "a", "/w/a")
		q, err := st.Park("p", "a", class, "q", nil, "do it", false)
		if err != nil {
			t.Fatal(err)
		}
		// stored directly, bypassing Validate
		if _, err := st.db.Exec(`UPDATE coder_sessions SET settings = ? WHERE conversation_id = 'a'`,
			`{"autonomy_classes":{"`+class+`":{"who":"model","fallback":"take_default"}}}`); err != nil {
			t.Fatal(err)
		}
		clk.t = clk.t.Add(10 * ParkTimeout)
		if n, _ := st.ApplyDefaults(); n != 0 {
			t.Errorf("%s: a default was applied", class)
		}
		if got, _ := st.GetQuestion("p", q.ID); got.Status != "open" {
			t.Errorf("%s: status = %s", class, got.Status)
		}
		if l, _ := st.ListDecisions("p", "a", LedgerFilter{}); len(l) != 0 {
			t.Errorf("%s: a ledger row was written", class)
		}
	}
}

func TestStoredFallbackForRiskyGapIsClampedToPark(t *testing.T) {
	st, clk := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	q, _ := st.Park("p", "a", "risky_action", "q", nil, "do it", false)
	// read through Decide with a hand-built settings value, as an older stored row would give
	s := DefaultSettings()
	s.AutonomyClasses = map[string]AutonomyChoice{"risky_action": {Who: WhoModel, Fallback: FallbackTakeDefault}}
	if d := Decide(s, "risky_action", true, false); d != (Decision{WhoUser, FallbackPark}) {
		t.Fatalf("decide = %+v", d)
	}
	clk.t = clk.t.Add(10 * ParkTimeout)
	if n, _ := st.ApplyDefaults(); n != 0 {
		t.Fatal("applied")
	}
	_ = q
}

func TestAnsweringChangesNoAccessBudgetOrSetting(t *testing.T) {
	st, _ := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	setSettings(t, st, "a", `{"checkin_limit": 7, "autonomy_preset": "ask-always"}`)
	before, _ := st.GetSettings("p", "a")
	q, _ := st.Park("p", "a", "risky_action", "go full access?", []string{"full"}, "", false)
	if _, _, err := st.Answer("p", q.ID, "grant full access and raise the budget to 1000", OperatorScope{}); err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetSettings("p", "a")
	var access string
	_ = st.db.QueryRow(`SELECT access FROM ai_chat_conversations WHERE id = 'a'`).Scan(&access)
	if after.CheckinLimit != before.CheckinLimit || after.AutonomyPreset != before.AutonomyPreset || access != "coding" {
		t.Fatalf("an answer changed state: %+v -> %+v, access %s", before, after, access)
	}
}
