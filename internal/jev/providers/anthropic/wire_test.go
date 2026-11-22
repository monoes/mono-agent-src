package anthropic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/credhttp/credhttptest"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/testdb"
)

var oneNoul = map[string]jev.Question{"q": noul("ok?")}

// A failed call is an error, not a miss: a bad key never reads as a bad answer.
func TestAnHTTPErrorIsNotAMiss(t *testing.T) {
	f := newFake(t, func(int, messagesRequest) (int, string) { return http.StatusUnauthorized, `{"error":"bad key"}` })
	c := providerClient(t, f.URL, testKey, "")
	_, err := c.Ask(context.Background(), map[string]any{}, oneNoul)
	if err == nil || errors.Is(err, jev.ErrInvalidAnswer) || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("got %v", err)
	}
	if n := len(f.requests()); n != 1 {
		t.Errorf("a 401 was retried: %d calls", n)
	}
}

func TestRetriesHonourRetryAfter(t *testing.T) {
	f := newFake(t, func(n int, r messagesRequest) (int, string) {
		if n < 2 { // the first sample fails twice, then every call succeeds
			return http.StatusTooManyRequests, `{"error":"slow down"}`
		}
		return toolReply("yes")
	})
	c := providerClient(t, f.URL, testKey, "")
	resp, err := c.Ask(context.Background(), map[string]any{}, oneNoul)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.requests()); n != 7 || !near(resp.Answers["q"].Noul, 6.0/7) {
		t.Errorf("%d calls, answer %+v", n, resp.Answers["q"])
	}

	f = newFake(t, func(int, messagesRequest) (int, string) { return http.StatusServiceUnavailable, `{}` })
	c = providerClient(t, f.URL, testKey, "")
	if _, err := c.Ask(context.Background(), map[string]any{}, oneNoul); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("got %v", err)
	}
	if n := len(f.requests()); n != 3 {
		t.Errorf("%d calls, want 3 (two retries)", n)
	}
}

// A server that echoes the key back never puts it into an error, even
// where the quoted body is cut short.
func TestTheKeyIsNeverInAnError(t *testing.T) {
	// pad 280: the quote is cut 11 characters into the key.
	for _, pad := range []int{0, 280} {
		f := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(strings.Repeat("x", pad) + "your key " + r.Header.Get("X-Api-Key") + " is wrong"))
		}))
		t.Cleanup(f.Close)
		c := providerClient(t, f.URL, testKey, "")
		_, err := c.Ask(context.Background(), map[string]any{}, oneNoul)
		if err == nil || strings.Contains(err.Error(), testKey[:10]) {
			t.Fatalf("pad %d: %v", pad, err)
		}
		b, _ := New(jevconf.Provider{BaseURL: f.URL}, testKey)
		if _, err := b.Models(context.Background()); err == nil || strings.Contains(err.Error(), testKey[:10]) {
			t.Fatalf("models, pad %d: %v", pad, err)
		}
		if _, err := b.Probe(context.Background()); err == nil || strings.Contains(err.Error(), testKey[:10]) {
			t.Fatalf("probe, pad %d: %v", pad, err)
		}
	}
}

// A redirect to another host carries no key, and a 307 that would resend
// the question there is not followed.
func TestRedirectsDoNotCarryTheKey(t *testing.T) {
	post := credhttptest.NewPair(t, http.StatusTemporaryRedirect, `{}`)
	c := providerClient(t, post.From, testKey, "")
	if _, err := c.Ask(context.Background(), map[string]any{}, oneNoul); err == nil {
		t.Fatal("a 307 elsewhere succeeded")
	}
	if got := post.Got(); len(got) != 0 {
		t.Errorf("the 307 reached the other host: %+v", got)
	}
	get := credhttptest.NewPair(t, http.StatusFound, `{"data":[{"id":"claude-x"}]}`)
	c = providerClient(t, get.From, testKey, "")
	if _, err := c.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(get.Got()) != 1 || get.Leaked(testKey) != "" {
		t.Errorf("the redirected GET carried %+v", get.Got())
	}
}

// For every caller, the provider's key goes to its own host only: never to
// the TypeSafe host or one TYPESAFE_BASE_URL names, and the TypeSafe key
// never goes to it. A step refused its key sends nothing.
func TestTheKeyReachesOnlyTheProvidersHost(t *testing.T) {
	const typesafeKey = "typesafe-stored-key-for-tests"
	for _, cl := range callers.All() {
		t.Run(cl.Name, func(t *testing.T) {
			typesafe, envHost := jevtest.TwoHosts(t)
			own := jevtest.NewProbe(t)
			db := testdb.Open(t).DB
			for name, v := range map[string]string{jevconf.SecretName: typesafeKey, "anthropic-key": testKey} {
				if _, err := secrets.Add(context.Background(), db, jevconf.VaultScope, "secret", name, map[string]string{"secret": v}, "", "", ""); err != nil {
					t.Fatal(err)
				}
			}
			if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{Name: "claude", Kind: Kind, BaseURL: own.URL, SecretName: "anthropic-key"}); err != nil {
				t.Fatal(err)
			}
			if err := jevconf.SetDefaultProvider(context.Background(), db, "claude"); err != nil {
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
				_, _ = c.Ask(ctx, map[string]any{}, oneNoul)
				_, _ = c.Models(ctx)
				if !own.Saw(testKey) || own.Calls() != 2 {
					t.Errorf("the provider's host got %d calls, key seen %v", own.Calls(), own.Saw(testKey))
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
			cl.Audited(t, db, "default", "anthropic-key")
		})
	}
}

func TestModels(t *testing.T) {
	f := newFake(t, func(int, messagesRequest) (int, string) {
		return http.StatusOK, `{"data":[{"id":"claude-a","type":"model"},{"id":""},{"id":"claude-b"}],"has_more":false}`
	})
	c := providerClient(t, f.URL, testKey, "")
	ms, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ms, []jev.Model{{ID: "claude-a"}, {ID: "claude-b"}}) {
		t.Errorf("models %v", ms)
	}
	s := f.requests()[0]
	if s.method != http.MethodGet || s.path != "/v1/models?limit=1000" || s.header.Get("X-Api-Key") != testKey || s.header.Get("Anthropic-Version") != apiVersion {
		t.Errorf("request %s %s %v", s.method, s.path, s.header)
	}
}

func TestProbe(t *testing.T) {
	f := newFake(t, sequence("yes"))
	b, err := New(jevconf.Provider{BaseURL: f.URL}, testKey)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := b.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.Probability != jev.ProbSampled || caps.QuestionsPerRequest != 1 || !caps.Models || len(caps.Kinds) != 3 {
		t.Errorf("caps %+v", caps)
	}
	if n := len(f.requests()); n != 1 || !slices.Equal(enum(f.requests()[0].body), []string{"yes", "no"}) {
		t.Errorf("probe sent %d request(s)", n)
	}

	f = newFake(t, sequence("maybe"))
	b, _ = New(jevconf.Provider{BaseURL: f.URL}, testKey)
	if _, err := b.Probe(context.Background()); !errors.Is(err, jev.ErrInvalidAnswer) {
		t.Errorf("an out-of-enum probe answer: %v", err)
	}
}

func TestNewDefaults(t *testing.T) {
	b, err := New(jevconf.Provider{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.base != DefaultBaseURL || b.model != DefaultModel || b.retries != 2 {
		t.Errorf("backend %+v", b)
	}
}
