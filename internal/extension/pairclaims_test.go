package extension

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account/accounttest"
)

type fakeCards struct {
	mu      sync.Mutex
	opened  []PendingClaim
	settled map[string]string
}

func (f *fakeCards) Open(_ context.Context, c PendingClaim) {
	f.mu.Lock()
	f.opened = append(f.opened, c)
	f.mu.Unlock()
}

func (f *fakeCards) Settle(_ context.Context, id, status string) {
	f.mu.Lock()
	if f.settled == nil {
		f.settled = map[string]string{}
	}
	f.settled[id] = status
	f.mu.Unlock()
}

func poll(srv *Server, id, secret string, mut func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9222/monoagent/pair/claim/"+id, nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Origin", extOrigin)
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	if mut != nil {
		mut(r)
	}
	w := httptest.NewRecorder()
	srv.handlePairPoll(w, r)
	return w
}

func askClaim(t *testing.T, srv *Server, i int, origin string) claimReply {
	t.Helper()
	w := claim(srv, instanceReq(i, origin))
	var rep claimReply
	_ = json.Unmarshal(w.Body.Bytes(), &rep)
	if w.Code != http.StatusAccepted || !rep.Pending || rep.ClaimID == "" || len(rep.PollSecret) != 64 {
		t.Fatalf("ask-mode claim = %d %s", w.Code, w.Body)
	}
	return rep
}

// The headline: a claim with a spoofed Origin gets no token until the owner
// approves it, whatever the caller does.
func TestAskModeSpoofedOriginGetsNoTokenWithoutApproval(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newClaimServer(t)
	cards := &fakeCards{}
	srv.SetPairCards(cards)
	w := claim(srv, claimReq(nil))
	if strings.Contains(w.Body.String(), `"token"`) {
		t.Fatalf("the claim reply carries a token: %s", w.Body)
	}
	var rep claimReply
	_ = json.Unmarshal(w.Body.Bytes(), &rep)
	if w.Code != http.StatusAccepted || !rep.Pending {
		t.Fatalf("claim = %d %s", w.Code, w.Body)
	}
	srv.flushCards()
	for i := 0; i < 3; i++ {
		p := poll(srv, rep.ClaimID, rep.PollSecret, nil)
		if p.Code != 200 || strings.Contains(p.Body.String(), `"token"`) || !strings.Contains(p.Body.String(), `"pending"`) {
			t.Fatalf("poll %d before approval = %d %s", i, p.Code, p.Body)
		}
	}
	if n := pairedBrowserCount(); n != 0 {
		t.Fatalf("%d browsers paired without approval", n)
	}
	if len(cards.opened) != 1 || cards.opened[0].ID != rep.ClaimID || !strings.Contains(cards.opened[0].Origin, "chrome-extension://") {
		t.Fatalf("cards opened = %+v", cards.opened)
	}
}

func TestAskModeApprovalYieldsExactlyOneToken(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newClaimServer(t)
	cards := &fakeCards{}
	srv.SetPairCards(cards)
	rep := askClaim(t, srv, 1, extOrigin)
	if _, err := DecidePendingClaim(rep.ClaimID, true, rep.Code); err != nil {
		t.Fatal(err)
	}
	p := poll(srv, rep.ClaimID, rep.PollSecret, nil)
	var got claimReply
	_ = json.Unmarshal(p.Body.Bytes(), &got)
	if p.Code != 200 || got.Status != ClaimApproved || len(got.Token) != 64 {
		t.Fatalf("poll after approval = %d %s", p.Code, p.Body)
	}
	if _, ok := pairedBrowserForToken(got.Token); !ok {
		t.Fatal("the delivered token does not work")
	}
	if again := poll(srv, rep.ClaimID, rep.PollSecret, nil); again.Code != 404 || strings.Contains(again.Body.String(), `"token"`) {
		t.Fatalf("a second poll = %d %s: the token must be delivered once", again.Code, again.Body)
	}
	if n := pairedBrowserCount(); n != 1 {
		t.Fatalf("%d browsers after one approval, want 1", n)
	}
	srv.flushCards()
	if cards.settled[rep.ClaimID] != "approved" {
		t.Fatalf("card status = %q", cards.settled[rep.ClaimID])
	}
	if left, _ := ListPendingClaims(); len(left) != 0 {
		t.Fatalf("pending record not destroyed: %+v", left)
	}
}

func TestAskModeDenyAndExpire(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newClaimServer(t)
	cards := &fakeCards{}
	srv.SetPairCards(cards)

	d := askClaim(t, srv, 1, extOrigin)
	if _, err := DecidePendingClaim(d.ClaimID, false, ""); err != nil {
		t.Fatal(err)
	}
	p := poll(srv, d.ClaimID, d.PollSecret, nil)
	if p.Code != 200 || !strings.Contains(p.Body.String(), `"denied"`) || strings.Contains(p.Body.String(), `"token"`) {
		t.Fatalf("poll after deny = %d %s", p.Code, p.Body)
	}
	srv.flushCards()
	if cards.settled[d.ClaimID] != "rejected" {
		t.Fatalf("denied card = %q", cards.settled[d.ClaimID])
	}

	e := askClaim(t, srv, 2, extOrigin)
	expireClaimNow(t, e.ClaimID)
	if _, err := DecidePendingClaim(e.ClaimID, true, e.Code); err == nil {
		t.Fatal("an expired claim could be approved")
	}
	p = poll(srv, e.ClaimID, e.PollSecret, nil)
	if p.Code != http.StatusGone || strings.Contains(p.Body.String(), `"token"`) {
		t.Fatalf("poll after expiry = %d %s", p.Code, p.Body)
	}
	if n := pairedBrowserCount(); n != 0 {
		t.Fatalf("%d browsers paired from denied/expired claims", n)
	}

	// An approved-then-expired claim yields nothing either.
	f := askClaim(t, srv, 3, extOrigin)
	if _, err := DecidePendingClaim(f.ClaimID, true, f.Code); err != nil {
		t.Fatal(err)
	}
	expireClaimNow(t, f.ClaimID)
	if p := poll(srv, f.ClaimID, f.PollSecret, nil); p.Code != http.StatusGone {
		t.Fatalf("poll of an approved but expired claim = %d %s", p.Code, p.Body)
	}
	if n := pairedBrowserCount(); n != 0 {
		t.Fatalf("an expired approval minted a token")
	}
}

func expireClaimNow(t *testing.T, id string) {
	t.Helper()
	unlock, err := lockState()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	list, err := readPendingLocked()
	if err != nil {
		t.Fatal(err)
	}
	for i := range list {
		if list[i].ID == id {
			list[i].ExpiresAt = time.Now().Add(-time.Second)
		}
	}
	if err := writePendingLocked(list); err != nil {
		t.Fatal(err)
	}
}

func TestAskModePollNeedsTheSecretAndTheOrigin(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newClaimServer(t)
	rep := askClaim(t, srv, 1, extOrigin)
	if _, err := DecidePendingClaim(rep.ClaimID, true, rep.Code); err != nil {
		t.Fatal(err)
	}
	other := "chrome-extension://otherotherotherotherotherotherot"
	for name, p := range map[string]*httptest.ResponseRecorder{
		"no secret":            poll(srv, rep.ClaimID, "", nil),
		"wrong secret":         poll(srv, rep.ClaimID, strings.Repeat("a", 64), nil),
		"id as secret":         poll(srv, rep.ClaimID, rep.ClaimID, nil),
		"unknown claim":        poll(srv, "0000000000000000", rep.PollSecret, nil),
		"other origin":         poll(srv, rep.ClaimID, rep.PollSecret, func(r *http.Request) { r.Header.Set("Origin", other) }),
		"web origin":           poll(srv, rep.ClaimID, rep.PollSecret, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }),
		"no origin, no secret": poll(srv, rep.ClaimID, "", func(r *http.Request) { r.Header.Del("Origin") }),
		"rebound host":         poll(srv, rep.ClaimID, rep.PollSecret, func(r *http.Request) { r.Host = "evil.example" }),
		"remote peer":          poll(srv, rep.ClaimID, rep.PollSecret, func(r *http.Request) { r.RemoteAddr = "192.0.2.7:1" }),
	} {
		if p.Code != 404 && p.Code != 403 || strings.Contains(p.Body.String(), `"token"`) {
			t.Errorf("%s: poll = %d %s", name, p.Code, p.Body)
		}
	}
	if n := pairedBrowserCount(); n != 0 {
		t.Fatalf("a poll without the secret minted a token")
	}
	// Chrome sends no Origin on a GET: the secret is the proof.
	noOrigin := func(r *http.Request) { r.Header.Del("Origin") }
	if p := poll(srv, rep.ClaimID, rep.PollSecret, noOrigin); p.Code != 200 || !strings.Contains(p.Body.String(), `"token"`) {
		t.Fatalf("the right poll after the wrong ones = %d %s", p.Code, p.Body)
	}
}

func TestAskModeQueueIsBoundedAndRefusesRatherThanEvicts(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newClaimServer(t)
	// The origin is the claimant's to choose, so it earns no share: the same
	// origin may fill the queue, each from its own instance.
	first := askClaim(t, srv, 1, extOrigin)
	for i := 1; i < maxPendingClaims; i++ {
		askClaim(t, srv, 1+i, extOrigin)
	}
	if w := claim(srv, instanceReq(30, extOrigin)); w.Code != 429 || strings.Contains(w.Body.String(), "pollSecret") {
		t.Fatalf("claim into a full queue = %d %s", w.Code, w.Body)
	}
	list, _ := ListPendingClaims()
	if len(list) != maxPendingClaims {
		t.Fatalf("%d pending, want %d", len(list), maxPendingClaims)
	}
	if p := poll(srv, first.ClaimID, first.PollSecret, nil); p.Code != 200 {
		t.Fatalf("the oldest waiting claim was evicted: %d %s", p.Code, p.Body)
	}
}

// One waiting claim per instance id: a repeat replaces that instance's own
// claim (its card is closed) and nobody else's; the replaced claim is gone.
func TestAskModeOneWaitingClaimPerInstance(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newClaimServer(t)
	cards := &fakeCards{}
	srv.SetPairCards(cards)
	other := askClaim(t, srv, 7, extOrigin)
	old := askClaim(t, srv, 1, extOrigin)
	again := askClaim(t, srv, 1, extOrigin)
	again2 := askClaim(t, srv, 1, "chrome-extension://"+strings.Repeat("q", 32))
	srv.flushCards()
	list, _ := ListPendingClaims()
	if len(list) != 2 {
		t.Fatalf("%d waiting, want 2 (one per instance): %+v", len(list), list)
	}
	if p := poll(srv, old.ClaimID, old.PollSecret, nil); p.Code != 404 {
		t.Fatalf("the replaced claim still answers: %d %s", p.Code, p.Body)
	}
	if p := poll(srv, again.ClaimID, again.PollSecret, nil); p.Code != 404 {
		t.Fatalf("the replaced claim still answers: %d %s", p.Code, p.Body)
	}
	if p := poll(srv, other.ClaimID, other.PollSecret, nil); p.Code != 200 {
		t.Fatalf("another instance's claim was touched: %d", p.Code)
	}
	if cards.settled[old.ClaimID] != "expired" || cards.settled[again.ClaimID] != "expired" || cards.settled[again2.ClaimID] != "" {
		t.Fatalf("cards settled = %v", cards.settled)
	}
}

// Approving a claim for an instance that is paired replaces it: that is the
// owner's decision, shown on the card; nothing else replaces it.
func TestAskModeApprovalMayReplaceAnInstanceTheOwnerSees(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newClaimServer(t)
	tok, _, err := addPairedBrowser(PairedBrowser{Label: "old", Instance: "0123456789abcdef"}, mintOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rep := askClaim(t, srv, 0, extOrigin)
	_ = rep
	w := claim(srv, claimReq(nil)) // same instance as the paired one
	var r2 claimReply
	_ = json.Unmarshal(w.Body.Bytes(), &r2)
	if w.Code != http.StatusAccepted {
		t.Fatalf("claim = %d %s", w.Code, w.Body)
	}
	if _, ok := pairedBrowserForToken(tok); !ok {
		t.Fatal("a pending claim killed the existing token before the owner decided")
	}
	_, _ = DecidePendingClaim(r2.ClaimID, true, r2.Code)
	p := poll(srv, r2.ClaimID, r2.PollSecret, nil)
	if p.Code != 200 || !strings.Contains(p.Body.String(), `"token"`) {
		t.Fatalf("poll = %d %s", p.Code, p.Body)
	}
	if _, ok := pairedBrowserForToken(tok); ok {
		t.Fatal("the approved claim did not replace the old token of its instance")
	}
}

func newClaimT(t *testing.T, label string) (PendingClaim, string) {
	t.Helper()
	c, secret, err := CreatePendingClaim(PendingClaim{Label: label, Origin: "chrome-extension://" + label, Instance: label + "-instance-1"})
	if err != nil {
		t.Fatal(err)
	}
	return c, PairCode(c.ID, secret)
}

// "The newest" is the attacker's to choose (the last forged claim wins), so no
// id names a claim only when exactly one is waiting.
func TestDecidePendingClaimWithoutAnIDNeedsExactlyOne(t *testing.T) {
	setHome(t, t.TempDir())
	a, aCode := newClaimT(t, "a")
	time.Sleep(5 * time.Millisecond)
	b, bCode := newClaimT(t, "b")
	if _, err := DecidePendingClaim("ab", true, aCode); err == nil {
		t.Fatal("a 2-character prefix must be refused")
	}
	if _, err := DecidePendingClaim("", true, bCode); !errors.Is(err, ErrAmbiguousClaim) {
		t.Fatalf("approving with no id while two wait = %v, want ErrAmbiguousClaim", err)
	}
	if _, err := DecidePendingClaim("", false, ""); !errors.Is(err, ErrAmbiguousClaim) {
		t.Fatalf("denying with no id while two wait = %v, want ErrAmbiguousClaim", err)
	}
	if left, _ := ListPendingClaims(); len(left) != 2 {
		t.Fatalf("a refused decision changed the queue: %+v", left)
	}
	if got, err := DecidePendingClaim(a.ID[:6], false, ""); err != nil || got.ID != a.ID || got.Status != ClaimDenied {
		t.Fatalf("by prefix = %+v, %v", got, err)
	}
	got, err := DecidePendingClaim("", true, bCode)
	if err != nil || got.ID != b.ID {
		t.Fatalf("the only waiting claim = %+v, %v; want %s", got, err, b.ID)
	}
	if _, err := DecidePendingClaim("", true, bCode); err == nil {
		t.Fatal("nothing is waiting any more")
	}
}

// The owner approves the claim THEIR browser shows: the code of another claim
// (a forged one's) or none does not approve.
func TestApprovalNeedsTheCodeOfThatClaim(t *testing.T) {
	setHome(t, t.TempDir())
	a, aCode := newClaimT(t, "real")
	_, forgedCode := newClaimT(t, "forged")
	for name, code := range map[string]string{"none": "", "other claim's": forgedCode, "garbage": "ZZZ-ZZZ"} {
		if _, err := DecidePendingClaim(a.ID, true, code); !errors.Is(err, ErrBadCode) {
			t.Fatalf("%s code: err = %v, want ErrBadCode", name, err)
		}
	}
	if left, _ := ListPendingClaims(); len(left) != 2 {
		t.Fatalf("a wrong code changed the queue: %+v", left)
	}
	if !regexp.MustCompile(`^[A-Z2-9]{3}-[A-Z2-9]{3}$`).MatchString(aCode) {
		t.Fatalf("code %q is not ABC-123 shaped", aCode)
	}
	// Case, dash and spaces do not matter when typing it.
	typed := strings.ToLower(strings.ReplaceAll(aCode, "-", " "))
	if got, err := DecidePendingClaim(a.ID, true, typed); err != nil || got.Status != ClaimApproved || got.DecidedAt == nil {
		t.Fatalf("approve with the right code = %+v, %v", got, err)
	}
	// Denying needs no code.
	b, _ := newClaimT(t, "other")
	if _, err := DecidePendingClaim(b.ID, false, ""); err != nil {
		t.Fatal(err)
	}
}

func TestDenyAllClearsTheQueue(t *testing.T) {
	setHome(t, t.TempDir())
	for i := 0; i < 3; i++ {
		newClaimT(t, string(rune('a'+i))+"xyz")
	}
	got, err := DenyAllPendingClaims()
	if err != nil || len(got) != 3 {
		t.Fatalf("deny all = %d, %v", len(got), err)
	}
	if left, _ := ListPendingClaims(); len(left) != 0 {
		t.Fatalf("still waiting: %+v", left)
	}
	if got, _ := DenyAllPendingClaims(); len(got) != 0 {
		t.Fatalf("a second deny all denied %d", len(got))
	}
}

// A decided claim is a tombstone: an approval cannot be undone by deciding
// again, and a denial cannot be turned into an approval.
func TestDecidedClaimStaysDecided(t *testing.T) {
	setHome(t, t.TempDir())
	a, code := newClaimT(t, "aaaa")
	if _, err := DecidePendingClaim(a.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := DecidePendingClaim(a.ID, true, code); !errors.Is(err, ErrNoSuchClaim) {
		t.Fatalf("approving a denied claim = %v", err)
	}
	if left, _ := DenyAllPendingClaims(); len(left) != 0 {
		t.Fatal("deny all touched a decided claim")
	}
	b, bcode := newClaimT(t, "bbbb")
	if _, err := DecidePendingClaim(b.ID, true, bcode); err != nil {
		t.Fatal(err)
	}
	if _, err := DecidePendingClaim(b.ID, false, ""); !errors.Is(err, ErrNoSuchClaim) {
		t.Fatalf("denying an approved claim = %v", err)
	}
	// The sweep of expired claims keeps the decided, unexpired one.
	expiredClaimIDs()
	list, _ := readPendingForTest(t)
	found := false
	for _, c := range list {
		found = found || (c.ID == b.ID && c.Status == ClaimApproved)
	}
	if !found {
		t.Fatalf("the approved claim vanished: %+v", list)
	}
}

func readPendingForTest(t *testing.T) ([]PendingClaim, error) {
	t.Helper()
	unlock, err := lockState()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	return readPendingLocked()
}

// Decided tombstones wait for their browser to collect the outcome; they must
// not eat the slots a legitimate claim needs (a forger would keep the queue
// full otherwise, even after the owner ran `deny --all`).
func TestDecidedTombstonesDoNotConsumeQueueSlots(t *testing.T) {
	setHome(t, t.TempDir())
	for i := 0; i < maxPendingClaims; i++ {
		newClaimT(t, "forged"+strconv.Itoa(i))
	}
	if _, _, err := CreatePendingClaim(PendingClaim{Label: "x", Instance: "late"}); !errors.Is(err, ErrClaimsFull) {
		t.Fatalf("full queue: err = %v", err)
	}
	if got, err := DenyAllPendingClaims(); err != nil || len(got) != maxPendingClaims {
		t.Fatalf("deny all = %d, %v", len(got), err)
	}
	for round := 0; round < 3; round++ {
		if _, _, err := CreatePendingClaim(PendingClaim{Label: "legit", Instance: "legit-" + strconv.Itoa(round)}); err != nil {
			t.Fatalf("round %d: legitimate claim after deny --all: %v", round, err)
		}
		if _, err := DenyAllPendingClaims(); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := readPendingForTest(t)
	if len(list) > 3*maxPendingClaims {
		t.Fatalf("file holds %d records, cap is %d", len(list), 3*maxPendingClaims)
	}
}

// Five wrong confirmation codes destroy the claim: the code cannot be guessed
// against a claim that stays open.
func TestWrongCodesDestroyTheClaim(t *testing.T) {
	setHome(t, t.TempDir())
	c, code := newClaimT(t, "guess")
	for i := 0; i < 5; i++ {
		if _, err := DecidePendingClaim(c.ID, true, "ZZZ-ZZ"+strconv.Itoa(2+i)); !errors.Is(err, ErrBadCode) {
			t.Fatalf("attempt %d: err = %v", i, err)
		}
	}
	if _, err := DecidePendingClaim(c.ID, true, code); err == nil {
		t.Fatal("the right code approved a claim after 5 wrong ones")
	}
	if left, _ := ListPendingClaims(); len(left) != 0 {
		t.Fatalf("destroyed claim still waiting: %+v", left)
	}
}

// Expired claims are pruned by every read-modify-write, not only by create.
func TestDecisionPrunesExpiredClaims(t *testing.T) {
	setHome(t, t.TempDir())
	old, _ := newClaimT(t, "old")
	live, _ := newClaimT(t, "live")
	expireClaimNow(t, old.ID)
	if _, err := DecidePendingClaim(live.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	list, _ := readPendingForTest(t)
	for _, c := range list {
		if c.ID == old.ID {
			t.Fatal("expired claim survived a decision")
		}
	}
}
