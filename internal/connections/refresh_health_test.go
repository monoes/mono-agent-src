package connections

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// healthTokenServer is a fake OAuth token endpoint. Each exchange answers
// with the next numbered token pair unless respond is set.
type healthTokenServer struct {
	*httptest.Server
	mu      sync.Mutex
	forms   []url.Values
	paths   []string
	respond func(w http.ResponseWriter, r *http.Request, n int) bool
}

func newHealthTokenServer(t *testing.T) *healthTokenServer {
	t.Helper()
	ts := &healthTokenServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ts.mu.Lock()
		ts.forms = append(ts.forms, r.PostForm)
		ts.paths = append(ts.paths, r.URL.Path)
		n := len(ts.forms)
		respond := ts.respond
		ts.mu.Unlock()
		if respond != nil && respond(w, r, n) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"access-%d","refresh_token":"refresh-%d","token_type":"Bearer","expires_in":3600}`, n, n)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *healthTokenServer) calls() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.forms)
}

func (ts *healthTokenServer) form(i int) url.Values {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.forms[i]
}

// registerHealthPlatform adds a throwaway OAuth platform pointing at tokenURL;
// a built-in client secret comes with a built-in client id.
func registerHealthPlatform(t *testing.T, id, tokenURL, clientID string) {
	t.Helper()
	secret := ""
	if clientID != "" {
		secret = "builtin-secret"
	}
	Registry[id] = PlatformDef{
		ID:      id,
		Name:    "Health " + id,
		Methods: []AuthMethod{MethodOAuth},
		OAuth:   &OAuthConfig{TokenURL: tokenURL, ClientID: clientID, ClientSecret: secret},
	}
	t.Cleanup(func() { delete(Registry, id) })
}

func saveHealthOAuthConn(t *testing.T, s *Store, platform string, data map[string]interface{}) *Connection {
	t.Helper()
	c := &Connection{Platform: platform, Method: MethodOAuth, Label: "health", Data: data}
	if err := s.Save(context.Background(), c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return c
}

func lockRows(t *testing.T, db *sql.DB, connID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM oauth_refresh_locks WHERE connection_id = ?`, connID).Scan(&n); err != nil {
		t.Fatalf("count locks: %v", err)
	}
	return n
}

func TestHealthRefreshTokenRotatesAndPersists(t *testing.T) {
	ts := newHealthTokenServer(t)
	registerHealthPlatform(t, "health-rotate", ts.URL+"/token", "cid")
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	c := saveHealthOAuthConn(t, s, "health-rotate", map[string]interface{}{
		"access_token": "old-access", "refresh_token": "old-refresh",
	})
	c.Status = "expired"
	if err := s.RefreshToken(ctx, c); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}

	f := ts.form(0)
	if f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "old-refresh" ||
		f.Get("client_id") != "cid" || f.Get("client_secret") != "builtin-secret" {
		t.Errorf("token request form = %v", f)
	}

	got, err := s.Get(ctx, c.ID)
	if err != nil || got == nil {
		t.Fatalf("Get: %v %v", got, err)
	}
	if got.Data["access_token"] != "access-1" || got.Data["refresh_token"] != "refresh-1" || got.Data["token_type"] != "Bearer" {
		t.Errorf("tokens after refresh = %v", got.Data)
	}
	if got.Status != "active" {
		t.Errorf("status = %q, want active", got.Status)
	}
	exp, err := time.Parse(time.RFC3339, fmt.Sprint(got.Data["expires_at"]))
	if err != nil {
		t.Fatalf("expires_at %v: %v", got.Data["expires_at"], err)
	}
	if d := time.Until(exp); d < 55*time.Minute || d > 61*time.Minute {
		t.Errorf("expires_at %v is not ~1h out", exp)
	}
	if got.VaultRef == "" || got.VaultRef != c.VaultRef {
		t.Errorf("refresh should keep the same vault entry: %q vs %q", got.VaultRef, c.VaultRef)
	}
	// The lock is released once the exchange is done.
	if n := lockRows(t, db, c.ID); n != 0 {
		t.Errorf("refresh lock rows left = %d", n)
	}

	// A provider that does not rotate keeps the stored refresh token.
	ts.respond = func(w http.ResponseWriter, r *http.Request, n int) bool {
		fmt.Fprint(w, `{"access_token":"access-norotate"}`)
		return true
	}
	if err := s.RefreshToken(ctx, got); err != nil {
		t.Fatalf("second RefreshToken: %v", err)
	}
	if ts.form(1).Get("refresh_token") != "refresh-1" {
		t.Errorf("second exchange used %q, want the rotated refresh-1", ts.form(1).Get("refresh_token"))
	}
	again, _ := s.Get(ctx, c.ID)
	if again.Data["access_token"] != "access-norotate" || again.Data["refresh_token"] != "refresh-1" {
		t.Errorf("after non-rotating refresh = %v", again.Data)
	}
}

func TestHealthRefreshTokenErrors(t *testing.T) {
	ctx := context.Background()
	ts := newHealthTokenServer(t)
	registerHealthPlatform(t, "health-errs", ts.URL+"/token", "cid")
	registerHealthPlatform(t, "health-noclient", ts.URL+"/token", "")
	Registry["health-nooauth"] = PlatformDef{ID: "health-nooauth", Name: "x", Methods: []AuthMethod{MethodAPIKey}}
	t.Cleanup(func() { delete(Registry, "health-nooauth") })

	db := newTestDB(t)
	s := NewStore(db)

	noRefresh := saveHealthOAuthConn(t, s, "health-errs", map[string]interface{}{"access_token": "a"})
	if err := s.RefreshToken(ctx, noRefresh); err == nil || !strings.Contains(err.Error(), "no refresh_token") {
		t.Errorf("missing refresh_token err = %v", err)
	}

	noOAuth := &Connection{Platform: "health-nooauth", Method: MethodOAuth, Data: map[string]interface{}{"refresh_token": "r"}}
	if err := s.RefreshToken(ctx, noOAuth); err == nil || !strings.Contains(err.Error(), "no OAuth config") {
		t.Errorf("platform without OAuth err = %v", err)
	}

	t.Setenv("MONOAGENT_HEALTH_NOCLIENT_CLIENT_ID", "")
	noClient := &Connection{Platform: "health-noclient", Method: MethodOAuth, Data: map[string]interface{}{"refresh_token": "r"}}
	if err := s.RefreshToken(ctx, noClient); err == nil || !strings.Contains(err.Error(), "MONOAGENT_HEALTH_NOCLIENT_CLIENT_ID") {
		t.Errorf("missing client id err = %v", err)
	}

	withRefresh := func() *Connection {
		return &Connection{Platform: "health-errs", Method: MethodOAuth, Data: map[string]interface{}{"refresh_token": "r", "access_token": "keep"}}
	}

	ts.respond = func(w http.ResponseWriter, r *http.Request, n int) bool {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
		return true
	}
	c := withRefresh()
	if err := s.RefreshToken(ctx, c); err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("400 err = %v", err)
	}
	if c.Data["access_token"] != "keep" {
		t.Errorf("a failed refresh changed the token: %v", c.Data)
	}

	ts.respond = func(w http.ResponseWriter, r *http.Request, n int) bool {
		fmt.Fprint(w, `not json`)
		return true
	}
	if err := s.RefreshToken(ctx, withRefresh()); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("bad json err = %v", err)
	}

	ts.respond = func(w http.ResponseWriter, r *http.Request, n int) bool {
		fmt.Fprint(w, `{"token_type":"Bearer"}`)
		return true
	}
	if err := s.RefreshToken(ctx, withRefresh()); err == nil || !strings.Contains(err.Error(), "missing access_token") {
		t.Errorf("no access_token err = %v", err)
	}

	// An unreachable token endpoint is a transport error, not a hang.
	registerHealthPlatform(t, "health-down", "http://127.0.0.1:1/token", "cid")
	if err := s.RefreshToken(ctx, &Connection{Platform: "health-down", Method: MethodOAuth, Data: map[string]interface{}{"refresh_token": "r"}}); err == nil || !strings.Contains(err.Error(), "token request") {
		t.Errorf("unreachable endpoint err = %v", err)
	}
}

// Client credentials saved for the connection's profile win over the
// platform's built-in ones; env vars fill in when neither has them.
func TestHealthRefreshTokenClientCredentialSources(t *testing.T) {
	ctx := context.Background()
	ts := newHealthTokenServer(t)
	registerHealthPlatform(t, "health-creds", ts.URL+"/token", "builtin")
	registerHealthPlatform(t, "health-env", ts.URL+"/token", "")
	db := newTestDB(t)
	s := NewStore(db)

	if err := s.SaveOAuthClient(ctx, "health-creds", "work", "work-client", "work-secret"); err != nil {
		t.Fatal(err)
	}
	if id, secret := s.GetOAuthClient(ctx, "health-creds", "work"); id != "work-client" || secret != "work-secret" {
		t.Fatalf("GetOAuthClient = %q/%q", id, secret)
	}
	if id, _ := s.GetOAuthClient(ctx, "health-creds", ""); id != "" {
		t.Errorf("default profile sees work's client: %q", id)
	}

	work := &Connection{Platform: "health-creds", Method: MethodOAuth, ProfileID: "work", Data: map[string]interface{}{"refresh_token": "r"}}
	if err := s.Save(ctx, work); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshToken(ctx, work); err != nil {
		t.Fatal(err)
	}
	if f := ts.form(0); f.Get("client_id") != "work-client" || f.Get("client_secret") != "work-secret" {
		t.Errorf("profile client creds not used: %v", f)
	}

	def := &Connection{Platform: "health-creds", Method: MethodOAuth, Data: map[string]interface{}{"refresh_token": "r"}}
	if err := s.Save(ctx, def); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshToken(ctx, def); err != nil {
		t.Fatal(err)
	}
	if f := ts.form(1); f.Get("client_id") != "builtin" {
		t.Errorf("default profile should fall back to built-in client: %v", f)
	}

	t.Setenv("MONOAGENT_HEALTH_ENV_CLIENT_ID", "env-client")
	t.Setenv("MONOAGENT_HEALTH_ENV_CLIENT_SECRET", "env-secret")
	envConn := &Connection{Platform: "health-env", Method: MethodOAuth, Data: map[string]interface{}{"refresh_token": "r"}}
	if err := s.Save(ctx, envConn); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshToken(ctx, envConn); err != nil {
		t.Fatal(err)
	}
	if f := ts.form(2); f.Get("client_id") != "env-client" || f.Get("client_secret") != "env-secret" {
		t.Errorf("env client creds not used: %v", f)
	}
}

// A live lock held by another process makes the caller wait, then take
// whatever the holder stored instead of exchanging the same token again.
func TestHealthRefreshTokenWaitsForHeldLock(t *testing.T) {
	ctx := context.Background()
	oldWait := refreshLockWaitTimeout
	refreshLockWaitTimeout = 300 * time.Millisecond
	t.Cleanup(func() { refreshLockWaitTimeout = oldWait })

	ts := newHealthTokenServer(t)
	registerHealthPlatform(t, "health-held", ts.URL+"/token", "cid")
	db := newTestDB(t)
	s := NewStore(db)
	c := saveHealthOAuthConn(t, s, "health-held", map[string]interface{}{"access_token": "a0", "refresh_token": "r0"})

	// Another process holds a fresh lock and has already stored new tokens.
	if _, err := db.Exec(`INSERT INTO oauth_refresh_locks (connection_id, locked_at) VALUES (?, ?)`,
		c.ID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	holder, _ := s.Get(ctx, c.ID)
	holder.Data["access_token"] = "from-holder"
	if err := s.Save(ctx, holder); err != nil {
		t.Fatal(err)
	}

	stale := &Connection{}
	*stale = *c
	stale.Data = map[string]interface{}{"access_token": "a0", "refresh_token": "r0"}
	start := time.Now()
	if err := s.RefreshToken(ctx, stale); err != nil {
		t.Fatalf("RefreshToken under a held lock: %v", err)
	}
	if waited := time.Since(start); waited < 250*time.Millisecond {
		t.Errorf("returned after %v without waiting for the holder", waited)
	}
	if ts.calls() != 0 {
		t.Errorf("token endpoint called %d times while another process held the lock", ts.calls())
	}
	if stale.Data["access_token"] != "from-holder" {
		t.Errorf("caller did not pick up the holder's tokens: %v", stale.Data)
	}
	if lockRows(t, db, c.ID) != 1 {
		t.Error("the loser must not release someone else's lock")
	}

	// A cancelled context stops the wait with its error.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.RefreshToken(cctx, stale); err == nil {
		t.Error("cancelled context should abort the lock wait")
	}
}

// A lock older than refreshLockStaleAfter belongs to a crashed holder and
// is taken over.
func TestHealthRefreshTokenStealsStaleLock(t *testing.T) {
	ctx := context.Background()
	ts := newHealthTokenServer(t)
	registerHealthPlatform(t, "health-stale", ts.URL+"/token", "cid")
	db := newTestDB(t)
	s := NewStore(db)
	c := saveHealthOAuthConn(t, s, "health-stale", map[string]interface{}{"access_token": "a0", "refresh_token": "r0"})

	old := time.Now().UTC().Add(-2 * refreshLockStaleAfter).Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO oauth_refresh_locks (connection_id, locked_at) VALUES (?, ?)`, c.ID, old); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshToken(ctx, c); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if ts.calls() != 1 || c.Data["access_token"] != "access-1" {
		t.Errorf("stale lock was not taken over: calls=%d data=%v", ts.calls(), c.Data)
	}
	if lockRows(t, db, c.ID) != 0 {
		t.Error("lock not released after the takeover")
	}
}

// GetOrResolve refreshes only when the token is within a minute of expiry,
// and never fails the lookup because a refresh failed.
func TestHealthGetOrResolveRefreshesOnlyNearExpiry(t *testing.T) {
	ctx := context.Background()
	ts := newHealthTokenServer(t)
	registerHealthPlatform(t, "health-expiry", ts.URL+"/token", "cid")
	db := newTestDB(t)
	s := NewStore(db)

	fresh := saveHealthOAuthConn(t, s, "health-expiry", map[string]interface{}{
		"access_token": "fresh", "refresh_token": "r",
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})
	got, err := s.GetOrResolve(ctx, fresh.ID, "")
	if err != nil || got.Data["access_token"] != "fresh" || ts.calls() != 0 {
		t.Fatalf("fresh token: %v %v calls=%d", got, err, ts.calls())
	}

	if err := s.Delete(ctx, fresh.ID, ""); err != nil {
		t.Fatal(err)
	}
	unparseable := saveHealthOAuthConn(t, s, "health-expiry", map[string]interface{}{
		"access_token": "odd", "refresh_token": "r", "expires_at": "tomorrow-ish",
	})
	if got, _ := s.GetOrResolve(ctx, unparseable.ID, ""); got.Data["access_token"] != "odd" || ts.calls() != 0 {
		t.Errorf("unparseable expiry should be used as-is: %v calls=%d", got.Data, ts.calls())
	}
	if err := s.Delete(ctx, unparseable.ID, ""); err != nil {
		t.Fatal(err)
	}

	// 30s left counts as expired; resolving by platform name refreshes too.
	soon := saveHealthOAuthConn(t, s, "health-expiry", map[string]interface{}{
		"access_token": "soon", "refresh_token": "r",
		"expires_at": time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339),
	})
	got, err = s.GetOrResolve(ctx, "health-expiry", "")
	if err != nil || got.ID != soon.ID {
		t.Fatalf("resolve by platform: %v %v", got, err)
	}
	if got.Data["access_token"] != "access-1" || ts.calls() != 1 {
		t.Errorf("near-expiry token not refreshed: %v calls=%d", got.Data, ts.calls())
	}

	// The provider now rejects the refresh: the caller still gets the
	// connection with its existing token.
	ts.respond = func(w http.ResponseWriter, r *http.Request, n int) bool {
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	expired, _ := s.Get(ctx, soon.ID)
	expired.Data["expires_at"] = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := s.Save(ctx, expired); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetOrResolve(ctx, soon.ID, "")
	if err != nil || got == nil || got.Data["access_token"] != "access-1" {
		t.Errorf("failed refresh should still return the connection: %v %v", got, err)
	}

	if _, err := s.GetOrResolve(ctx, "health-nothing", ""); err == nil {
		t.Error("resolving an unknown id/platform should fail")
	}
}

func TestHealthGetOrResolvePrefersActive(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	s := NewStore(db)
	first := &Connection{Platform: "github", Method: MethodAPIKey, Label: "old", Status: "expired", Data: map[string]interface{}{"token": "t1"}}
	if err := s.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // created_at has second resolution
	second := &Connection{Platform: "github", Method: MethodAPIKey, Label: "new", Data: map[string]interface{}{"token": "t2"}}
	if err := s.Save(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOrResolve(ctx, "github", "")
	if err != nil || got.ID != second.ID {
		t.Errorf("GetOrResolve picked %v (%v), want the active connection", got, err)
	}
	if err := s.MarkTested(ctx, second.ID, "error"); err != nil {
		t.Fatal(err)
	}
	// No active one left: the oldest is used.
	if got, _ := s.GetOrResolve(ctx, "github", ""); got.ID != first.ID {
		t.Errorf("without an active connection got %s, want the first", got.ID)
	}
}

func TestHealthAudienceFallbackRetriesConsumers(t *testing.T) {
	ts := newHealthTokenServer(t)
	ts.respond = func(w http.ResponseWriter, r *http.Request, n int) bool {
		if strings.Contains(r.URL.Path, "/common/") {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_request","error_codes":[9002331]}`)
			return true
		}
		return false
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"r"}}
	body, status, err := PostTokenRequestWithAudienceFallback("outlook", ts.URL+"/common/oauth2/v2.0/token", form)
	if err != nil || status != 200 {
		t.Fatalf("fallback = %d %v %s", status, err, body)
	}
	if len(ts.paths) != 2 || ts.paths[1] != "/consumers/oauth2/v2.0/token" {
		t.Errorf("paths = %v, want a retry against /consumers/", ts.paths)
	}

	ts2 := newHealthTokenServer(t)
	ts2.respond = func(w http.ResponseWriter, r *http.Request, n int) bool {
		if strings.Contains(r.URL.Path, "/common/") {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error_codes":[9002332]}`)
			return true
		}
		return false
	}
	if _, status, _ := PostTokenRequestWithAudienceFallback("outlook", ts2.URL+"/common/token", form); status != 200 || ts2.paths[1] != "/organizations/token" {
		t.Errorf("org fallback status=%d paths=%v", status, ts2.paths)
	}

	// Other errors, and URLs without /common/, are returned untouched.
	ts3 := newHealthTokenServer(t)
	ts3.respond = func(w http.ResponseWriter, r *http.Request, n int) bool {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error_codes":[70000]}`)
		return true
	}
	if _, status, _ := PostTokenRequestWithAudienceFallback("outlook", ts3.URL+"/common/token", form); status != 400 || ts3.calls() != 1 {
		t.Errorf("unrelated error: status=%d calls=%d", status, ts3.calls())
	}
	if _, status, _ := PostTokenRequestWithAudienceFallback("outlook", ts3.URL+"/tenant/token", form); status != 400 || ts3.calls() != 2 {
		t.Errorf("non-common URL: status=%d calls=%d", status, ts3.calls())
	}
}

func TestHealthManagerRefresh(t *testing.T) {
	ctx := context.Background()
	ts := newHealthTokenServer(t)
	registerHealthPlatform(t, "health-mgr", ts.URL+"/token", "cid")
	mgr, _ := newManagerDB(t)

	if err := mgr.Refresh(ctx, "missing", time.Second); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing connection err = %v", err)
	}
	apiKey := &Connection{Platform: "github", Method: MethodAPIKey, Data: map[string]interface{}{"token": "t"}}
	if err := mgr.store.Save(ctx, apiKey); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Refresh(ctx, apiKey.ID, time.Second); err == nil || !strings.Contains(err.Error(), "only OAuth") {
		t.Errorf("non-OAuth refresh err = %v", err)
	}
	unknown := &Connection{Platform: "health-gone", Method: MethodOAuth, Data: map[string]interface{}{"refresh_token": "r"}}
	if err := mgr.store.Save(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Refresh(ctx, unknown.ID, time.Second); err == nil || !strings.Contains(err.Error(), "unknown platform") {
		t.Errorf("unknown platform err = %v", err)
	}

	c := &Connection{Platform: "health-mgr", Method: MethodOAuth, Label: "before", Status: "expired", Data: map[string]interface{}{"refresh_token": "r0"}}
	if err := mgr.store.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Refresh(ctx, c.ID, time.Second); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, err := mgr.Get(ctx, c.ID)
	if err != nil || got == nil {
		t.Fatalf("Get: %v %v", got, err)
	}
	if got.Data["access_token"] != "access-1" || got.Status != "active" || got.LastTested == "" {
		t.Errorf("after Manager.Refresh: status=%q tested=%q data=%v", got.Status, got.LastTested, got.Data)
	}

	ts.respond = func(w http.ResponseWriter, r *http.Request, n int) bool {
		w.WriteHeader(http.StatusBadRequest)
		return true
	}
	if err := mgr.store.RefreshToken(ctx, got); err == nil {
		t.Error("RefreshToken should surface the provider's rejection")
	}
}

// Vault round trip: secrets leave the connections row, come back on read,
// and go away with the connection.
func TestHealthVaultRefLifecycle(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	s := NewStore(db)

	c := &Connection{Platform: "github", Method: MethodOAuth, Label: "gh", AccountID: "octo",
		Data: map[string]interface{}{"access_token": "gho_secret", "refresh_token": "ghr_secret", "scope": "repo"}}
	if err := s.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if c.VaultRef == "" {
		t.Fatal("OAuth tokens should be stored in the vault")
	}
	var raw string
	if err := db.QueryRow(`SELECT data FROM connections WHERE id = ?`, c.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "gho_secret") || strings.Contains(raw, "ghr_secret") {
		t.Errorf("token in the connections row: %s", raw)
	}
	all, err := s.ListAll(ctx, "default")
	if err != nil || len(all) != 1 {
		t.Fatalf("ListAll: %v %v", all, err)
	}
	if all[0].Data["access_token"] != "gho_secret" || all[0].Data["scope"] != "repo" {
		t.Errorf("ListAll did not merge vault fields: %v", all[0].Data)
	}
	if other, _ := s.ListAll(ctx, "work"); len(other) != 0 {
		t.Errorf("work profile lists default's connections: %d", len(other))
	}

	safe, _ := json.Marshal(RedactAll(all))
	if strings.Contains(string(safe), "gho_secret") {
		t.Errorf("RedactAll leaked a token: %s", safe)
	}

	var vaultRows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM vault_secrets WHERE id = ?`, c.VaultRef).Scan(&vaultRows)
	if vaultRows != 1 {
		t.Fatalf("vault rows for %s = %d", c.VaultRef, vaultRows)
	}
	if err := s.Delete(ctx, c.ID, "work"); err == nil {
		t.Error("deleting from another profile should fail")
	}
	if err := s.Delete(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM vault_secrets WHERE id = ?`, c.VaultRef).Scan(&vaultRows)
	if vaultRows != 0 {
		t.Errorf("vault entry survived the connection's delete")
	}
	if err := s.Delete(ctx, c.ID, ""); err == nil {
		t.Error("deleting twice should fail")
	}
}

func TestHealthMigrateProfileBlobs(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	s := NewStore(db)

	// Already on the profile key: nothing to do.
	c := &Connection{Platform: "github", Method: MethodAPIKey, Data: map[string]interface{}{"note": "x"}}
	if err := s.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if n, errs := MigrateProfileBlobs(ctx, db, "default"); n != 0 || len(errs) != 0 {
		t.Errorf("already-migrated: n=%d errs=%v", n, errs)
	}

	// A legacy plaintext row and plaintext client secret get encrypted.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO connections (id, platform, method, label, data, profile_id, created_at, updated_at)
		VALUES ('legacy', 'github', 'apikey', 'legacy', '{"note":"plain"}', 'default', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOAuthClient(ctx, "health-mig", "default", "cid", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE platform_oauth_credentials SET client_secret = 'plain-secret' WHERE platform = 'health-mig'`); err != nil {
		t.Fatal(err)
	}

	n, errs := MigrateProfileBlobs(ctx, db, "default")
	if n != 2 || len(errs) != 0 {
		t.Fatalf("MigrateProfileBlobs = %d, %v; want 2, none", n, errs)
	}
	var raw, secret string
	_ = db.QueryRow(`SELECT data FROM connections WHERE id = 'legacy'`).Scan(&raw)
	_ = db.QueryRow(`SELECT client_secret FROM platform_oauth_credentials WHERE platform = 'health-mig'`).Scan(&secret)
	if strings.Contains(raw, "plain") || strings.Contains(secret, "plain-secret") {
		t.Errorf("still plaintext after migration: %q / %q", raw, secret)
	}
	got, err := s.Get(ctx, "legacy")
	if err != nil || got.Data["note"] != "plain" {
		t.Errorf("legacy row after migration: %v %v", got, err)
	}
	if _, sec := s.GetOAuthClient(ctx, "health-mig", "default"); sec != "plain-secret" {
		t.Errorf("client secret after migration = %q", sec)
	}
	// Idempotent.
	if n, errs := MigrateProfileBlobs(ctx, db, "default"); n != 0 || len(errs) != 0 {
		t.Errorf("second run: n=%d errs=%v", n, errs)
	}
}

func TestHealthRegistryLookups(t *testing.T) {
	all := All()
	if len(all) != len(Registry) {
		t.Errorf("All() = %d, Registry = %d", len(all), len(Registry))
	}
	seen := map[string]bool{}
	for _, p := range all {
		if seen[p.ID] {
			t.Errorf("duplicate platform %s", p.ID)
		}
		seen[p.ID] = true
	}
}

// --- validators -------------------------------------------------------------

// healthFakeAPI answers every outbound request from a table keyed by host+path,
// so validators run end to end without touching the network.
type healthFakeAPI struct {
	routes map[string]healthRoute
	seen   []*http.Request
}

type healthRoute struct {
	status int
	body   string
	header http.Header
}

func (f *healthFakeAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	f.seen = append(f.seen, r)
	rt, ok := f.routes[r.URL.Host+r.URL.Path]
	if !ok {
		rt = healthRoute{status: 404, body: `{}`}
	}
	h := rt.header
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{StatusCode: rt.status, Body: io.NopCloser(strings.NewReader(rt.body)), Header: h, Request: r}, nil
}

func withHealthFakeAPI(t *testing.T, routes map[string]healthRoute) *healthFakeAPI {
	t.Helper()
	f := &healthFakeAPI{routes: routes}
	orig := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = orig })
	return f
}

func healthOK(body string) healthRoute { return healthRoute{status: 200, body: body} }

func TestHealthValidatorsResolveAccount(t *testing.T) {
	f := withHealthFakeAPI(t, map[string]healthRoute{
		"api.github.com/user":                                        healthOK(`{"login":"octocat"}`),
		"api.notion.com/v1/users/me":                                 healthOK(`{"name":"Bot","bot":{"workspace_name":"Acme WS"}}`),
		"api.airtable.com/v0/meta/whoami":                            healthOK(`{"id":"usr1","email":"a@air.example"}`),
		"acme.atlassian.net/rest/api/3/myself":                       healthOK(`{"displayName":"Jira Jane"}`),
		"api.linear.app/graphql":                                     healthOK(`{"data":{"viewer":{"name":"Lin","email":"lin@example.com"}}}`),
		"api.stripe.com/v1/account":                                  healthOK(`{"business_profile":{"name":"Acme Inc"},"email":"s@example.com"}`),
		"slack.com/api/auth.test":                                    healthOK(`{"ok":true,"team":"Acme","user":"sam"}`),
		"discord.com/api/v10/users/@me":                              healthOK(`{"username":"discobot"}`),
		"bsky.social/xrpc/com.atproto.server.createSession":          healthOK(`{"handle":"me.bsky.social"}`),
		"oauth.reddit.com/api/v1/me":                                 healthOK(`{"name":"redditor"}`),
		"mastodon.example/api/v1/accounts/verify_credentials":        healthOK(`{"username":"toot"}`),
		"api.twilio.com/2010-04-01/Accounts/AC123.json":              healthOK(`{"friendly_name":"My Twilio"}`),
		"api.telegram.org/bot123:abc/getMe":                          healthOK(`{"ok":true,"result":{"username":"healthbot","first_name":"H"}}`),
		"gmail.googleapis.com/gmail/v1/users/me/profile":             healthOK(`{"emailAddress":"g@gmail.example"}`),
		"www.googleapis.com/drive/v3/about":                          healthOK(`{"user":{"displayName":"Drive D","emailAddress":"d@drive.example"}}`),
		"www.googleapis.com/youtube/v3/channels":                     healthOK(`{"items":[{"snippet":{"title":"My Channel"}}]}`),
		"graph.microsoft.com/v1.0/me/mailFolders/sentitems/messages": healthOK(`{"value":[{"from":{"emailAddress":{"address":"me@outlook.example"}}}]}`),
		"api.hubapi.com/oauth/v1/access-tokens/hub-tok":              healthOK(`{"user":"hub@example.com","hub_id":42}`),
		"sf.example/services/oauth2/userinfo":                        healthOK(`{"email":"sf@example.com","name":"SF"}`),
		"dev.to/api/users/me":                                        healthOK(`{"username":"devy"}`),
		"gql.hashnode.com":                                           healthOK(`{"data":{"me":{"username":"hashy"}}}`),
		"api.producthunt.com/v2/api/graphql":                         healthOK(`{"data":{"viewer":{"user":{"name":"Hunter"}}}}`),
	})

	cases := []struct {
		platform string
		method   AuthMethod
		data     map[string]interface{}
		want     string
	}{
		{"github", MethodAPIKey, map[string]interface{}{"access_token": "gh"}, "octocat"},
		{"notion", MethodOAuth, map[string]interface{}{"access_token": "n"}, "Acme WS"},
		{"airtable", MethodAPIKey, map[string]interface{}{"api_key": "k"}, "a@air.example"},
		{"jira", MethodAPIKey, map[string]interface{}{"email": "j@example.com", "api_token": "t", "domain": "acme.atlassian.net"}, "Jira Jane"},
		{"linear", MethodAPIKey, map[string]interface{}{"api_key": "lin_api"}, "lin@example.com"},
		{"stripe", MethodAPIKey, map[string]interface{}{"secret_key": "sk_test"}, "Acme Inc"},
		{"slack", MethodOAuth, map[string]interface{}{"access_token": "xoxb"}, "Acme / sam"},
		{"discord", MethodAPIKey, map[string]interface{}{"bot_token": "d"}, "discobot"},
		{"bluesky", MethodAPIKey, map[string]interface{}{"identifier": "me", "app_password": "p"}, "me.bsky.social"},
		{"reddit", MethodOAuth, map[string]interface{}{"access_token": "r"}, "redditor"},
		{"mastodon", MethodAPIKey, map[string]interface{}{"instance_url": "https://mastodon.example/", "access_token": "m"}, "toot"},
		{"twilio", MethodAPIKey, map[string]interface{}{"account_sid": "AC123", "auth_token": "t"}, "My Twilio"},
		{"telegram", MethodAPIKey, map[string]interface{}{"bot_token": "123:abc"}, "@healthbot"},
		{"gmail", MethodOAuth, map[string]interface{}{"access_token": "g"}, "g@gmail.example"},
		{"google_drive", MethodOAuth, map[string]interface{}{"access_token": "g"}, "d@drive.example"},
		{"youtube", MethodOAuth, map[string]interface{}{"access_token": "y"}, "My Channel"},
		{"outlook", MethodOAuth, map[string]interface{}{"access_token": "o"}, "me@outlook.example"},
		{"hubspot", MethodOAuth, map[string]interface{}{"access_token": "hub-tok"}, "hub@example.com"},
		{"salesforce", MethodOAuth, map[string]interface{}{"access_token": "s", "instance_url": "https://sf.example"}, "sf@example.com"},
		{"devto", MethodAPIKey, map[string]interface{}{"api_key": "dk"}, "devy"},
		{"hashnode", MethodAPIKey, map[string]interface{}{"token": "hn"}, "hashy"},
		{"producthunt", MethodAPIKey, map[string]interface{}{"access_token": "ph"}, "Hunter"},
		{"generic_api", MethodAPIKey, map[string]interface{}{"base_url": "https://api.example"}, "https://api.example"},
		{"health-unknown", MethodAPIKey, nil, ""},
	}
	for _, c := range cases {
		got, err := ValidateConnection(context.Background(), &Connection{Platform: c.platform, Method: c.method, Data: c.data})
		if err != nil {
			t.Errorf("%s: %v", c.platform, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: account = %q, want %q", c.platform, got, c.want)
		}
	}

	// Credentials go where each API expects them.
	auth := map[string]string{}
	for _, r := range f.seen {
		key := r.URL.Host
		if u, p, ok := r.BasicAuth(); ok {
			auth[key] = "basic:" + u + ":" + p
		} else {
			auth[key] = r.Header.Get("Authorization")
		}
		if r.URL.Host == "dev.to" && r.Header.Get("api-key") != "dk" {
			t.Errorf("dev.to api-key header = %q", r.Header.Get("api-key"))
		}
		if r.URL.Host == "oauth.reddit.com" && !strings.Contains(r.Header.Get("User-Agent"), "monoagent") {
			t.Errorf("reddit needs a custom User-Agent, got %q", r.Header.Get("User-Agent"))
		}
	}
	for host, want := range map[string]string{
		"api.github.com":     "Bearer gh",
		"api.linear.app":     "lin_api",
		"discord.com":        "Bot d",
		"api.stripe.com":     "basic:sk_test:",
		"acme.atlassian.net": "basic:j@example.com:t",
		"api.twilio.com":     "basic:AC123:t",
		"gql.hashnode.com":   "hn",
	} {
		if auth[host] != want {
			t.Errorf("%s auth = %q, want %q", host, auth[host], want)
		}
	}
}

func TestHealthValidatorsFallbacksAndFailures(t *testing.T) {
	withHealthFakeAPI(t, map[string]healthRoute{
		// Gmail profile fails, Drive about answers.
		"gmail.googleapis.com/gmail/v1/users/me/profile": {status: 403, body: `{}`},
		"www.googleapis.com/drive/v3/about":              healthOK(`{"user":{"displayName":"Only Name"}}`),
		// Sent Items is empty, Inbox names the owner.
		"graph.microsoft.com/v1.0/me/mailFolders/sentitems/messages": healthOK(`{"value":[]}`),
		"graph.microsoft.com/v1.0/me/mailFolders/inbox/messages":     healthOK(`{"value":[{"toRecipients":[{"emailAddress":{"address":"inbox@outlook.example"}}]}]}`),
		"api.notion.com/v1/users/me":                                 healthOK(`{"name":"Just Name"}`),
		"api.airtable.com/v0/meta/whoami":                            healthOK(`{"id":"usr9"}`),
		"api.linear.app/graphql":                                     healthOK(`{"data":{"viewer":{"name":"NameOnly"}}}`),
		"api.stripe.com/v1/account":                                  healthOK(`{"email":"only@stripe.example"}`),
		"api.hubapi.com/oauth/v1/access-tokens/h":                    healthOK(`{"hub_id":7}`),
		"login.salesforce.com/services/oauth2/userinfo":              healthOK(`{"name":"SF Name"}`),
		"api.telegram.org/botbad/getMe":                              healthOK(`{"ok":false}`),
		"api.telegram.org/botnoname/getMe":                           healthOK(`{"ok":true,"result":{"first_name":"First"}}`),
		"slack.com/api/auth.test":                                    healthOK(`{"ok":false,"error":"invalid_auth"}`),
		"www.googleapis.com/youtube/v3/channels":                     healthOK(`{"items":[]}`),
		"gql.hashnode.com":                                           {status: 301, body: "", header: http.Header{"Location": {"https://hashnode.example/pro"}}},
		"dev.to/api/users/me":                                        healthOK(`{}`),
		"api.producthunt.com/v2/api/graphql":                         healthOK(`{"data":{}}`),
		"api.github.com/user":                                        healthOK(`{}`),
	})
	ctx := context.Background()
	val := func(platform string, method AuthMethod, data map[string]interface{}) (string, error) {
		return ValidateConnection(ctx, &Connection{Platform: platform, Method: method, Data: data})
	}

	for _, c := range []struct {
		platform string
		method   AuthMethod
		data     map[string]interface{}
		want     string
	}{
		{"gmail", MethodOAuth, map[string]interface{}{"access_token": "g"}, "Only Name"},
		{"outlook", MethodOAuth, map[string]interface{}{"access_token": "o"}, "inbox@outlook.example"},
		{"notion", MethodOAuth, map[string]interface{}{"token": "n"}, "Just Name"},
		{"airtable", MethodOAuth, map[string]interface{}{"access_token": "a"}, "usr9"},
		{"linear", MethodOAuth, map[string]interface{}{"access_token": "l"}, "NameOnly"},
		{"stripe", MethodAPIKey, map[string]interface{}{"secret_key": "sk"}, "only@stripe.example"},
		{"hubspot", MethodOAuth, map[string]interface{}{"access_token": "h"}, "hub-7"},
		{"salesforce", MethodOAuth, map[string]interface{}{"access_token": "s"}, "SF Name"},
		{"telegram", MethodAPIKey, map[string]interface{}{"bot_token": "noname"}, "First"},
	} {
		got, err := val(c.platform, c.method, c.data)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.platform, got, err, c.want)
		}
	}

	for _, c := range []struct {
		platform string
		method   AuthMethod
		data     map[string]interface{}
		errHas   string
	}{
		{"telegram", MethodAPIKey, map[string]interface{}{"bot_token": "bad"}, "rejected"},
		{"telegram", MethodAPIKey, nil, "missing bot_token"},
		{"slack", MethodOAuth, map[string]interface{}{"access_token": "x"}, "invalid_auth"},
		{"youtube", MethodOAuth, map[string]interface{}{"access_token": "y"}, "no channel"},
		{"hashnode", MethodAPIKey, map[string]interface{}{"token": "t"}, "Pro plan"},
		{"hashnode", MethodAPIKey, nil, "missing token"},
		{"devto", MethodAPIKey, map[string]interface{}{"api_key": "k"}, "could not resolve"},
		{"devto", MethodAPIKey, nil, "missing api_key"},
		{"producthunt", MethodAPIKey, map[string]interface{}{"access_token": "p"}, "could not resolve"},
		{"producthunt", MethodAPIKey, nil, "missing access_token"},
		{"github", MethodAPIKey, map[string]interface{}{"token": "t"}, "empty login"},
		{"github", MethodAPIKey, nil, "missing token"},
		{"jira", MethodAPIKey, map[string]interface{}{"email": "e"}, "missing api_token"},
		{"jira", MethodAPIKey, map[string]interface{}{"email": "e", "api_token": "t"}, "missing domain"},
		{"jira", MethodAPIKey, nil, "missing email"},
		{"stripe", MethodAPIKey, nil, "missing secret_key"},
		{"twilio", MethodAPIKey, map[string]interface{}{"account_sid": "AC"}, "missing account_sid or auth_token"},
		{"mastodon", MethodAPIKey, map[string]interface{}{"access_token": "m"}, "missing instance_url"},
		{"gmail", MethodOAuth, nil, "missing access_token"},
		{"outlook", MethodOAuth, nil, "missing access_token"},
		{"hubspot", MethodOAuth, nil, "missing access_token"},
		{"salesforce", MethodOAuth, nil, "missing access_token"},
		{"generic_basic", MethodAPIKey, nil, "missing base_url"},
		{"redis", MethodAPIKey, nil, "missing connection_string"},
		{"outlook", MethodAPIKey, map[string]interface{}{"email": "e"}, "app_password are required"},
		// Unrouted hosts answer 404: surfaced as a status error.
		{"discord", MethodAPIKey, map[string]interface{}{"bot_token": "d"}, "404"},
		{"reddit", MethodOAuth, map[string]interface{}{"access_token": "r"}, "404"},
		{"bluesky", MethodAPIKey, map[string]interface{}{"identifier": "i"}, "404"},
		{"twilio", MethodAPIKey, map[string]interface{}{"account_sid": "AC9", "auth_token": "t"}, "404"},
		{"mastodon", MethodAPIKey, map[string]interface{}{"instance_url": "https://nowhere.example", "access_token": "m"}, "404"},
		{"jira", MethodAPIKey, map[string]interface{}{"email": "e", "api_token": "t", "domain": "nowhere.example"}, "404"},
	} {
		_, err := val(c.platform, c.method, c.data)
		if err == nil || !strings.Contains(err.Error(), c.errHas) {
			t.Errorf("%s %v: err %v, want it to mention %q", c.platform, c.data, err, c.errHas)
		}
	}
}

func TestHealthValidatorsRejectedCredentials(t *testing.T) {
	withHealthFakeAPI(t, map[string]healthRoute{
		"www.googleapis.com/drive/v3/about":                          {status: 401, body: `{}`},
		"gmail.googleapis.com/gmail/v1/users/me/profile":             {status: 401, body: `{}`},
		"graph.microsoft.com/v1.0/me/mailFolders/sentitems/messages": {status: 401, body: `{}`},
		"graph.microsoft.com/v1.0/me/mailFolders/inbox/messages":     {status: 401, body: `{}`},
	})
	for _, platform := range []string{"airtable", "linear", "stripe", "slack", "youtube", "hubspot", "salesforce", "devto", "producthunt", "gmail", "outlook"} {
		data := map[string]interface{}{"access_token": "t", "api_key": "k", "secret_key": "s"}
		method := MethodOAuth
		_, err := ValidateConnection(context.Background(), &Connection{Platform: platform, Method: method, Data: data})
		if err == nil {
			t.Errorf("%s: expected an error for a rejected token", platform)
			continue
		}
		if platform == "gmail" || platform == "outlook" {
			if HTTPStatus(err) != 401 {
				t.Errorf("%s: status %d, want 401 (%v)", platform, HTTPStatus(err), err)
			}
		}
	}
	// Drive's 401 (sheets/drive) is reported in words rather than as a StatusError.
	if _, err := ValidateConnection(context.Background(), &Connection{Platform: "google_sheets", Data: map[string]interface{}{"access_token": "t"}}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("google_sheets 401 err = %v", err)
	}
}
