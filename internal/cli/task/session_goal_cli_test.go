package task

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/storage"
)

const goalJSON = `[{"kind":"command","spec":"go test ./...","expected":"exit:0"},{"kind":"rubric","spec":"The person is satisfied with the report"}]`
const otherGoalJSON = `[{"kind":"file","spec":"docs/report.md","expected":"exists"},{"kind":"rubric","spec":"Nothing is made up"}]`

type goalDoc struct {
	Goal missionpursuit.Goal `json:"goal"`
}

// goalMission is a mission session in the chat "chat1", whose mode is mode, and the session token of that chat's turn.
func goalMission(t *testing.T, db, mode string) (int64, string) {
	t.Helper()
	tok := mintSessionToken(t, db, "default", "chat1")
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`UPDATE coder_sessions SET chat_mode = ? WHERE conversation_id = 'chat1'`, mode); err != nil {
		t.Fatal(err)
	}
	asOperator(t)
	return sessionNew(t, db, "Mission", "--kind", "mission", "--conversation", "chat1"), tok
}

// asLead runs the following commands as the lead of a chat (its verified session token), asOperator as the person.
func asLead(t *testing.T, tok string) { setenv(t, "MONOAGENT_SESSION_TOKEN", tok) }
func asOperator(t *testing.T)         { setenv(t, "MONOAGENT_SESSION_TOKEN", "") }

func goalState(t *testing.T, db string, s int64) (missionpursuit.Goal, bool) {
	t.Helper()
	out, _, err := runTask(t, db, "default", true, "", "session", "goal", "show", id(s))
	if err != nil {
		return missionpursuit.Goal{}, false
	}
	var d goalDoc
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}
	return d.Goal, true
}

func TestSessionGoalProposeShowConfirm(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, tok := goalMission(t, db, "mission")
	if _, ok := goalState(t, db, s); ok {
		t.Fatal("a mission with no goal shows one")
	}
	asLead(t, tok)
	var p goalDoc
	mustTaskJSON(t, db, "default", &p, goalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")
	if p.Goal.State != "proposed" || p.Goal.ProposedBy != "lead" || len(p.Goal.Criteria) != 2 || p.Goal.ConfirmedBy != "" {
		t.Fatalf("propose: %+v", p.Goal)
	}
	// the lead revises; the operator reads the new hash and confirms exactly that
	var r goalDoc
	mustTaskJSON(t, db, "default", &r, otherGoalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")
	if r.Goal.Hash == p.Goal.Hash {
		t.Fatal("the revision kept the hash")
	}
	asOperator(t)
	stale := failedTaskJSON(t, db, "default", 3, "session", "goal", "confirm", id(s), "--hash", p.Goal.Hash)
	if errCode(stale) != "invalid_input" || !strings.Contains(stale["error"].(string), "changed") {
		t.Errorf("a confirm of the hash that was replaced: %v", stale)
	}
	noHash := failedTaskJSON(t, db, "default", 3, "session", "goal", "confirm", id(s))
	if errCode(noHash) != "invalid_input" {
		t.Errorf("confirm without --hash: %v", noHash)
	}
	var c goalDoc
	mustTaskJSON(t, db, "default", &c, "", "session", "goal", "confirm", id(s), "--hash", r.Goal.Hash)
	if c.Goal.State != "confirmed" || c.Goal.ConfirmedBy != "you" {
		t.Fatalf("confirm: %+v", c.Goal)
	}
	text, _, err := runTask(t, db, "default", false, "", "session", "goal", "show", id(s))
	if err != nil || !strings.Contains(text, "confirmed") || !strings.Contains(text, r.Goal.Hash) || !strings.Contains(text, "c2") {
		t.Errorf("text show: %v %q", err, text)
	}
	asLead(t, tok)
	out, _, err := runTask(t, db, "default", true, goalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")
	if exitCode(err) != 3 || !strings.Contains(out, "new mission") {
		t.Errorf("a proposal after the confirmation: %v %s", err, out)
	}
}

// An agent that is not the lead of this mission's chat cannot propose, nor overwrite the proposal of another mission.
func TestSessionGoalProposeNeedsTheLeadOfThatMission(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, tok := goalMission(t, db, "mission")
	asLead(t, tok)
	var mine goalDoc
	mustTaskJSON(t, db, "default", &mine, goalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")

	// a second mission in a chat of its own, with its own lead
	tok2 := mintSessionToken(t, db, "default", "chat2")
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE coder_sessions SET chat_mode = 'mission' WHERE conversation_id = 'chat2'`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	asOperator(t)
	s2 := sessionNew(t, db, "Mission 2", "--kind", "mission", "--conversation", "chat2")
	asLead(t, tok2)
	var theirs goalDoc
	mustTaskJSON(t, db, "default", &theirs, otherGoalJSON, "session", "goal", "propose", id(s2), "--criteria", "-", "--as", "lead2")

	// chat2's lead cannot touch mission 1; an agent with no session token cannot touch either
	out, _, err := runTask(t, db, "default", true, otherGoalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead2")
	if exitCode(err) != 3 || !strings.Contains(out, `"operator_only"`) {
		t.Errorf("the lead of another mission: exit %d %s", exitCode(err), out)
	}
	asOperator(t)
	for _, sess := range []int64{s, s2} {
		out, _, err = runTask(t, db, "default", true, goalJSON, "session", "goal", "propose", id(sess), "--criteria", "-", "--as", "bot")
		if exitCode(err) != 3 || !strings.Contains(out, `"operator_only"`) {
			t.Errorf("an unbound agent, session %d: exit %d %s", sess, exitCode(err), out)
		}
	}
	if g, _ := goalState(t, db, s); g.Hash != mine.Goal.Hash || g.ProposedBy != "lead" {
		t.Errorf("mission 1's proposal changed: %+v", g)
	}
	if g, _ := goalState(t, db, s2); g.Hash != theirs.Goal.Hash || g.ProposedBy != "lead2" {
		t.Errorf("mission 2's proposal changed: %+v", g)
	}
}

func TestSessionGoalSetIsFinalAndNeedsARubric(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, tok := goalMission(t, db, "mission")
	noRubric := `[{"kind":"command","spec":"true"}]`
	out, _, err := runTask(t, db, "default", true, noRubric, "session", "goal", "set", id(s), "--criteria", "-")
	if exitCode(err) != 3 || !strings.Contains(out, "rubric") {
		t.Fatalf("set without a rubric: %v %s", err, out)
	}
	var g goalDoc
	mustTaskJSON(t, db, "default", &g, goalJSON, "session", "goal", "set", id(s), "--criteria", "-")
	if g.Goal.State != "confirmed" || g.Goal.ConfirmedBy != "you" {
		t.Fatalf("set: %+v", g.Goal)
	}
	out, _, err = runTask(t, db, "default", true, otherGoalJSON, "session", "goal", "set", id(s), "--criteria", "-")
	if exitCode(err) != 3 || !strings.Contains(out, "new mission") {
		t.Errorf("set over a confirmed goal: %v %s", err, out)
	}
	asLead(t, tok)
	for _, bad := range []string{`not json`, `[]`, `[{"kind":"rubric","spec":"x","extra":1}]`, `{"kind":"rubric"}`} {
		if _, _, err := runTask(t, db, "default", true, bad, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead"); exitCode(err) != 3 {
			t.Errorf("criteria %q: exit %d", bad, exitCode(err))
		}
	}
}

// The legal maximum (16 criteria of 2 KiB specs and 1 KiB expectations) can be quote-heavy, which JSON doubles: it must fit.
func TestSessionGoalCriteriaCapBoundsTheDecodedSizeSensibly(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, tok := goalMission(t, db, "mission")
	var cs []missionpursuit.Criterion
	for i := 0; i < missionpursuit.MaxCriteria; i++ {
		cs = append(cs, missionpursuit.Criterion{Kind: "rubric", Spec: fmt.Sprintf("%c", 'A'+i) + strings.Repeat(`"`, missionpursuit.MaxSpecBytes-1), Expected: strings.Repeat(`\`, missionpursuit.MaxExpectBytes/2)})
	}
	raw, err := json.Marshal(cs)
	if err != nil || len(raw) <= 64<<10 {
		t.Fatalf("the test set is %d bytes (%v): it must be larger than 64 KiB to mean anything", len(raw), err)
	}
	if err := missionpursuit.Validate(cs); err != nil {
		t.Fatalf("the test set is not legal: %v", err)
	}
	asLead(t, tok)
	var g goalDoc
	mustTaskJSON(t, db, "default", &g, string(raw), "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")
	if len(g.Goal.Criteria) != missionpursuit.MaxCriteria {
		t.Fatalf("stored %d criteria", len(g.Goal.Criteria))
	}
	// but input without a bound is refused, and nothing is read past it
	huge := `[` + strings.Repeat(` `, 300<<10) + `]`
	if _, _, err := runTask(t, db, "default", true, huge, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead"); exitCode(err) != 3 {
		t.Errorf("an oversized --criteria: exit %d", exitCode(err))
	}
}

// A proposal whose rows no longer match their hash is replaced by proposing again, through the verb.
func TestSessionGoalDamagedProposalIsReplacedByProposingAgain(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, tok := goalMission(t, db, "mission")
	asLead(t, tok)
	var p goalDoc
	mustTaskJSON(t, db, "default", &p, goalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE mission_goal_criteria SET spec = 'tampered' WHERE pos = 1`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	asOperator(t)
	out, _, err := runTask(t, db, "default", true, "", "session", "goal", "show", id(s))
	if err == nil || !strings.Contains(out, "propose the criteria again") {
		t.Fatalf("show of a damaged proposal: %v %s", err, out)
	}
	asLead(t, tok)
	var r goalDoc
	mustTaskJSON(t, db, "default", &r, otherGoalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")
	if r.Goal.State != "proposed" || r.Goal.Hash == p.Goal.Hash {
		t.Fatalf("the replacement: %+v", r.Goal)
	}
}

func TestSessionGoalRefusesAChatThatIsNotAMission(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s, tok := goalMission(t, db, "off")
	if _, _, err := runTask(t, db, "default", true, goalJSON, "session", "goal", "set", id(s), "--criteria", "-"); exitCode(err) != 3 {
		t.Errorf("set in a chat with mode off: exit %d", exitCode(err))
	}
	asLead(t, tok)
	if _, _, err := runTask(t, db, "default", true, goalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead"); exitCode(err) != 3 {
		t.Errorf("propose in a chat with mode off: exit %d", exitCode(err))
	}
}

// Guard: set and confirm are the operator's. Every kind of agent context is refused with operator_only, before the
// database is opened and with it open, and nothing changes.
func TestSessionGoalSetAndConfirmRefuseEveryAgentContext(t *testing.T) {
	t.Parallel()
	broken := opsBrokenDB(t)
	for name, setup := range opsAgentContexts {
		t.Run(name, func(t *testing.T) {
			db := newTaskTestDB(t)
			s, tok := goalMission(t, db, "mission")
			asLead(t, tok)
			var p goalDoc
			mustTaskJSON(t, db, "default", &p, goalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")
			asOperator(t)
			extra := setup(t)
			for _, c := range [][]string{
				{"session", "goal", "set", id(s), "--criteria", "-"},
				{"session", "goal", "confirm", id(s), "--hash", p.Goal.Hash},
			} {
				full := append(append([]string{}, c...), extra...)
				for _, path := range []string{broken, db} {
					out, _, err := runTask(t, path, "default", true, otherGoalJSON, full...)
					if exitCode(err) != 3 || !strings.Contains(out, `"operator_only"`) {
						t.Errorf("task %s (db %q): exit %d, %s, want operator_only", strings.Join(full, " "), path, exitCode(err), out)
					}
				}
			}
			if g, ok := goalState(t, db, s); !ok || g.State != "proposed" || g.Hash != p.Goal.Hash {
				t.Errorf("a refused agent changed the goal: %+v", g)
			}
		})
	}
}

// The words a person must see wherever the goal verbs are explained.
const newMissionText = "a changed goal means a new mission"

func TestSessionGoalHelpSaysAChangedGoalIsANewMission(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, args := range [][]string{{"session", "goal", "--help"}, {"session", "goal", "set", "--help"}, {"session", "goal", "confirm", "--help"}} {
		out, _, err := runTask(t, db, "default", false, "", args...)
		if err != nil || !strings.Contains(out, newMissionText) {
			t.Errorf("task %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
