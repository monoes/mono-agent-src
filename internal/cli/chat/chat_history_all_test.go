package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/storage"
)

type allPage struct {
	Items      []HistoryRow `json:"items"`
	NextCursor string       `json:"next_cursor"`
	Warnings   []string     `json:"warnings"`
}

func listAll(t *testing.T, dbPath string, args ...string) allPage {
	t.Helper()
	out, code := runChatHistory(t, dbPath, "default", append([]string{"list", "--all"}, args...)...)
	if code != 0 {
		t.Fatalf("list --all %v: exit %d: %s", args, code, out)
	}
	var p allPage
	decodeChatJSON(t, out, &p)
	return p
}

// noOrgs keeps the org half of the list out of a test that is about chats.
func noOrgs(t *testing.T) {
	t.Helper()
	old := orgNamesFn
	orgNamesFn = func(string) ([]string, error) { return nil, nil }
	t.Cleanup(func() { orgNamesFn = old })
}

func seedConv(t *testing.T, store *ai.AIStore, db *sql.DB, access, updated string) ai.Conversation {
	t.Helper()
	cwd := ""
	if access != ai.AccessScoped {
		cwd = t.TempDir()
	}
	c, err := store.CreateConversationAccess("default", "agent", "general", "claude", "", "sonnet", access, cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ai_chat_conversations SET updated_at = ? WHERE id = ?`, updated, c.ID); err != nil {
		t.Fatal(err)
	}
	return c
}

func rawDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func seedTurn(t *testing.T, store *ai.AIStore, db *sql.DB, convID, turnID, prompt, status, at string) {
	t.Helper()
	if _, _, err := store.CreateTurn(convID, "default", turnID, "i", prompt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ai_chat_turns SET status = ?, created_at = ? WHERE id = ?`, status, at, turnID); err != nil {
		t.Fatal(err)
	}
}

func seedTask(t *testing.T, db *sql.DB, id, convID, status, goal, app, at string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO ai_tasks (id, profile_id, kind, conversation_id, status, goal, application_id, created_at, updated_at)
		VALUES (?, 'default', 'apply', ?, ?, ?, ?, ?, ?)`, id, convID, status, goal, app, at, at); err != nil {
		t.Fatal(err)
	}
}

func rowByID(t *testing.T, p allPage, id string) HistoryRow {
	t.Helper()
	for _, r := range p.Items {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("row %s not in %+v", id, p.Items)
	return HistoryRow{}
}

func TestHistoryAllStatusAndTitle(t *testing.T) {
	noOrgs(t)
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	folder := seedConv(t, store, db, ai.AccessFull, "2026-10-22T10:00:07Z")
	none := seedConv(t, store, db, ai.AccessScoped, "2026-10-22T10:00:06Z")
	run := seedConv(t, store, db, ai.AccessScoped, "2026-10-22T10:00:05Z")
	done := seedConv(t, store, db, ai.AccessScoped, "2026-10-22T10:00:04Z")
	bad := seedConv(t, store, db, ai.AccessScoped, "2026-10-22T10:00:03Z")
	stopped := seedConv(t, store, db, ai.AccessScoped, "2026-10-22T10:00:02Z")
	needs := seedConv(t, store, db, ai.AccessCoding, "2026-10-22T10:00:01Z")

	seedTurn(t, store, db, run.ID, "r1", "  first   line\nsecond line ", "completed", "2026-10-22T09:00:00Z")
	seedTurn(t, store, db, run.ID, "r2", "follow up", "active", "2026-10-22T09:05:00Z")
	seedTurn(t, store, db, done.ID, "d1", strings.Repeat("long ", 40), "completed", "2026-10-22T09:00:00Z")
	seedTurn(t, store, db, bad.ID, "b1", "oops", "failed", "2026-10-22T09:00:00Z")
	seedTurn(t, store, db, stopped.ID, "s1", "stop me", "cancelled", "2026-10-22T09:00:00Z")
	seedTurn(t, store, db, needs.ID, "n1", "apply to the job", "active", "2026-10-22T09:00:00Z")
	seedTask(t, db, "task-old", needs.ID, "failed", "old goal", "", "2026-10-22T08:00:00Z")
	seedTask(t, db, "task-new", needs.ID, "needs_user", "Apply to Acme", "app-1", "2026-10-22T09:00:00Z")

	p := listAll(t, dbPath)
	want := map[string]struct{ status, title string }{
		run.ID:     {histRunning, "first line second line"},
		done.ID:    {histDone, strings.TrimSpace(strings.Repeat("long ", 12))[:59] + "…"},
		bad.ID:     {histError, "oops"},
		stopped.ID: {histIdle, "stop me"},
		needs.ID:   {histNeeds, "Apply to Acme"},
		none.ID:    {histIdle, "New chat"},
	}
	for id, w := range want {
		r := rowByID(t, p, id)
		if r.Status != w.status || r.Title != w.title {
			t.Errorf("%s: status %q title %q, want %q %q", id, r.Status, r.Title, w.status, w.title)
		}
		if r.Kind != "chat" {
			t.Errorf("%s: kind %q", id, r.Kind)
		}
	}
	n := rowByID(t, p, needs.ID)
	if n.TaskID != "task-new" || n.TaskStatus != "needs_user" || n.TaskGoal != "Apply to Acme" || n.ApplicationID != "app-1" || n.Access != ai.AccessCoding {
		t.Errorf("task join (latest task wins): %+v", n)
	}
	// A coder chat with no turns and no task is named after its folder.
	if r := rowByID(t, p, folder.ID); r.Title == "New chat" || r.Title != lastElem(folder.Cwd) {
		t.Errorf("folder title = %q, cwd %q", r.Title, folder.Cwd)
	}
	if got := listAll(t, dbPath, "--status", "needs"); len(got.Items) != 1 || got.Items[0].ID != needs.ID {
		t.Errorf("--status needs = %+v", got.Items)
	}
	if got := listAll(t, dbPath, "--application", "app-1"); len(got.Items) != 1 || got.Items[0].ID != needs.ID {
		t.Errorf("--application = %+v", got.Items)
	}
	// "any" means has an application link: only the one task-bound chat, no org rows.
	if got := listAll(t, dbPath, "--application", "any"); len(got.Items) != 1 || got.Items[0].ID != needs.ID {
		t.Errorf("--application any = %+v", got.Items)
	}
	if got := listAll(t, dbPath, "--query", "SECOND"); len(got.Items) != 1 || got.Items[0].ID != run.ID {
		t.Errorf("--query = %+v", got.Items)
	}
	if got := listAll(t, dbPath, "--query", "%"); len(got.Items) != 0 {
		t.Errorf("--query %% must be literal, got %d rows", len(got.Items))
	}
}

func lastElem(p string) string { return p[strings.LastIndex(p, "/")+1:] }

func TestHistoryAllTitlesAreRedacted(t *testing.T) {
	noOrgs(t)
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	c := seedConv(t, store, db, ai.AccessScoped, "2026-10-22T10:00:00Z")
	seedTurn(t, store, db, c.ID, "t1", "login with password: hunter2 and key sk-abcdef0123456789abcdef0123456789abcdef01 ok", "completed", "2026-10-22T09:00:00Z")
	d := seedConv(t, store, db, ai.AccessScoped, "2026-10-22T10:00:01Z")
	seedTask(t, db, "tk", d.ID, "running", "use token=abc123 for the call", "", "2026-10-22T09:00:00Z")
	p := listAll(t, dbPath)
	for _, r := range p.Items {
		for _, leak := range []string{"hunter2", "sk-abcdef", "abc123"} {
			if strings.Contains(r.Title+r.TaskGoal, leak) {
				t.Errorf("%s leaks %q: title %q goal %q", r.ID, leak, r.Title, r.TaskGoal)
			}
		}
	}
	if !strings.Contains(rowByID(t, p, c.ID).Title, "[redacted]") {
		t.Errorf("title = %q", rowByID(t, p, c.ID).Title)
	}
}

// The cursor is stable while rows are added or touched between pages: no row
// is repeated or skipped among those that existed when paging began.
func TestHistoryAllPaginationIsStable(t *testing.T) {
	noOrgs(t)
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	var ids []string
	for i := 0; i < 8; i++ {
		// Equal timestamps in pairs: the id breaks the tie.
		c := seedConv(t, store, db, ai.AccessScoped, fmt.Sprintf("2026-10-22T10:00:%02dZ", i/2))
		ids = append(ids, c.ID)
	}
	var seen []string
	cursor := ""
	for page := 0; ; page++ {
		args := []string{"--limit", "3"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		p := listAll(t, dbPath, args...)
		for _, r := range p.Items {
			seen = append(seen, r.ID)
		}
		if page == 0 {
			// New and touched rows land above the cursor; paging must not move.
			seedConv(t, store, db, ai.AccessScoped, "2026-10-22T11:00:00Z")
			if _, err := db.Exec(`UPDATE ai_chat_conversations SET updated_at = '2026-10-22T12:00:00Z' WHERE id = ?`, seen[0]); err != nil {
				t.Fatal(err)
			}
		}
		if p.NextCursor == "" {
			break
		}
		if page > 5 {
			t.Fatal("cursor never ends")
		}
		cursor = p.NextCursor
	}
	got := map[string]int{}
	for _, id := range seen {
		got[id]++
	}
	for _, id := range ids {
		if id != seen[0] && got[id] != 1 {
			t.Errorf("%s seen %d times in %v", id, got[id], seen)
		}
	}
	if len(seen) != 8 {
		t.Errorf("saw %d rows, want 8: %v", len(seen), seen)
	}
}

type countingDB struct {
	db      *sql.DB
	queries int
}

func (c *countingDB) Query(q string, args ...any) (*sql.Rows, error) {
	c.queries++
	return c.db.Query(q, args...)
}

// One statement reads a whole page, however many rows it holds.
func TestHistoryAllIsOneQuery(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	for i := 0; i < 30; i++ {
		c := seedConv(t, store, db, ai.AccessScoped, fmt.Sprintf("2026-10-22T10:00:%02dZ", i))
		seedTurn(t, store, db, c.ID, fmt.Sprintf("t%d", i), "hello", "completed", "2026-10-22T09:00:00Z")
		seedTask(t, db, fmt.Sprintf("k%d", i), c.ID, "running", "goal", "", "2026-10-22T09:00:00Z")
	}
	cd := &countingDB{db: db}
	rows, err := queryHistoryRows(cd, "default", historyFilter{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 30 || cd.queries != 1 {
		t.Fatalf("%d rows in %d queries, want 30 in 1", len(rows), cd.queries)
	}
}

func stubOrgs(t *testing.T, names []string, report func(name string) (json.RawMessage, error)) *int {
	t.Helper()
	calls := new(int)
	var callsMu sync.Mutex
	oN, oR, oL, oT := orgNamesFn, orgReportFn, orgLiveFn, orgNow
	orgNamesFn = func(string) ([]string, error) { return names, nil }
	orgReportFn = func(_ context.Context, _ string, name string, all bool, _ string) (json.RawMessage, error) {
		callsMu.Lock()
		*calls++
		callsMu.Unlock()
		if !all {
			t.Errorf("report must ask for --all")
		}
		return report(name)
	}
	orgLiveFn = func(string, string) bool { return false }
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	orgNow = func() time.Time { return now }
	orgReportMu.Lock()
	orgReportCache = map[string]orgReportEntry{}
	orgReportMu.Unlock()
	t.Cleanup(func() { orgNamesFn, orgReportFn, orgLiveFn, orgNow = oN, oR, oL, oT })
	return calls
}

const goodReport = `{"items":[
 {"run":"r2","started_at":"2026-10-22T09:00:00Z","ended_at":"2026-10-22T09:30:00Z","outcome":{"status":"achieved"},"cost_usd":1.5},
 {"run":"r1","started_at":"2026-10-21T09:00:00Z","outcome":{"status":"failed"}}]}`

func TestHistoryAllOrgRunsAndFailureIsolation(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	c := seedConv(t, store, db, ai.AccessScoped, "2026-10-22T09:15:00Z")
	calls := stubOrgs(t, []string{"alpha", "broken"}, func(name string) (json.RawMessage, error) {
		if name == "broken" {
			return nil, errors.New("monomind exploded")
		}
		return json.RawMessage(goodReport), nil
	})
	// The org half needs a profile root; any non-empty one will do.
	if _, err := db.Exec(`UPDATE profiles SET root_dir = ? WHERE id = 'default'`, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	p := listAll(t, dbPath)
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "org broken") {
		t.Fatalf("warnings = %v", p.Warnings)
	}
	var order []string
	for _, r := range p.Items {
		order = append(order, r.ID)
	}
	if strings.Join(order, ",") != "org:alpha#r2,"+c.ID+",org:alpha#r1" {
		t.Fatalf("order = %v", order)
	}
	r2 := rowByID(t, p, "org:alpha#r2")
	if r2.Kind != "org" || r2.OrgName != "alpha" || r2.RunID != "r2" || r2.Status != histDone || r2.CostUSD == nil || *r2.CostUSD != 1.5 || !strings.HasPrefix(r2.Title, "alpha") {
		t.Errorf("org row = %+v", r2)
	}
	if rowByID(t, p, "org:alpha#r1").Status != histError {
		t.Error("failed outcome must be error")
	}
	// Cached: a second list inside the TTL does not start monomind again.
	before := *calls
	listAll(t, dbPath)
	if after := *calls; after != before+1 { // only the failing org is asked again
		t.Errorf("report calls %d -> %d, want one more (the failed org is not cached)", before, after)
	}
	if got := listAll(t, dbPath, "--kind", "chat"); len(got.Items) != 1 || got.Items[0].ID != c.ID {
		t.Errorf("--kind chat = %+v", got.Items)
	}
	if got := listAll(t, dbPath, "--kind", "org", "--status", "error"); len(got.Items) != 1 || got.Items[0].ID != "org:alpha#r1" {
		t.Errorf("--kind org --status error = %+v", got.Items)
	}
	// And past the TTL it asks again.
	orgNow = func() time.Time { return time.Date(2026, 10, 22, 12, 1, 0, 0, time.UTC) }
	before = *calls
	listAll(t, dbPath)
	if *calls != before+2 {
		t.Errorf("after the TTL report calls %d -> %d, want +2", before, *calls)
	}
}

func TestHistoryAllOrgPagesMergeWithChats(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	for i := 0; i < 3; i++ {
		seedConv(t, store, db, ai.AccessScoped, fmt.Sprintf("2026-10-22T09:%02d:00Z", 10+i*10))
	}
	stubOrgs(t, []string{"alpha"}, func(string) (json.RawMessage, error) { return json.RawMessage(goodReport), nil })
	if _, err := db.Exec(`UPDATE profiles SET root_dir = ? WHERE id = 'default'`, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var seen []string
	cursor := ""
	for i := 0; i < 6; i++ {
		args := []string{"--limit", "2"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		p := listAll(t, dbPath, args...)
		for _, r := range p.Items {
			seen = append(seen, r.ID)
		}
		if cursor = p.NextCursor; cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("saw %v, want 3 chats and 2 org runs", seen)
	}
	uniq := map[string]bool{}
	for _, id := range seen {
		uniq[id] = true
	}
	if len(uniq) != 5 {
		t.Fatalf("duplicates in %v", seen)
	}
}

func TestParseOrgRunsToleratesShapes(t *testing.T) {
	rows, err := parseOrgRuns("o", json.RawMessage(`[{"run":"a","outcome":{"status":"partial"}},{"nope":1}]`))
	if err != nil || len(rows) != 1 || rows[0].Status != histDone || rows[0].UpdatedAt != "" {
		t.Fatalf("rows = %+v err %v", rows, err)
	}
	if _, err := parseOrgRuns("o", json.RawMessage(`"x"`)); err == nil {
		t.Fatal("unreadable report must be an error")
	}
}

func TestHistoryAllFlagsNeedAll(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	if _, code := runChatHistory(t, dbPath, "default", "list", "--status", "done"); code == 0 {
		t.Error("--status without --all must be refused")
	}
	if _, code := runChatHistory(t, dbPath, "default", "list", "--all", "--kind", "bogus"); code == 0 {
		t.Error("bad --kind must be refused")
	}
	if _, code := runChatHistory(t, dbPath, "default", "list", "--all", "--limit", "500"); code == 0 {
		t.Error("--limit over 200 must be refused")
	}
}

func TestHistoryAllIDFilter(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	a := seedConv(t, store, db, ai.AccessScoped, "2026-10-22T10:00:01Z")
	seedConv(t, store, db, ai.AccessScoped, "2026-10-22T10:00:02Z")
	stubOrgs(t, []string{"alpha"}, func(string) (json.RawMessage, error) { return json.RawMessage(goodReport), nil })
	if _, err := db.Exec(`UPDATE profiles SET root_dir = ? WHERE id = 'default'`, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := listAll(t, dbPath, "--id", a.ID); len(got.Items) != 1 || got.Items[0].ID != a.ID {
		t.Fatalf("--id = %+v", got.Items)
	}
	if got := listAll(t, dbPath, "--id", "nope"); len(got.Items) != 0 {
		t.Fatalf("unknown id = %+v", got.Items)
	}
}

// A Stop's notice is the newest turn of the chat but not its status: a running turn still lists as running, and the title
// still comes from the first real prompt. Mutant tried: dropping the system-turn filter of the status join lists it as done.
func TestHistoryAllIgnoresASystemNoticeTurn(t *testing.T) {
	noOrgs(t)
	dbPath := newChatCLITestDB(t)
	store, db := openTestChatStore(t, dbPath), rawDB(t, dbPath)
	c := seedConv(t, store, db, ai.AccessScoped, "2026-10-22T10:00:05Z")
	seedTurn(t, store, db, c.ID, "w1", "real work", "active", "2026-10-22T09:00:00Z")
	if err := store.RecordSystemNotice("default", c.ID, "doomsday.stopped", "Open-ended run stopped by you at T: stopped"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ai_chat_turns SET created_at = '2026-10-22T09:30:00Z' WHERE id LIKE 'system-%'`); err != nil {
		t.Fatal(err)
	}
	r := rowByID(t, listAll(t, dbPath), c.ID)
	if r.Status != histRunning || r.Title != "real work" {
		t.Fatalf("status %q title %q, want running / real work", r.Status, r.Title)
	}
}
