package procbind

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func mintReq(extra ...MintToken) MintRequest {
	toks := []MintToken{{Kind: KindSession, ID: "s1", Hash: HashSecret("mst-one"), Scope: "session:7", ConversationID: "conv", TurnID: "turn", Expires: t0.Add(time.Hour)}}
	toks = append(toks, extra...)
	return MintRequest{Profile: "p1", WorkerPID: 4242, WorkerStamp: "start:1", HostGeneration: "gen-1", Now: t0, Tokens: toks}
}

func missionTok(id string) MintToken {
	return MintToken{Kind: KindMission, ID: id, Hash: HashSecret("mat-" + id), Scope: "mission:7:3", Owner: "owner/run", Role: "worker", Actor: "worker-a", Session: 7, Task: 3, Expires: t0.Add(time.Hour)}
}

// countAll counts the rows of the five v2 tables.
func countAll(t *testing.T, db DBTX) int {
	t.Helper()
	n := 0
	for _, tbl := range []string{"agent_tokens_v2", "coder_session_tokens_v2", "process_bindings", "token_revocations_v2", "procbind_host"} {
		var c int
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+tbl).Scan(&c); err != nil {
			t.Fatal(err)
		}
		n += c
	}
	return n
}

// Every row the launch needs is written, each with a genuine signature, and reads back as it was written.
func TestMintWritesTokensAndBindingsThatVerify(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	if err := MintTx(ctx, db, storeKey, mintReq(missionTok("m1"))); err != nil {
		t.Fatal(err)
	}
	s, err := GetSessionToken(ctx, db, storeKey, "p1", "s1")
	if err != nil || s.ConversationID != "conv" || s.TurnID != "turn" || s.TokenHash != HashSecret("mst-one") {
		t.Fatalf("session token: %+v %v", s, err)
	}
	m, err := GetMissionToken(ctx, db, storeKey, "p1", "m1")
	if err != nil || m.Owner != "owner/run" || m.Session != 7 || m.Task != 3 {
		t.Fatalf("mission token: %+v %v", m, err)
	}
	for _, c := range []struct {
		kind Kind
		id   string
	}{{KindSession, "s1"}, {KindMission, "m1"}} {
		b, err := GetBinding(ctx, db, storeKey, c.kind, "p1", c.id)
		if err != nil || b.WorkerPID != 4242 || b.WorkerStamp != "start:1" || b.HostGeneration != "gen-1" {
			t.Errorf("binding of %s: %+v %v", c.id, b, err)
		}
	}
}

// A refused Check, an id that is taken, a duplicate in the request, a bad row and a bad request each write nothing:
// not the first token, not a binding.
func TestMintIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	refuse := func(context.Context, DBTX) error { return errors.New("the turn is not current") }
	cases := map[string]struct {
		pre  func(t *testing.T, db DBTX)
		req  func() MintRequest
		want error
	}{
		"check refuses": {req: func() MintRequest { r := mintReq(missionTok("m1")); r.Check = refuse; return r }, want: ErrMintRefused},
		"id in use by the other family": {
			pre: func(t *testing.T, db DBTX) {
				if err := InsertMissionToken(ctx, db, storeKey, mission("s1", "p1", "owner")); err != nil {
					t.Fatal(err)
				}
			},
			req: func() MintRequest { return mintReq(missionTok("m1")) }, want: ErrIDInUse},
		"duplicate in the request": {req: func() MintRequest { return mintReq(missionTok("s1")) }, want: ErrIDInUse},
		"second token is bad": {req: func() MintRequest {
			return mintReq(MintToken{Kind: "bogus", ID: "m1"})
		}, want: ErrBadSelector},
	}
	for name, c := range cases {
		db := newDB(t)
		if c.pre != nil {
			c.pre(t, db)
		}
		before := countAll(t, db)
		err := MintTx(ctx, db, storeKey, c.req())
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		if after := countAll(t, db); after != before {
			t.Errorf("%s: rows %d -> %d, the mint wrote something", name, before, after)
		}
	}
	for name, mutate := range map[string]func(*MintRequest){
		"no profile":    func(r *MintRequest) { r.Profile = "" },
		"no worker pid": func(r *MintRequest) { r.WorkerPID = 0 },
		"no stamp":      func(r *MintRequest) { r.WorkerStamp = "" },
		"no generation": func(r *MintRequest) { r.HostGeneration = "" },
		"no tokens":     func(r *MintRequest) { r.Tokens = nil },
		"no time":       func(r *MintRequest) { r.Now = time.Time{} },
		"pid 1":         func(r *MintRequest) { r.WorkerPID = 1 },
	} {
		db := newDB(t)
		r := mintReq()
		mutate(&r)
		if err := MintTx(ctx, db, storeKey, r); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if countAll(t, db) != 0 {
			t.Errorf("%s: rows were written", name)
		}
	}
}

// The delete-first and mint-first orders: a revoke that ran first makes Check fail and nothing is inserted; a mint that
// ran first is found by the revoke's selector (by session) and by the owner.
func TestMintAndARevokeAreSerialised(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	// mint first: the delete's RevokeTx finds the rows
	if err := MintTx(ctx, db, storeKey, mintReq(missionTok("m1"))); err != nil {
		t.Fatal(err)
	}
	refs, err := RevokeTx(ctx, db, keyFor(nil), Selector{Profile: "p1", Session: "conv", Kind: KindSession}, "deleted", t0)
	if err != nil || len(refs) != 1 || refs[0].TokenID != "s1" {
		t.Fatalf("revoke by session: %v %v", refs, err)
	}
	refs, err = RevokeTx(ctx, db, keyFor(nil), Selector{Profile: "p1", Owner: "owner", Kind: KindMission}, "deleted", t0)
	if err != nil || len(refs) != 1 || refs[0].TokenID != "m1" {
		t.Fatalf("revoke by owner: %v %v", refs, err)
	}
	// delete first: Check re-reads the (now deleted) state and refuses
	db2 := newDB(t)
	exists := func(context.Context, DBTX) error { return errors.New("the conversation is gone") }
	r := mintReq()
	r.Check = exists
	if err := MintTx(ctx, db2, storeKey, r); !errors.Is(err, ErrMintRefused) {
		t.Fatalf("%v", err)
	}
	if countAll(t, db2) != 0 {
		t.Error("rows were written after the state check failed")
	}
}

// Check runs on the transaction's own connection, inside the write transaction: it sees the data of this database and
// a write it attempted would be part of the rolled-back transaction.
func TestMintCheckRunsInsideTheTransaction(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	r := mintReq()
	r.Check = func(ctx context.Context, x DBTX) error {
		if _, err := x.ExecContext(ctx, `INSERT INTO token_revocations_v2 (token_kind, token_id, profile_id, reason, created_at, mac) VALUES ('session', 'witness', 'p1', 'x', ?, 'm')`, t0.Format(tsLayout)); err != nil {
			return err
		}
		return errors.New("refuse after writing")
	}
	if err := MintTx(ctx, db, storeKey, r); !errors.Is(err, ErrMintRefused) {
		t.Fatal(err)
	}
	if countAll(t, db) != 0 {
		t.Error("a write made by Check survived the refusal")
	}
}

func TestNewSecretIsRandomPrefixedAndHashed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		s, h, err := NewSecret(SessionPrefixV2)
		if err != nil || !strings.HasPrefix(s, SessionPrefixV2) || len(s) != len(SessionPrefixV2)+43 || h != HashSecret(s) || len(h) != 64 || seen[s] {
			t.Fatalf("%q %q %v", s, h, err)
		}
		seen[s] = true
	}
}

// AnswerKeyIfExists gives the key AnswerKey gives for a profile that has one, and creates nothing for one that has not.
func TestAnswerKeyIfExistsNeverCreatesAKey(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	if _, err := AnswerKeyIfExists(ctx, db, "fresh", "g1"); !errors.Is(err, ErrNoRowKey) {
		t.Fatalf("a profile with no key: %v", err)
	}
	if vaultRowsOf(t, db, "fresh") != 0 {
		t.Fatal("a key was created")
	}
	want, err := AnswerKey(ctx, db, "live", "g1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := AnswerKeyIfExists(ctx, db, "live", "g1")
	if err != nil || string(got) != string(want) {
		t.Errorf("a live profile's key differs: %v", err)
	}
	other, _ := AnswerKeyIfExists(ctx, db, "live", "g2")
	if string(other) == string(got) {
		t.Error("two generations share an answer key")
	}
}

// The cause of a refused Check stays visible to errors.Is next to ErrMintRefused (a caller can tell "the conversation is
// gone" from "the turn moved on" without reading text).
func TestARefusedMintKeepsItsCauseForErrorsIs(t *testing.T) {
	cause := errors.New("the turn is not current")
	r := mintReq()
	r.Check = func(context.Context, DBTX) error { return cause }
	err := MintTx(context.Background(), newDB(t), storeKey, r)
	if !errors.Is(err, ErrMintRefused) || !errors.Is(err, cause) {
		t.Fatalf("refused mint error %v: want both ErrMintRefused and its cause", err)
	}
}
