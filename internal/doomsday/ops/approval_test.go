package ops

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type fakeApprovals struct {
	mu   sync.Mutex
	recs map[string]ApprovalRecord
	seq  int64
	gets int
}

func (f *fakeApprovals) Get(_ context.Context, id string) (ApprovalRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	r, ok := f.recs[id]
	if !ok {
		return ApprovalRecord{}, ErrUnknownApproval
	}
	return r, nil
}

func (f *fakeApprovals) Seq(context.Context, string) (int64, error) { return f.seq, nil }

type trig struct {
	e   *TypedEnforcer
	ap  *fakeApprovals
	l   *fakeLedger
	au  *fakeAudit
	act Active
	n   int
}

func typedCharter() Constraints {
	v := *testVault
	return Constraints{
		SendThirdParty:   &SendThirdParty{Platform: "x", Account: "acct", Destinations: []string{"@Alice"}, MaxPerRun: 2, ContentClass: "text"},
		VaultUse:         &v,
		LocalDestructive: &LocalDestructive{Roots: []string{"/data/r"}, Verbs: []string{"delete", "overwrite"}, MaxCount: 3, MaxBytes: 100},
		HostSetting:      &HostSetting{Keys: map[string][]string{"theme": {"dark"}, "motd": {Any}}},
	}
}

func newTrig(t *testing.T, c Constraints) *trig {
	t.Helper()
	act := Active{ID: "c1", Hash: "H", Generation: 1, Profile: "p", Constraints: c}
	ap := &fakeApprovals{recs: map[string]ApprovalRecord{}, seq: 10}
	l, au := newLedger(), &fakeAudit{}
	return &trig{e: &TypedEnforcer{Charters: &fakeCharters{act: act, live: true}, Approvals: ap, Ledger: l, Auditor: au, Bounds: bounds}, ap: ap, l: l, au: au, act: act}
}

func sendReq(dest, content string) TypedRequest {
	return TypedRequest{Op: OpSendThirdParty, Send: &SendArgs{Platform: "x", Account: "acct", Destination: dest, ContentHash: content}}
}
func vaultReq() TypedRequest {
	a := testReq
	return TypedRequest{Op: OpVaultUse, Vault: &a}
}

// issue records an approval for exactly the effect of req (what the boss approved), addressed to session 7.
func (g *trig) issue(t *testing.T, req TypedRequest) TypedRequest {
	t.Helper()
	g.n++
	req.Profile, req.CharterID, req.Hash, req.Generation = "p", "c1", "H", 1
	req.Caller = Caller{Session: 7}
	canon, _, _, err := typedEffect(g.act.Constraints, req)
	if err != nil {
		t.Fatalf("the approved effect is outside the charter: %v", err)
	}
	h, _ := ArgsHash(req.Op, canon)
	req.ApprovalID = string(rune('a'+g.n)) + "-approval"
	g.ap.recs[req.ApprovalID] = ApprovalRecord{ID: req.ApprovalID, Profile: "p", CharterID: "c1", CharterHash: "H", Generation: 1, Op: req.Op, ArgsHash: h, Caller: 7, ExpiresSeq: 100}
	return req
}

func TestAnApprovedEffectIsAuthorizedOnceAndAudited(t *testing.T) {
	g := newTrig(t, typedCharter())
	for _, req := range []TypedRequest{
		sendReq("@alice", "c1"),
		vaultReq(),
		{Op: OpLocalDestructive, Local: &LocalArgs{Root: "/data/r", Verb: "delete", Targets: []string{"a/b", "c"}, Bytes: 5}},
		{Op: OpHostSetting, Setting: &SettingArgs{Key: "theme", Value: "dark"}},
	} {
		r := g.issue(t, req)
		a, err := g.e.Authorize(bg, r)
		if err != nil || a.Op() != r.Op || a.ArgsHash() == "" || a.ID() != r.ApprovalID {
			t.Fatalf("%s: %+v %v", r.Op, a, err)
		}
		if _, err := g.e.Authorize(bg, r); !errors.Is(err, ErrAlreadyConsumed) {
			t.Fatalf("%s: a second presentation: %v", r.Op, err)
		}
	}
	if len(g.l.audits) != 4 {
		t.Fatalf("audits %d", len(g.l.audits))
	}
}

func TestADifferentEffectThanApprovedIsRefusedAndLeavesTheApprovalUnspent(t *testing.T) { // T1, T5c, T5g, T17
	g := newTrig(t, typedCharter())
	g.act.Constraints.SendThirdParty.Destinations = []string{"@alice", "@bob"}
	g.e.Charters = &fakeCharters{act: g.act, live: true}
	a := g.issue(t, sendReq("@alice", "c1"))
	other := a
	other.Send = &SendArgs{Platform: "x", Account: "acct", Destination: "@bob", ContentHash: "c1"}
	if _, err := g.e.Authorize(bg, other); !errors.Is(err, ErrArgsMismatch) {
		t.Fatalf("another destination: %v", err)
	}
	payload := a
	payload.Send = &SendArgs{Platform: "x", Account: "acct", Destination: "@alice", ContentHash: "c2"}
	if _, err := g.e.Authorize(bg, payload); !errors.Is(err, ErrArgsMismatch) {
		t.Fatalf("another payload: %v", err)
	}
	v := g.issue(t, vaultReq())
	extra := v
	x := *v.Vault
	x.Headers = [][2]string{{"X-Extra", "1"}}
	extra.Vault = &x
	if _, err := g.e.Authorize(bg, extra); !errors.Is(err, ErrArgsMismatch) {
		t.Fatalf("an extra header: %v", err)
	}
	// crossed: two approvals for different arguments of one operation, each only for its own
	b := g.issue(t, sendReq("@bob", "c1"))
	crossed := b
	crossed.ApprovalID = a.ApprovalID
	if _, err := g.e.Authorize(bg, crossed); !errors.Is(err, ErrArgsMismatch) {
		t.Fatalf("crossed: %v", err)
	}
	if len(g.l.consumed) != 0 {
		t.Fatalf("a refusal spent an approval: %v", g.l.consumed)
	}
	if _, err := g.e.Authorize(bg, a); err != nil {
		t.Fatalf("the approval was spent by the refusals: %v", err)
	}
	if len(g.au.rows) == 0 || g.au.rows[0].ArgsHash == "" {
		t.Fatal("a mismatch was not audited with the approved hash")
	}
}

func TestVaultRequestsOutsideTheConstraintsNeverReachTheApproval(t *testing.T) { // T5, T12
	g := newTrig(t, typedCharter())
	for name, mut := range map[string]func(*VaultArgs){
		"another secret":                        func(a *VaultArgs) { a.Secret = "S2" },
		"another host":                          func(a *VaultArgs) { a.URL = "https://example.org/x" },
		"suffix not label":                      func(a *VaultArgs) { a.URL = "https://evil-example.com/x" },
		"approved host as subdomain of another": func(a *VaultArgs) { a.URL = "https://example.com.evil.com/x" },
		"userinfo trick":                        func(a *VaultArgs) { a.URL = "https://example.com@evil.com/x" },
		"userinfo":                              func(a *VaultArgs) { a.URL = "https://user@example.com/x" },
		"port":                                  func(a *VaultArgs) { a.URL = "https://example.com:8443/x" },
		"plain http":                            func(a *VaultArgs) { a.URL = "http://example.com/x" },
		"ip literal":                            func(a *VaultArgs) { a.URL = "https://127.0.0.1/x" },
		"no injection point":                    func(a *VaultArgs) { a.Inject = "" },
		"odd method":                            func(a *VaultArgs) { a.Method = "CONNECT" },
	} {
		req := vaultReq()
		mut(req.Vault)
		req.Profile, req.CharterID, req.Hash, req.Generation, req.Caller, req.ApprovalID = "p", "c1", "H", 1, Caller{Session: 7}, "any"
		if _, err := g.e.Authorize(bg, req); !errors.Is(err, ErrOutsideConstraints) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if g.ap.gets != 0 || len(g.l.consumed) != 0 {
		t.Fatalf("the approval was read (%d) or spent for a request outside the charter", g.ap.gets)
	}
	// the same host in another canonical spelling and a subdomain are the approved host
	for _, u := range []string{"https://EXAMPLE.com./x", "https://api.example.com/x"} {
		req := vaultReq()
		req.Vault.URL = u
		if _, err := g.e.Authorize(bg, g.issue(t, req)); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
	}
}

func TestAnApprovalIsBoundToItsOperationGenerationCharterAndCaller(t *testing.T) { // T6, T7, T8
	g := newTrig(t, typedCharter())
	r := g.issue(t, sendReq("@alice", "c1"))
	rec := g.ap.recs[r.ApprovalID]
	try := func(name string, mut func(*ApprovalRecord), want error) {
		t.Helper()
		g.ap.recs[r.ApprovalID] = rec
		x := rec
		mut(&x)
		g.ap.recs[r.ApprovalID] = x
		if _, err := g.e.Authorize(bg, r); !errors.Is(err, want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	try("another operation", func(a *ApprovalRecord) { a.Op = OpVaultUse }, ErrOpMismatch)
	try("another generation", func(a *ApprovalRecord) { a.Generation = 2 }, ErrCharterStale)
	try("another profile", func(a *ApprovalRecord) { a.Profile = "q" }, ErrCharterStale)
	try("another charter hash", func(a *ApprovalRecord) { a.CharterHash = "X" }, ErrCharterStale)
	try("another charter id", func(a *ApprovalRecord) { a.CharterID = "c2" }, ErrCharterStale)
	try("another session", func(a *ApprovalRecord) { a.Caller = 8 }, ErrCallerMismatch)
	try("no caller binding", func(a *ApprovalRecord) { a.Caller = 0 }, ErrCallerMismatch)
	try("rescinded", func(a *ApprovalRecord) { a.Revoked = true }, ErrRevoked) // T21
	g.ap.seq = 101
	try("expired", func(a *ApprovalRecord) {}, ErrExpired)
	g.ap.seq = 10
	// the request itself: stale charter, another presenter
	stale := r
	stale.Generation = 2
	if _, err := g.e.Authorize(bg, stale); !errors.Is(err, ErrCharterStale) {
		t.Fatalf("stale request: %v", err)
	}
	other := r
	other.Caller = Caller{Session: 8}
	g.ap.recs[r.ApprovalID] = rec
	if _, err := g.e.Authorize(bg, other); !errors.Is(err, ErrCallerMismatch) {
		t.Fatalf("another presenter: %v", err)
	}
	if len(g.l.consumed) != 0 {
		t.Fatal("a refusal spent the approval")
	}
}

func TestOneApprovalSpendsOnceUnderConcurrency(t *testing.T) { // T9
	g := newTrig(t, typedCharter())
	r := g.issue(t, sendReq("@alice", "c1"))
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.e.Authorize(bg, r); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d presentations succeeded", ok)
	}
}

func TestCountersAreSpentAtTheConsume(t *testing.T) { // T20
	g := newTrig(t, typedCharter())
	g.act.Constraints.SendThirdParty.Destinations = []string{Any}
	g.e.Charters = &fakeCharters{act: g.act, live: true}
	for i := 0; i < 2; i++ {
		if _, err := g.e.Authorize(bg, g.issue(t, sendReq("@alice", "c"+string(rune('1'+i))))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.e.Authorize(bg, g.issue(t, sendReq("@alice", "c3"))); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("the third send of a run capped at two: %v", err)
	}
	// local: count and bytes
	a := g.issue(t, TypedRequest{Op: OpLocalDestructive, Local: &LocalArgs{Root: "/data/r", Verb: "delete", Targets: []string{"a", "b"}, Bytes: 60}})
	if _, err := g.e.Authorize(bg, a); err != nil {
		t.Fatal(err)
	}
	b := g.issue(t, TypedRequest{Op: OpLocalDestructive, Local: &LocalArgs{Root: "/data/r", Verb: "delete", Targets: []string{"c"}, Bytes: 60}})
	if _, err := g.e.Authorize(bg, b); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("bytes over the run ceiling: %v", err)
	}
	// vault attempts: three then no more
	for i := 0; i < 3; i++ {
		if _, err := g.e.Authorize(bg, g.issue(t, vaultReq())); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := g.e.Authorize(bg, g.issue(t, vaultReq())); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("a fourth attempt of three: %v", err)
	}
}

func TestOperationsOffTheTypedAllowlistAreDenied(t *testing.T) { // T15, T5d, T5h2
	g := newTrig(t, typedCharter())
	for _, op := range []string{"install.package", "vault.grant", "agent.access", "", "send.third_party ", OpPlanApprove} {
		r := TypedRequest{Op: op, Profile: "p", CharterID: "c1", Hash: "H", Generation: 1, ApprovalID: "x", Caller: Caller{Session: 7}}
		if _, err := g.e.Authorize(bg, r); !errors.Is(err, ErrNotAllowlisted) {
			t.Fatalf("%q: %v", op, err)
		}
	}
	for _, k := range NeverLiftableSettings {
		c := typedCharter()
		c.HostSetting = &HostSetting{Keys: map[string][]string{k: {Any}}}
		if err := c.Validate(bounds); !errors.Is(err, ErrNotEnabled) && !errors.Is(err, ErrInvalidLimits) {
			t.Fatalf("a charter listing the never-liftable %q: %v", k, err)
		}
		g2 := newTrig(t, typedCharter())
		r := g2.issue(t, TypedRequest{Op: OpHostSetting, Setting: &SettingArgs{Key: "motd", Value: "x"}})
		r.Setting.Key = k
		if _, err := g2.e.Authorize(bg, r); !errors.Is(err, ErrOutsideConstraints) {
			t.Fatalf("a request for the never-liftable %q: %v", k, err)
		}
	}
}

func TestALineWithoutConstraintsEnablesNothingForTheTypedOperations(t *testing.T) { // T13
	g := newTrig(t, Constraints{})
	for _, r := range []TypedRequest{sendReq("@alice", "c"), vaultReq(),
		{Op: OpLocalDestructive, Local: &LocalArgs{Root: "/data/r", Verb: "delete", Targets: []string{"a"}}},
		{Op: OpHostSetting, Setting: &SettingArgs{Key: "motd", Value: "x"}}} {
		r.Profile, r.CharterID, r.Hash, r.Generation, r.Caller, r.ApprovalID = "p", "c1", "H", 1, Caller{Session: 7}, "x"
		if _, err := g.e.Authorize(bg, r); !errors.Is(err, ErrNotEnabled) {
			t.Fatalf("%s: %v", r.Op, err)
		}
	}
}

func TestNoCharterDeniesTheTypedOperationsBeforeReadingAnything(t *testing.T) {
	g := newTrig(t, typedCharter())
	g.e.Charters = &fakeCharters{act: g.act, live: false}
	if _, err := g.e.Authorize(bg, sendReq("@alice", "c")); !errors.Is(err, ErrNoCharter) || g.ap.gets != 0 {
		t.Fatalf("%v gets %d", err, g.ap.gets)
	}
}

func TestAFailedConsumeLeavesNoAuthorization(t *testing.T) { // T25
	g := newTrig(t, typedCharter())
	r := g.issue(t, sendReq("@alice", "c1"))
	g.l.failNext = true // the audit insert fails: nothing is consumed
	if _, err := g.e.Authorize(bg, r); err == nil {
		t.Fatal("authorized with no audit row")
	}
	if len(g.l.consumed) != 0 {
		t.Fatal("consumed without its audit row")
	}
	if _, err := g.e.Authorize(bg, r); err != nil {
		t.Fatalf("the approval was lost by the failed consume: %v", err)
	}
}

func TestLocalTargetSetsAreExactAndRelative(t *testing.T) {
	g := newTrig(t, typedCharter())
	for name, a := range map[string]LocalArgs{
		"dotdot":              {Root: "/data/r", Verb: "delete", Targets: []string{"a/../../x"}},
		"absolute":            {Root: "/data/r", Verb: "delete", Targets: []string{"/etc/passwd"}},
		"nul":                 {Root: "/data/r", Verb: "delete", Targets: []string{"a\x00b"}},
		"twice":               {Root: "/data/r", Verb: "delete", Targets: []string{"a", "./a"}},
		"another root":        {Root: "/data/other", Verb: "delete", Targets: []string{"a"}},
		"another verb":        {Root: "/data/r", Verb: "chmod", Targets: []string{"a"}},
		"overwrite no digest": {Root: "/data/r", Verb: "overwrite", Targets: []string{"a"}},
		"delete with digest":  {Root: "/data/r", Verb: "delete", Targets: []string{"a"}, ContentHash: "h"},
		"empty":               {Root: "/data/r", Verb: "delete"},
		"negative bytes":      {Root: "/data/r", Verb: "delete", Targets: []string{"a"}, Bytes: -1},
	} {
		a := a
		r := TypedRequest{Op: OpLocalDestructive, Local: &a, Profile: "p", CharterID: "c1", Hash: "H", Generation: 1, Caller: Caller{Session: 7}, ApprovalID: "x"}
		if _, err := g.e.Authorize(bg, r); !errors.Is(err, ErrOutsideConstraints) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// the approval binds the exact set: a set with an extra target is another hash
	r := g.issue(t, TypedRequest{Op: OpLocalDestructive, Local: &LocalArgs{Root: "/data/r", Verb: "delete", Targets: []string{"a", "b"}}})
	bigger := r
	bigger.Local = &LocalArgs{Root: "/data/r", Verb: "delete", Targets: []string{"a", "b", "c"}}
	if _, err := g.e.Authorize(bg, bigger); !errors.Is(err, ErrArgsMismatch) {
		t.Fatalf("a larger set: %v", err)
	}
	reordered := r
	reordered.Local = &LocalArgs{Root: "/data/r", Verb: "delete", Targets: []string{"b", "a"}}
	if _, err := g.e.Authorize(bg, reordered); err != nil {
		t.Fatalf("the same set in another order: %v", err)
	}
}
