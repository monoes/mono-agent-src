package ops

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
)

// These tests run the adapter and the enforcer over the real SQLite store (no fakes): approvals, rescinds, expiry and
// replays are the store's rows, and the charter carries the typed constraints.

func realDB(t *testing.T) *sql.DB {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(t.TempDir() + "/dd.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	db.DB.SetMaxOpenConns(1)
	return db.DB
}

func typedStoreCharter() store.Charter {
	return store.Charter{
		Profile: "p", Generation: 1, Goal: "g",
		Criteria: []store.Criterion{{Kind: "command", Spec: "go test", Expected: "ok"}},
		Limits:   store.Limits{TreeDepth: 4, LivePerTree: 30, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: 30, MaxTotal: 100, BudgetMicro: 5_000_000},
		Lines:    []string{store.LineSpending, store.LineThirdParty, store.LineVault, store.LineDestructive, store.LineHostSettings},
		BossPin:  store.Pin{Provider: "a", Model: "m1", Version: "v1", PurposePolicy: "pp"}, VerifierPin: store.Pin{Provider: "a", Model: "m2", Version: "v1", PurposePolicy: "pp"},
		Typed: store.Typed{
			Send:     &store.SendLine{Platform: "x", Account: "acct", Destinations: []string{"@alice", "@bob"}, MaxPerRun: 2, ContentClass: "text"},
			Vault:    &store.VaultLine{Secrets: []string{"api"}, Hosts: []string{"example.com"}, MaxAttempts: 5, BucketSize: 1024, BucketCount: 2, MaxCallMS: 5000, SlotMS: 500},
			Local:    &store.LocalLine{Roots: []string{"/data/r"}, Verbs: []string{"delete"}, MaxCount: 3, MaxBytes: 100},
			Settings: &store.SettingsLine{Keys: map[string][]string{"theme": {"dark"}, "motd": {"*"}}},
		},
	}
}

type realFix struct {
	st  *store.SQLStore
	ad  StoreAdapter
	e   *TypedEnforcer
	act Active
}

func newRealFix(t *testing.T, c store.Charter) *realFix {
	t.Helper()
	st := store.New(realDB(t), store.NewHMACKeyer([]byte("k"))).WithEntryCheck(EntryCheck(bounds))
	if _, err := st.Enter(bg, c, c.Hash(), "op"); err != nil {
		t.Fatal(err)
	}
	ad := StoreAdapter{Store: st, Bounds: bounds}
	act, ok, err := ad.Active(bg, "p")
	if err != nil || !ok {
		t.Fatalf("active: %v %v", ok, err)
	}
	return &realFix{st: st, ad: ad, act: act, e: &TypedEnforcer{Charters: ad, Approvals: StoreApprovals{Store: st}, Ledger: ad, Auditor: ad, Bounds: bounds}}
}

// issue has the store record an approval for exactly the effect of req and returns the request that presents it.
func (f *realFix) issue(t *testing.T, req TypedRequest, ttl int64) TypedRequest {
	t.Helper()
	req.Profile, req.CharterID, req.Hash, req.Generation, req.Caller = "p", f.act.ID, f.act.Hash, f.act.Generation, Caller{Session: 7}
	canon, _, _, err := typedEffect(f.act.Constraints, req)
	if err != nil {
		t.Fatalf("effect outside the charter: %v", err)
	}
	h, _ := ArgsHash(req.Op, canon)
	a, err := f.st.IssueApproval(bg, store.IssueInput{Profile: "p", Generation: f.act.Generation, CharterHash: f.act.Hash, Op: req.Op, ArgsHash: h, Caller: 7, Boss: "boss", TTLSeq: ttl})
	if err != nil {
		t.Fatal(err)
	}
	req.ApprovalID = a.ID
	return req
}

func TestActiveCarriesTheTypedConstraintsOfTheCharter(t *testing.T) {
	f := newRealFix(t, typedStoreCharter())
	c := f.act.Constraints
	if c.SendThirdParty == nil || c.SendThirdParty.Account != "acct" || c.SendThirdParty.MaxPerRun != 2 || len(c.SendThirdParty.Destinations) != 2 {
		t.Fatalf("send %+v", c.SendThirdParty)
	}
	if c.VaultUse == nil || c.VaultUse.Secrets[0] != "api" || c.VaultUse.Hosts[0] != "example.com" || c.VaultUse.SlotMS != 500 {
		t.Fatalf("vault %+v", c.VaultUse)
	}
	if c.LocalDestructive == nil || c.LocalDestructive.Roots[0] != "/data/r" || c.HostSetting == nil || len(c.HostSetting.Keys) != 2 {
		t.Fatalf("local/settings %+v %+v", c.LocalDestructive, c.HostSetting)
	}
	if err := c.Validate(bounds); err != nil {
		t.Fatalf("the charter's constraints do not validate: %v", err)
	}
}

func TestACharterWithoutTheSectionsEnablesNone(t *testing.T) { // default deny
	c := typedStoreCharter()
	c.Typed = store.Typed{}
	f := newRealFix(t, c)
	k := f.act.Constraints
	if k.SendThirdParty != nil || k.VaultUse != nil || k.LocalDestructive != nil || k.HostSetting != nil {
		t.Fatalf("a typed operation is enabled with no section: %+v", k)
	}
	req := TypedRequest{Profile: "p", CharterID: f.act.ID, Hash: f.act.Hash, Generation: 1, Op: OpHostSetting, ApprovalID: "ap-x", Caller: Caller{Session: 7},
		Setting: &SettingArgs{Key: "theme", Value: "dark"}}
	if _, err := f.e.Authorize(bg, req); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("got %v", err)
	}
}

func TestGrantTargetsComeFromTheCharter(t *testing.T) {
	c := typedStoreCharter()
	c.Typed.GrantTargets = []string{"child"}
	f := newRealFix(t, c)
	if g := f.act.Constraints.SpendGrant; g == nil || len(g.Targets) != 1 || g.Targets[0] != "child" {
		t.Fatalf("%+v", g)
	}
	d := newRealFix(t, typedStoreCharter())
	if g := d.act.Constraints.SpendGrant; g == nil || len(g.Targets) != 2 {
		t.Fatalf("default targets %+v", g)
	}
}

func TestEntryCheckRefusesWhatOpsWouldNeverEnforce(t *testing.T) {
	st := store.New(realDB(t), store.NewHMACKeyer([]byte("k"))).WithEntryCheck(EntryCheck(bounds))
	c := typedStoreCharter()
	c.Typed.Settings = &store.SettingsLine{Keys: map[string][]string{"operator_gate": {"*"}}}
	if _, err := st.Enter(bg, c, c.Hash(), "op"); !errors.Is(err, store.ErrInvalidCharter) {
		t.Fatalf("never-liftable key: %v", err)
	}
	c = typedStoreCharter()
	c.Typed.Send.Platform = "no-such-platform"
	if _, err := st.Enter(bg, c, c.Hash(), "op"); !errors.Is(err, store.ErrInvalidCharter) {
		t.Fatalf("platform without a handle rule: %v", err)
	}
	if n, _ := st.NextGeneration(bg, "p"); n != 1 {
		t.Fatal("a refused charter was recorded")
	}
}

func TestStoreBoundsAgreeWithOpsBounds(t *testing.T) {
	base := typedStoreCharter().Typed.Vault
	cases := map[string]func(v *store.VaultLine){
		"bucket min":   func(v *store.VaultLine) { v.BucketSize = 1 },
		"bucket max":   func(v *store.VaultLine) { v.BucketSize = MaxBucketSize },
		"bucket over":  func(v *store.VaultLine) { v.BucketSize = MaxBucketSize + 1 },
		"bucket zero":  func(v *store.VaultLine) { v.BucketSize = 0 },
		"count max":    func(v *store.VaultLine) { v.BucketCount = MaxBucketCount },
		"count over":   func(v *store.VaultLine) { v.BucketCount = MaxBucketCount + 1 },
		"call min":     func(v *store.VaultLine) { v.MaxCallMS, v.SlotMS = MinCallMS, MinSlotMS },
		"call under":   func(v *store.VaultLine) { v.MaxCallMS = MinCallMS - 1 },
		"call max":     func(v *store.VaultLine) { v.MaxCallMS, v.SlotMS = MaxCallMS, 1000 },
		"call over":    func(v *store.VaultLine) { v.MaxCallMS = MaxCallMS + 1 },
		"slot under":   func(v *store.VaultLine) { v.SlotMS = MinSlotMS - 1 },
		"slot over":    func(v *store.VaultLine) { v.SlotMS = v.MaxCallMS + 1 },
		"slots at max": func(v *store.VaultLine) { v.MaxCallMS, v.SlotMS = MaxSlots*MinSlotMS, MinSlotMS },
		"slots over":   func(v *store.VaultLine) { v.MaxCallMS, v.SlotMS = MaxSlots*MinSlotMS+1, MinSlotMS },
		"attempts max": func(v *store.VaultLine) { v.MaxAttempts = MaxAttemptsCap },
		"attempts out": func(v *store.VaultLine) { v.MaxAttempts = MaxAttemptsCap + 1 },
	}
	for name, m := range cases {
		v := *base
		m(&v)
		c := typedStoreCharter()
		c.Typed.Vault = &v
		storeErr := c.Validate()
		opsErr := EntryCheck(bounds)(c)
		if (storeErr == nil) != (opsErr == nil) && storeErr == nil {
			t.Errorf("%s: store accepts, ops refuses: %v", name, opsErr)
		}
		if storeErr != nil && opsErr == nil {
			t.Errorf("%s: store refuses (%v), ops would accept: the bounds drifted", name, storeErr)
		}
	}
}

func TestRealApprovalIsSpentOnceAndBoundToItsEffect(t *testing.T) {
	f := newRealFix(t, typedStoreCharter())
	r := f.issue(t, TypedRequest{Op: OpSendThirdParty, Send: &SendArgs{Platform: "x", Account: "acct", Destination: "@alice", ContentHash: "c1"}}, 0)
	other := r
	other.Send = &SendArgs{Platform: "x", Account: "acct", Destination: "@bob", ContentHash: "c1"}
	if _, err := f.e.Authorize(bg, other); !errors.Is(err, ErrArgsMismatch) {
		t.Fatalf("another destination: %v", err)
	}
	a, err := f.e.Authorize(bg, r)
	if err != nil || a.Op() != OpSendThirdParty || a.ID() != r.ApprovalID {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := f.e.Authorize(bg, r); !errors.Is(err, ErrAlreadyConsumed) {
		t.Fatalf("replay: %v", err)
	}
}

func TestRealApprovalRescindAndExpiry(t *testing.T) {
	f := newRealFix(t, typedStoreCharter())
	send := func(dest string) TypedRequest {
		return TypedRequest{Op: OpSendThirdParty, Send: &SendArgs{Platform: "x", Account: "acct", Destination: dest, ContentHash: "c1"}}
	}
	r := f.issue(t, send("@alice"), 0)
	if err := f.st.RescindApproval(bg, "p", 1, r.ApprovalID, "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.Authorize(bg, r); !errors.Is(err, ErrRevoked) {
		t.Fatalf("rescinded: %v", err)
	}
	e := f.issue(t, send("@bob"), 2)
	for i := 0; i < 3; i++ {
		if _, err := f.st.AppendAudit(bg, "p", 1, "note", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.e.Authorize(bg, e); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
}

func TestRealCumulativeCapAndStoppedRun(t *testing.T) {
	f := newRealFix(t, typedStoreCharter()) // max_per_run 2
	for _, d := range []string{"@alice", "@bob"} {
		r := f.issue(t, TypedRequest{Op: OpSendThirdParty, Send: &SendArgs{Platform: "x", Account: "acct", Destination: d, ContentHash: "c1"}}, 0)
		if _, err := f.e.Authorize(bg, r); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	third := f.issue(t, TypedRequest{Op: OpSendThirdParty, Send: &SendArgs{Platform: "x", Account: "acct", Destination: "@alice", ContentHash: "c2"}}, 0)
	if _, err := f.e.Authorize(bg, third); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("third send over max_per_run: %v", err)
	}
	s := f.issue(t, TypedRequest{Op: OpHostSetting, Setting: &SettingArgs{Key: "theme", Value: "dark"}}, 0)
	for _, to := range []store.RunState{store.StateStopRequested} {
		if _, err := f.st.Advance(bg, "p", 1, to, "op", "test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.e.Authorize(bg, s); err == nil {
		t.Fatal("an approval was spent after the run was stopped")
	}
}

func TestTheHashOfAnApprovalIsTheCharterHash(t *testing.T) {
	f := newRealFix(t, typedStoreCharter())
	c := typedStoreCharter()
	h := c.Hash()
	if f.act.Hash != hex.EncodeToString(h[:]) {
		t.Fatal("Active.Hash is not the hex of the charter hash")
	}
}
