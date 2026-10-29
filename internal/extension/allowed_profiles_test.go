package extension

import (
	"encoding/json"
	"errors"
	"github.com/monoes/mono-agent/internal/account/accounttest"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// mintRestricted pairs a browser whose token may claim only the given profiles.
func mintRestricted(t *testing.T, instance string, allowed ...string) (token string, id string) {
	t.Helper()
	tok, e, err := addPairedBrowser(PairedBrowser{Label: "b", Instance: instance}, mintOpts{})
	if err != nil {
		t.Fatalf("addPairedBrowser: %v", err)
	}
	if _, err := SetAllowedProfiles(e.ID, allowed, true); err != nil {
		t.Fatalf("SetAllowedProfiles: %v", err)
	}
	return tok, e.ID
}

// dialToken connects with a paired token without reading anything back (a
// timed-out read would poison the connection for the reads a test makes later).
func dialToken(t *testing.T, wsURL, token, instance string, profiles ...string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	first := ""
	if len(profiles) > 0 {
		first = profiles[0]
	}
	if err := conn.WriteJSON(authFrame{Type: "auth", Token: token, Instance: instance, Version: "1.12.0", Profile: first, Profiles: profiles}); err != nil {
		t.Fatalf("write auth frame: %v", err)
	}
	return conn
}

// stallable makes the connection stall on a client that does not read: both ends get a tiny socket
// buffer, so a large write blocks whatever the machine's default buffer sizes are (a CI runner's
// loopback absorbs tens of megabytes otherwise).
func stallable(t *testing.T, c *extConn, client *websocket.Conn) {
	t.Helper()
	if tc, ok := client.UnderlyingConn().(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(1024)
	}
	if tc, ok := c.ws.UnderlyingConn().(*net.TCPConn); ok {
		_ = tc.SetWriteBuffer(1024)
	}
}

// closedUnauthorized reports whether the next read on conn is the 4401 close.
func closedUnauthorized(conn *websocket.Conn, within time.Duration) bool {
	conn.SetReadDeadline(time.Now().Add(within))
	_, _, err := conn.ReadMessage()
	ce, ok := err.(*websocket.CloseError)
	return ok && ce.Code == unauthorizedCloseCode
}

func TestAuthFrameOutsideTheAllowedSetIsRefused(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, _ := mintRestricted(t, instA, "p-a")
	if _, refused := dialWithToken(t, wsURL, tok, instA, "p-a", "p-x"); !refused {
		t.Fatal("a claim naming a profile outside the allowed set was accepted")
	}
	if got := srv.Browsers(); len(got) != 0 {
		t.Fatalf("the refused socket was installed: %+v", got)
	}
}

func TestAuthFrameInsideTheAllowedSetOrUnboundWorks(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, _ := mintRestricted(t, instA, "p-a", "p-b")
	if _, refused := dialWithToken(t, wsURL, tok, instA, "p-b"); refused {
		t.Fatal("an allowed profile was refused")
	}
	waitBrowsers(t, srv, 1)
	tokB, _ := mintRestricted(t, instB) // restricted to nothing: only the unbound default browser
	if _, refused := dialWithToken(t, wsURL, tokB, instB); refused {
		t.Fatal("an unbound browser must keep working under an empty allowed set")
	}
	waitBrowsers(t, srv, 2)
}

func TestBindingFrameOutsideTheAllowedSetClosesTheSocket(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, _ := mintRestricted(t, instA, "p-a")
	conn := dialToken(t, wsURL, tok, instA, "p-a")
	waitBrowsers(t, srv, 1)
	if err := conn.WriteJSON(map[string]any{"kind": "binding", "profile": "p-a", "profiles": []string{"p-a", "p-x"}}); err != nil {
		t.Fatal(err)
	}
	// Well under the 2s revoke sweep: the frame itself is refused, not the sweep later.
	if !closedUnauthorized(conn, 800*time.Millisecond) {
		t.Fatal("a binding frame naming a profile outside the set did not close the socket with 4401 at once")
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(srv.Browsers()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the browser stayed attached: %+v", srv.Browsers())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBindingFrameInsideTheAllowedSetIsApplied(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, _ := mintRestricted(t, instA, "p-a", "p-b")
	conn := dialToken(t, wsURL, tok, instA, "p-a")
	waitBrowsers(t, srv, 1)
	if err := conn.WriteJSON(map[string]any{"kind": "binding", "profile": "p-b", "profiles": []string{"p-b"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := srv.Browsers(); len(got) == 1 && len(got[0].ProfileSet()) == 1 && got[0].ProfileSet()[0] == "p-b" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("binding not applied: %+v", srv.Browsers())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The relay forwards set_binding with only the relay token, so the allowed
// set has to be enforced where the bridge sends the command.
func TestSetBindingOutsideTheAllowedSetIsRefusedOnTheRelay(t *testing.T) {
	srv, base, wsURL, _ := startStatusTestServer(t)
	tok, _ := mintRestricted(t, instA, "p-a")
	conn := dialToken(t, wsURL, tok, instA, "p-a")
	waitBrowsers(t, srv, 1)

	r := NewRemoteSender(base)
	// Inside the set it goes through.
	errc := make(chan error, 1)
	go func() {
		_, err := r.SendCommandTo(Target{Instance: instA}, setBindingCmd("p-a"), 3*time.Second)
		errc <- err
	}()
	if cmd := answerNext(t, conn, 0, nil); cmd == nil || cmd.Type != CmdSetBinding {
		t.Fatalf("got %+v", cmd)
	}
	if err := <-errc; err != nil {
		t.Fatalf("set_binding inside the set: %v", err)
	}

	_, err := r.SendCommandTo(Target{Instance: instA}, setBindingCmd("p-a", "p-x"), 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("relayed set_binding outside the set: err = %v", err)
	}
	if _, err := srv.SendCommandTo(Target{Instance: instA}, setBindingCmd("p-x"), time.Second); err == nil {
		t.Fatal("direct set_binding outside the set was sent")
	}
	if got := srv.Browsers(); len(got) != 1 || strings.Join(got[0].ProfileSet(), ",") != "p-a" {
		t.Fatalf("the set changed: %+v", got)
	}
	expectSilence(t, conn, 200*time.Millisecond) // nothing reached the browser
}

// A restricted token never limits the shared extension.token or an unpaired legacy socket.
func TestSharedTokenAndLegacyRecordsAreNotRestricted(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	dialBrowserSet(t, wsURL, instA, "p-x", "p-y")
	waitBrowsers(t, srv, 1)

	// A record written before the allowed set existed.
	legacy := `[{"id":"0123456789abcdef","label":"old","origin":"","instance":"` + instB + `","tokenHash":"` + hashToken("legacy-token") + `","pairedAt":"2026-01-01T00:00:00Z"}]`
	path, err := browsersPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(strings.TrimSuffix(path, "/extension.browsers.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := ListPairedBrowsers()
	if err != nil || len(list) != 1 || list[0].Restricted || len(list[0].AllowedProfiles) != 0 {
		t.Fatalf("legacy record = %+v, %v", list, err)
	}
	if _, refused := dialWithToken(t, wsURL, "legacy-token", instB, "anything", "else"); refused {
		t.Fatal("a legacy record must keep claiming any profile until it is restricted")
	}
	waitBrowsers(t, srv, 2)

	// Restricting rewrites the file; the other fields survive.
	if _, err := SetAllowedProfiles("0123", []string{"p-a"}, true); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var back []map[string]any
	if err := json.Unmarshal(raw, &back); err != nil || back[0]["instance"] != instB || back[0]["restricted"] != true {
		t.Fatalf("file after restrict: %s", raw)
	}
}

func TestRestrictedTokenKeepsItsInstancePin(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, _ := mintRestricted(t, instA, "p-a")
	if _, refused := dialWithToken(t, wsURL, tok, instB, "p-a"); !refused {
		t.Fatal("a token paired to one instance connected as another")
	}
	if len(srv.Browsers()) != 0 {
		t.Fatal("installed")
	}
}

// Narrowing the set of a connected browser drops it when it runs more than the new set.
func TestNarrowingTheAllowedSetDropsAConnectedBrowser(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, id := mintRestricted(t, instA, "p-a", "p-b")
	conn := dialToken(t, wsURL, tok, instA, "p-a", "p-b")
	waitBrowsers(t, srv, 1)
	if _, err := SetAllowedProfiles(id, []string{"p-a"}, true); err != nil {
		t.Fatal(err)
	}
	if !closedUnauthorized(conn, 6*time.Second) {
		t.Fatal("a browser running a profile outside the narrowed set stayed connected")
	}
}

func TestSetAllowedProfilesValidatesAndCanLiftTheRestriction(t *testing.T) {
	setHome(t, t.TempDir())
	_, e, err := addPairedBrowser(PairedBrowser{Label: "b"}, mintOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetAllowedProfiles(e.ID, []string{"../evil"}, true); err == nil {
		t.Fatal("an invalid profile id was stored")
	}
	if _, err := SetAllowedProfiles("zzzz", nil, true); err == nil {
		t.Fatal("unknown browser accepted")
	}
	got, err := SetAllowedProfiles(e.ID, []string{"p-a", "p-a", "p-b"}, true)
	if err != nil || strings.Join(got.AllowedProfiles, ",") != "p-a,p-b" || !got.Restricted {
		t.Fatalf("restrict = %+v, %v", got, err)
	}
	got, err = SetAllowedProfiles(e.ID, nil, false)
	if err != nil || got.Restricted || len(got.AllowedProfiles) != 0 {
		t.Fatalf("lift = %+v, %v", got, err)
	}
}

// The owner can limit a browser when approving its claim: the paired record
// minted at the browser's next poll carries the set.
func TestApprovalRecordsTheAllowedSetOnThePairedRecord(t *testing.T) {
	setHome(t, t.TempDir())
	c, secret, err := CreatePendingClaim(PendingClaim{Label: "x", Origin: "chrome-extension://x", Instance: "x-instance-1", Restricted: true, AllowedProfiles: []string{"p-forged"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Restricted || len(c.AllowedProfiles) != 0 {
		t.Fatalf("a new claim arrived limited by its creator: %+v", c)
	}
	if _, err := DecidePendingClaimWith(c.ID, true, PairCode(c.ID, secret), []string{"../evil"}, false); err == nil {
		t.Fatal("an invalid profile id was recorded")
	}
	if _, err := DecidePendingClaimWith(c.ID, true, PairCode(c.ID, secret), []string{"p-a", "p-b"}, false); err != nil {
		t.Fatal(err)
	}
	out, err := pollClaim(c.ID, secret, "")
	if err != nil || out.State != ClaimApproved {
		t.Fatalf("poll = %+v, %v", out, err)
	}
	if !out.Entry.Restricted || strings.Join(out.Entry.AllowedProfiles, ",") != "p-a,p-b" {
		t.Fatalf("paired record = %+v", out.Entry)
	}
}

// Re-pairing with the browser's own token (rotation) must not lift the limit:
// only the owner's approval or `browsers allow` changes it.
func TestTokenRotationKeepsTheAllowedSet(t *testing.T) {
	setHome(t, t.TempDir())
	tok, _ := mintRestricted(t, instA, "p-a")
	_, entry, err := addPairedBrowser(PairedBrowser{Label: "b", Instance: instA}, mintOpts{RotateToken: tok})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Restricted || strings.Join(entry.AllowedProfiles, ",") != "p-a" {
		t.Fatalf("rotated record lost its limit: %+v", entry)
	}
	list, _ := ListPairedBrowsers()
	if len(list) != 1 || !list[0].Restricted {
		t.Fatalf("stored = %+v", list)
	}
}

// A restricted browser is never the fallback for a profile outside its set,
// bound or not; an empty set serves no profile and no profile-less command.
func TestRestrictedBrowserIsNotTheDefaultForOtherProfiles(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, _ := mintRestricted(t, instA, "p-allowed")
	a := dialToken(t, wsURL, tok, instA) // connected unbound
	waitBrowsers(t, srv, 1)

	// Its own profile still reaches it as the default; others do not.
	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Profile: "p-allowed"}, &Command{Type: CmdCreateTab}, 3*time.Second)
		errc <- err
	}()
	if cmd := answerNext(t, a, 0, map[string]any{"tabId": 1}); cmd == nil || cmd.Type != CmdCreateTab {
		t.Fatalf("an allowed profile did not reach its browser: %+v", cmd)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	for _, tgt := range []Target{{Profile: "p-victim"}, {}} {
		if _, err := srv.SendCommandTo(tgt, &Command{Type: CmdCreateTab}, time.Second); err == nil {
			t.Fatalf("target %+v reached a browser limited to other profiles", tgt)
		}
	}
	expectSilence(t, a, 200*time.Millisecond)
}

func TestEmptyAllowedSetServesNothingAndAnUnrestrictedDefaultStillDoes(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	plain := dialBrowserSet(t, wsURL, instA) // shared token, unbound: the default browser
	waitBrowsers(t, srv, 1)
	time.Sleep(20 * time.Millisecond)
	tok, _ := mintRestricted(t, instB) // newer, restricted to nothing, unbound
	dialToken(t, wsURL, tok, instB)
	waitBrowsers(t, srv, 2)

	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Profile: "p-victim"}, &Command{Type: CmdCreateTab}, 3*time.Second)
		errc <- err
	}()
	if cmd := answerNext(t, plain, 0, map[string]any{"tabId": 1}); cmd == nil || cmd.Type != CmdCreateTab {
		t.Fatalf("the unrestricted default browser was skipped: %+v", cmd)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

// The owner narrowing the set while a set_binding waits for the browser's
// binding lock must stop that command: the policy is read where it is sent.
func TestNarrowingWhileASetBindingWaitsRefusesIt(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, id := mintRestricted(t, instA, "p-a", "p-b")
	conn := dialToken(t, wsURL, tok, instA, "p-a")
	waitBrowsers(t, srv, 1)
	c := srv.connList()[0]

	c.bindMu.Lock()
	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA}, setBindingCmd("p-a", "p-b"), 3*time.Second)
		errc <- err
	}()
	time.Sleep(150 * time.Millisecond) // it is now waiting for bindMu
	if _, err := SetAllowedProfiles(id, []string{"p-a"}, true); err != nil {
		t.Fatal(err)
	}
	c.bindMu.Unlock()

	if err := <-errc; err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("a set_binding that became forbidden while waiting was sent: %v", err)
	}
	expectSilence(t, conn, 200*time.Millisecond)
}

// New pairings start limited to nothing: the owner names what a browser may
// run (--profiles), or says --any. Automatic pairing is no exception.
func TestNewPairingsStartRestricted(t *testing.T) {
	setHome(t, t.TempDir())
	c, secret, _ := CreatePendingClaim(PendingClaim{Label: "x", Origin: "chrome-extension://x", Instance: "x-instance-1"})
	if _, err := DecidePendingClaim(c.ID, true, PairCode(c.ID, secret)); err != nil {
		t.Fatal(err)
	}
	out, err := pollClaim(c.ID, secret, "")
	if err != nil || !out.Entry.Restricted || len(out.Entry.AllowedProfiles) != 0 {
		t.Fatalf("plain approval = %+v, %v", out.Entry, err)
	}
	c2, s2, _ := CreatePendingClaim(PendingClaim{Label: "y", Origin: "chrome-extension://y", Instance: "y-instance-1"})
	if _, err := DecidePendingClaimWith(c2.ID, true, PairCode(c2.ID, s2), nil, true); err != nil {
		t.Fatal(err)
	}
	if out, err := pollClaim(c2.ID, s2, ""); err != nil || out.Entry.Restricted {
		t.Fatalf("--any approval = %+v, %v", out.Entry, err)
	}
	// An approval recorded before this default (no marker) still mints restricted.
	c3, s3, _ := CreatePendingClaim(PendingClaim{Label: "z", Origin: "chrome-extension://z", Instance: "z-instance-1"})
	if _, err := DecidePendingClaimWith(c3.ID, true, PairCode(c3.ID, s3), []string{"p-a"}, false); err != nil {
		t.Fatal(err)
	}
	if out, err := pollClaim(c3.ID, s3, ""); err != nil || !out.Entry.Restricted || strings.Join(out.Entry.AllowedProfiles, ",") != "p-a" {
		t.Fatalf("--profiles approval = %+v, %v", out.Entry, err)
	}
}

func TestAutomaticPairingStartsRestricted(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newAutoServer(t)
	w := claim(srv, claimReq(nil))
	if w.Code != 200 {
		t.Fatalf("claim = %d %s", w.Code, w.Body)
	}
	list, err := ListPairedBrowsers()
	if err != nil || len(list) != 1 || !list[0].Restricted || len(list[0].AllowedProfiles) != 0 {
		t.Fatalf("auto-paired record = %+v, %v", list, err)
	}
}

// Naming a browser by instance does not get around its allowed set: an ordinary
// command still needs a profile the browser may serve. set_binding, which
// manages the set itself, still works by instance alone.
func TestInstanceTargetedCommandsStillFollowTheAllowedSet(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, _ := mintRestricted(t, instA) // restricted to nothing
	a := dialToken(t, wsURL, tok, instA)
	waitBrowsers(t, srv, 1)

	for _, tgt := range []Target{{Instance: instA}, {Instance: instA, Profile: "p-x"}} {
		_, err := srv.SendCommandTo(tgt, &Command{Type: CmdCreateTab}, time.Second)
		if !errors.Is(err, ErrProfileNotAllowed) {
			t.Fatalf("target %+v: err = %v, want ErrProfileNotAllowed", tgt, err)
		}
	}
	if _, err := srv.ResolveTarget(Target{Instance: instA}); !errors.Is(err, ErrProfileNotAllowed) {
		t.Fatalf("resolving by instance alone: %v", err)
	}

	// Unbinding to nothing is the owner narrowing a browser: allowed by instance alone.
	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA}, &Command{Type: CmdSetBinding, Params: map[string]interface{}{"profiles": []string{}}}, 3*time.Second)
		errc <- err
	}()
	if cmd := answerNext(t, a, 0, nil); cmd == nil || cmd.Type != CmdSetBinding {
		t.Fatalf("set_binding did not reach the browser: %+v", cmd)
	}
	if err := <-errc; err != nil {
		t.Fatalf("set_binding by instance: %v", err)
	}
}

func TestInstanceTargetWithAnAllowedProfileIsDelivered(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, _ := mintRestricted(t, instA, "p-a")
	a := dialToken(t, wsURL, tok, instA, "p-a")
	waitBrowsers(t, srv, 1)
	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA, Profile: "p-a"}, &Command{Type: CmdCreateTab}, 3*time.Second)
		errc <- err
	}()
	if cmd := answerNext(t, a, 0, map[string]any{"tabId": 1}); cmd == nil || cmd.Type != CmdCreateTab {
		t.Fatalf("got %+v", cmd)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

// The owner narrowing the set after a browser was chosen but before the command
// is written must stop the command: the decision is made again where it is delivered.
func TestNarrowingBetweenChoosingAndWritingRefusesTheCommand(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, id := mintRestricted(t, instA, "p-a")
	conn := dialToken(t, wsURL, tok, instA, "p-a")
	waitBrowsers(t, srv, 1)

	beforeDeliver = func() {
		if _, err := SetAllowedProfiles(id, nil, true); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeDeliver = nil })
	_, err := srv.SendCommandTo(Target{Profile: "p-a"}, &Command{Type: CmdCreateTab}, time.Second)
	if !errors.Is(err, ErrProfileNotAllowed) {
		t.Fatalf("a command resolved before the narrowing was delivered: %v", err)
	}
	expectSilence(t, conn, 200*time.Millisecond)
}

// A set_binding the browser acknowledges after the owner narrowed its set must not be
// mirrored as the browser's binding: the browser holds a profile it may not, so it is dropped.
func TestSetBindingAcknowledgedAfterNarrowingIsNotMirrored(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, id := mintRestricted(t, instA, "p-a", "p-b")
	conn := dialToken(t, wsURL, tok, instA, "p-a")
	waitBrowsers(t, srv, 1)
	c := srv.connList()[0]

	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA}, setBindingCmd("p-a", "p-b"), 3*time.Second)
		errc <- err
	}()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage() // the command is out; the browser has not answered
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetAllowedProfiles(id, []string{"p-a"}, true); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(Response{ID: cmdID(string(msg)), Success: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; !errors.Is(err, ErrProfileNotAllowed) {
		t.Fatalf("the stale acknowledgement was applied: %v", err)
	}
	if got := c.boundProfiles(); strings.Join(got, ",") != "p-a" {
		t.Fatalf("the narrowed browser is mirrored as running %v", got)
	}
	if !closedUnauthorized(conn, time.Second) {
		t.Fatal("the browser that holds a forbidden binding stayed connected")
	}
}

// The decision is checked again after the write: a command whose policy was narrowed while
// it was being written is reported as refused and its browser is dropped.
func TestNarrowingDuringTheWriteDropsTheBrowser(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, id := mintRestricted(t, instA, "p-a")
	conn := dialToken(t, wsURL, tok, instA, "p-a")
	waitBrowsers(t, srv, 1)
	afterWrite = func() {
		if _, err := SetAllowedProfiles(id, nil, true); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterWrite = nil })
	_, err := srv.SendCommandTo(Target{Profile: "p-a"}, &Command{Type: CmdCreateTab}, time.Second)
	if !errors.Is(err, ErrProfileNotAllowed) {
		t.Fatalf("a policy narrowed during the write went unnoticed: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, _ = conn.ReadMessage() // the command that was already written
	if !closedUnauthorized(conn, time.Second) {
		t.Fatal("the browser stayed connected")
	}
}

// Commands queued behind another send on the browser's write lock are authorised when they are
// written, not when they were chosen: the owner narrowing the set while they wait refuses every one of
// them, and none reaches the browser.
func TestQueuedCommandsAreAuthorisedWhenTheyAreWritten(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, id := mintRestricted(t, instA, "p-a")
	conn := dialToken(t, wsURL, tok, instA, "p-a")
	waitBrowsers(t, srv, 1)
	c := srv.connList()[0]

	c.writeMu.Lock() // a send ahead of them holds the browser's writer
	const queued = 4
	errc := make(chan error, queued)
	for i := 0; i < queued; i++ {
		go func() {
			_, err := srv.SendCommandTo(Target{Profile: "p-a"}, &Command{Type: CmdCreateTab}, 5*time.Second)
			errc <- err
		}()
	}
	time.Sleep(300 * time.Millisecond) // they have chosen the browser and wait for its writer
	if _, err := SetAllowedProfiles(id, nil, true); err != nil {
		t.Fatal(err)
	}
	c.writeMu.Unlock()

	for i := 0; i < queued; i++ {
		if err := <-errc; !errors.Is(err, ErrProfileNotAllowed) {
			t.Fatalf("queued command %d: err = %v, want ErrProfileNotAllowed", i, err)
		}
	}
	expectSilence(t, conn, 300*time.Millisecond) // none was delivered
}

// On a real socket: the wait for the browser's writer is bounded too: when another writer on the connection (a reply,
// an acknowledgement, a ping) is stuck on a socket nobody reads, a command waiting behind it is
// refused within about two seconds and the socket is dropped, which also frees the stuck writer.
func TestACommandWaitingBehindAStalledWriterIsBounded(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tok, _ := mintRestricted(t, instA, "p-a")
	client := dialToken(t, wsURL, tok, instA, "p-a") // never read from again
	waitBrowsers(t, srv, 1)
	c := srv.connList()[0]
	stallable(t, c, client)

	stuck := make(chan error, 1)
	go func() { stuck <- c.write([]byte(strings.Repeat("x", 64<<20))) }() // a reply the browser does not read
	time.Sleep(400 * time.Millisecond)                                    // it holds the writer now
	select {
	case <-stuck:
		t.Skip("this machine's socket buffers absorbed the write, so nothing stalled; the fake-connection tests cover the bound")
	default:
	}

	start := time.Now()
	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Profile: "p-a"}, &Command{Type: CmdCreateTab}, 30*time.Second)
		errc <- err
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("a command behind a stalled writer reported success")
		}
		if d := time.Since(start); d > deliveryWriteTimeout+2*time.Second {
			t.Fatalf("the command waited %s behind the stalled writer", d)
		}
	case <-time.After(deliveryWriteTimeout + 4*time.Second):
		t.Fatal("a command waiting for the browser's writer was not bounded")
	}
	select {
	case <-stuck:
	case <-time.After(2 * time.Second):
		t.Fatal("the stalled writer was not released when the socket was dropped")
	}
	deadline := time.Now().Add(time.Second)
	for len(srv.Browsers()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stalled browser stayed connected")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
