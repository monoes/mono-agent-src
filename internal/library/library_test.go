package library_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
)

// memStore is a TokenStore in memory.
type memStore struct{ t *library.Token }

func (m *memStore) Load(context.Context) (*library.Token, error) { return m.t, nil }
func (m *memStore) Save(_ context.Context, t *library.Token) error {
	c := *t
	m.t = &c
	return nil
}
func (m *memStore) Delete(context.Context) error { m.t = nil; return nil }

// browser plays the user's browser: it opens the authorize URL and follows
// the redirect to MonoAgent's loopback listener.
func browser(t *testing.T) func(string) error {
	return func(u string) error {
		go func() {
			resp, err := http.Get(u)
			if err != nil {
				t.Errorf("browser: %v", err)
				return
			}
			resp.Body.Close()
		}()
		return nil
	}
}

func login(t *testing.T, fake *libraryfake.Server, store *memStore) *library.Client {
	t.Helper()
	c, err := library.NewClient(fake.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	var shown string
	tok, err := c.LoginPKCE(context.Background(), library.LoginOptions{Open: browser(t), OnURL: func(u string) { shown = u }, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("LoginPKCE: %v", err)
	}
	if !strings.Contains(shown, "code_challenge_method=S256") || !strings.Contains(shown, "redirect_uri=http%3A%2F%2F127.0.0.1%3A") {
		t.Fatalf("authorize URL = %s", shown)
	}
	if tok.User == nil || tok.User.Username != "ada" || tok.RefreshToken == "" || tok.Method != "pkce" {
		t.Fatalf("token = %+v", tok)
	}
	if !strings.Contains(tok.Scope, "library:write") {
		t.Fatalf("scope = %q", tok.Scope)
	}
	return c
}

func TestLoginPKCELoopback(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	store := &memStore{}
	c := login(t, fake, store)
	if store.t == nil || store.t.User.Email != "ada@example.com" {
		t.Fatalf("stored = %+v", store.t)
	}
	me, err := c.Me(context.Background())
	if err != nil || me.User.Username != "ada" {
		t.Fatalf("Me = %+v, %v", me, err)
	}
	if err := c.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.t != nil {
		t.Fatal("logout kept the token")
	}
	if _, err := c.Me(context.Background()); !errors.Is(err, library.ErrNotLoggedIn) {
		t.Fatalf("Me after logout = %v", err)
	}
}

func TestLoginPKCETimesOutWithoutBrowser(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	c, _ := library.NewClient(fake.URL, &memStore{})
	_, err := c.LoginPKCE(context.Background(), library.LoginOptions{Timeout: 200 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "no answer from the browser") {
		t.Fatalf("err = %v", err)
	}
}

func TestTokenRefresh(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	store := &memStore{}
	c := login(t, fake, store)
	first := store.t.AccessToken

	// Expired locally: refreshed before the call.
	store.t.ExpiresAt = time.Now().Add(-time.Minute)
	c2, _ := library.NewClient(fake.URL, store)
	if _, err := c2.Me(context.Background()); err != nil {
		t.Fatalf("Me with an expired token: %v", err)
	}
	if store.t.AccessToken == first || fake.Refreshes != 1 {
		t.Fatalf("not refreshed: %q (refreshes %d)", store.t.AccessToken, fake.Refreshes)
	}

	// Expired on the server only: the 401 triggers one refresh and a retry.
	fake.ExpireAccessTokens()
	if _, err := c2.Me(context.Background()); err != nil {
		t.Fatalf("Me after server-side expiry: %v", err)
	}
	if fake.Refreshes != 2 {
		t.Fatalf("refreshes = %d, want 2", fake.Refreshes)
	}
	_ = c
}

func TestEmailCodeLogin(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	store := &memStore{}
	c, _ := library.NewClient(fake.URL, store)
	ctx := context.Background()
	if err := c.SendEmailCode(ctx, "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.VerifyEmailCode(ctx, "ada@example.com", "000000"); err == nil {
		t.Fatal("a wrong code logged in")
	}
	tok, err := c.VerifyEmailCode(ctx, "ada@example.com", "123456")
	if err != nil || tok.User.Username != "ada" || tok.Method != "email" {
		t.Fatalf("token = %+v, %v", tok, err)
	}
}

func TestDownloadVerifiesSHA256(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	id := fake.Add("monoes", "workflow", "demo", "Demo", "official", "1.0.0", []byte(`{"name":"Demo","nodes":[]}`), nil)
	c := login(t, fake, &memStore{})
	ctx := context.Background()
	it, err := c.Get(ctx, "workflow/demo")
	if err != nil || it.ID != id || !it.Official() {
		t.Fatalf("Get = %+v, %v", it, err)
	}
	b, sum, err := c.Download(ctx, it)
	if err != nil || sum != it.SHA256 || len(b) == 0 {
		t.Fatalf("Download = %d bytes %s, %v", len(b), sum, err)
	}
	fake.TamperArtifact = true
	if _, _, err := c.Download(ctx, it); !errors.Is(err, library.ErrSHA256Mismatch) {
		t.Fatalf("tampered download: %v", err)
	}
	fake.TamperArtifact = false
	stale := *it
	stale.SHA256 = strings.Repeat("0", 64)
	if _, _, err := c.Download(ctx, &stale); !errors.Is(err, library.ErrSHA256Mismatch) {
		t.Fatalf("item sha mismatch: %v", err)
	}
}

func TestErrorsAndVisibility(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	fake.Add("ada", "org", "secret-org", "Secret", "private", "1.0.0", []byte(`{}`), nil)
	fake.AddUser(&libraryfake.User{ID: "u-eve", Username: "eve", Name: "Eve", Email: "eve@example.com"})
	ctx := context.Background()
	for status, want := range map[int]string{413: "too large", 429: "rate limited", 403: "not allowed", 401: "Log in to monoes.me first"} {
		e := &library.APIError{Status: status, Message: "x"}
		if !strings.Contains(e.Error(), want) {
			t.Errorf("%d: %q lacks %q", status, e.Error(), want)
		}
		if errors.Is(e, library.ErrNotLoggedIn) != (status == 401) {
			t.Errorf("%d: errors.Is(ErrNotLoggedIn) = %v", status, !(status == 401))
		}
	}
	store := &memStore{}
	c := login(t, fake, store)
	it, err := c.Get(ctx, "org/secret-org")
	if err != nil || it.Visibility != "private" {
		t.Fatalf("owner Get = %+v, %v", it, err)
	}
	fake.BrowserUser = fake.User("eve")
	other := login2(t, fake)
	_, err = other.Get(ctx, "org/secret-org")
	var ae *library.APIError
	if !errors.As(err, &ae) || ae.Status != 404 || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("someone else's private item: %v", err)
	}
}

// login2 logs the fake's current BrowserUser in, whoever that is.
func login2(t *testing.T, fake *libraryfake.Server) *library.Client {
	t.Helper()
	c, _ := library.NewClient(fake.URL, &memStore{})
	if _, err := c.LoginPKCE(context.Background(), library.LoginOptions{Open: browser(t), Timeout: 10 * time.Second}); err != nil {
		t.Fatalf("LoginPKCE: %v", err)
	}
	return c
}

// Every read needs a login, official items included: without one the
// client refuses before sending anything.
func TestReadsNeedALogin(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	fake.Add("monoes", "workflow", "demo", "Demo", "official", "1.0.0", []byte(`{}`), nil)
	ctx := context.Background()
	for name, c := range map[string]*library.Client{"no store": mustClient(t, fake.URL, nil), "empty store": mustClient(t, fake.URL, &memStore{})} {
		if _, err := c.List(ctx, library.ListQuery{Scope: "official"}); !errors.Is(err, library.ErrNotLoggedIn) {
			t.Errorf("%s: List = %v", name, err)
		}
		if _, err := c.Get(ctx, "workflow/demo"); !errors.Is(err, library.ErrNotLoggedIn) {
			t.Errorf("%s: Get = %v", name, err)
		}
		if _, _, err := c.Download(ctx, &library.Item{ID: "it-1"}); !errors.Is(err, library.ErrNotLoggedIn) {
			t.Errorf("%s: Download = %v", name, err)
		}
	}
	if n := fake.Requests["GET /api/library/items"] + fake.Requests["GET /api/library/items/workflow/demo"] +
		fake.Requests["GET /api/library/items/it-1/artifact"]; n != 0 {
		t.Fatalf("%d library requests went out without a login", n)
	}
	if msg := library.ErrNotLoggedIn.Error(); msg != "Log in to monoes.me first: monoagentcli library login" {
		t.Fatalf("ErrNotLoggedIn = %q", msg)
	}
}

func mustClient(t *testing.T, base string, store library.TokenStore) *library.Client {
	t.Helper()
	c, err := library.NewClient(base, store)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A 401 on a read refreshes the token once and retries; when the refresh
// fails too, the answer is "log in first".
func TestReadRefreshesOnceOn401(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	fake.Add("monoes", "workflow", "demo", "Demo", "official", "1.0.0", []byte(`{}`), nil)
	store := &memStore{}
	c := login(t, fake, store)
	ctx := context.Background()

	fake.ExpireAccessTokens()
	res, err := c.List(ctx, library.ListQuery{Scope: "official"})
	if err != nil || res.Total != 1 || fake.Refreshes != 1 {
		t.Fatalf("List after server-side expiry = %+v, %v (refreshes %d)", res, err, fake.Refreshes)
	}
	if fake.Requests["GET /api/library/items"] != 2 {
		t.Fatalf("list requests = %d, want 2 (401, then the retry)", fake.Requests["GET /api/library/items"])
	}

	fake.RevokeAll()
	_, err = c.Get(ctx, "workflow/demo")
	var ae *library.APIError
	if !errors.As(err, &ae) || ae.Status != 401 || !errors.Is(err, library.ErrNotLoggedIn) ||
		err.Error() != library.ErrNotLoggedIn.Error() {
		t.Fatalf("Get after revocation = %v", err)
	}
	if fake.Refreshes != 1 || fake.Requests["GET /api/library/items/workflow/demo"] != 1 {
		t.Fatalf("refreshes %d, gets %d: want no retry after the failed refresh",
			fake.Refreshes, fake.Requests["GET /api/library/items/workflow/demo"])
	}
}

// With AnonymousReads the fake behaves like monoes.me before the change,
// so a server still allowing it just sees an authenticated read.
func TestFakeAnonymousReadsToggle(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	fake.Add("monoes", "workflow", "demo", "Demo", "official", "1.0.0", []byte(`{}`), nil)
	get := func() int {
		resp, err := http.Get(fake.URL + "/api/library/items?scope=official")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := get(); got != 401 {
		t.Fatalf("anonymous list = %d, want 401", got)
	}
	fake.AnonymousReads = true
	if got := get(); got != 200 {
		t.Fatalf("anonymous list with AnonymousReads = %d, want 200", got)
	}
}

func TestPublishRoundTrip(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	c := login(t, fake, &memStore{})
	ctx := context.Background()
	it, err := c.Create(ctx, library.Upload{Kind: "org", Visibility: "private", Name: "Team", Version: "1.0.0",
		Tags: []string{"a", "b"}, Filename: "team.json", ContentType: "application/json", Data: []byte(`{"name":"team"}`)})
	if err != nil || it.Visibility != "private" || it.Owner.Username != "ada" || len(it.Tags) != 2 {
		t.Fatalf("Create = %+v, %v", it, err)
	}
	it2, err := c.PutArtifact(ctx, it.ID, library.Upload{Version: "1.0.1", Filename: "team.json",
		ContentType: "application/json", Data: []byte(`{"name":"team","goal":"g"}`)})
	if err != nil || it2.Version != "1.0.1" || it2.SHA256 == it.SHA256 {
		t.Fatalf("PutArtifact = %+v, %v", it2, err)
	}
	mine, err := c.List(ctx, library.ListQuery{Scope: "mine"})
	if err != nil || mine.Total != 1 {
		t.Fatalf("mine = %+v, %v", mine, err)
	}
	if _, err := c.Create(ctx, library.Upload{Kind: "workflow", Visibility: "official", Name: "X",
		Filename: "x.json", ContentType: "application/json", Data: []byte(`{}`)}); err == nil {
		t.Fatal("a non-admin published an official item")
	}
}

func TestBaseURLMustBeHTTPS(t *testing.T) {
	if _, err := library.NewClient("http://monoes.example", nil); err == nil {
		t.Fatal("plain http to a remote host accepted")
	}
	for _, ok := range []string{"https://monoes.me", "http://127.0.0.1:3100", "http://localhost:3100"} {
		if _, err := library.NewClient(ok, nil); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	t.Setenv("MONOES_BASE_URL", "http://127.0.0.1:3100/")
	if library.BaseURL() != "http://127.0.0.1:3100" {
		t.Fatalf("BaseURL = %s", library.BaseURL())
	}
}

func TestProvenanceIsProfileScoped(t *testing.T) {
	home := t.TempDir()
	p := library.OpenProvenance(home)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(p.Put(library.Record{Kind: "workflow", Profile: "work", LocalID: "wf1", ItemID: "i1", Version: "1.0.0"}))
	must(p.Put(library.Record{Kind: "workflow", Profile: "home", LocalID: "wf2", ItemID: "i1", Version: "1.0.0"}))
	must(p.Put(library.Record{Kind: "workflow", Profile: "work", LocalID: "wf1", ItemID: "i1", Version: "1.1.0"}))
	work, _ := p.List("work", "")
	if len(work) != 1 || work[0].Version != "1.1.0" || work[0].Source != "monoes" {
		t.Fatalf("work = %+v", work)
	}
	must(p.Remove("work", "workflow", "wf1"))
	if work, _ := p.List("work", ""); len(work) != 0 {
		t.Fatalf("after remove: %+v", work)
	}
	if st, err := os.Stat(filepath.Join(home, "library", "installed.json")); err != nil || st.Size() == 0 {
		t.Fatalf("file: %v", err)
	}
}

func TestKindAPI(t *testing.T) {
	for in, want := range map[string]string{"api": "api", "APIs": "api", " apis ": "api", "orgs": "org"} {
		if got := library.NormalizeKind(in); got != want || !library.ValidKind(got) {
			t.Errorf("NormalizeKind(%q) = %q (valid %v), want %q", in, got, library.ValidKind(got), want)
		}
	}
	fake := libraryfake.New()
	defer fake.Close()
	c := login(t, fake, &memStore{})
	ctx := context.Background()
	pkg := []byte(`{"$schema":"apipkg.v1","id":"acme","title":"Acme","base_url":"https://acme.test","operations":[]}`)
	it, err := c.Create(ctx, library.Upload{Kind: library.KindAPI, Visibility: "private", Name: "Acme", Version: "1.0.0",
		Filename: "acme.json", ContentType: "application/json", Data: pkg})
	if err != nil || it.Kind != library.KindAPI {
		t.Fatalf("Create api = %+v, %v", it, err)
	}
	got, err := c.Get(ctx, "api/"+it.Slug)
	if err != nil || got.ID != it.ID {
		t.Fatalf("Get api/%s = %+v, %v", it.Slug, got, err)
	}
	data, _, err := c.Download(ctx, got)
	if err != nil || string(data) != string(pkg) {
		t.Fatalf("Download = %s, %v", data, err)
	}
	if res, err := c.List(ctx, library.ListQuery{Kind: library.KindAPI, Scope: "mine"}); err != nil || res.Total != 1 {
		t.Fatalf("List kind api = %+v, %v", res, err)
	}
	if _, err := c.Create(ctx, library.Upload{Kind: library.KindAPI, Visibility: "private", Name: "Bad",
		Filename: "bad.json", ContentType: "application/json", Data: []byte(`not json`)}); err == nil {
		t.Fatal("the fake accepted an api item that is not JSON")
	}
}
