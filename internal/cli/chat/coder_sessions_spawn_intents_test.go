package chat

import (
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/storage"
)

const resolveWords = "resolve spawn intent"

type intentCLI struct {
	cfg    *Deps
	dbPath string
	opened int
}

// newIntentCLI is an operator terminal on a scratch database holding one chat with a child and a held intent.
func newIntentCLI(t *testing.T, state string) *intentCLI {
	t.Helper()
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	spawner, err := store.CreateConversationAccess("default", "agent", "general", "claude", "", "", ai.AccessWorkspace, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateConversationAccess("default", "agent", "general", "claude", "", "", ai.AccessWorkspace, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	m := &intentCLI{dbPath: dbPath}
	m.cfg = testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	open := m.cfg.OpenDB
	m.cfg.OpenDB = func() (*storage.Database, error) { m.opened++; return open() }
	m.cfg.RequireOperator = func(what string) error { // the real gate refuses an agent context like this
		if os.Getenv("CLAUDECODE") != "" {
			return clicore.OperatorOnly("only the user can %s", what)
		}
		return nil
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO coder_spawn_intents (profile_id, spawner, scope_hash, child, state, generation, created_at, claimed_at, attempts)
		VALUES ('default', ?, 'h', ?, ?, 7, '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z', 2)`, spawner.ID, child.ID, state); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENT_TEST_SPAWNER", spawner.ID)
	return m
}

func (m *intentCLI) rows(t *testing.T, q string) int {
	t.Helper()
	db, err := storage.NewDatabase(m.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.DB.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSpawnIntentsVerbsRefuseAnAgentBeforeAnyDatabaseOpen(t *testing.T) {
	m := newIntentCLI(t, "needs-operator")
	t.Setenv("CLAUDECODE", "1")
	before := m.opened
	for _, args := range [][]string{
		{"spawn-intents", "list"},
		{"spawn-intents", "resolve", os.Getenv("MONOAGENT_TEST_SPAWNER"), "--keep", "--generation", "7", "--confirm", resolveWords},
		{"spawn-intents", "resolve"}, // even malformed: the gate is first
	} {
		if out, code := runSessionsCLI(t, m.cfg, args...); code != 3 {
			t.Errorf("%v: exit %d %s, want 3", args, code, out)
		}
	}
	if m.opened != before {
		t.Errorf("the database was opened %d times by a refused agent", m.opened-before)
	}
	if n := m.rows(t, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 1 {
		t.Errorf("%d intents", n)
	}
}

func TestResolveNeedsTheConfirmWordsAndAHeldRow(t *testing.T) {
	m := newIntentCLI(t, "needs-operator")
	sp := os.Getenv("MONOAGENT_TEST_SPAWNER")
	for _, confirm := range []string{"", "yes", "resolve spawn"} {
		if out, code := runSessionsCLI(t, m.cfg, "spawn-intents", "resolve", sp, "--keep", "--generation", "7", "--confirm", confirm); code == 0 {
			t.Errorf("confirm %q accepted: %s", confirm, out)
		}
	}
	if out, code := runSessionsCLI(t, m.cfg, "spawn-intents", "resolve", sp, "--generation", "7", "--confirm", resolveWords); code == 0 {
		t.Errorf("neither --keep nor --discard accepted: %s", out)
	}
	if out, code := runSessionsCLI(t, m.cfg, "spawn-intents", "resolve", sp, "--keep", "--discard", "--generation", "7", "--confirm", resolveWords); code == 0 {
		t.Errorf("both --keep and --discard accepted: %s", out)
	}
	if out, code := runSessionsCLI(t, m.cfg, "spawn-intents", "resolve", sp, "--keep", "--generation", "6", "--confirm", resolveWords); code == 0 {
		t.Errorf("a stale generation accepted: %s", out)
	}
	if out, code := runSessionsCLI(t, m.cfg, "spawn-intents", "resolve", "nobody", "--keep", "--generation", "7", "--confirm", resolveWords); code == 0 {
		t.Errorf("an unknown spawner accepted: %s", out)
	}
	if n := m.rows(t, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 1 {
		t.Errorf("%d intents after refusals", n)
	}
	out, code := runSessionsCLI(t, m.cfg, "spawn-intents", "resolve", sp, "--keep", "--generation", "7", "--confirm", resolveWords)
	if code != 0 || !strings.Contains(out, "deletes") {
		t.Fatalf("keep: exit %d %s (the output must say the operator deletes the child and session separately)", code, out)
	}
	if n := m.rows(t, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 0 {
		t.Errorf("%d intents after keep", n)
	}
	if n := m.rows(t, `SELECT COUNT(*) FROM coder_session_decisions WHERE choice = 'accepted'`); n != 1 {
		t.Errorf("%d acceptance rows, want 1", n)
	}
	if n := m.rows(t, `SELECT COUNT(*) FROM ai_chat_conversations`); n < 2 {
		t.Errorf("%d conversations: keep must leave the child", n)
	}
}

func TestResolveRefusesAPendingRow(t *testing.T) {
	m := newIntentCLI(t, "pending")
	sp := os.Getenv("MONOAGENT_TEST_SPAWNER")
	if out, code := runSessionsCLI(t, m.cfg, "spawn-intents", "resolve", sp, "--discard", "--generation", "7", "--confirm", resolveWords); code == 0 {
		t.Errorf("a pending row was resolved: %s", out)
	}
	if n := m.rows(t, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 1 {
		t.Errorf("%d intents", n)
	}
}

func TestSpawnIntentsListJSON(t *testing.T) {
	m := newIntentCLI(t, "needs-operator")
	out, code := runSessionsCLI(t, m.cfg, "spawn-intents", "list")
	if code != 0 {
		t.Fatalf("exit %d %s", code, out)
	}
	var got struct {
		Intents []struct {
			Spawner       string
			State         string
			Generation    int64
			Attempts      int64
			ClaimedAt     string
			Child         string
			NeedsOperator bool
		}
	}
	decodeChatJSON(t, out, &got)
	if len(got.Intents) != 1 || got.Intents[0].State != "needs-operator" || got.Intents[0].Generation != 7 || got.Intents[0].Attempts != 2 || !got.Intents[0].NeedsOperator || got.Intents[0].Child == "" || got.Intents[0].ClaimedAt == "" {
		t.Errorf("list = %s", out)
	}
}
