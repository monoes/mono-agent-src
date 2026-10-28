package extension

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/account/accounttest"
)

const extOrigin = "chrome-extension://abcdefghijklmnopabcdefghijklmnop"

// claimReq is a claim that passes every check; each test spoils one thing.
func claimReq(mut func(*http.Request)) *http.Request {
	return claimReqBody(`{"instance":"0123456789abcdef","label":"Edge Work"}`, mut)
}

func claimReqBody(body string, mut func(*http.Request)) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9222/monoagent/pair/claim", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Origin", extOrigin)
	r.Header.Set("User-Agent", "Mozilla/5.0 Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0")
	if mut != nil {
		mut(r)
	}
	return r
}

func newClaimServer(t *testing.T) *Server {
	t.Helper()
	setHome(t, t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv(AutoPairEnv, "")
	t.Setenv(ExtensionIDsEnv, "")
	return NewServer("127.0.0.1:9222", zerolog.Nop())
}

// newAutoServer is a server in auto mode, the zero-step opt-in.
func newAutoServer(t *testing.T) *Server {
	t.Helper()
	srv := newClaimServer(t)
	srv.SetPairMode(func() string { return PairModeAuto })
	return srv
}

func claim(srv *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.handlePairClaim(w, r)
	return w
}

// A claim is refused, with no token in the reply and nothing recorded, for
// every way it can come from somewhere that is not the extension.
func TestPairClaimRefusalMatrix(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*http.Request)
		code int
	}{
		{"web page origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403},
		{"loopback web page origin", func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:3000") }, 403},
		{"no origin", func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"opaque origin", func(r *http.Request) { r.Header.Set("Origin", "null") }, 403},
		{"extension origin without id", func(r *http.Request) { r.Header.Set("Origin", "chrome-extension://") }, 403},
		{"rebound host", func(r *http.Request) { r.Host = "evil.example:9222" }, 403},
		{"non-loopback peer", func(r *http.Request) { r.RemoteAddr = "192.0.2.7:4000" }, 403},
		{"same-origin fetch", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }, 403},
		{"navigation", func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }, 403},
		{"GET", func(r *http.Request) { r.Method = http.MethodGet }, 405},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			accounttest.Install(t, accounttest.SignedIn)
			srv := newClaimServer(t)
			w := claim(srv, claimReq(c.mut))
			if w.Code != c.code || strings.Contains(w.Body.String(), `"token"`) {
				t.Fatalf("claim = %d %s, want %d and no token", w.Code, w.Body, c.code)
			}
			if list, _ := ListPairedBrowsers(); len(list) != 0 {
				t.Fatalf("a refused claim recorded %v", list)
			}
		})
	}
}

func TestPairClaimSignedOutIsRefused(t *testing.T) {
	for _, mode := range []accounttest.Mode{accounttest.LockedNoLogin, accounttest.LockedRefused} {
		accounttest.Install(t, mode)
		srv := newClaimServer(t)
		w := claim(srv, claimReq(nil))
		if w.Code != 403 || strings.Contains(w.Body.String(), `"token"`) || !strings.Contains(w.Body.String(), "signed out") {
			t.Fatalf("signed-out claim = %d %s", w.Code, w.Body)
		}
	}
}

func TestPairClaimModes(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newClaimServer(t)
	if srv.PairMode() != PairModeAsk {
		t.Fatalf("default mode = %q, want ask", srv.PairMode())
	}
	srv.SetPairMode(func() string { return "off" })
	if w := claim(srv, claimReq(nil)); w.Code != 403 || !strings.Contains(w.Body.String(), "extension pair") {
		t.Fatalf("claim with pairing off = %d %s", w.Code, w.Body)
	}
	srv.SetPairMode(func() string { return "auto" })
	if w := claim(srv, claimReq(nil)); w.Code != 200 || !strings.Contains(w.Body.String(), `"token"`) {
		t.Fatalf("claim in auto = %d %s", w.Code, w.Body)
	}
	srv.SetPairMode(func() string { return "garbage" }) // unknown is the safe mode
	if srv.PairMode() != PairModeAsk {
		t.Fatalf("unknown setting = %q, want ask", srv.PairMode())
	}
	srv.SetPairMode(nil)
	t.Setenv(AutoPairEnv, "0")
	if w := claim(srv, claimReq(nil)); w.Code != 403 {
		t.Fatalf("claim with %s=0 = %d", AutoPairEnv, w.Code)
	}
	t.Setenv(AutoPairEnv, "auto")
	if srv.PairMode() != PairModeAsk {
		t.Fatalf("env auto over an unset setting = %q, want ask: the env may not loosen", srv.PairMode())
	}
}

func TestPairClaimExtensionIDList(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newAutoServer(t)
	t.Setenv(ExtensionIDsEnv, "someotherextensionidsomeotherextens, "+strings.TrimPrefix(extOrigin, "chrome-extension://"))
	if w := claim(srv, claimReq(nil)); w.Code != 200 {
		t.Fatalf("listed id = %d %s", w.Code, w.Body)
	}
	t.Setenv(ExtensionIDsEnv, "someotherextensionidsomeotherextens")
	if w := claim(srv, claimReq(nil)); w.Code != 403 {
		t.Fatalf("unlisted id = %d %s", w.Code, w.Body)
	}
}

func instanceReq(i int, origin string) *http.Request {
	return claimReqBody(fmt.Sprintf(`{"instance":"inst%012d","label":"b%d"}`, i, i), func(r *http.Request) { r.Header.Set("Origin", origin) })
}

// The rate limit is per origin: forged claims from one origin do not use up
// another origin's quota (a global limit let a forger starve the real extension).
func TestPairClaimRateLimitIsPerOrigin(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newAutoServer(t)
	forged := "chrome-extension://forgedforgedforgedforgedforgedfo"
	for i := 0; i < claimsPerWindow; i++ {
		if w := claim(srv, instanceReq(i, forged)); w.Code != 200 {
			t.Fatalf("claim %d = %d %s", i, w.Code, w.Body)
		}
	}
	w := claim(srv, instanceReq(99, forged))
	if w.Code != 429 || strings.Contains(w.Body.String(), `"token"`) || w.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit = %d %s", w.Code, w.Body)
	}
	if w := claim(srv, instanceReq(100, extOrigin)); w.Code != 200 {
		t.Fatalf("the real origin after a forger's flood = %d %s", w.Code, w.Body)
	}
	// Refused requests (a web page) do not use up anyone's quota.
	srv2 := newAutoServer(t)
	for i := 0; i < 3*claimsPerWindow; i++ {
		claim(srv2, claimReq(func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }))
	}
	if w := claim(srv2, claimReq(nil)); w.Code != 200 {
		t.Fatalf("claim after refused ones = %d %s", w.Code, w.Body)
	}
}

func TestPairClaimGrantsAPerBrowserTokenThatRevokes(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv, base := startPairingTestServer(t)
	srv.SetPairMode(func() string { return PairModeAuto })
	w := claim(srv, claimReq(nil))
	if w.Code != 200 {
		t.Fatalf("claim = %d %s", w.Code, w.Body)
	}
	var got claimReply
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Token) != 64 || got.ID == "" {
		t.Fatalf("reply = %s (%v)", w.Body, err)
	}
	if got.Token == srv.token {
		t.Fatal("the claim handed out the shared extension.token")
	}
	list, _ := ListPairedBrowsers()
	if len(list) != 1 || list[0].Label != "Edge Work" || list[0].Origin != extOrigin || list[0].Agent != "Edg/140.0.0.0" {
		t.Fatalf("recorded = %+v", list)
	}
	if strings.Contains(mustRead(t, browsersPathT(t)), got.Token) {
		t.Fatal("the browsers file holds the token itself, not its hash")
	}

	dial := func(token string) (*websocket.Conn, bool) {
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(base, "http")+"/monoagent", http.Header{"Origin": {extOrigin}})
		if err != nil {
			t.Fatal(err)
		}
		_ = c.WriteJSON(map[string]string{"type": "auth", "token": token, "instance": "0123456789abcdef"})
		c.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
		_, _, err = c.ReadMessage()
		ce, closed := err.(*websocket.CloseError)
		return c, !(closed && ce.Code == unauthorizedCloseCode)
	}
	conn, ok := dial(got.Token)
	if !ok {
		t.Fatal("the claimed token was refused by the handshake")
	}
	defer conn.Close()
	if !srv.IsConnected() {
		t.Fatal("the claimed browser is not connected")
	}

	if _, err := RevokePairedBrowser(got.ID[:6]); err != nil {
		t.Fatal(err)
	}
	if _, ok := dial(got.Token); ok {
		t.Fatal("a revoked token still authenticates")
	}
	// No sweep is called here: the socket notices the revoke by itself.
	deadline := time.Now().Add(revokeCheckInterval + 3*time.Second)
	for srv.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if srv.IsConnected() {
		t.Fatal("the socket of a revoked browser stayed connected")
	}
	if _, err := RevokePairedBrowser(got.ID); err == nil {
		t.Fatal("revoking twice should fail")
	}
}

// A claim naming a victim's instance id must not delete the victim's token.
func TestPairClaimCannotReplaceAnInstanceItDoesNotOwn(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newAutoServer(t)
	var victim claimReply
	_ = json.Unmarshal(claim(srv, claimReq(nil)).Body.Bytes(), &victim)
	if victim.Token == "" {
		t.Fatal("no victim token")
	}
	for _, body := range []string{
		`{"instance":"0123456789abcdef","label":"attacker"}`,
		`{"instance":"0123456789abcdef","label":"attacker","token":"` + strings.Repeat("0", 64) + `"}`,
	} {
		w := claim(srv, claimReqBody(body, nil))
		if w.Code != 409 || strings.Contains(w.Body.String(), `"token"`) {
			t.Fatalf("claim over a victim's instance = %d %s", w.Code, w.Body)
		}
	}
	if _, ok := pairedBrowserForToken(victim.Token); !ok {
		t.Fatal("a forged claim killed the victim's token")
	}
	// The browser that holds its token may rotate it.
	var rotated claimReply
	w := claim(srv, claimReqBody(`{"instance":"0123456789abcdef","label":"Edge Work","token":"`+victim.Token+`"}`, nil))
	_ = json.Unmarshal(w.Body.Bytes(), &rotated)
	if w.Code != 200 || rotated.Token == "" || rotated.Token == victim.Token {
		t.Fatalf("rotation = %d %s", w.Code, w.Body)
	}
	if _, ok := pairedBrowserForToken(victim.Token); ok {
		t.Fatal("the rotated-out token still works")
	}
	if _, ok := pairedBrowserForToken(rotated.Token); !ok {
		t.Fatal("the new token does not work")
	}
}

// At the cap a new pairing is refused; no valid token is evicted.
func TestPairClaimRefusesAtTheCapWithoutEvicting(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	for _, mode := range []string{PairModeAuto, PairModeAsk} {
		srv := newClaimServer(t)
		srv.SetPairMode(func() string { return mode })
		var list []PairedBrowser
		for i := 0; i < maxPairedBrowsers; i++ {
			tok, e, err := addPairedBrowser(PairedBrowser{Label: fmt.Sprint("b", i), Instance: fmt.Sprintf("seed%012d", i)}, mintOpts{})
			if err != nil {
				t.Fatal(err)
			}
			_ = tok
			list = append(list, e)
		}
		w := claim(srv, instanceReq(500, extOrigin))
		if w.Code != 409 || strings.Contains(w.Body.String(), `"token"`) || strings.Contains(w.Body.String(), `"pollSecret"`) {
			t.Fatalf("%s: claim at the cap = %d %s", mode, w.Code, w.Body)
		}
		got, _ := ListPairedBrowsers()
		if len(got) != maxPairedBrowsers {
			t.Fatalf("%s: %d browsers after a refused claim, want %d", mode, len(got), maxPairedBrowsers)
		}
		have := map[string]bool{}
		for _, e := range got {
			have[e.ID] = true
		}
		for _, e := range list {
			if !have[e.ID] {
				t.Fatalf("%s: entry %s was evicted", mode, e.ID)
			}
		}
	}
}

func TestPairClaimCapsStoredFields(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	srv := newAutoServer(t)
	long := strings.Repeat("x", 5000)
	w := claim(srv, claimReqBody(`{"instance":"0123456789abcdef","label":"`+long+`"}`, func(r *http.Request) {
		r.Header.Set("Origin", "chrome-extension://"+strings.Repeat("a", 3000))
		r.Header.Set("User-Agent", "")
	}))
	if w.Code != 200 {
		t.Fatalf("claim = %d %s", w.Code, w.Body)
	}
	list, _ := ListPairedBrowsers()
	if len(list) != 1 || len(list[0].Label) > maxFieldLen || len(list[0].Origin) > maxFieldLen {
		t.Fatalf("stored fields are not capped: %d %d", len(list[0].Label), len(list[0].Origin))
	}
}

func TestClaimStatus(t *testing.T) {
	srv := newClaimServer(t)
	get := func(mut func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9222/monoagent/status", nil)
		r.RemoteAddr = "127.0.0.1:50000"
		if mut != nil {
			mut(r)
		}
		w := httptest.NewRecorder()
		srv.handleClaimStatus(w, r)
		return w
	}
	accounttest.Install(t, accounttest.SignedIn)
	var st ClaimStatus
	w := get(func(r *http.Request) { r.Header.Set("Origin", extOrigin) })
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || !st.SignedIn || st.Service != ServiceName || st.PairMode != PairModeAsk || st.Paired {
		t.Fatalf("signed in status = %s", w.Body)
	}
	if strings.Count(w.Body.String(), ":") != 4 || strings.Contains(w.Body.String(), "token") {
		t.Fatalf("status says more than it should: %s", w.Body)
	}
	accounttest.Install(t, accounttest.LockedNoLogin)
	_ = json.Unmarshal(get(nil).Body.Bytes(), &st)
	if st.SignedIn {
		t.Fatal("signed out status says signed in")
	}
	for name, mut := range map[string]func(*http.Request){
		"web origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"rebound":    func(r *http.Request) { r.Host = "evil.example" },
		"remote":     func(r *http.Request) { r.RemoteAddr = "192.0.2.1:1" },
	} {
		if w := get(mut); w.Code != 403 {
			t.Errorf("%s status = %d, want 403", name, w.Code)
		}
	}
}

func browsersPathT(t *testing.T) string {
	t.Helper()
	p, err := browsersPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
