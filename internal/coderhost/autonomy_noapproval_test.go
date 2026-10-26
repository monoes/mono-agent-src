package coderhost

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// The autonomy machinery (preset decide-and-log, ask-on-risk model decisions,
// take_default / ApplyDefaults, answer-p/m/d notes, master answers and
// ReopenFailedAnswers) settles WHO answers a gap. It must never reach a
// mission confirmation, a plan approval or any approval (human-in-the-loop send
// gates, task-session approval, operator-only settings, access and budget
// raises). These tests run every path with genuinely signed records and real
// queued commands, then check that nothing but the autonomy tables moved.

// autonomyTables are the only tables the autonomy paths may write: the queue, the
// questions, the ledger and the host's own turn bookkeeping. Everything else
// (tasks and their session approvals, HIL items, configs with confirmations and
// keys, conversations and their access level) must be byte-identical afterwards.
var autonomyTables = map[string]bool{
	"coder_session_commands":   true,
	"coder_session_questions":  true,
	"coder_session_decisions":  true,
	"coder_session_host_turns": true,
	"coder_session_turn_caps":  true,
	"sqlite_sequence":          true,
}

func tableDump(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var lines []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			parts[i] = fmt.Sprint(v)
		}
		lines = append(lines, strings.Join(parts, "|"))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// protectedState is everything an approval could live in: all tables outside
// autonomyTables, and the settings, unattended flag, budget and tree of every
// session (a session's state and turn columns are the host's to move).
func protectedState(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	for _, n := range names {
		if autonomyTables[n] || n == "coder_sessions" {
			continue
		}
		out[n] = tableDump(t, db, `SELECT * FROM "`+n+`"`)
	}
	out["coder_sessions(protected)"] = tableDump(t, db, `SELECT profile_id, conversation_id, role, parent_conversation_id, unattended, settings, budget_usd FROM coder_sessions`)
	return out
}

func diffProtected(t *testing.T, label string, before, after map[string]string) {
	t.Helper()
	for k, b := range before {
		if after[k] != b {
			t.Errorf("%s: %s changed:\nbefore: %.300s\nafter:  %.300s", label, k, b, after[k])
		}
	}
}

// startedTurn is "turnid:message", as the fake launcher records it.
func turnOf(started string) string { return started[:strings.Index(started, ":")] }

// answerer is one way a parked question gets its answer. run does it and says which
// note origin it queued ("p" a person's, "m" a master's, "d" a default; "" when none).
// failFirst makes the first start fail, so the note fails and the question is reopened.
type answerer struct {
	name      string
	failFirst bool
	run       func(t *testing.T, st *Store, c *clock, q Question, master masterScope) string
}

func answerBy(scope func(masterScope) CallerScope, origin string) func(*testing.T, *Store, *clock, Question, masterScope) string {
	return func(t *testing.T, st *Store, c *clock, q Question, m masterScope) string {
		if _, _, err := st.Answer("p", q.ID, "go on", scope(m)); err != nil {
			return "" // the policy names someone else: refused, nothing queued
		}
		return origin
	}
}

func byTimeout(t *testing.T, st *Store, c *clock, q Question, m masterScope) string {
	c.t = c.t.Add(ParkTimeout + 2*MinCheckinGap)
	n, err := st.ApplyDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if n > 0 {
		return "d"
	}
	return ""
}

var answerers = []answerer{
	{"operator", false, answerBy(func(masterScope) CallerScope { return OperatorScope{} }, "p")},
	{"master", false, answerBy(func(m masterScope) CallerScope { return m }, "m")},
	{"timeout-default", false, byTimeout},
	{"operator+failed-start", true, answerBy(func(masterScope) CallerScope { return OperatorScope{} }, "p")},
	{"master+failed-start", true, answerBy(func(m masterScope) CallerScope { return m }, "m")},
	{"timeout-default+failed-start", true, byTimeout},
}

// choicesFor lists no override plus the overrides that reach every answerer and fallback
// (the others only repeat these: Decide is covered exhaustively in autonomy_test.go).
func choicesFor() []*AutonomyChoice {
	return []*AutonomyChoice{nil, {Who: WhoModel, Fallback: FallbackTakeDefault}, {Who: WhoMaster, Fallback: FallbackTakeDefault}, {Who: WhoUser, Fallback: FallbackPark}}
}

// startedBy counts the autonomy turns the host started, by note origin, over all scenarios.
var startedBy = map[string]int{}

func TestNoAutonomyPathReachesAnApproval(t *testing.T) {
	// One database for all scenarios (a migrated copy per scenario is too slow): each
	// scenario has conversations of its own, and the protected state is compared
	// across each scenario.
	h, l, st := newHost(t)
	startedBy = map[string]int{}
	i := 0
	for _, preset := range AutonomyPresets {
		for _, class := range GapClasses {
			for _, ov := range choicesFor() {
				for _, rev := range []bool{true, false} {
					for _, who := range answerers {
						if who.failFirst && ov != nil {
							continue // the failure rounds are run once per preset and class, with the preset's own policy
						}
						i++
						name := fmt.Sprintf("%s/%s/%+v/rev=%v/%s", preset, class, ov, rev, who.name)
						runNoApprovalScenario(t, h, l, st, i, name, preset, class, ov, rev, who)
					}
				}
			}
		}
	}
	t.Logf("%d scenarios; autonomy turns started by origin: %v", i, startedBy)
	// The test must not pass by starting nothing: a person's, a master's and a default's
	// note each reached a started turn, and none of them carried a token.
	for _, origin := range []string{"p", "m", "d"} {
		if startedBy[origin] == 0 {
			t.Errorf("no answer-%s- turn was ever started: the scenarios do not exercise that path", origin)
		}
	}
}

func runNoApprovalScenario(t *testing.T, h *Host, l *fakeLauncher, st *Store, n int, name, preset, class string, ov *AutonomyChoice, rev bool, who answerer) {
	t.Helper()
	clk := &clock{t: t0}
	st.now = clk.now
	master, child, missionTurn := fmt.Sprintf("m%d", n), fmt.Sprintf("k%d", n), fmt.Sprintf("person-mission-%d", n)
	addConversation(t, st, master, "/work/"+master)
	addConversation(t, st, child, "/work/"+child)
	if err := st.SetRole("p", master, "master", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRole("p", child, "child", master); err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings()
	s.AutonomyPreset = preset
	if ov != nil {
		s.AutonomyClasses = map[string]AutonomyChoice{class: *ov}
	}
	if err := st.SaveSettings("p", child, s); err != nil {
		return // validation refuses it (a hard class given to the model): the setting never exists
	}
	// A genuine, signed mission confirmation for the person's own send to the same
	// session is already in place. The autonomy turns queued after it must neither
	// use nor consume it.
	mc, err := st.Enqueue(Command{ProfileID: "p", ConversationID: child, Kind: KindSend, TurnID: missionTurn, Message: "run the mission", WantsMission: true})
	if err != nil {
		t.Fatal(err)
	}
	conf, err := st.Confirm(mc, 0)
	if err != nil {
		t.Fatal(err)
	}
	before := protectedState(t, st.db)
	l.mu.Lock()
	n0 := len(l.started)
	l.mu.Unlock()

	q, perr := st.Park("p", child, class, "which way?", []string{"a", "b"}, "a", rev)
	queued := ""
	switch perr {
	case nil:
		queued = who.run(t, st, clk, q, masterScope{master})
	case ErrModelDecides: // the model path: the decision is written down, nothing else
		if _, err := st.Record(LedgerEntry{ProfileID: "p", ConversationID: child, Class: class, Question: "q", Choice: "a", MadeBy: WhoModel, Reversible: rev}); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("%s: park: %v", name, perr)
	}
	// Round 0 runs the person's mission send (it was queued first, so it holds the chat);
	// round 1 starts the answer note, or, for the failed-start variants, fails it so
	// ReopenFailedAnswers gives the question back; later rounds let defaults and
	// redelivery run again.
	for round := 0; round < 5; round++ {
		l.mu.Lock()
		l.startErr = nil
		if round == 1 && who.failFirst {
			l.startErr = fmt.Errorf("cannot start")
		}
		l.mu.Unlock()
		if round >= 2 {
			clk.t = clk.t.Add(ParkTimeout + 2*MinCheckinGap)
			_, _ = st.ApplyDefaults()
		}
		_ = h.Tick()
		endAllTurns(t, h, l)
	}
	l.mu.Lock()
	started, tokens := append([]string{}, l.started[n0:]...), append([]string{}, l.tokens[n0:]...)
	l.mu.Unlock()

	// 1. only the person's mission send carries a confirmation token
	withToken := 0
	byOrigin := map[string]int{}
	for i, s := range started {
		for _, o := range []string{"p", "m", "d"} {
			if strings.HasPrefix(turnOf(s), AnswerPrefix+o+"-") {
				byOrigin[o]++
				startedBy[o]++
				if tokens[i] != "" {
					t.Errorf("%s: an answer-%s- turn carried a token", name, o)
				}
			}
		}
		if tokens[i] != "" {
			withToken++
			if turnOf(s) != missionTurn || tokens[i] != conf.Token() {
				t.Errorf("%s: turn %s got a mission token", name, turnOf(s))
			}
		}
	}
	if withToken != 1 {
		t.Errorf("%s: %d turns carried a token, want the person's mission only", name, withToken)
	}
	// A note that was queued and had nothing in its way started as a turn.
	if queued != "" && !who.failFirst && byOrigin[queued] == 0 {
		t.Errorf("%s: an answer-%s- note was queued and never started (started: %v)", name, queued, started)
	}
	// 2. no queued command of the autonomy paths asks for a mission, and they are all host-made
	cmds := tableDump(t, st.db, `SELECT turn_id, issuer, error FROM coder_session_commands WHERE conversation_id = '`+child+`'`)
	for _, line := range strings.Split(cmds, "\n") {
		f := strings.SplitN(line, "|", 3)
		if len(f) < 3 || f[0] == missionTurn {
			continue
		}
		if !HostMade(f[0]) {
			t.Errorf("%s: a command that is not host-made: %s", name, line)
		}
		if strings.Contains(f[2], wantsMissionMark) {
			t.Errorf("%s: an autonomy command asks for a mission: %s", name, line)
		}
	}
	// 3. nothing else moved: no approval, setting, budget or access changed
	diffProtected(t, name, before, protectedState(t, st.db))
}

func endAllTurns(t *testing.T, h *Host, l *fakeLauncher) {
	t.Helper()
	l.mu.Lock()
	for _, tn := range l.turns {
		tn.end()
	}
	l.mu.Unlock()
	h.Wait()
}

// A turn id the autonomy paths use cannot be made into a mission, whoever asks.
func TestAutonomyTurnsCannotBeConfirmedAsMissions(t *testing.T) {
	_, _, st := newHost(t)
	addConversation(t, st, "c", "/w/c")
	for _, id := range []string{answerTurnID(answerFromPerson), answerTurnID(answerFromMaster), answerTurnID(answerFromDefault), CheckinTurnPrefix + "x", newMasterTurnID()} {
		if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: "c", Kind: KindSend, TurnID: id, Message: "m", WantsMission: true}); err == nil {
			t.Errorf("%s: queued as a mission", id)
		}
		if _, err := st.Confirm(Command{ID: 1, ProfileID: "p", ConversationID: "c", Kind: KindSend, TurnID: id, WantsMission: true}, 0); err == nil {
			t.Errorf("%s: confirmed", id)
		}
	}
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: "c", Kind: KindSend, TurnID: "x", Message: "m", WantsMission: true, Issuer: "m"}); err == nil {
		t.Error("a master's send was queued as a mission")
	}
}
