package hilsend

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

const heldValue = "vsecret-777"

type heldFix struct {
	db  *sql.DB
	ctx context.Context // the agent's run, under the grant
	st  Store
}

func newHeldFix(t *testing.T, g secrets.AccessGrant) *heldFix {
	t.Helper()
	keyring.MockInit()
	asAgent(t)
	d, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	base := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), d.DB), "default")
	if _, err := secrets.Add(base, d.DB, "default", "secret", "vs", map[string]string{"secret": heldValue}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(base, d.DB, "default", g); err != nil {
		t.Fatal(err)
	}
	ctx := secrets.WithAccessNode(secrets.AllowResolving(base, d.DB, "default", secrets.AccessOrigin{WorkflowID: "wf"}), "n1", "Post")
	if _, err := secrets.Resolve(ctx, d.DB, "default", "@secret:vs"); err != nil {
		t.Fatal(err)
	}
	return &heldFix{db: d.DB, ctx: ctx, st: Store{DB: d.DB, Profile: "default"}}
}

func (f *heldFix) hold(t *testing.T, text string) string {
	t.Helper()
	cfg := map[string]interface{}{"operation": "post_message", "channel": "#general", "text": text}
	outs, handled, err := Guard(f.ctx, "comm.slack", workflow.NodeInput{}, cfg)
	if err != nil || !handled || len(outs) == 0 {
		t.Fatalf("not held: handled=%v err=%v", handled, err)
	}
	return outs[0].Items[0].JSON["hil_id"].(string)
}

func (f *heldFix) everything(t *testing.T) string {
	t.Helper()
	var all strings.Builder
	for _, q := range []string{
		`SELECT COALESCE(readonly_data,'')||COALESCE(editable_data,'')||COALESCE(edited_data,'')||COALESCE(node_config,'') FROM hil_pending`,
		`SELECT COALESCE(body,'')||COALESCE(subject,'') FROM person_messages`,
		`SELECT workflow_id||execution_id||node_id||node_name||secret_name FROM profile_agent_secret_audit`,
	} {
		rows, err := f.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			all.WriteString(s + "\n")
		}
		rows.Close()
	}
	return all.String()
}

func anyHost() secrets.AccessGrant { return secrets.AccessGrant{AnyHost: true} }

func TestTheCardShowsTheReferenceNameAndNeverTheValueAnywhere(t *testing.T) {
	f := newHeldFix(t, anyHost())
	id := f.hold(t, "key is "+heldValue)
	if all := f.everything(t); strings.Contains(all, heldValue) {
		t.Errorf("a stored place holds the value: %s", all)
	}
	p, err := f.st.Get(f.ctx, id)
	if err != nil || !strings.Contains(p.Text, "[[held-secret:vs:") {
		t.Fatalf("the card text should name the secret: %q %v", p.Text, err)
	}
	list, _ := f.st.List(f.ctx)
	for _, it := range list {
		if strings.Contains(it.Text+it.Subject, heldValue) {
			t.Error("List shows the value")
		}
	}
	if fp := FromRow(id, "pending", "{}", `{"text":"`+p.Text+`"}`, "{}"); fp != nil && strings.Contains(fp.Text, heldValue) {
		t.Error("FromRow shows the value")
	}
}

func TestApprovalResolvesTheReferenceAndAuditsIt(t *testing.T) {
	f := newHeldFix(t, anyHost())
	id := f.hold(t, "key is "+heldValue)
	r := &fakeRunner{}
	if _, err := Resolve(f.ctx, f.st, id, true, Edit{}, false, r.run); err != nil || len(r.calls) != 1 {
		t.Fatalf("approve: %v calls=%d", err, len(r.calls))
	}
	if got := r.calls[0].Config["text"]; got != "key is "+heldValue {
		t.Errorf("the approved send carried %q", got)
	}
	var n int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM profile_agent_secret_audit WHERE workflow_id = 'hil-approval' AND execution_id = ? AND secret_name = 'vs'`, id).Scan(&n)
	if n != 1 {
		t.Errorf("the approval's read is audited %d times", n)
	}
	if strings.Contains(f.everything(t), heldValue) {
		t.Error("the value was stored by the approval")
	}
}

// An agent that types a reference itself gets nothing resolved: the approval
// refuses, the card stays pending and nothing is sent.
func TestATypedReferenceIsNeverResolvedAtApproval(t *testing.T) {
	for _, typed := range []string{
		"@secret:vs", "x @SECRET:vs", "x @Secret:vs", "@ secret : vs", "@secret :vs",
		"%40secret%3Avs", `{"k":"@secret:vs"}`, "line\n@secret:vs",
	} {
		f := newHeldFix(t, anyHost())
		id := f.hold(t, "hi "+typed)
		r := &fakeRunner{}
		_, err := Resolve(f.ctx, f.st, id, true, Edit{}, false, r.run)
		if !errors.Is(err, secrets.ErrLiteralRef) || len(r.calls) != 0 {
			t.Errorf("%q: err=%v calls=%d", typed, err, len(r.calls))
		}
		if p, _ := f.st.Get(f.ctx, id); p.State != StatePending {
			t.Errorf("%q: the refused card is %s, want pending", typed, p.State)
		}
	}
}

func TestATypedReferenceInAFieldOrAKeyIsRefusedAndASplitOneNeverResolves(t *testing.T) {
	f := newHeldFix(t, anyHost())
	cfg := map[string]interface{}{"operation": "post_message", "channel": "@secret:vs", "text": "hi",
		"headers": map[string]interface{}{"@secret:vs": "x", "X-A": "@sec", "X-B": "ret:vs"}}
	outs, _, err := Guard(f.ctx, "comm.slack", workflow.NodeInput{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := outs[0].Items[0].JSON["hil_id"].(string)
	r := &fakeRunner{}
	if _, err := Resolve(f.ctx, f.st, id, true, Edit{}, false, r.run); !errors.Is(err, secrets.ErrLiteralRef) || len(r.calls) != 0 {
		t.Errorf("a reference in a field or key: err=%v calls=%d", err, len(r.calls))
	}
	// Split across fields: no field holds a reference, so nothing resolves.
	g := f.hold(t, "hi")
	cfg2 := map[string]interface{}{"a": "@sec", "b": "ret:vs"}
	if _, _, err := f.st.resolveHeldReplay(f.ctx, Payload{ID: g}, replay{Config: cfg2}, cfg2, false); err != nil {
		t.Fatal(err)
	}
}

func TestAForgedOrCopiedTokenIsRefused(t *testing.T) {
	f := newHeldFix(t, anyHost())
	forged := secrets.HeldToken("vs", strings.Repeat("a", 32))
	id := f.hold(t, "hi "+forged)
	r := &fakeRunner{}
	if _, err := Resolve(f.ctx, f.st, id, true, Edit{}, false, r.run); !errors.Is(err, secrets.ErrHeldRefused) || len(r.calls) != 0 {
		t.Errorf("forged: err=%v calls=%d", err, len(r.calls))
	}
	// A real token of card A copied into card B.
	a := f.hold(t, "key "+heldValue)
	pa, _ := f.st.Get(f.ctx, a)
	tok := regexp.MustCompile(`\[\[held-secret:[^\]]+\]\]`).FindString(pa.Text)
	b := f.hold(t, "copy "+tok)
	if _, err := Resolve(f.ctx, f.st, b, true, Edit{}, false, r.run); !errors.Is(err, secrets.ErrHeldRefused) || len(r.calls) != 0 {
		t.Errorf("copied: err=%v calls=%d", err, len(r.calls))
	}
}

func TestApprovalIsRefusedWhenTheGrantIsGoneOrNoLongerCovers(t *testing.T) {
	cases := map[string]func(t *testing.T, f *heldFix){
		"off": func(t *testing.T, f *heldFix) {
			if err := secrets.SetAgentAccess(f.ctx, f.db, "default", false); err != nil {
				t.Fatal(err)
			}
		},
		"narrowed": func(t *testing.T, f *heldFix) {
			if err := secrets.SetAgentAccessGrant(f.ctx, f.db, "default", secrets.AccessGrant{AnyHost: true, Secrets: []string{"other"}}); err != nil {
				t.Fatal(err)
			}
		},
		"host-bound": func(t *testing.T, f *heldFix) {
			if err := secrets.SetAgentAccessGrant(f.ctx, f.db, "default", secrets.AccessGrant{Hosts: []string{"api.example.test"}}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		f := newHeldFix(t, anyHost())
		id := f.hold(t, "key "+heldValue)
		change(t, f)
		r := &fakeRunner{}
		_, err := Resolve(f.ctx, f.st, id, true, Edit{}, false, r.run)
		if !errors.Is(err, secrets.ErrHeldRefused) || len(r.calls) != 0 {
			t.Errorf("%s: err=%v calls=%d", name, err, len(r.calls))
		}
		if p, _ := f.st.Get(f.ctx, id); p.State != StatePending {
			t.Errorf("%s: the refused card is %s, want pending (held)", name, p.State)
		}
		var n int
		_ = f.db.QueryRow(`SELECT COUNT(*) FROM profile_agent_secret_audit WHERE workflow_id = 'hil-approval'`).Scan(&n)
		if n != 0 {
			t.Errorf("%s: a refused approval was audited as a read", name)
		}
	}
}

func TestEditingBeforeApprovalKeepsTheOwnTextAndALeftOutReferenceWorks(t *testing.T) {
	f := newHeldFix(t, anyHost())
	id := f.hold(t, "key "+heldValue)
	r := &fakeRunner{}
	edit := "my own words, and I type " + heldValue + " myself"
	if _, err := Resolve(f.ctx, f.st, id, true, Edit{Text: &edit}, false, r.run); err != nil {
		t.Fatal(err)
	}
	if got := r.calls[0].Config["text"]; got != edit {
		t.Errorf("sent %q, want the person's text", got)
	}
	var n int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM profile_agent_secret_audit WHERE workflow_id = 'hil-approval'`).Scan(&n)
	if n != 0 {
		t.Error("an edit that left the reference out still read the secret")
	}
	// Edit after a refusal: the person typing @secret: is refused, removing it works.
	g := f.hold(t, "x @secret:vs")
	bad := "again @secret:vs"
	if _, err := Resolve(f.ctx, f.st, g, true, Edit{Text: &bad}, false, r.run); !errors.Is(err, secrets.ErrLiteralRef) {
		t.Errorf("a typed reference in an edit: %v", err)
	}
	ok := "fixed"
	if _, err := Resolve(f.ctx, f.st, g, true, Edit{Text: &ok}, false, r.run); err != nil || len(r.calls) != 2 {
		t.Errorf("the corrected edit: %v calls=%d", err, len(r.calls))
	}
}

// An encoded form of the value cannot be held as a reference: nothing is queued.
func TestACardCarryingAnEncodedSecretIsNotQueued(t *testing.T) {
	f := newHeldFix(t, anyHost())
	cfg := map[string]interface{}{"operation": "post_message", "channel": "#general", "text": "k " + base64.StdEncoding.EncodeToString([]byte(heldValue))}
	_, handled, err := Guard(f.ctx, "comm.slack", workflow.NodeInput{}, cfg)
	if err == nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	var n int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM hil_pending`).Scan(&n)
	if n != 0 || strings.Contains(err.Error(), heldValue) {
		t.Errorf("rows=%d err=%v", n, err)
	}
}

// Rows written before the guard existed hold the value: reading them scrubs
// them, a pending one stays approvable.
func TestAnOlderRowHoldingTheValueIsScrubbedAndStaysApprovable(t *testing.T) {
	f := newHeldFix(t, anyHost())
	old, err := f.st.create(f.ctx, Payload{Action: ActionPost, Channel: "comm", NodeType: "comm.slack", Text: "key " + heldValue,
		Account: Account{Platform: "slack"}, Recipient: Recipient{Name: "#general"}},
		replay{NodeType: "comm.slack", Config: map[string]interface{}{"operation": "post_message", "channel": "#general", "text": "key " + heldValue}, TextField: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.everything(t), heldValue) {
		t.Fatal("the fixture row should hold the value")
	}
	got, err := f.st.Get(f.ctx, old.ID)
	if err != nil || strings.Contains(got.Text, heldValue) || !strings.Contains(got.Text, "[[held-secret:vs:") {
		t.Fatalf("scrub: %q %v", got.Text, err)
	}
	if strings.Contains(f.everything(t), heldValue) {
		t.Errorf("a place still holds the value after the scrub: %s", f.everything(t))
	}
	r := &fakeRunner{}
	if _, err := Resolve(f.ctx, f.st, old.ID, true, Edit{}, false, r.run); err != nil || r.calls[0].Config["text"] != "key "+heldValue {
		t.Errorf("approving the scrubbed row: %v %v", err, r.calls)
	}
	// Idempotent: a second read changes nothing.
	again, _ := f.st.Get(f.ctx, old.ID)
	if again.Text != got.Text {
		t.Error("the scrub is not idempotent")
	}
}

// Every writer of hil_pending is named here with why it cannot carry a
// granted secret's value. A new writer fails this test until it is decided.
func TestEveryHilPendingWriterIsAccountedFor(t *testing.T) {
	known := map[string]string{
		"internal/hilsend/store.go":               "send cards: createHeld puts references in place of values",
		"internal/nodes/control/human_in_loop.go": "workflow HIL node: refuses to queue an item holding a tracked value",
		"internal/browserctl/hil.go":              "browser submit card: a page's form, never resolves @secret (package does not import secrets)",
		"internal/browserctl/site.go":             "browser site card: origin and url only",
		"internal/extension/pairclaims.go":        "pairing code card: no run data",
	}
	root, _ := filepath.Abs("../..")
	found := map[string]bool{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), "INSERT INTO hil_pending") {
			rel, _ := filepath.Rel(root, path)
			found[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	for f := range found {
		if _, ok := known[f]; !ok {
			t.Errorf("%s writes hil_pending and is not accounted for: decide how it keeps a granted secret's value out, then list it", f)
		}
	}
	for f := range known {
		if !found[f] {
			t.Errorf("%s is listed but no longer writes hil_pending", f)
		}
	}
}
