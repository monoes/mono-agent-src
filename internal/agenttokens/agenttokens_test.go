package agenttokens

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/testdb"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(testdb.Open(t).DB)
}

func TestScopeLadder(t *testing.T) {
	cases := []struct {
		have, need Scope
		want       bool
	}{
		{ScopeRead, ScopeRead, true}, {ScopeRead, ScopeWrite, false}, {ScopeRead, ScopeRun, false}, {ScopeRead, ScopeFull, false},
		{ScopeWrite, ScopeRead, true}, {ScopeWrite, ScopeWrite, true}, {ScopeWrite, ScopeRun, false}, {ScopeWrite, ScopeFull, false},
		{ScopeRun, ScopeWrite, true}, {ScopeRun, ScopeRun, true}, {ScopeRun, ScopeFull, false},
		{ScopeFull, ScopeFull, true}, {ScopeFull, ScopeRead, true},
		{"", ScopeRead, false}, {"admin", ScopeRead, false}, {ScopeFull, "", false},
	}
	for _, c := range cases {
		if got := c.have.Allows(c.need); got != c.want {
			t.Errorf("%q.Allows(%q) = %v, want %v", c.have, c.need, got, c.want)
		}
	}
}

func TestCreateNeverIssuesFullOrUnknownScope(t *testing.T) {
	s := newStore(t)
	for _, sc := range []Scope{ScopeFull, "admin", ""} {
		if _, _, err := s.Create(context.Background(), "default", "x", sc, time.Hour); err == nil {
			t.Errorf("scope %q was issued", sc)
		}
	}
}

func TestCreateStoresOnlyTheHashAndVerifies(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tok, secret, err := s.Create(ctx, "default", "ci", ScopeRead, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, Prefix) || tok.Scope != ScopeRead || tok.ExpiresAt == nil {
		t.Fatalf("token %+v secret %q", tok, secret)
	}
	var dump string
	rows, _ := s.db.Query(`SELECT id||name||scope||prefix||token_hash FROM agent_tokens`)
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		dump += v
	}
	rows.Close()
	if strings.Contains(dump, secret) {
		t.Fatal("the token is stored in the clear")
	}
	if !strings.Contains(dump, Hash(secret)) {
		t.Fatal("the hash is not stored")
	}
	got, err := s.Verify(ctx, secret)
	if err != nil || got.ID != tok.ID {
		t.Fatalf("Verify = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "mat-nope", secret + "x", secret[:len(secret)-1]} {
		if _, err := s.Verify(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("Verify(%q) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestExpiredAndRevokedTokensAreInvalid(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	_, exp, _ := s.Create(ctx, "default", "short", ScopeWrite, time.Hour)
	_, rev, _ := s.Create(ctx, "default", "gone", ScopeWrite, 0)
	_, forever, _ := s.Create(ctx, "default", "forever", ScopeWrite, 0)

	if _, err := s.Verify(ctx, exp); err != nil {
		t.Fatalf("fresh token refused: %v", err)
	}
	now = now.Add(time.Hour) // the expiry instant itself is already over
	if _, err := s.Verify(ctx, exp); !errors.Is(err, ErrInvalid) {
		t.Errorf("expired token: %v", err)
	}
	if _, err := s.Verify(ctx, forever); err != nil {
		t.Errorf("a token with no expiry was refused: %v", err)
	}
	if _, err := s.Revoke(ctx, "default", "gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, rev); !errors.Is(err, ErrInvalid) {
		t.Errorf("revoked token: %v", err)
	}
	// revoking twice is fine; the name is free again
	if _, err := s.Revoke(ctx, "default", "gone"); err == nil {
		t.Log("second revoke by name: not found (name resolves only unrevoked)")
	}
	if _, _, err := s.Create(ctx, "default", "gone", ScopeRead, 0); err != nil {
		t.Errorf("a revoked token's name was not reusable: %v", err)
	}
}

func TestNamesAreValidatedAndUniquePerProfile(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, n := range []string{"", " x", "a b", "mat-abc", "xMAT-y", strings.Repeat("a", 65)} {
		if _, _, err := s.Create(ctx, "default", n, ScopeRead, 0); !errors.Is(err, ErrInvalidName) {
			t.Errorf("name %q: %v", n, err)
		}
	}
	if _, _, err := s.Create(ctx, "default", "a", ScopeRead, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(ctx, "default", "a", ScopeRead, 0); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate: %v", err)
	}
	if _, _, err := s.Create(ctx, "other", "a", ScopeRead, 0); err != nil {
		t.Errorf("same name in another profile: %v", err)
	}
}

func TestAuditHoldsTheNameNeverTheToken(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tok, secret, _ := s.Create(ctx, "default", "bot", ScopeRead, 0)
	id, err := s.BeginUse(ctx, tok, "GET", "/profiles/default/workflows")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishUse(ctx, id, 200); err != nil {
		t.Fatal(err)
	}
	uses, err := s.Audit(ctx, tok, 10)
	if err != nil || len(uses) != 1 || uses[0].TokenName != "bot" || uses[0].Status != 200 || uses[0].Method != "GET" {
		t.Fatalf("uses = %+v, %v", uses, err)
	}
	var dump string
	rows, _ := s.db.Query(`SELECT token_id||token_name||profile_id||method||path FROM agent_token_audit`)
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		dump += v
	}
	rows.Close()
	if strings.Contains(dump, secret) {
		t.Fatal("audit holds the token")
	}
}

// A path with control characters is stored escaped and cut short, and printed without them.
func TestAuditPathIsEscapedTruncatedAndSanitized(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tok, _, _ := s.Create(ctx, "default", "bot", ScopeRead, 0)
	id, err := s.BeginUse(ctx, tok, "POST", "/a/\n\x1b[2Jevil/"+strings.Repeat("x", 500))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.FinishUse(ctx, id, 200)
	var stored string
	if err := s.db.QueryRow(`SELECT path FROM agent_token_audit`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(stored, "\n\x1b") || len(stored) > 200 {
		t.Errorf("stored path %q (%d bytes)", stored, len(stored))
	}
	// A row written before this rule holds raw bytes: reading still prints them harmlessly.
	_, _ = s.db.Exec(`INSERT INTO agent_token_audit (at, token_id, token_name, profile_id, method, path, status) VALUES ('2026-01-01T00:00:00.000Z', ?, 'bot', 'default', 'GET', ?, 200)`, tok.ID, "/x\n\x1b[2Jy")
	uses, _ := s.Audit(ctx, tok, 10)
	for _, u := range uses {
		if strings.ContainsAny(u.Path, "\n\x1b") {
			t.Errorf("audit printed %q", u.Path)
		}
	}
}

func TestAuditTableIsCapped(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	defer func(n int) { auditKeep = n }(auditKeep)
	auditKeep = 20
	tok, _, _ := s.Create(ctx, "default", "bot", ScopeRead, 0)
	for i := 0; i < 50; i++ {
		id, err := s.BeginUse(ctx, tok, "GET", "/p")
		if err != nil {
			t.Fatal(err)
		}
		_ = s.FinishUse(ctx, id, 200)
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM agent_token_audit`).Scan(&n)
	if n != 20 {
		t.Errorf("audit rows = %d, want 20", n)
	}
}

func TestAuditPruneIsPerToken(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	defer func(n int) { auditKeepPerToken = n }(auditKeepPerToken)
	auditKeepPerToken = 5
	quiet, _, _ := s.Create(ctx, "default", "quiet", ScopeRead, 0)
	noisy, _, _ := s.Create(ctx, "default", "noisy", ScopeRead, 0)
	for i := 0; i < 3; i++ {
		id, _ := s.BeginUse(ctx, quiet, "GET", "/q")
		_ = s.FinishUse(ctx, id, 200)
	}
	for i := 0; i < 40; i++ {
		id, _ := s.BeginUse(ctx, noisy, "GET", "/n")
		_ = s.FinishUse(ctx, id, 200)
	}
	count := func(tok Token) int {
		var n int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM agent_token_audit WHERE token_id = ?`, tok.ID).Scan(&n)
		return n
	}
	if n := count(quiet); n != 3 {
		t.Errorf("quiet token rows = %d, want 3 (another token pushed them out)", n)
	}
	if n := count(noisy); n != 5 {
		t.Errorf("noisy token rows = %d, want 5", n)
	}
}

func TestBoundTokenCarriesItsBindingAndNeverRun(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	b := Binding{TaskID: "at-1", ConversationID: "conv-1"}
	tok, secret, err := s.CreateBound(ctx, "default", "turn", ScopeWrite, time.Hour, b)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Verify(ctx, secret)
	if err != nil || !got.Bound() || !got.BoundTo("at-1", "conv-1") || got.ID != tok.ID {
		t.Fatalf("verify: %+v err %v", got, err)
	}
	if got.BoundTo("at-2", "conv-1") || got.BoundTo("at-1", "conv-2") || got.BoundTo("", "") {
		t.Errorf("binding matched something else: %+v", got)
	}
	// Half a binding, and a scope a task has no business with, are refused.
	for _, c := range []struct {
		b  Binding
		sc Scope
	}{
		{Binding{TaskID: "at-1"}, ScopeRead}, {Binding{ConversationID: "c"}, ScopeRead}, {b, ScopeRun},
	} {
		if _, _, err := s.CreateBound(ctx, "default", "x", c.sc, time.Hour, c.b); !errors.Is(err, ErrBinding) {
			t.Errorf("%+v %s: err %v, want ErrBinding", c.b, c.sc, err)
		}
	}
	plain, psecret, _ := s.Create(ctx, "default", "plain", ScopeRead, time.Hour)
	if plain.Bound() {
		t.Error("an unbound token reports a binding")
	}
	if g, _ := s.Verify(ctx, psecret); g.BoundTo("at-1", "conv-1") {
		t.Error("an unbound token matched a task")
	}
}

func auditCountOf(t *testing.T, s *Store, tok Token, refused bool) int {
	t.Helper()
	q := `SELECT COUNT(*) FROM agent_token_audit WHERE token_id = ? AND status IN (401, 403)`
	if !refused {
		q = `SELECT COUNT(*) FROM agent_token_audit WHERE token_id = ? AND status NOT IN (401, 403)`
	}
	var n int
	if err := s.db.QueryRow(q, tok.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func use(t *testing.T, s *Store, tok Token, status int) {
	t.Helper()
	id, err := s.BeginUse(context.Background(), tok, "GET", "/p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishUse(context.Background(), id, status); err != nil {
		t.Fatal(err)
	}
}

// One token's flood past the whole table's cap leaves another token's rows, served and
// refused, where they were: the table is trimmed by count, from the token holding the most.
func TestAFloodDoesNotEraseAnotherTokensAudit(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	defer func(a, b, c int) { auditKeep, auditKeepPerToken, auditKeepRefusedPerToken = a, b, c }(auditKeep, auditKeepPerToken, auditKeepRefusedPerToken)
	auditKeep, auditKeepPerToken, auditKeepRefusedPerToken = 60, 1000, 1000
	quiet, _, _ := s.Create(ctx, "default", "quiet", ScopeRead, 0)
	loud, _, _ := s.Create(ctx, "default", "loud", ScopeRead, 0)
	for i := 0; i < 3; i++ {
		use(t, s, quiet, 200)
	}
	for i := 0; i < 2; i++ {
		use(t, s, quiet, 401)
	}
	for i := 0; i < 400; i++ { // several times the cap, served and refused
		use(t, s, loud, 200)
		use(t, s, loud, 401)
	}
	if n := auditCountOf(t, s, quiet, false); n != 3 {
		t.Errorf("the quiet token's served rows = %d, want 3", n)
	}
	if n := auditCountOf(t, s, quiet, true); n != 2 {
		t.Errorf("the quiet token's refused rows = %d, want 2", n)
	}
	var total int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM agent_token_audit`).Scan(&total)
	if total > 60 {
		t.Errorf("the table holds %d rows, want at most 60", total)
	}
}

// A token cannot wash its earlier refused requests (its probing of another profile, a scope
// it lacked) out of the log with ordinary calls.
func TestServedCallsDoNotWashRefusedRows(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	defer func(b int) { auditKeepPerToken = b }(auditKeepPerToken)
	auditKeepPerToken = 10
	tok, _, _ := s.Create(ctx, "default", "bot", ScopeRead, 0)
	use(t, s, tok, 401)
	use(t, s, tok, 403)
	for i := 0; i < 200; i++ {
		use(t, s, tok, 200)
	}
	if n := auditCountOf(t, s, tok, true); n != 2 {
		t.Errorf("refused rows after 200 served calls = %d, want 2", n)
	}
	if n := auditCountOf(t, s, tok, false); n != 10 {
		t.Errorf("served rows = %d, want 10", n)
	}
}
