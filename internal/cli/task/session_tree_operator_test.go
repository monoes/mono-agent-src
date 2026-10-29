package task

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// poke runs SQL against the test database, for a state the CLI cannot make.
func poke(t *testing.T, dbPath, q string) {
	t.Helper()
	raw, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.DB.Exec(q); err != nil {
		t.Fatal(err)
	}
}

// These tests run the verbs as the operator (no agent marker, no --as: the test
// harness clears the environment) and check each guard of the session tree
// through the CLI. Each says in its comment which guard it holds, so that the
// mutation that removes it can be named.

// treeNode is a node of `session tree --json`.
type treeNode struct {
	ID            int64      `json:"id"`
	Title         string     `json:"title"`
	Parent        int64      `json:"parent_session_id"`
	Kind          string     `json:"kind"`
	Depth         int        `json:"depth"`
	Archived      bool       `json:"archived"`
	ApprovalBasis string     `json:"approval_basis"`
	Children      []treeNode `json:"children"`
}

type treeNodeDoc struct {
	Session treeNode `json:"session"`
}

func newChild(t *testing.T, db string, parent int64, args ...string) treeNode {
	t.Helper()
	var d treeNodeDoc
	mustTaskJSON(t, db, "default", &d, "", append([]string{"session", "new", "--parent", id(parent)}, args...)...)
	return d.Session
}

func treeOf(t *testing.T, db string, extra ...string) []treeNode {
	t.Helper()
	out, _, err := runTask(t, db, "default", true, "", append([]string{"session", "tree"}, extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	var tree []treeNode
	if err := json.Unmarshal([]byte(out), &tree); err != nil {
		t.Fatalf("tree is a JSON array: %v\n%s", err, out)
	}
	return tree
}

func errCode(doc map[string]any) string {
	c, _ := doc["code"].(string)
	return c
}

// Guard: a child may be started below a running parent, and carries parent, kind and depth.
func TestOperatorStartsAChildOfARunningParent(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	parent := sessionNew(t, db, "Mission", "--kind", "mission")
	sessionAdd(t, db, parent, "first")
	var ap sessionDoc
	mustTaskJSON(t, db, "default", &ap, "", "session", "approve", id(parent))
	if ap.Session.State != "running" {
		t.Fatalf("parent: %+v", ap.Session)
	}
	kid := newChild(t, db, parent, "Research", "--kind", "research")
	if kid.Parent != parent || kid.Kind != "research" || kid.Depth != 2 {
		t.Fatalf("child: %+v", kid)
	}
	grand := newChild(t, db, kid.ID, "Direction", "--kind", "direction")
	if grand.Parent != kid.ID || grand.Depth != 3 {
		t.Fatalf("grandchild: %+v", grand)
	}
	// The child is a draft the operator approves itself; the parent's running state does not start it.
	var shown struct {
		Session struct {
			State string `json:"state"`
		} `json:"session"`
	}
	mustTaskJSON(t, db, "default", &shown, "", "session", "show", id(kid.ID))
	if shown.Session.State != "draft" {
		t.Fatalf("a child starts as a draft: %+v", shown.Session)
	}
}

// Guard: a parent must be a session of the same profile (Store.checkParent -> sessionTx).
func TestSessionParentInAnotherProfileIsRefused(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	addTaskProfile(t, db, "work-id", "Work")
	var other treeNodeDoc
	mustTaskJSON(t, db, "work-id", &other, "", "session", "new", "Elsewhere")
	if other.Session.ID == 0 {
		t.Fatal("no session in the other profile")
	}
	doc := failedTaskJSON(t, db, "default", 2, "session", "new", "Kid", "--parent", id(other.Session.ID))
	if errCode(doc) != "not_found" {
		t.Fatalf("want not_found, got %v", doc)
	}
	if n := len(treeOf(t, db)); n != 0 {
		t.Fatalf("the refused child left %d sessions in the profile", n)
	}
	var work []treeNode
	out, _, err := runTask(t, db, "work-id", true, "", "session", "tree")
	if err != nil || json.Unmarshal([]byte(out), &work) != nil || len(work) != 1 || len(work[0].Children) != 0 {
		t.Fatalf("the other profile's tree: %v %s", err, out)
	}
}

// Guard: a session cannot be its own parent or the parent of its ancestor: a parent must exist
// already, and a chain that is a cycle (written by hand into the database) is refused, never walked.
func TestSessionCycleIsImpossible(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	a := sessionNew(t, db, "A")
	b := newChild(t, db, a, "B").ID
	// the id the next session would take is not a session yet: no parent
	failedTaskJSON(t, db, "default", 2, "session", "new", "Self", "--parent", id(b+1))
	// there is no verb that moves a session below another one
	root := NewCommand(testDeps(t, db, "default", true))
	sess, _, err := root.Find([]string{"session"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range sess.Commands() {
		if strings.Contains(c.Name(), "move") || strings.Contains(c.Name(), "parent") {
			t.Fatalf("session %s could change a parent", c.Name())
		}
	}
	var ren treeNodeDoc
	mustTaskJSON(t, db, "default", &ren, "", "session", "rename", id(a), "A2")
	if ren.Session.Parent != 0 {
		t.Fatalf("rename moved the session: %+v", ren.Session)
	}
	// a ring written under the CLI (an older build, a hand edit): starting below it is refused
	// and the tree leaves the ring's rows out, never a walk round the ring and never a failed list.
	poke(t, db, `UPDATE task_sessions SET parent_session_id = `+id(b)+` WHERE id = `+id(a))
	doc := failedTaskJSON(t, db, "default", 3, "session", "new", "C", "--parent", id(b))
	if !strings.Contains(doc["error"].(string), "cycle") {
		t.Fatalf("want a cycle refusal, got %v", doc)
	}
	if _, _, err := runTask(t, db, "default", true, "", "session", "tree"); err != nil {
		t.Fatalf("tree of a ring: %v", err)
	}
}

// Guard: HardMaxSessionDepth = 4 (Store.checkParent).
func TestSessionDepthFiveIsRefused(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	cur := sessionNew(t, db, "L1")
	for level := 2; level <= 4; level++ {
		kid := newChild(t, db, cur, "L"+id(int64(level)))
		if kid.Depth != level {
			t.Fatalf("level %d: %+v", level, kid)
		}
		cur = kid.ID
	}
	doc := failedTaskJSON(t, db, "default", 3, "session", "new", "L5", "--parent", id(cur))
	if errCode(doc) != "limit" || !strings.Contains(doc["error"].(string), "4 levels") {
		t.Fatalf("level 5: %v", doc)
	}
	if n := countTree(treeOf(t, db)); n != 4 {
		t.Fatalf("%d sessions after the refusal, want 4", n)
	}
}

func countTree(nodes []treeNode) int {
	n := 0
	for _, x := range nodes {
		n += 1 + countTree(x.Children)
	}
	return n
}

// Guard: a child never has more access, budget or caps than its parent (withinParent).
func TestSessionChildLimitsAboveTheParentAreRefused(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	parent := sessionNew(t, db, "P", "--access", "coding", "--budget-usd", "10", "--max-tasks", "30", "--max-depth", "2")
	for _, bad := range [][]string{
		{"--access", "full"},
		{"--budget-usd", "11"},
		{"--max-tasks", "31"},
		{"--max-depth", "3"},
	} {
		doc := failedTaskJSON(t, db, "default", 3, append([]string{"session", "new", "C", "--parent", id(parent)}, bad...)...)
		if errCode(doc) != "limit" {
			t.Fatalf("%v: %v", bad, doc)
		}
	}
	if n := countTree(treeOf(t, db)); n != 1 {
		t.Fatalf("refusals left %d sessions", n)
	}
	var d struct {
		Session struct {
			treeNode
			AccessLevel string  `json:"access_level"`
			BudgetUSD   float64 `json:"budget_usd"`
			MaxTasks    int     `json:"max_tasks"`
			MaxDepth    int     `json:"max_depth"`
		} `json:"session"`
	}
	mustTaskJSON(t, db, "default", &d, "", "session", "new", "ok", "--parent", id(parent), "--access", "coding", "--budget-usd", "4")
	if d.Session.AccessLevel != "coding" || d.Session.BudgetUSD != 4 || d.Session.MaxTasks != 30 || d.Session.MaxDepth != 2 {
		t.Fatalf("within and inherited: %+v", d.Session)
	}
}

// Guard: the operator renames any session, one an agent started included; an agent does not
// rename the operator's.
func TestOperatorRenamesAnySession(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	mine := sessionNew(t, db, "Mine")
	var bots treeNodeDoc
	mustTaskJSON(t, db, "default", &bots, "", "session", "new", "Bot", "plan", "--as", "bot")
	for _, s := range []int64{mine, bots.Session.ID} {
		var r treeNodeDoc
		mustTaskJSON(t, db, "default", &r, "", "session", "rename", id(s), "Renamed", id(s))
		if r.Session.Title != "Renamed "+id(s) {
			t.Fatalf("rename #%d: %+v", s, r.Session)
		}
	}
	// the bot renames its own, not the operator's
	var own treeNodeDoc
	mustTaskJSON(t, db, "default", &own, "", "session", "rename", id(bots.Session.ID), "By", "bot", "--as", "bot")
	failedTaskJSON(t, db, "default", 3, "session", "rename", id(mine), "Hijack", "--as", "bot")
	failedTaskJSON(t, db, "default", 2, "session", "rename", "9999", "x")
	// the title stays within the store's 200 characters, and an empty one is refused
	failedTaskJSON(t, db, "default", 3, "session", "rename", id(mine), "  ")
}

// Guard: the tree is ordered by id at every level and an empty branch is [], not null.
func TestSessionTreeJSONOrderingAndChildren(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	a := sessionNew(t, db, "A")
	b := sessionNew(t, db, "B")
	b1 := newChild(t, db, b, "B1")
	a1 := newChild(t, db, a, "A1") // created after B1, listed under A, which has the lower id
	a2 := newChild(t, db, a, "A2")
	c := sessionNew(t, db, "C")
	tree := treeOf(t, db)
	if len(tree) != 3 || tree[0].ID != a || tree[1].ID != b || tree[2].ID != c {
		t.Fatalf("roots in id order: %+v", tree)
	}
	if len(tree[0].Children) != 2 || tree[0].Children[0].ID != a1.ID || tree[0].Children[1].ID != a2.ID {
		t.Fatalf("A's children: %+v", tree[0].Children)
	}
	if len(tree[1].Children) != 1 || tree[1].Children[0].ID != b1.ID || tree[1].Children[0].Depth != 2 {
		t.Fatalf("B's children: %+v", tree[1].Children)
	}
	raw, _, _ := runTask(t, db, "default", true, "", "session", "tree")
	if strings.Contains(raw, `"children": null`) || !strings.Contains(raw, `"children": []`) {
		t.Fatalf("a leaf has [] children:\n%s", raw)
	}
}

// Guard: --all lists archived sessions in the tree; without it an archived parent leaves and its
// open child stands as a root.
func TestSessionTreeAllShowsArchived(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	a := sessionNew(t, db, "A")
	kid := newChild(t, db, a, "A1")
	var ar treeNodeDoc
	mustTaskJSON(t, db, "default", &ar, "", "session", "archive", id(a))
	if !ar.Session.Archived {
		t.Fatalf("archive: %+v", ar.Session)
	}
	without := treeOf(t, db)
	for _, n := range without {
		if n.ID == a {
			t.Fatalf("an archived session is in the default tree: %+v", without)
		}
	}
	with := treeOf(t, db, "--all")
	if len(with) != 1 || with[0].ID != a || !with[0].Archived || len(with[0].Children) != 1 || with[0].Children[0].ID != kid.ID {
		t.Fatalf("--all: %+v", with)
	}
	text, _, err := runTask(t, db, "default", false, "", "session", "tree", "--all")
	if err != nil || !strings.Contains(text, "#"+id(a)) || !strings.Contains(text, "\n  #"+id(kid.ID)) {
		t.Fatalf("text --all: %v\n%s", err, text)
	}
}
