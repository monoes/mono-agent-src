package hilscrub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

const val = "vsecret-777"

type fix struct {
	db  *sql.DB
	ctx context.Context
}

func newFix(t *testing.T) *fix {
	t.Helper()
	keyring.MockInit()
	d, err := storage.NewDatabase(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), d.DB), "default")
	if _, err := secrets.Add(ctx, d.DB, "default", "secret", "vs", map[string]string{"secret": val}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(ctx, d.DB, "default", secrets.AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	return &fix{db: d.DB, ctx: ctx}
}

// card inserts an older send card whose editable text is text.
func (f *fix) card(t *testing.T, id, text string, hashes map[string]string) {
	t.Helper()
	ed, _ := json.Marshal(map[string]string{"text": text})
	nc, _ := json.Marshal(map[string]interface{}{"kind": "send", "config": map[string]string{"text": text}, "refHashes": hashes})
	if _, err := f.db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id, created_at)
		VALUES (?, ?, '', 'comm.slack', 'send:post', 'pending', '{}', ?, ?, 'default', datetime('now','-1 day'))`, id, "send:"+id, string(ed), string(nc)); err != nil {
		t.Fatal(err)
	}
}

func (f *fix) editable(t *testing.T, id string) string {
	t.Helper()
	var ed string
	_ = f.db.QueryRow(`SELECT editable_data FROM hil_pending WHERE id = ?`, id).Scan(&ed)
	var m map[string]string
	_ = json.Unmarshal([]byte(ed), &m)
	return m["text"]
}

// The value after a newline, a tab, a carriage return or inside <...> is
// replaced: the raw JSON has \n before it, which is not a word boundary.
func TestTheValueAfterAnEscapeIsScrubbed(t *testing.T) {
	for i, text := range []string{"key:\n" + val, "a\t" + val, "<" + val + ">", "line\r\n" + val + "\nend"} {
		f := newFix(t)
		id := "c" + string(rune('0'+i))
		f.card(t, id, text, nil)
		Ensure(f.ctx, f.db, "default")
		got := f.editable(t, id)
		if strings.Contains(got, val) || !strings.Contains(got, "[[held-secret:vs:") {
			t.Errorf("%q -> %q", text, got)
		}
	}
}

func TestOnlyAnOlderCardsRowsAreTouched(t *testing.T) {
	f := newFix(t)
	f.card(t, "old", "k "+val, nil)
	// A card created after the cutoff, and mail that is no card's.
	ed := `{"text":"guess ` + val + `"}`
	if _, err := f.db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id, created_at)
		VALUES ('new','send:new','','comm.slack','send:post','pending','{}',?, '{"kind":"send"}','default', datetime('now','+1 day'))`, ed); err != nil {
		t.Fatal(err)
	}
	Ensure(f.ctx, f.db, "default")
	if strings.Contains(f.editable(t, "old"), val) {
		t.Error("the older card kept the value")
	}
	if !strings.Contains(f.editable(t, "new"), val) {
		t.Error("a card newer than the cutoff was rewritten")
	}
}

func TestACardKeepsItsTagWhenAnOperatorTypedTheValueBesideTheReference(t *testing.T) {
	f := newFix(t)
	mac := secrets.HeldNewMAC()
	f.card(t, "c", "see "+secrets.HeldToken("vs", mac)+" and "+val, map[string]string{"vs": secrets.HeldMACHash(mac)})
	Ensure(f.ctx, f.db, "default")
	var nc string
	_ = f.db.QueryRow(`SELECT node_config FROM hil_pending WHERE id = 'c'`).Scan(&nc)
	var cfg struct {
		RefHashes map[string]string `json:"refHashes"`
	}
	_ = json.Unmarshal([]byte(nc), &cfg)
	if !secrets.HeldMACMatches(cfg.RefHashes["vs"], mac) || strings.Contains(f.editable(t, "c"), val) {
		t.Errorf("the original tag no longer opens its reference: %s", nc)
	}
}

// A secret granted after the first pass is picked up in the same process.
func TestASecretGrantedLaterIsScrubbedByTheNextPass(t *testing.T) {
	f := newFix(t)
	Ensure(f.ctx, f.db, "default")
	if _, err := secrets.Add(f.ctx, f.db, "default", "secret", "later", map[string]string{"secret": "later-value-9"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	f.card(t, "c", "k later-value-9", nil)
	// the card is an older one: the bound lies past it
	if _, err := f.db.Exec(`UPDATE settings SET value = '1000000' WHERE key = ?`, rowidKey); err != nil {
		t.Fatal(err)
	}
	Ensure(f.ctx, f.db, "default")
	if got := f.editable(t, "c"); strings.Contains(got, "later-value-9") {
		t.Errorf("%q", got)
	}
}

// The scrub reaches no mail, ever, and links to storage's read hook.
func TestTheMessageReadHookIsLinked(t *testing.T) {
	if storage.ReadHook == nil {
		t.Fatal("storage.ReadHook is not set: messages would be read unscrubbed")
	}
}

func (f *fix) marker(t *testing.T) string {
	t.Helper()
	var v string
	_ = f.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, markerKey("default")).Scan(&v)
	return v
}

// The marker is in the database: a new process (no state) finds the work done
// and reads neither the vault nor the rows.
func TestAFinishedPassIsRememberedInTheDatabase(t *testing.T) {
	f := newFix(t)
	f.card(t, "c", "k "+val, nil)
	Ensure(f.ctx, f.db, "default")
	if f.marker(t) == "" {
		t.Fatal("no marker after a clean pass")
	}
	states.Delete(fmt.Sprintf("%p|%s", f.db, "default")) // a new process
	f.card(t, "late", "k "+val, nil)
	before := passes.Load()
	Ensure(f.ctx, f.db, "default")
	if passes.Load() != before || !strings.Contains(f.editable(t, "late"), val) {
		t.Error("a new process repeated the pass although the marker says it is done")
	}
}

func TestNoGrantIsRememberedWithoutReadingTheVaultAgain(t *testing.T) {
	f := newFix(t)
	if _, err := f.db.Exec(`UPDATE profile_agent_secret_access SET expires_at = '2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	Ensure(f.ctx, f.db, "default")
	states.Delete(fmt.Sprintf("%p|%s", f.db, "default"))
	before := passes.Load()
	Ensure(f.ctx, f.db, "default")
	if passes.Load() != before || f.marker(t) == "" {
		t.Errorf("no grant: passes %d -> %d, marker %q", before, passes.Load(), f.marker(t))
	}
}

func TestConcurrentFirstReadsRunOnePass(t *testing.T) {
	f := newFix(t)
	f.card(t, "c", "k "+val, nil)
	before := passes.Load()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); Ensure(f.ctx, f.db, "default") }()
	}
	wg.Wait()
	if n := passes.Load() - before; n != 1 {
		t.Errorf("%d passes ran", n)
	}
}

// With the write lock held elsewhere a read does not wait the whole budget,
// the next read does not wait at all, and the value is masked in memory.
func TestALockedDatabaseDelaysNoReadAndTheValueIsMasked(t *testing.T) {
	f := newFix(t)
	f.card(t, "c", "k "+val, nil)
	MarkCutoff(f.ctx, f.db)
	if _, err := f.db.Exec(`UPDATE settings SET value = datetime('now','+1 day') WHERE key = ?`, cutoffKey); err != nil {
		t.Fatal(err)
	}
	conn, err := f.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		conn.Close()
	}()
	start := time.Now()
	Ensure(f.ctx, f.db, "default")
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("the first read waited %v", d)
	}
	start = time.Now()
	Ensure(f.ctx, f.db, "default")
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Errorf("the next read waited %v (no back-off)", d)
	}
	if got := Mask(f.db, "default", "c", "row: "+val); strings.Contains(got, val) {
		t.Errorf("Mask shows the value: %q", got)
	}
}

// Every raw reader of person_messages is named here with how it keeps a card's
// old value from being shown. A new reader fails until it is decided.
func TestEveryRawReaderOfMessagesIsAccountedFor(t *testing.T) {
	known := map[string]string{
		"internal/storage/repository_messages.go":    "ReadHook before each list/get",
		"internal/storage/person_messages_read.go":   "unread counts only",
		"internal/ai/chat/monoagent_tools_people.go": "storage.EnsureMessagesScrubbed before list/get",
		"internal/cli/application/assets.go":         "storage.EnsureMessagesScrubbed in queryMessages",
		"internal/cli/people/messages_classify.go":   "storage.EnsureMessagesScrubbed in selectInboxCandidates; existence check reads no text",
		"internal/cli/people/messages_drafts.go":     "reads the owner only",
		"internal/cli/profile/delete_cleanup.go":     "reads attachment metadata to delete files",
		"internal/commsync/queue.go":                 "writer; reads ids",
		"internal/commsync/status.go":                "card-joined status machinery; reads ids and metadata",
		"internal/summary/inbox.go":                  "counts only",
		"internal/hilscrub/hilscrub.go":              "the scrub itself",
	}
	root, _ := filepath.Abs("../..")
	found := map[string]bool{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(path)
		if rx.Match(b) {
			rel, _ := filepath.Rel(root, path)
			found[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	for f := range found {
		if _, ok := known[f]; !ok {
			t.Errorf("%s reads person_messages and is not accounted for: call storage.EnsureMessagesScrubbed before it reads text, then list it", f)
		}
	}
	// A listed reader must still make the call it is listed for: deleting it
	// is what this pins.
	for f, call := range map[string]string{
		"internal/storage/repository_messages.go":    "ReadHook(",
		"internal/ai/chat/monoagent_tools_people.go": "storage.EnsureMessagesScrubbed(",
		"internal/cli/application/assets.go":         "storage.EnsureMessagesScrubbed(",
		"internal/cli/people/messages_classify.go":   "storage.EnsureMessagesScrubbed(",
	} {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil || !strings.Contains(string(b), call) {
			t.Errorf("%s no longer calls %s before it reads messages", f, call)
		}
	}
}

var rx = regexp.MustCompile(`(FROM|JOIN) person_messages`)

// A card and its Communications row are scrubbed together or not at all: if
// the message cannot be written, the card keeps its old text too (so the next
// pass finds both).
func TestACardAndItsMessageAreScrubbedInOneTransaction(t *testing.T) {
	f := newFix(t)
	f.card(t, "c", "k "+val, nil)
	if _, err := f.db.Exec(`INSERT INTO people (id, platform_username, platform, full_name, profile_id) VALUES ('p1','u','slack','U','default')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO person_messages (id, person_id, source, direction, body, status, hil_ref, profile_id) VALUES ('m1','p1','slack','outbound',?,'pending_approval','c','default')`, "k "+val); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`CREATE TRIGGER no_msg_write BEFORE UPDATE ON person_messages BEGIN SELECT RAISE(ABORT, 'locked'); END`); err != nil {
		t.Fatal(err)
	}
	Ensure(f.ctx, f.db, "default")
	if !strings.Contains(f.editable(t, "c"), val) {
		t.Error("the card was changed although its message could not be: not one transaction")
	}
	if f.marker(t) != "" {
		t.Error("a failed pass was marked done")
	}
}
