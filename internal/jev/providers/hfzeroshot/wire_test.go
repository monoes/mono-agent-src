package hfzeroshot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/credhttp/credhttptest"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/testdb"
)

var oneNoul = map[string]jev.Question{"q": {Type: jev.TypeNoul, Instructions: "ok?"}}

// A keyless provider (a local server) sends no Authorization header at all.
func TestAKeylessProviderSendsNoAuthorization(t *testing.T) {
	f := newFake(t, pairs(0.9, 0.1))
	c := providerClient(t, f.URL, "")
	resp, err := c.Ask(context.Background(), map[string]any{}, oneNoul)
	if err != nil {
		t.Fatal(err)
	}
	if !near(resp.Answers["q"].Noul, 0.9) {
		t.Errorf("answer %+v", resp.Answers["q"])
	}
	if h := f.requests()[0].header; len(h.Values("Authorization")) != 0 {
		t.Errorf("headers %v", h)
	}
}

func TestRetries(t *testing.T) {
	var calls atomic.Int32
	f := newFake(t, func(r classifyRequest) (int, string) {
		if calls.Add(1) <= 2 {
			return http.StatusServiceUnavailable, `{"error":"Model is currently loading"}`
		}
		return pairs(0.5, 0.5)(r)
	})
	c := providerClient(t, f.URL, testKey)
	if _, err := c.Ask(context.Background(), map[string]any{}, oneNoul); err != nil {
		t.Fatal(err)
	}
	if n := len(f.requests()); n != 3 {
		t.Errorf("%d calls, want 3", n)
	}
	f = newFake(t, func(classifyRequest) (int, string) { return http.StatusBadRequest, `{"error":"bad"}` })
	c = providerClient(t, f.URL, testKey)
	if _, err := c.Ask(context.Background(), map[string]any{}, oneNoul); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("got %v", err)
	}
	if n := len(f.requests()); n != 1 {
		t.Errorf("a 400 was retried: %d calls", n)
	}
}

// A server that echoes the key back never puts it into an error, even
// where the quoted body is cut short.
func TestTheKeyIsNeverInAnError(t *testing.T) {
	// pad 270: the quote is cut 12 characters into the key ("Bearer " included).
	for _, pad := range []int{0, 270} {
		f := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(strings.Repeat("x", pad) + "your token " + r.Header.Get("Authorization") + " is wrong"))
		}))
		t.Cleanup(f.Close)
		c := providerClient(t, f.URL, testKey)
		_, err := c.Ask(context.Background(), map[string]any{}, oneNoul)
		if err == nil || strings.Contains(err.Error(), testKey[:10]) {
			t.Fatalf("pad %d: %v", pad, err)
		}
		b, _ := New(jevconf.Provider{BaseURL: f.URL}, testKey)
		if _, err := b.Probe(context.Background()); err == nil || strings.Contains(err.Error(), testKey[:10]) {
			t.Fatalf("probe, pad %d: %v", pad, err)
		}
	}
}

// A redirect to another host carries no key: a 307 that would resend the
// body is not followed, and a 302 arrives there without it.
func TestRedirectsDoNotCarryTheKey(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusFound} {
		p := credhttptest.NewPair(t, code, `[{"label":"yes","score":1}]`)
		c := providerClient(t, p.From, testKey)
		_, _ = c.Ask(context.Background(), map[string]any{}, oneNoul)
		if code == http.StatusTemporaryRedirect && len(p.Got()) != 0 {
			t.Errorf("the 307 reached the other host: %+v", p.Got())
		}
		if leak := p.Leaked(testKey); leak != "" {
			t.Errorf("HTTP %d: the key reached the other host in %s", code, leak)
		}
	}
}

// The provider's key goes to its own endpoint only: never to the TypeSafe
// host or one TYPESAFE_BASE_URL names, and the TypeSafe key never goes to it.
func TestTheKeyReachesOnlyTheProvidersHost(t *testing.T) {
	typesafe, envHost := jevtest.TwoHosts(t)
	own := jevtest.NewProbe(t)
	ctx := context.Background()
	db := testdb.Open(t).DB
	const typesafeKey = "typesafe-stored-key-for-tests"
	for name, v := range map[string]string{jevconf.SecretName: typesafeKey, "hf-token": testKey} {
		if _, err := secrets.Add(ctx, db, jevconf.VaultScope, "secret", name, map[string]string{"secret": v}, "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := jevconf.SaveProvider(ctx, db, jevconf.Provider{Name: "hf", Kind: Kind, BaseURL: own.URL + "/models/m", SecretName: "hf-token"}); err != nil {
		t.Fatal(err)
	}
	if err := jevconf.SetDefaultProvider(ctx, db, "hf"); err != nil {
		t.Fatal(err)
	}
	c, err := jevconf.NewClient(ctx, db, "default", "", "", jevconf.NodeSurface("ai.choose"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ask(ctx, map[string]any{}, oneNoul); err == nil {
		t.Fatal("the probe's 401 succeeded")
	}
	if !own.Saw(testKey) || own.Calls() != 1 {
		t.Errorf("the provider's host got %d calls, key seen %v", own.Calls(), own.Saw(testKey))
	}
	if own.Saw(typesafeKey) {
		t.Error("the TypeSafe key reached the provider")
	}
	if typesafe.Calls()+envHost.Calls() != 0 {
		t.Errorf("calls reached the TypeSafe host (%d) or the environment's host (%d)", typesafe.Calls(), envHost.Calls())
	}
	if _, err := c.Models(ctx); !errors.Is(err, jev.ErrUnsupported) {
		t.Errorf("models: %v", err)
	}
}

func TestProbe(t *testing.T) {
	f := newFake(t, pairs(0.7, 0.3))
	b, err := New(jevconf.Provider{BaseURL: f.URL}, testKey)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := b.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.Probability != jev.ProbNative || caps.MaxChoiceOptions != MaxLabels || caps.QuestionsPerRequest != 1 || caps.Models ||
		!caps.Supports(jev.TypeChoice) || !caps.Supports(jev.TypeNoul) || caps.Supports(jev.TypeScore) {
		t.Errorf("caps %+v", caps)
	}
	if n := len(f.requests()); n != 1 {
		t.Errorf("probe sent %d request(s)", n)
	}
	f = newFake(t, func(classifyRequest) (int, string) { return http.StatusOK, `{"error":"x"}` })
	b, _ = New(jevconf.Provider{BaseURL: f.URL}, testKey)
	if _, err := b.Probe(context.Background()); !errors.Is(err, jev.ErrInvalidAnswer) {
		t.Errorf("an empty reply: %v", err)
	}
}
