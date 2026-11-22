package hfzeroshot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/credhttp/credhttptest"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
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

// A cold endpoint answers 503 while its model loads, often longer than two
// short retries: a 503 waits what estimated_time (or Retry-After, the longer)
// says, at most 20s per wait, for up to 4 attempts; other statuses keep
// their 3 attempts and 5s waits; every wait ends with the caller's context.
func TestALoadingModelIsWaitedFor(t *testing.T) {
	loading := func(failures int32, body string) (*fake, *atomic.Int32) {
		var calls atomic.Int32
		return newFake(t, func(r classifyRequest) (int, string) {
			if calls.Add(1) <= failures {
				return http.StatusServiceUnavailable, body
			}
			return pairs(0.5, 0.5)(r)
		}), &calls
	}
	withSleeps := func(c *jev.Client) *[]time.Duration {
		waits := &[]time.Duration{}
		c.Backend.(*Backend).sleep = func(ctx context.Context, d time.Duration) error {
			*waits = append(*waits, d)
			return ctx.Err()
		}
		return waits
	}

	f, _ := loading(3, `{"error":"Model facebook/bart-large-mnli is currently loading","estimated_time":12.5}`)
	c := providerClient(t, f.URL, testKey)
	waits := withSleeps(c)
	if _, err := c.Ask(context.Background(), map[string]any{}, oneNoul); err != nil {
		t.Fatal(err)
	}
	if n := len(f.requests()); n != 4 || len(*waits) != 3 || (*waits)[0] != 12500*time.Millisecond {
		t.Errorf("%d calls, waits %v; want 4 calls and 12.5s waits", n, *waits)
	}

	f, _ = loading(9, `{"error":"loading","estimated_time":300}`)
	c = providerClient(t, f.URL, testKey)
	waits = withSleeps(c)
	if _, err := c.Ask(context.Background(), map[string]any{}, oneNoul); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("got %v", err)
	}
	if n := len(f.requests()); n != 4 || len(*waits) != 3 || (*waits)[2] != 20*time.Second {
		t.Errorf("%d calls, waits %v; want 4 calls and waits capped at 20s", n, *waits)
	}

	// A 502 keeps today's budget.
	var calls atomic.Int32
	f = newFake(t, func(classifyRequest) (int, string) { calls.Add(1); return http.StatusBadGateway, `{"estimated_time":30}` })
	c = providerClient(t, f.URL, testKey)
	waits = withSleeps(c)
	if _, err := c.Ask(context.Background(), map[string]any{}, oneNoul); err == nil || calls.Load() != 3 || (*waits)[0] > 5*time.Second {
		t.Errorf("502: %v, %d calls, waits %v", err, calls.Load(), *waits)
	}

	// The caller's context ends the wait.
	f, _ = loading(9, `{"estimated_time":15}`)
	c = providerClient(t, f.URL, testKey)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := c.Ask(ctx, map[string]any{}, oneNoul); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
		t.Errorf("err %v after %v", err, time.Since(started))
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

// For every caller, the provider's key goes to its own endpoint only: never
// to the TypeSafe host or one TYPESAFE_BASE_URL names, and the TypeSafe key
// never goes to it. A step refused its key sends nothing.
func TestTheKeyReachesOnlyTheProvidersHost(t *testing.T) {
	const typesafeKey = "typesafe-stored-key-for-tests"
	for _, cl := range callers.All() {
		t.Run(cl.Name, func(t *testing.T) {
			typesafe, envHost := jevtest.TwoHosts(t)
			own := jevtest.NewProbe(t)
			db := testdb.Open(t).DB
			for name, v := range map[string]string{jevconf.SecretName: typesafeKey, "hf-token": testKey} {
				if _, err := secrets.Add(context.Background(), db, jevconf.VaultScope, "secret", name, map[string]string{"secret": v}, "", "", ""); err != nil {
					t.Fatal(err)
				}
			}
			if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{Name: "hf", Kind: Kind, BaseURL: own.URL + "/models/m", SecretName: "hf-token"}); err != nil {
				t.Fatal(err)
			}
			if err := jevconf.SetDefaultProvider(context.Background(), db, "hf"); err != nil {
				t.Fatal(err)
			}
			cl.Prepare(t)
			ctx := cl.Ctx(t, db, context.Background(), "default", "ai.choose")
			c, err := jevconf.NewClient(ctx, db, "default", "", "", jevconf.NodeSurface("ai.choose"))
			if cl.Refused {
				if !errors.Is(err, secrets.ErrResolveRefused) || c != nil {
					t.Fatalf("a refused step got the provider's key: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.Ask(ctx, map[string]any{}, oneNoul); err == nil {
					t.Fatal("the probe's 401 succeeded")
				}
				if !own.Saw(testKey) || own.Calls() != 1 {
					t.Errorf("the provider's host got %d calls, key seen %v", own.Calls(), own.Saw(testKey))
				}
				if _, err := c.Models(ctx); !errors.Is(err, jev.ErrUnsupported) {
					t.Errorf("models: %v", err)
				}
			}
			if cl.Refused && own.Calls() != 0 {
				t.Errorf("a refused step sent %d request(s)", own.Calls())
			}
			if own.Saw(typesafeKey) {
				t.Error("the TypeSafe key reached the provider")
			}
			if typesafe.Calls()+envHost.Calls() != 0 {
				t.Errorf("calls reached the TypeSafe host (%d) or the environment's host (%d)", typesafe.Calls(), envHost.Calls())
			}
			cl.Audited(t, db, "default", "hf-token")
		})
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
