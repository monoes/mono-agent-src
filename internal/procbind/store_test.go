package procbind

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	storeKey = bytesOf(5)
	t0       = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
)

func bytesOf(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func keyFor(calls *[]string) func(profile string) ([]byte, error) {
	return func(profile string) ([]byte, error) {
		if calls != nil {
			*calls = append(*calls, profile)
		}
		return storeKey, nil
	}
}

func mission(id, profile, owner string) MissionTokenRow {
	return MissionTokenRow{ID: id, ProfileID: profile, TokenHash: "hash-" + id, Owner: owner, Session: 7, Task: 3, Role: "worker", Actor: "actor",
		CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
}

func session(id, profile, conv, turn string) SessionTokenRow {
	return SessionTokenRow{ID: id, ProfileID: profile, TokenHash: "hash-" + id, ConversationID: conv, TurnID: turn, CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
}

func mustInsertMission(t *testing.T, db *sql.DB, r MissionTokenRow) {
	t.Helper()
	if err := InsertMissionToken(context.Background(), db, storeKey, r); err != nil {
		t.Fatal(err)
	}
}

func mustInsertSession(t *testing.T, db *sql.DB, r SessionTokenRow) {
	t.Helper()
	if err := InsertSessionToken(context.Background(), db, storeKey, r); err != nil {
		t.Fatal(err)
	}
}

func TestRowsRoundTripAndAreSigned(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	m := mission("m1", "p1", "owner/run1")
	mustInsertMission(t, db, m)
	got, err := GetMissionToken(ctx, db, storeKey, "p1", "m1")
	if err != nil || got != m {
		t.Fatalf("mission token: %+v %v", got, err)
	}
	s := session("s1", "p1", "conv", "turn")
	mustInsertSession(t, db, s)
	gs, err := GetSessionToken(ctx, db, storeKey, "p1", "s1")
	if err != nil || gs != s {
		t.Fatalf("session token: %+v %v", gs, err)
	}
	b := BindingRow{Kind: KindMission, TokenID: "m1", ProfileID: "p1", Scope: "session:7", WorkerPID: 4242, WorkerStamp: "start:1", HostGeneration: "gen-1", CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
	if err := InsertBinding(ctx, db, storeKey, b); err != nil {
		t.Fatal(err)
	}
	gb, err := GetBinding(ctx, db, storeKey, KindMission, "p1", "m1")
	if err != nil || gb != b {
		t.Fatalf("binding: %+v %v", gb, err)
	}
	if _, err := GetMissionToken(ctx, db, storeKey, "p1", "nope"); !errors.Is(err, ErrRowNotFound) {
		t.Errorf("a missing row: %v", err)
	}
	if _, err := GetMissionToken(ctx, db, storeKey, "p2", "m1"); !errors.Is(err, ErrRowNotFound) {
		t.Errorf("another profile's lookup found the row: %v", err)
	}
}

// A row that was written without the key, or with another row's signature, or read under another key, is refused.
func TestARowWithoutAGenuineSignatureIsRefused(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	m := mission("m1", "p1", "owner")
	mustInsertMission(t, db, m)
	if _, err := GetMissionToken(ctx, db, bytesOf(6), "p1", "m1"); !errors.Is(err, ErrRowTampered) {
		t.Errorf("read under another key: %v", err)
	}
	// written by something that does not hold the key: a valid shape, a made-up MAC
	mustExec(t, db, `INSERT INTO agent_tokens_v2 (id, profile_id, token_hash, mission_owner, mission_session, mission_task, mission_role, mission_actor, created_at, expires_at, version, mac)
		VALUES ('forged', 'p1', 'h', 'owner', 7, 3, 'worker', 'actor', ?, ?, 2, 'not-a-mac')`, t0.Format(tsLayout), t0.Add(time.Hour).Format(tsLayout))
	if _, err := GetMissionToken(ctx, db, storeKey, "p1", "forged"); !errors.Is(err, ErrRowTampered) {
		t.Errorf("a forged row was accepted: %v", err)
	}
	// the signature of one kind of row does not validate as another kind of row (domain separation)
	var mac string
	if err := db.QueryRow(`SELECT mac FROM agent_tokens_v2 WHERE id = 'm1'`).Scan(&mac); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO coder_session_tokens_v2 (id, profile_id, token_hash, conversation_id, turn_id, created_at, expires_at, version, mac)
		VALUES ('m1', 'p1', 'hash-m1', 'conv', 'turn', ?, ?, 2, ?)`, t0.Format(tsLayout), t0.Add(time.Hour).Format(tsLayout), mac)
	if _, err := GetSessionToken(ctx, db, storeKey, "p1", "m1"); !errors.Is(err, ErrRowTampered) {
		t.Errorf("a mission token's signature validated a session token: %v", err)
	}
}

func TestInsertRefusesAnUnusableRow(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	if err := InsertMissionToken(ctx, db, storeKey[:8], mission("m1", "p1", "o")); err == nil {
		t.Error("a short key was accepted")
	}
	bad := mission("", "p1", "o")
	if err := InsertMissionToken(ctx, db, storeKey, bad); err == nil {
		t.Error("an empty id was accepted")
	}
	noExp := mission("m2", "p1", "o")
	noExp.ExpiresAt = time.Time{}
	if err := InsertMissionToken(ctx, db, storeKey, noExp); err == nil {
		t.Error("a missing expiry was accepted")
	}
	if err := InsertRevocation(ctx, db, storeKey, RevocationRow{Kind: "other", TokenID: "x", ProfileID: "p1", CreatedAt: t0}); err == nil {
		t.Error("an unknown kind was accepted")
	}
}

func TestHostLogAllocatesSequenceAndReadsTheNewest(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	if _, err := CurrentHost(ctx, db, storeKey, "p1"); !errors.Is(err, ErrRowNotFound) {
		t.Fatalf("no host row yet: %v", err)
	}
	for i := 1; i <= 3; i++ {
		row, err := InsertHost(ctx, db, storeKey, "p1", "gen-"+string(rune('0'+i)), t0.Add(time.Duration(i)*time.Minute))
		if err != nil || row.Seq != int64(i) {
			t.Fatalf("start %d: %+v %v", i, row, err)
		}
	}
	if _, err := InsertHost(ctx, db, storeKey, "p2", "g", t0); err != nil {
		t.Fatal(err)
	}
	cur, err := CurrentHost(ctx, db, storeKey, "p1")
	if err != nil || cur.Seq != 3 || cur.Generation != "gen-3" {
		t.Fatalf("current host row: %+v %v", cur, err)
	}
	// the newest row is the one a verifier trusts: if its signature is not genuine it is refused, not skipped
	mustExec(t, db, `INSERT INTO procbind_host (profile_id, seq, generation, started_at, mac) VALUES ('p1', 4, 'gen-forged', ?, 'nope')`, t0.Format(tsLayout))
	if _, err := CurrentHost(ctx, db, storeKey, "p1"); !errors.Is(err, ErrRowTampered) {
		t.Errorf("a forged newest host row: %v", err)
	}
}

func TestRevokeTxBySelector(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("m1", "p1", "owner/run1"))
	mustInsertMission(t, db, mission("m2", "p1", "owner"))
	mustInsertMission(t, db, mission("m3", "p1", "ownerX"))
	mustInsertMission(t, db, mission("o1", "p2", "owner"))
	mustInsertSession(t, db, session("s1", "p1", "conv-1", "turn-1"))
	mustInsertSession(t, db, session("s2", "p1", "conv-1", "turn-2"))
	mustInsertSession(t, db, session("s3", "p1", "conv-2", "turn-1"))
	revoke := func(sel Selector) []TokenRef {
		t.Helper()
		refs, err := RevokeTx(ctx, db, keyFor(nil), sel, "test", t0)
		if err != nil {
			t.Fatal(err)
		}
		return refs
	}
	ids := func(refs []TokenRef) string {
		var out []string
		for _, r := range refs {
			out = append(out, string(r.Kind)+":"+r.TokenID)
		}
		return strings.Join(sortStrings(out), ",")
	}
	if got := ids(revoke(Selector{Profile: "p1", Session: "conv-1", Turn: "turn-2"})); got != "session:s2" {
		t.Errorf("session and turn: %s", got)
	}
	if got := ids(revoke(Selector{Profile: "p1", Session: "conv-1"})); got != "session:s1" {
		t.Errorf("the rest of the session (s2 is already revoked): %s", got)
	}
	if got := ids(revoke(Selector{Profile: "p1", Owner: "owner"})); got != "mission:m1,mission:m2" {
		t.Errorf("the owner and its runs, not ownerX: %s", got)
	}
	if got := ids(revoke(Selector{Profile: "p1", TokenID: "s3", Kind: KindMission})); got != "" {
		t.Errorf("a token id of the wrong kind: %s", got)
	}
	if got := ids(revoke(Selector{Profile: "p1", TokenID: "s3"})); got != "session:s3" {
		t.Errorf("a token id of either kind: %s", got)
	}
	if got := ids(revoke(Selector{Profile: "p1", All: true})); got != "mission:m3" {
		t.Errorf("all that was left of the profile: %s", got)
	}
	if got := ids(revoke(Selector{Profile: "p1", All: true})); got != "" {
		t.Errorf("revoking again must find nothing new: %s", got)
	}
	for _, id := range []string{"m1", "m2", "m3"} {
		if rv, err := IsRevoked(ctx, db, KindMission, id); err != nil || !rv {
			t.Errorf("mission %s revoked=%v %v", id, rv, err)
		}
	}
	if rv, err := IsRevoked(ctx, db, KindMission, "o1"); err != nil || rv {
		t.Errorf("another profile's token was revoked: %v %v", rv, err)
	}
	// the revocation rows are signed
	got, err := GetRevocation(ctx, db, storeKey, KindMission, "p1", "m1")
	if err != nil || got.Reason != "test" || !got.CreatedAt.Equal(t0) {
		t.Errorf("revocation row: %+v %v", got, err)
	}
}

func sortStrings(in []string) []string {
	out := append([]string{}, in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// An owner selector without a profile is the one broad selector (the legacy RevokeOwner is profile-less): it
// finds the owner's tokens in every profile, and nothing else may be broad.
func TestAnOwnerSelectorMayOmitTheProfileButNothingElseMay(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("a", "p1", "owner/run"))
	mustInsertMission(t, db, mission("b", "p2", "owner"))
	mustInsertMission(t, db, mission("c", "p2", "ownerX"))
	mustInsertSession(t, db, session("s", "p1", "owner", "turn")) // a conversation named like the owner: not a mission token
	refs, err := RevokeTx(ctx, db, keyFor(nil), Selector{Owner: "owner"}, "run ended", t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("the owner across profiles: %v", refs)
	}
	for _, r := range refs {
		if r.Kind != KindMission || (r.TokenID != "a" && r.TokenID != "b") {
			t.Errorf("unexpected ref %+v", r)
		}
	}
	if rv, _ := IsRevoked(ctx, db, KindMission, "c"); rv {
		t.Error("ownerX was revoked by the owner selector")
	}
	for name, sel := range map[string]Selector{
		"nothing at all":               {},
		"All without a profile":        {All: true},
		"a session without a profile":  {Session: "conv"},
		"a token id without a profile": {TokenID: "a"},
		"an owner and All, no profile": {Owner: "owner", All: true},
		"an owner and a session":       {Owner: "owner", Session: "conv"},
		"an owner and a token id":      {Owner: "owner", TokenID: "a"},
		"a kind alone":                 {Kind: KindMission},
		"a profile alone":              {Profile: "p1"},
		"a generation alone":           {Profile: "p1", Generation: Generation{1, 1}},
		"an owner of the session kind": {Owner: "owner", Kind: KindSession},
	} {
		if _, err := RevokeTx(ctx, db, keyFor(nil), sel, "x", t0); !errors.Is(err, ErrBadSelector) {
			t.Errorf("%s: err = %v, want ErrBadSelector", name, err)
		}
	}
}

func TestRevokeTxAsksForTheKeyOnlyWhenItWritesAndFailsClosedWithoutIt(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("m1", "p1", "owner"))
	var calls []string
	if refs, err := RevokeTx(ctx, db, keyFor(&calls), Selector{Profile: "p1", Owner: "nobody"}, "x", t0); err != nil || len(refs) != 0 || len(calls) != 0 {
		t.Fatalf("nothing to revoke: %v %v %v", refs, err, calls)
	}
	failing := func(string) ([]byte, error) { return nil, errors.New("no key") }
	if _, err := RevokeTx(ctx, db, failing, Selector{Profile: "p1", Owner: "owner"}, "x", t0); err == nil {
		t.Fatal("a revoke without a key succeeded")
	}
	if rv, _ := IsRevoked(ctx, db, KindMission, "m1"); rv {
		t.Error("a failed revoke left a revocation row")
	}
	if _, err := RevokeTx(ctx, db, nil, Selector{Profile: "p1", Owner: "owner"}, "x", t0); err == nil {
		t.Error("a nil key function was accepted")
	}
	if _, err := RevokeTx(ctx, db, func(string) ([]byte, error) { return storeKey[:4], nil }, Selector{Profile: "p1", Owner: "owner"}, "x", t0); err == nil {
		t.Error("a short key was accepted")
	}
	if refs, err := RevokeTx(ctx, db, keyFor(&calls), Selector{Profile: "p1", Owner: "owner"}, "x", t0); err != nil || len(refs) != 1 {
		t.Fatalf("the revoke itself: %v %v", refs, err)
	}
	if len(calls) != 1 || calls[0] != "p1" {
		t.Errorf("the key was derived %v, want once for p1", calls)
	}
}

// RevokeTx writes inside the caller's transaction: rolled back with it, committed with it, and a failure makes
// the whole transaction fail (decision K1).
func TestRevokeTxIsPartOfTheCallersTransaction(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	mustInsertMission(t, db, mission("m1", "p1", "owner"))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RevokeTx(ctx, tx, keyFor(nil), Selector{Profile: "p1", Owner: "owner"}, "x", t0); err != nil {
		t.Fatal(err)
	}
	if rv, _ := IsRevoked(ctx, tx, KindMission, "m1"); !rv {
		t.Error("the revocation is not visible inside its own transaction")
	}
	_ = tx.Rollback()
	if rv, _ := IsRevoked(ctx, db, KindMission, "m1"); rv {
		t.Error("a rolled-back transaction left a revocation")
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RevokeTx(ctx, tx, keyFor(nil), Selector{Profile: "p1", Owner: "owner"}, "x", t0); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if rv, _ := IsRevoked(ctx, db, KindMission, "m1"); !rv {
		t.Error("a committed transaction lost the revocation")
	}
	// a write failure inside RevokeTx is an error for the caller, who rolls back its legacy change too
	mustInsertMission(t, db, mission("m2", "p1", "owner2"))
	mustExec(t, db, `INSERT INTO token_revocations_v2 (token_kind, token_id, profile_id, reason, created_at, mac) VALUES ('mission', 'm2', 'p1', 'x', ?, 'bad-but-present')`, t0.Format(tsLayout))
	if refs, err := RevokeTx(ctx, db, keyFor(nil), Selector{Profile: "p1", Owner: "owner2"}, "x", t0); err != nil || len(refs) != 0 {
		t.Errorf("an already-revoked token must be skipped, not fail the call: %v %v", refs, err)
	}
}

func TestDropEndsTheRegistryEntriesOfTheRefs(t *testing.T) {
	b, _ := newTestBinder()
	h1 := activate(t, b, spec("t1"))
	s2 := spec("t2")
	s2.Profile = "p2"
	h2 := activate(t, b, s2)
	n := Drop(b, []TokenRef{{Kind: KindSession, TokenID: "t1", Profile: "p1"}})
	if n != 1 || h1.timesClosed() != 1 || h2.timesClosed() != 0 {
		t.Fatalf("dropped %d, handles closed %d and %d", n, h1.timesClosed(), h2.timesClosed())
	}
	if Drop(b, nil) != 0 || Drop(nil, []TokenRef{{Kind: KindSession, TokenID: "t2", Profile: "p2"}}) != 0 {
		t.Error("Drop of nothing, or on a nil binder, must do nothing")
	}
}

// A revocation is keyed by kind and token id (the table's primary key, ids are global in the token tables): another kind
// is a different token, and a row naming another profile still denies (a misplaced row can only deny).
func TestIsRevokedIsExactToKindAndId(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	if err := InsertRevocation(ctx, db, storeKey, RevocationRow{Kind: KindSession, TokenID: "x", ProfileID: "p1", Reason: "r", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		kind Kind
		id   string
		want bool
	}{"same": {KindSession, "x", true}, "other kind": {KindMission, "x", false}, "other id": {KindSession, "y", false}} {
		if got, err := IsRevoked(ctx, db, c.kind, c.id); err != nil || got != c.want {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}

// The key of a profile is asked for once however many of its tokens are revoked, and once per profile.
func TestRevokeTxAsksForEachProfilesKeyOnce(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		mustInsertMission(t, db, mission(id, "p1", "owner"))
	}
	mustInsertMission(t, db, mission("d", "p2", "owner"))
	var calls []string
	refs, err := RevokeTx(ctx, db, keyFor(&calls), Selector{Owner: "owner"}, "x", t0)
	if err != nil || len(refs) != 4 {
		t.Fatalf("%v %v", refs, err)
	}
	if len(calls) != 2 {
		t.Errorf("the key was derived %v, want once for each of the two profiles", calls)
	}
}

func TestRowKindsAreSignedInSeparateDomains(t *testing.T) {
	a, err := rowMAC(storeKey, "mission-token", "x", "y")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := rowMAC(storeKey, "session-token", "x", "y")
	c, _ := rowMAC(storeKey, "mission-token", "x", "z")
	if a == b {
		t.Error("two kinds of row with the same fields have the same signature")
	}
	if a == c {
		t.Error("a different field gave the same signature")
	}
	if _, err := rowMAC(storeKey[:8], "x"); err == nil {
		t.Error("a short key signed")
	}
}

// Every column of every kind of row is signed. For each column, a row that equals a genuine row except in that one
// column, carrying the genuine row's signature, is refused. (The genuine signature is computed in Go from the
// genuine row; the variant is written with raw SQL, as something without the key could write it.)
func TestEveryColumnOfEveryRowIsSigned(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	exp := t0.Add(time.Hour)
	later := t0.Add(5 * time.Hour)
	n := 0
	fresh := func(prefix string) string { n++; return prefix + string(rune('a'+n)) }
	f := func(tm time.Time) string { return tm.Format(tsLayout) }

	type mvar struct {
		name string
		mut  func(r *MissionTokenRow)
	}
	for _, v := range []mvar{
		{"id", func(r *MissionTokenRow) { r.ID += "x" }}, {"token_hash", func(r *MissionTokenRow) { r.TokenHash += "x" }}, {"owner", func(r *MissionTokenRow) { r.Owner = "other" }},
		{"session", func(r *MissionTokenRow) { r.Session++ }}, {"task", func(r *MissionTokenRow) { r.Task++ }},
		{"role", func(r *MissionTokenRow) { r.Role = "verifier" }}, {"actor", func(r *MissionTokenRow) { r.Actor = "other" }},
		{"created_at", func(r *MissionTokenRow) { r.CreatedAt = later }}, {"expires_at", func(r *MissionTokenRow) { r.ExpiresAt = later }},
		{"profile_id", func(r *MissionTokenRow) { r.ProfileID = "p2" }},
	} {
		g := mission(fresh("m"), "p1", "owner")
		mac, _ := rowMAC(storeKey, "mission-token", g.fields()...)
		r := g
		v.mut(&r)
		mustExec(t, db, `INSERT INTO agent_tokens_v2 (id, profile_id, token_hash, mission_owner, mission_session, mission_task, mission_role, mission_actor, created_at, expires_at, version, mac)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 2, ?)`, r.ID, r.ProfileID, r.TokenHash, r.Owner, r.Session, r.Task, r.Role, r.Actor, f(r.CreatedAt), f(r.ExpiresAt), mac)
		if _, err := GetMissionToken(ctx, db, storeKey, r.ProfileID, r.ID); !errors.Is(err, ErrRowTampered) {
			t.Errorf("mission token differing in %s was accepted: %v", v.name, err)
		}
		// and the genuine row, written properly, reads
		if v.name == "token_hash" {
			if err := InsertMissionToken(ctx, db, storeKey, mission(fresh("m"), "p1", "owner")); err != nil {
				t.Fatal(err)
			}
		}
	}

	type svar struct {
		name string
		mut  func(r *SessionTokenRow)
	}
	for _, v := range []svar{
		{"id", func(r *SessionTokenRow) { r.ID += "x" }}, {"token_hash", func(r *SessionTokenRow) { r.TokenHash += "x" }}, {"conversation", func(r *SessionTokenRow) { r.ConversationID = "other" }},
		{"turn", func(r *SessionTokenRow) { r.TurnID = "other" }}, {"created_at", func(r *SessionTokenRow) { r.CreatedAt = later }},
		{"expires_at", func(r *SessionTokenRow) { r.ExpiresAt = later }}, {"profile_id", func(r *SessionTokenRow) { r.ProfileID = "p2" }},
	} {
		g := session(fresh("s"), "p1", "conv", "turn")
		mac, _ := rowMAC(storeKey, "session-token", g.fields()...)
		r := g
		v.mut(&r)
		mustExec(t, db, `INSERT INTO coder_session_tokens_v2 (id, profile_id, token_hash, conversation_id, turn_id, created_at, expires_at, version, mac) VALUES (?, ?, ?, ?, ?, ?, ?, 2, ?)`,
			r.ID, r.ProfileID, r.TokenHash, r.ConversationID, r.TurnID, f(r.CreatedAt), f(r.ExpiresAt), mac)
		if _, err := GetSessionToken(ctx, db, storeKey, r.ProfileID, r.ID); !errors.Is(err, ErrRowTampered) {
			t.Errorf("session token differing in %s was accepted: %v", v.name, err)
		}
	}

	type bvar struct {
		name string
		mut  func(r *BindingRow)
	}
	for _, v := range []bvar{
		{"kind", func(r *BindingRow) { r.Kind = KindSession }}, {"token_id", func(r *BindingRow) { r.TokenID += "x" }}, {"scope", func(r *BindingRow) { r.Scope = "other" }}, {"worker_pid", func(r *BindingRow) { r.WorkerPID++ }},
		{"worker_stamp", func(r *BindingRow) { r.WorkerStamp = "other" }}, {"host_generation", func(r *BindingRow) { r.HostGeneration = "other" }},
		{"created_at", func(r *BindingRow) { r.CreatedAt = later }}, {"expires_at", func(r *BindingRow) { r.ExpiresAt = later }},
		{"profile_id", func(r *BindingRow) { r.ProfileID = "p2" }},
	} {
		g := BindingRow{Kind: KindMission, TokenID: fresh("b"), ProfileID: "p1", Scope: "sc", WorkerPID: 4242, WorkerStamp: "st", HostGeneration: "g", CreatedAt: t0, ExpiresAt: exp}
		mac, _ := rowMAC(storeKey, "binding", g.fields()...)
		r := g
		v.mut(&r)
		mustExec(t, db, `INSERT INTO process_bindings (token_id, profile_id, scope, mode, worker_pid, worker_stamp, host_generation, version, created_at, expires_at, mac) VALUES (?, ?, ?, 'peercred', ?, ?, ?, 2, ?, ?, ?)`,
			r.TokenID, r.ProfileID, r.Scope, r.WorkerPID, r.WorkerStamp, r.HostGeneration, f(r.CreatedAt), f(r.ExpiresAt), mac)
		if _, err := GetBinding(ctx, db, storeKey, r.Kind, r.ProfileID, r.TokenID); !errors.Is(err, ErrRowTampered) {
			t.Errorf("binding differing in %s was accepted: %v", v.name, err)
		}
	}

	type rvar struct {
		name string
		mut  func(r *RevocationRow)
	}
	for _, v := range []rvar{
		{"kind", func(r *RevocationRow) { r.Kind = KindSession }}, {"token_id", func(r *RevocationRow) { r.TokenID += "x" }}, {"reason", func(r *RevocationRow) { r.Reason = "other" }},
		{"created_at", func(r *RevocationRow) { r.CreatedAt = later }}, {"profile_id", func(r *RevocationRow) { r.ProfileID = "p2" }},
	} {
		g := RevocationRow{Kind: KindMission, TokenID: fresh("r"), ProfileID: "p1", Reason: "why", CreatedAt: t0}
		mac, _ := rowMAC(storeKey, "revocation", g.fields()...)
		r := g
		v.mut(&r)
		mustExec(t, db, `INSERT INTO token_revocations_v2 (token_kind, token_id, profile_id, reason, created_at, mac) VALUES (?, ?, ?, ?, ?, ?)`, string(r.Kind), r.TokenID, r.ProfileID, r.Reason, f(r.CreatedAt), mac)
		if _, err := GetRevocation(ctx, db, storeKey, r.Kind, r.ProfileID, r.TokenID); !errors.Is(err, ErrRowTampered) {
			t.Errorf("revocation differing in %s was accepted: %v", v.name, err)
		}
	}

	type hvar struct {
		name string
		mut  func(r *HostRow)
	}
	for i, v := range []hvar{
		{"generation", func(r *HostRow) { r.Generation = "other" }}, {"started_at", func(r *HostRow) { r.StartedAt = later }},
		{"seq", func(r *HostRow) { r.Seq++ }}, {"profile_id", func(r *HostRow) { r.ProfileID += "-moved" }},
	} {
		profile := "ph" + string(rune('a'+i))
		g := HostRow{ProfileID: profile, Seq: 1, Generation: "gen", StartedAt: t0}
		mac, _ := rowMAC(storeKey, "host", g.fields()...)
		r := g
		v.mut(&r)
		mustExec(t, db, `INSERT INTO procbind_host (profile_id, seq, generation, started_at, mac) VALUES (?, ?, ?, ?, ?)`, r.ProfileID, r.Seq, r.Generation, f(r.StartedAt), mac)
		if _, err := CurrentHost(ctx, db, storeKey, r.ProfileID); !errors.Is(err, ErrRowTampered) {
			t.Errorf("host row differing in %s was accepted: %v", v.name, err)
		}
	}
}
