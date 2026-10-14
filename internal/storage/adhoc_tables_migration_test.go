package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/connections"
)

// legacyRuntimeDDL is, verbatim, the schema the Go code used to create at
// runtime outside data/migrations/ — internal/ai (initTables,
// initChatEventTables, ensureAnswersTable) and internal/connections
// (EnsureTable, SaveOAuthClient) — before migration 095 took it over and
// that code was deleted. Every database that ran a monoagent older than 095
// has tables built by it, so it stays here as the fixture the migrations
// must match and must upgrade without change.
var legacyRuntimeDDL = []string{
	`CREATE TABLE IF NOT EXISTS ai_chat_messages (
	id TEXT PRIMARY KEY,
	workflow_id TEXT NOT NULL,
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	tool_calls TEXT NOT NULL DEFAULT '',
	tool_call_id TEXT NOT NULL DEFAULT '',
	provider_id TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	token_count INTEGER NOT NULL DEFAULT 0,
	session_id TEXT NOT NULL DEFAULT '',
	profile_id TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
	)`,
	`ALTER TABLE ai_chat_messages ADD COLUMN tool_call_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE ai_chat_messages ADD COLUMN session_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE ai_chat_messages ADD COLUMN profile_id TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS ai_chat_answers (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	profile_id      TEXT NOT NULL,
	conversation_id TEXT NOT NULL,
	turn_id         TEXT NOT NULL,
	agent_id        TEXT NOT NULL,
	question_id     TEXT NOT NULL,
	text            TEXT NOT NULL,
	created_at      TEXT NOT NULL,
	consumed_at     TEXT NOT NULL DEFAULT '',
	UNIQUE(profile_id, turn_id, agent_id, question_id)
	)`,
	`CREATE TABLE IF NOT EXISTS ai_chat_conversations (
	id TEXT PRIMARY KEY,
	profile_id TEXT NOT NULL,
	backend TEXT NOT NULL,
	workflow_context TEXT NOT NULL DEFAULT '',
	runtime_id TEXT NOT NULL DEFAULT '',
	provider_id TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	history_key TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS ai_chat_turns (
	id TEXT PRIMARY KEY,
	conversation_id TEXT NOT NULL,
	profile_id TEXT NOT NULL,
	owner_instance_id TEXT NOT NULL DEFAULT '',
	prompt TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'active',
	reason TEXT NOT NULL DEFAULT '',
	last_committed_seq INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	FOREIGN KEY (conversation_id) REFERENCES ai_chat_conversations(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS ai_chat_events (
	profile_id TEXT NOT NULL,
	conversation_id TEXT NOT NULL,
	turn_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	version INTEGER NOT NULL,
	type TEXT NOT NULL,
	payload TEXT NOT NULL,
	PRIMARY KEY (profile_id, conversation_id, turn_id, seq),
	FOREIGN KEY (conversation_id) REFERENCES ai_chat_conversations(id) ON DELETE CASCADE,
	FOREIGN KEY (turn_id) REFERENCES ai_chat_turns(id) ON DELETE CASCADE
	)`,
	`ALTER TABLE ai_chat_conversations ADD COLUMN mode TEXT NOT NULL DEFAULT 'assistant'`,
	`ALTER TABLE ai_chat_conversations ADD COLUMN cwd TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE ai_chat_conversations ADD COLUMN effort TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE ai_chat_conversations ADD COLUMN org_mode TEXT NOT NULL DEFAULT 'solo'`,
	`CREATE INDEX IF NOT EXISTS idx_ai_chat_conversations_profile ON ai_chat_conversations(profile_id, updated_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_chat_turns_conversation ON ai_chat_turns(conversation_id, profile_id, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS connections (
    id          TEXT PRIMARY KEY,
    platform    TEXT NOT NULL,
    method      TEXT NOT NULL,
    label       TEXT NOT NULL,
    account_id  TEXT NOT NULL DEFAULT '',
    data        TEXT NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'active',
    last_tested TEXT NOT NULL DEFAULT '',
    profile_id  TEXT NOT NULL DEFAULT 'default',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_connections_platform ON connections(platform);
CREATE INDEX IF NOT EXISTS idx_connections_status   ON connections(status);
CREATE INDEX IF NOT EXISTS idx_connections_profile  ON connections(profile_id);`,
	`ALTER TABLE connections ADD COLUMN vault_ref TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS oauth_refresh_locks (
    connection_id TEXT PRIMARY KEY,
    locked_at     TEXT NOT NULL
);`,
	`CREATE TABLE IF NOT EXISTS platform_oauth_credentials (
    platform      TEXT NOT NULL,
    profile_id    TEXT NOT NULL DEFAULT 'default',
    client_id     TEXT NOT NULL,
    client_secret TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL,
    PRIMARY KEY (platform, profile_id)
)`,
}

// runtimeDDL builds the tables legacyRuntimeDDL describes on d, the way the
// old runtime code did (a repeated ADD COLUMN was ignored), then runs
// ReconcileSchema (vault_keys).
func runtimeDDL(t *testing.T, d *Database) {
	t.Helper()
	for _, stmt := range legacyRuntimeDDL {
		if _, err := d.DB.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			t.Fatalf("legacy DDL %.60q: %v", stmt, err)
		}
	}
	if err := d.ReconcileSchema(context.Background()); err != nil {
		t.Fatalf("ReconcileSchema: %v", err)
	}
}

func userTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

// tableShape is PRAGMA table_info (in column order) plus the foreign keys.
func tableShape(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	var out []string
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("col %d %s %s notnull=%d default=%v pk=%d", cid, name, strings.ToUpper(ctype), notnull, dflt, pk))
	}
	rows.Close()
	rows, err = db.Query("PRAGMA foreign_key_list(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, seq int
		var ref, from, to, onUpd, onDel, match string
		if err := rows.Scan(&id, &seq, &ref, &from, &to, &onUpd, &onDel, &match); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("fk %s -> %s(%s) on delete %s", from, ref, to, onDel))
	}
	rows.Close()
	return out
}

// A database built by ApplyMigrations alone has every table the runtime
// DDL creates, with the same columns in the same order and the same keys.
func TestFreshMigrationsCoverRuntimeDDL(t *testing.T) {
	dir := t.TempDir()
	migrated, err := NewDatabase(filepath.Join(dir, "migrated.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if err := migrated.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}

	runtime, err := NewDatabase(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	runtimeDDL(t, runtime)

	have := map[string]bool{}
	for _, n := range userTables(t, migrated.DB) {
		have[n] = true
	}
	want := userTables(t, runtime.DB)
	for _, table := range []string{"ai_chat_messages", "ai_chat_answers", "ai_chat_conversations", "ai_chat_turns",
		"ai_chat_events", "connections", "oauth_refresh_locks", "platform_oauth_credentials", "vault_keys"} {
		found := false
		for _, n := range want {
			found = found || n == table
		}
		if !found {
			t.Errorf("runtime DDL no longer creates %s — update this test", table)
		}
	}
	for _, table := range want {
		if !have[table] {
			t.Errorf("table %s is created by runtime code but not by migrations", table)
			continue
		}
		got, exp := tableShape(t, migrated.DB, table), tableShape(t, runtime.DB, table)
		if !reflect.DeepEqual(got, exp) {
			t.Errorf("%s differs:\nmigrations: %v\nruntime:    %v", table, got, exp)
		}
	}
	if have["credentials"] {
		t.Error("legacy credentials table survived migration 096")
	}
}

// An existing database whose runtime code already created these tables
// (and holds rows in them) upgrades through 095/096 without any change to
// their shape or data.
func TestAdhocTablesMigrationIsNoOpOnExistingDatabase(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "existing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	applyMigrationsBelow(t, db, 95)
	runtimeDDL(t, db)

	seed := []string{
		`INSERT INTO connections (id, platform, method, label, created_at, updated_at, vault_ref) VALUES ('c1', 'github', 'oauth', 'me', 't', 't', 'v1')`,
		`INSERT INTO oauth_refresh_locks (connection_id, locked_at) VALUES ('c1', 't')`,
		`INSERT INTO ai_chat_messages (id, workflow_id, role, content, profile_id, created_at) VALUES ('m1', 'w', 'user', 'hi', 'default', 't')`,
		`INSERT INTO ai_chat_answers (profile_id, conversation_id, turn_id, agent_id, question_id, text, created_at) VALUES ('default', 'cv', 'tu', 'ag', 'q', 'yes', 't')`,
		`INSERT INTO platform_oauth_credentials (platform, profile_id, client_id, updated_at) VALUES ('probe', 'default', 'client', 't')`,
	}
	for _, s := range seed {
		if _, err := db.DB.Exec(s); err != nil {
			t.Fatalf("seeding %q: %v", s, err)
		}
	}

	tables := []string{"ai_chat_messages", "ai_chat_answers", "ai_chat_conversations", "connections", "oauth_refresh_locks", "platform_oauth_credentials"}
	before := map[string][]string{}
	for _, tb := range tables {
		before[tb] = tableShape(t, db.DB, tb)
	}

	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	for _, tb := range tables {
		if got := tableShape(t, db.DB, tb); !reflect.DeepEqual(got, before[tb]) {
			t.Errorf("%s changed:\nbefore: %v\nafter:  %v", tb, before[tb], got)
		}
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM connections WHERE vault_ref = 'v1'`:       1,
		`SELECT COUNT(*) FROM oauth_refresh_locks`:                      1,
		`SELECT COUNT(*) FROM ai_chat_messages`:                         1,
		`SELECT COUNT(*) FROM ai_chat_answers WHERE text = 'yes'`:       1,
		`SELECT COUNT(*) FROM platform_oauth_credentials`:               1,
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'credentials'`: 0,
	} {
		var n int
		if err := db.DB.QueryRow(q).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d (%v), want %d", q, n, err, want)
		}
	}
}

// A connections / ai_chat_conversations table created before its later
// columns existed gets them from ReconcileSchema, without the runtime code.
func TestReconcileAddsLaterColumns(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	applyMigrationsBelow(t, db, 95) // 040 creates the base ai_chat_conversations
	if _, err := db.DB.Exec(`CREATE TABLE connections (
		id TEXT PRIMARY KEY, platform TEXT NOT NULL, method TEXT NOT NULL, label TEXT NOT NULL,
		account_id TEXT NOT NULL DEFAULT '', data TEXT NOT NULL DEFAULT '{}', status TEXT NOT NULL DEFAULT 'active',
		last_tested TEXT NOT NULL DEFAULT '', profile_id TEXT NOT NULL DEFAULT 'default',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	for _, c := range addedColumns {
		ok, err := db.columnExists(context.Background(), c.table, c.column)
		if err != nil || !ok {
			t.Errorf("%s.%s missing after ApplyMigrations (%v)", c.table, c.column, err)
		}
	}
	// Idempotent.
	if err := db.ReconcileSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The stores create and alter nothing any more: on a migrated database,
// opening them and writing through them leaves the schema exactly as the
// migrations made it.
func TestStoresUseMigratedSchemaWithoutDDL(t *testing.T) {
	keyring.MockInit()
	db, err := NewDatabase(filepath.Join(t.TempDir(), "migrated.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	schema := func() []string {
		t.Helper()
		rows, err := db.DB.Query(`SELECT type || ' ' || name || ' ' || COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	before := schema()

	ctx := context.Background()
	store, err := ai.NewAIStore(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation("default", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, _, err := store.CreateTurn(conv.ID, "default", "turn-1", "inst", "hi"); err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}
	if err := store.AddAnswer("default", conv.ID, "turn-1", "agent-1", "q1", "yes"); err != nil {
		t.Fatalf("AddAnswer: %v", err)
	}
	if err := store.SaveChatMessage(ai.ChatMessage{WorkflowID: "w", Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("SaveChatMessage: %v", err)
	}
	if _, err := connections.NewManager(db.DB); err != nil {
		t.Fatal(err)
	}
	cs := connections.NewStore(db.DB)
	if err := cs.Save(ctx, &connections.Connection{Platform: "github", Method: connections.MethodAPIKey, Label: "gh",
		Data: map[string]interface{}{"token": "t"}}); err != nil {
		t.Fatalf("Save connection: %v", err)
	}
	if err := cs.SaveOAuthClient(ctx, "probe", "default", "client", ""); err != nil {
		t.Fatalf("SaveOAuthClient: %v", err)
	}
	if n, total, err := connections.MigrateConnectionsToVault(ctx, db.DB); err != nil {
		t.Fatalf("MigrateConnectionsToVault: %d/%d %v", n, total, err)
	}

	if after := schema(); !reflect.DeepEqual(after, before) {
		t.Errorf("the stores changed the schema:\nbefore: %v\nafter:  %v", before, after)
	}
}
