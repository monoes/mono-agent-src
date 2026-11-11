package hilsend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
)

func (f *heldFix) addSecret(t *testing.T, name, value string) {
	t.Helper()
	if _, err := secrets.Add(f.ctx, f.db, "default", "secret", name, map[string]string{"secret": value}, "", "", ""); err != nil {
		t.Fatal(err)
	}
}

func (f *heldFix) oldCard(t *testing.T, text string) Payload {
	t.Helper()
	p, err := f.st.create(f.ctx, Payload{Action: ActionPost, Channel: "comm", NodeType: "comm.slack", Text: text,
		Account: Account{Platform: "slack"}, Recipient: Recipient{Name: "#general"}},
		replay{NodeType: "comm.slack", Config: map[string]interface{}{"operation": "post_message", "channel": "#general", "text": text}, TextField: "text"})
	if err != nil {
		t.Fatal(err)
	}
	f.beforeCutoff(t)
	return p
}

// beforeCutoff makes every card so far an older one: the scrub's cutoff moves
// past them.
func (f *heldFix) beforeCutoff(t *testing.T) {
	t.Helper()
	if _, err := f.db.Exec(`UPDATE settings SET value = datetime('now','+1 day') WHERE key = 'hilscrub.cutoff'`); err != nil {
		t.Fatal(err)
	}
}

// The card keeps hashes of its tags: the tag itself, which is what a reader
// would need to write a reference, is stored nowhere, and a tag for another
// granted secret cannot be made from what the card keeps.
func TestTheCardKeepsNoKeyForWritingAReference(t *testing.T) {
	f := newHeldFix(t, anyHost())
	id := f.hold(t, "key "+heldValue)
	p, _ := f.st.Get(f.ctx, id)
	mac := regexp.MustCompile(`\[\[held-secret:vs:([0-9a-f]{32})\]\]`).FindStringSubmatch(p.Text)[1]
	var nc string
	_ = f.db.QueryRow(`SELECT node_config FROM hil_pending WHERE id = ?`, id).Scan(&nc)
	if strings.Contains(nc, "refKey") || !strings.Contains(nc, "refHashes") {
		t.Errorf("node_config keeps a key: %s", nc)
	}
	var rp replay
	_ = json.Unmarshal([]byte(nc), &rp)
	for _, guess := range []string{rp.RefHashes["vs"], id, heldValue} {
		if guess == mac {
			continue
		}
		if secrets.HeldMACMatches(rp.RefHashes["vs"], guess) {
			t.Errorf("%q opens a reference", guess)
		}
	}
	if heldVerifier(rp.RefHashes)("vs2", mac) {
		t.Error("a tag for another name verified")
	}
}

// What the send echoes back is stored on the card: the value stays out of it.
func TestAnApprovalsResultAndErrorDoNotKeepTheResolvedValue(t *testing.T) {
	f := newHeldFix(t, anyHost())
	id := f.hold(t, "key "+heldValue)
	echo := func(_ context.Context, r Run) (json.RawMessage, error) {
		b, _ := json.Marshal(map[string]interface{}{"echo": r.Config["text"]})
		return b, nil
	}
	if _, err := Resolve(f.ctx, f.st, id, true, Edit{}, false, echo); err != nil {
		t.Fatal(err)
	}
	if all := f.everything(t); strings.Contains(all, heldValue) {
		t.Errorf("the result keeps the value: %s", all)
	}
	id2 := f.hold(t, "key "+heldValue)
	fail := func(_ context.Context, r Run) (json.RawMessage, error) {
		return nil, errors.New("rejected body: " + r.Config["text"].(string))
	}
	p, err := Resolve(f.ctx, f.st, id2, true, Edit{}, false, fail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Error, heldValue) || strings.Contains(f.everything(t), heldValue) {
		t.Errorf("the error keeps the value: %q", p.Error)
	}
}

// The grant lapses between the pre-check and the claim: nothing is sent and
// the card says why.
func TestAGrantGoneAfterTheClaimSendsNothing(t *testing.T) {
	f := newHeldFix(t, anyHost())
	id := f.hold(t, "key "+heldValue)
	if _, err := f.db.Exec(`CREATE TRIGGER lapse_on_claim AFTER UPDATE ON hil_pending WHEN NEW.status = 'sending'
		BEGIN UPDATE profile_agent_secret_access SET expires_at = '2000-01-01T00:00:00Z'; END`); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	p, err := Resolve(f.ctx, f.st, id, true, Edit{}, false, r.run)
	if len(r.calls) != 0 {
		t.Fatal("the send ran with the grant gone")
	}
	if err != nil || p.State != StateFailed || !strings.Contains(p.Error, "agent access") {
		t.Errorf("state=%s err=%v card error=%q", p.State, err, p.Error)
	}
}

func TestTheScrubReachesEditedDataAndFinishedRowsWithAnUnusableName(t *testing.T) {
	f := newHeldFix(t, anyHost())
	const odd = "oddvalue-4242"
	f.addSecret(t, "bad name!", odd)
	if _, err := f.db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, edited_data, node_config, profile_id)
		VALUES ('wfcard', 'ex1', 'w', 'n', 'Review', 'approved', '{}', '{}', ?, '{}', 'default')`, `{"k":"`+heldValue+`"}`); err != nil {
		t.Fatal(err)
	}
	fin := f.oldCard(t, "done "+heldValue+" and "+odd)
	if _, err := f.db.Exec(`UPDATE hil_pending SET status = 'sent' WHERE id = ?`, fin.ID); err != nil {
		t.Fatal(err)
	}
	pend := f.oldCard(t, "wait "+odd)
	_, _ = f.st.List(f.ctx)
	all := f.everything(t)
	if strings.Contains(all, heldValue) || strings.Contains(all, odd) {
		t.Errorf("plaintext left after the scrub: %s", all)
	}
	if fp, _ := f.st.Get(f.ctx, fin.ID); fp.State != StateSent || !strings.Contains(fp.Text, "done") {
		t.Errorf("the finished row should be masked, not wiped or rejected: %s %q", fp.State, fp.Text)
	}
	got, _ := f.st.Get(f.ctx, pend.ID)
	if got.State != StateRejected {
		t.Errorf("a pending send holding an unusable name is %s, want rejected", got.State)
	}
}

func TestTheScrubDoesNotRewriteOrdinaryTextAndSkipsAPlainWord(t *testing.T) {
	f := newHeldFix(t, anyHost())
	f.addSecret(t, "word", "internalword")
	f.addSecret(t, "tok", "tok-abc123")
	c := f.oldCard(t, "the internalword team, xtok-abc123y and tok-abc123 end")
	got, _ := f.st.Get(f.ctx, c.ID)
	if !strings.Contains(got.Text, "internalword team") || !strings.Contains(got.Text, "xtok-abc123y") {
		t.Errorf("ordinary text was rewritten: %q", got.Text)
	}
	if strings.Contains(got.Text, " tok-abc123 end") {
		t.Errorf("the whole-token occurrence was not scrubbed: %q", got.Text)
	}
}

// Every reader reaches clean rows: a message read does not wait for a card verb.
func TestReadingTheMessageScrubsAnOlderRow(t *testing.T) {
	f := newHeldFix(t, anyHost())
	c := f.oldCard(t, "key "+heldValue)
	var mid string
	if err := f.db.QueryRow(`SELECT id FROM person_messages WHERE hil_ref = ?`, c.ID).Scan(&mid); err != nil {
		t.Fatal(err)
	}
	m, err := f.sdb.GetPersonMessage(mid)
	if err != nil || strings.Contains(m.Body, heldValue) {
		t.Errorf("message get: %q %v", m.Body, err)
	}
}

// A refused approval does not keep the bad edit it came with.
func TestARefusedApprovalDoesNotSaveItsEdit(t *testing.T) {
	f := newHeldFix(t, anyHost())
	id := f.hold(t, "key "+heldValue)
	bad := "again @secret:vs"
	r := &fakeRunner{}
	if _, err := Resolve(f.ctx, f.st, id, true, Edit{Text: &bad}, false, r.run); !errors.Is(err, secrets.ErrLiteralRef) {
		t.Fatalf("err=%v", err)
	} else if !strings.Contains(err.Error(), "reject it and have the agent prepare it again") {
		t.Errorf("the refusal names no remedy: %v", err)
	}
	if p, _ := f.st.Get(f.ctx, id); strings.Contains(p.Text, "again") {
		t.Errorf("the refused edit was saved: %q", p.Text)
	}
}

func TestFriendlyShowsTheNameOnly(t *testing.T) {
	tok := secrets.HeldToken("vs", strings.Repeat("a", 32))
	if got := Friendly("key " + tok + "!"); got != "key <vs secret, held>!" {
		t.Errorf("%q", got)
	}
}

// A longer value that holds a shorter one is replaced whole.
func TestTheScrubReplacesTheLongestValueFirst(t *testing.T) {
	f := newHeldFix(t, anyHost())
	f.addSecret(t, "short", "abcdef-1234")
	f.addSecret(t, "long", "abcdef-1234-xyz9")
	c := f.oldCard(t, "k abcdef-1234-xyz9 e")
	got, _ := f.st.Get(f.ctx, c.ID)
	if !strings.Contains(got.Text, "held-secret:long:") || strings.Contains(got.Text, "held-secret:short:") {
		t.Errorf("%q", got.Text)
	}
}

// The scrub never reads a row that is not a card's: mail the person received
// keeps a number that equals a granted secret.
func TestTheScrubLeavesAMessageThatIsNoCardsAlone(t *testing.T) {
	f := newHeldFix(t, anyHost())
	f.addSecret(t, "pin", "123456")
	c := f.oldCard(t, "key "+heldValue)
	if _, err := f.db.Exec(`INSERT INTO people (id, platform_username, platform, full_name, profile_id) VALUES ('p1','u','email','U','default')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO person_messages (id, person_id, source, direction, body, status, profile_id) VALUES ('m1','p1','email','inbound','Your order 123456 shipped; code 123456.','received','default')`); err != nil {
		t.Fatal(err)
	}
	_, _ = f.st.Get(f.ctx, c.ID)
	var body string
	_ = f.db.QueryRow(`SELECT body FROM person_messages WHERE id = 'm1'`).Scan(&body)
	if body != "Your order 123456 shipped; code 123456." {
		t.Errorf("inbound mail was rewritten: %q", body)
	}
}

// A card created after the cutoff is never rewritten, so planting guesses in
// a new card and listing it shows nothing about which one is a secret; and the
// queue-time guard names only a secret the run itself read.
func TestPlantedGuessesComeBackUnchanged(t *testing.T) {
	f := newHeldFix(t, anyHost()) // the run read vs
	f.addSecret(t, "other", "483917")
	guesses := "483915 483916 483917 483918 and vsecret-778"
	id := f.hold(t, "guesses: "+guesses)
	_, _ = f.st.List(f.ctx)
	got, _ := f.st.Get(f.ctx, id)
	if !strings.Contains(got.Text, guesses) || strings.Contains(got.Text, "held-secret:other") {
		t.Errorf("a planted guess came back changed: %q", got.Text)
	}
}

// A read-only handle skips the scrub silently and never fails the read.
func TestAReadOnlyHandleSkipsTheScrubWithoutFailing(t *testing.T) {
	f := newHeldFix(t, anyHost())
	c := f.oldCard(t, "key "+heldValue)
	ro, err := sql.Open("sqlite", "file:"+f.path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	got, err := (Store{DB: ro, Profile: "default"}).Get(f.ctx, c.ID)
	if err != nil {
		t.Fatalf("a read-only read failed: %v", err)
	}
	_ = got
	if !strings.Contains(f.everything(t), heldValue) {
		t.Error("the read-only handle should not have been able to scrub")
	}
	if _, err := f.st.Get(f.ctx, c.ID); err != nil || strings.Contains(f.everything(t), heldValue) {
		t.Errorf("the next read that can write scrubs: %v", err)
	}
}

// An approval's result stays valid JSON when the secret is a number.
func TestMaskJSONKeepsTheResultValid(t *testing.T) {
	got := maskJSON([]string{"123456"}, json.RawMessage(`{"code":123456,"s":"pin 123456","n":[1,2]}`))
	var m map[string]interface{}
	if err := json.Unmarshal(got, &m); err != nil || strings.Contains(string(got), "123456") {
		t.Errorf("%s %v", got, err)
	}
	if g := maskJSON([]string{"abcdef"}, json.RawMessage(`not json abcdef`)); !json.Valid(g) || strings.Contains(string(g), "abcdef") {
		t.Errorf("%s", g)
	}
}

// A scrub that commits between claim's read and its write is not overwritten:
// the write is guarded by the node_config it read, and claim reads again.
func TestAClaimDoesNotOverwriteARowScrubbedUnderIt(t *testing.T) {
	f := newHeldFix(t, anyHost())
	id := f.hold(t, "key "+heldValue)
	n := 0
	betweenReadAndWrite = func() {
		n++
		if n == 1 {
			_, _ = f.db.Exec(`UPDATE hil_pending SET node_config = json_set(node_config, '$.address', 'scrubbedUnderIt') WHERE id = ?`, id)
		}
	}
	defer func() { betweenReadAndWrite = nil }()
	_, _, _, claimed, err := f.st.claim(f.ctx, id, false)
	if err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	var nc string
	_ = f.db.QueryRow(`SELECT node_config FROM hil_pending WHERE id = ?`, id).Scan(&nc)
	if !strings.Contains(nc, "scrubbedUnderIt") {
		t.Errorf("the claim overwrote the concurrent change: %s", nc)
	}
}

// A secret that is a number: the stored result stays valid JSON.
func TestAnApprovalWhoseSecretIsANumberStoresValidJSON(t *testing.T) {
	f := newHeldFix(t, anyHost())
	f.addSecret(t, "pin", "123456")
	if _, err := secrets.Resolve(f.ctx, f.db, "default", "@secret:pin"); err != nil {
		t.Fatal(err)
	}
	id := f.hold(t, "pin 123456")
	echo := func(_ context.Context, r Run) (json.RawMessage, error) {
		return json.RawMessage(`{"code":123456}`), nil
	}
	p, err := Resolve(f.ctx, f.st, id, true, Edit{}, false, echo)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p.Result)
	if !json.Valid(b) || strings.Contains(f.everything(t), "123456") {
		t.Errorf("result %s", b)
	}
}

// While the scrub cannot write (a lock held elsewhere) a card read shows the
// value masked, not the stored plaintext.
func TestACardReadWhileTheScrubCannotWriteIsMasked(t *testing.T) {
	f := newHeldFix(t, anyHost())
	c := f.oldCard(t, "key "+heldValue)
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
	got, err := f.st.Get(f.ctx, c.ID)
	if err != nil || strings.Contains(got.Text, heldValue) {
		t.Errorf("Get while locked: %q %v", got.Text, err)
	}
	list, _ := f.st.List(f.ctx)
	for _, it := range list {
		if strings.Contains(it.Text, heldValue) {
			t.Errorf("List while locked shows the value")
		}
	}
}
