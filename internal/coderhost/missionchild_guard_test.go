package coderhost

import (
	"go/ast"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A static guard for the mission child sources (missionchild*.go, children_task.go): like the autonomy
// sources they name no approval writer, but they may also write their own tables. They write only
// coder_mission_* (and, once a child's turn has ended, lower coder_sessions.budget_usd), and they only
// read tasks, task_sessions and the sessions' questions. A change that needs more has to change this list
// on purpose. The files take no import from the repository but internal/tasks (its status names).

var missionChildFiles = []string{"missionchild*.go", "children_task.go"}

var missionWritable = map[string]bool{"coder_mission_policy": true, "coder_mission_children": true, "coder_mission_events": true}

var missionReadable = map[string]bool{
	"coder_mission_policy": true, "coder_mission_children": true, "coder_mission_events": true,
	"coder_sessions": true, "coder_session_questions": true, "tasks": true, "task_sessions": true,
}

var (
	missionRead  = regexp.MustCompile(`(?i)\b(?:FROM|INTO|UPDATE|JOIN)\s+([a-z_][a-z_0-9]*)`)
	missionWrite = regexp.MustCompile(`(?i)\b(UPDATE|INSERT\s+(?:OR\s+\w+\s+)?INTO|DELETE\s+FROM)\s+([a-z_][a-z_0-9]*)`)
)

// missionSQLProblems says what is wrong with one piece of SQL text.
func missionSQLProblems(s string) []string {
	if !looksLikeSQL(s) {
		return nil
	}
	var out []string
	for _, m := range missionRead.FindAllStringSubmatch(s, -1) {
		if n := strings.ToLower(m[1]); n != "set" && !missionReadable[n] { // "DO UPDATE SET"
			out = append(out, "reads "+m[1])
		}
	}
	for _, m := range missionWrite.FindAllStringSubmatch(s, -1) {
		table := strings.ToLower(m[2])
		if table == "set" {
			continue
		}
		if missionWritable[table] {
			continue
		}
		if table == "coder_sessions" && strings.EqualFold(m[1], "UPDATE") && onlyBudgetColumns(s) {
			continue
		}
		out = append(out, "writes "+m[2])
	}
	if sqlFormat.MatchString(s) {
		out = append(out, "builds a table name with a format verb")
	}
	return out
}

// onlyBudgetColumns says whether every UPDATE coder_sessions in s assigns only budget_usd and updated_at.
func onlyBudgetColumns(s string) bool {
	found := false
	for _, m := range sqlSession.FindAllStringSubmatch(s, -1) {
		found = true
		for _, a := range sqlAssign.FindAllStringSubmatch(m[1], -1) {
			if a[1] != "budget_usd" && a[1] != "updated_at" {
				return false
			}
		}
	}
	return found
}

func TestMissionChildSourcesNameNoApprovalWriterAndTouchOnlyTheirTables(t *testing.T) {
	files := nonTestFiles(t, missionChildFiles...)
	if len(files) < 1 {
		t.Fatalf("the guard sees only %v: it would pass on nothing", files)
	}
	forbidden := map[string]bool{}
	for _, n := range approvalWriters {
		forbidden[n] = true
	}
	for _, file := range files {
		f := parse(t, file)
		inChain := map[*ast.BinaryExpr]bool{}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(p, "mono-agent/") && p != "github.com/monoes/mono-agent/internal/tasks" {
				t.Errorf("%s imports %s: these files use no repository package but internal/tasks", file, p)
			}
		}
		report := func(text string) {
			for _, p := range missionSQLProblems(text) {
				t.Errorf("%s: SQL %s", file, p)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if forbidden[x.Name] {
					t.Errorf("%s: names %s, an approval/settings writer", file, x.Name)
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					v, _ := strconv.Unquote(x.Value)
					report(v)
				}
			case *ast.BinaryExpr:
				if x.Op == token.ADD && !inChain[x] {
					var lits []string
					flattenAdd(x, inChain, &lits)
					report(strings.Join(lits, ""))
				}
			}
			return true
		})
	}
}

// The checker itself must flag what it is there for: this fails if a rule is loosened.
func TestMissionSQLCheckerFlagsWhatItGuards(t *testing.T) {
	bad := map[string]string{
		"a task write":              `UPDATE tasks SET status = 'done' WHERE id = ?`,
		"a task session write":      `UPDATE task_sessions SET state = 'running'`,
		"a plan approval":           `UPDATE task_sessions SET approved_by = 'x' WHERE id = ?`,
		"a task insert":             `INSERT INTO tasks (title) VALUES ('x')`,
		"a command write":           `INSERT INTO coder_session_commands (kind) VALUES ('send')`,
		"a settings write":          `UPDATE coder_sessions SET settings = '{}' WHERE conversation_id = ?`,
		"a session role write":      `UPDATE coder_sessions SET role = 'master', budget_usd = 1 WHERE conversation_id = ?`,
		"a session insert":          `INSERT INTO coder_sessions (profile_id) VALUES ('p')`,
		"a config write":            `UPDATE configs SET value = 'x'`,
		"a read of another table":   `SELECT * FROM configs`,
		"a delete outside":          `DELETE FROM coder_session_questions WHERE id = 1`,
		"a format verb table":       `SELECT * FROM %s`,
		"a token read":              `SELECT token_hash FROM coder_session_tokens`,
		"a delete of session rows ": `DELETE FROM coder_sessions WHERE conversation_id = ?`,
	}
	for name, q := range bad {
		if len(missionSQLProblems(q)) == 0 {
			t.Errorf("%s was not flagged: %s", name, q)
		}
	}
	for name, q := range map[string]string{
		"a mission write":       `UPDATE coder_mission_children SET state = 'gone' WHERE id = ?`,
		"an upsert":             `INSERT INTO coder_mission_policy (a) VALUES (?) ON CONFLICT(a) DO UPDATE SET a = excluded.a`,
		"the carve return":      `UPDATE coder_sessions SET budget_usd = ?, updated_at = ? WHERE conversation_id = ?`,
		"a read of the tasks":   `SELECT status FROM tasks WHERE id = ?`,
		"a read of the session": `SELECT state FROM task_sessions WHERE id = ?`,
	} {
		if p := missionSQLProblems(q); len(p) != 0 {
			t.Errorf("%s was flagged: %v", name, p)
		}
	}
}

// Each mission child table is part of the no-approval dumps: the autonomy paths must leave it untouched, and
// only the mission child store may change it.
func TestMissionChildTablesAreProtectedFromTheAutonomyPaths(t *testing.T) {
	st := newStoreT(t)
	got := protectedState(t, st.db)
	for _, tbl := range []string{"coder_mission_policy", "coder_mission_children", "coder_mission_events"} {
		if _, ok := got[tbl]; !ok {
			t.Errorf("%s is not in the protected dump", tbl)
		}
		if autonomyTables[tbl] {
			t.Errorf("%s is listed as a table the autonomy paths may write", tbl)
		}
	}
}
