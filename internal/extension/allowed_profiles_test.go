package extension

import (
	"encoding/json"
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
	if !closedUnauthorized(conn, 2*time.Second) {
		t.Fatal("a binding frame naming a profile outside the set did not close the socket with 4401")
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
	if _, err := DecidePendingClaimWith(c.ID, true, PairCode(c.ID, secret), []string{"../evil"}); err == nil {
		t.Fatal("an invalid profile id was recorded")
	}
	if _, err := DecidePendingClaimWith(c.ID, true, PairCode(c.ID, secret), []string{"p-a", "p-b"}); err != nil {
		t.Fatal(err)
	}
	out, err := pollClaim(c.ID, secret, "")
	if err != nil || out.State != ClaimApproved {
		t.Fatalf("poll = %+v, %v", out, err)
	}
	if !out.Entry.Restricted || strings.Join(out.Entry.AllowedProfiles, ",") != "p-a,p-b" {
		t.Fatalf("paired record = %+v", out.Entry)
	}
	// Approving with no set leaves the browser unlimited, as before.
	c2, s2, _ := CreatePendingClaim(PendingClaim{Label: "y", Origin: "chrome-extension://y", Instance: "y-instance-1"})
	if _, err := DecidePendingClaim(c2.ID, true, PairCode(c2.ID, s2)); err != nil {
		t.Fatal(err)
	}
	if out, err := pollClaim(c2.ID, s2, ""); err != nil || out.Entry.Restricted {
		t.Fatalf("poll = %+v, %v", out, err)
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
