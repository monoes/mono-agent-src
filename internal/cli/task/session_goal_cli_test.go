package task

import (
	"encoding/json"
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

// goalMission is a mission session in a chat whose mode is mission (a coder_sessions row), and its id.
func goalMission(t *testing.T, db, mode string) int64 {
	t.Helper()
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, chat_mode, created_at, updated_at) VALUES ('default', 'chat1', ?, 'x', 'x')`, mode); err != nil {
		t.Fatal(err)
	}
	return sessionNew(t, db, "Mission", "--kind", "mission", "--conversation", "chat1")
}

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
	s := goalMission(t, db, "mission")
	if _, ok := goalState(t, db, s); ok {
		t.Fatal("a mission with no goal shows one")
	}
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
	out, _, err := runTask(t, db, "default", true, goalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")
	if exitCode(err) != 3 || !strings.Contains(out, "new mission") {
		t.Errorf("a proposal after the confirmation: %v %s", err, out)
	}
}

func TestSessionGoalSetIsFinalAndNeedsARubric(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := goalMission(t, db, "mission")
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
	for _, bad := range []string{`not json`, `[]`, `[{"kind":"rubric","spec":"x","extra":1}]`, `{"kind":"rubric"}`} {
		if _, _, err := runTask(t, db, "default", true, bad, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead"); exitCode(err) != 3 {
			t.Errorf("criteria %q: exit %d", bad, exitCode(err))
		}
	}
}

func TestSessionGoalRefusesAChatThatIsNotAMission(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := goalMission(t, db, "off")
	if _, _, err := runTask(t, db, "default", true, goalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead"); exitCode(err) != 3 {
		t.Errorf("propose in a chat with mode off: exit %d", exitCode(err))
	}
	if _, _, err := runTask(t, db, "default", true, goalJSON, "session", "goal", "set", id(s), "--criteria", "-"); exitCode(err) != 3 {
		t.Errorf("set in a chat with mode off: exit %d", exitCode(err))
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
			s := goalMission(t, db, "mission")
			var p goalDoc
			mustTaskJSON(t, db, "default", &p, goalJSON, "session", "goal", "propose", id(s), "--criteria", "-", "--as", "lead")
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

func TestSessionGoalHelpSaysAChangedGoalIsANewMission(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	out, _, err := runTask(t, db, "default", false, "", "session", "goal", "set", "--help")
	if err != nil || !strings.Contains(out, "a changed goal means a new mission") {
		t.Errorf("goal set --help: %v\n%s", err, out)
	}
}
