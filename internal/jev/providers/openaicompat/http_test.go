package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/testdb"
)

const testKey = "prov-TEST-KEY-0123456789"

// A keyless provider (a local server) gets no Authorization header; a keyed
// one gets its own key as a bearer token, for answers and for models.
func TestTheKeyIsSentOnlyWhenThereIsOne(t *testing.T) {
	for _, c := range []struct{ key, want string }{{"", ""}, {testKey, "Bearer " + testKey}} {
		f := newFake(t, func(_ int, r seen) (int, any) {
			if r.Path == "/models" {
				return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "llama3"}, map[string]any{"id": "qwen"}}}
			}
			return oracle(t, "A", 0)(0, r)
		})
		cl, _ := newClient(t, f.URL, c.key, jev.Capabilities{})
		if _, err := ask(t, cl, choiceOf("x", "y")); err != nil {
			t.Fatal(err)
		}
		models, err := cl.Models(context.Background())
		if err != nil || len(models) != 2 || models[0].ID != "llama3" || models[1].ID != "qwen" {
			t.Fatalf("models = %+v %v", models, err)
		}
		reqs := f.requests()
		if reqs[1].Method != "GET" || reqs[1].Path != "/models" {
			t.Fatalf("models request = %+v", reqs[1])
		}
		for _, r := range reqs {
			if got := r.Header.Get("Authorization"); got != c.want {
				t.Errorf("key %q: Authorization %q", c.key, got)
			}
		}
	}
}

// A server that echoes the Authorization header into its error body never
// puts the key into an error.
func TestTheKeyNeverAppearsInAnError(t *testing.T) {
	for _, status := range []int{401, 500} {
		f := newFake(t, func(_ int, r seen) (int, any) {
			return status, fmt.Sprintf(`{"error":"bad header %s"}`, r.Header.Get("Authorization"))
		}, retryAfterZero)
		cl, _ := newClient(t, f.URL, testKey, jev.Capabilities{})
		_, err := ask(t, cl, choiceOf("x", "y"))
		if err == nil || strings.Contains(err.Error(), testKey) || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) ||
			!strings.Contains(err.Error(), "[redacted]") {
			t.Fatalf("%d: err = %v", status, err)
		}
		_, err = cl.Models(context.Background())
		if err == nil || strings.Contains(err.Error(), testKey) {
			t.Fatalf("%d: models err = %v", status, err)
		}
	}
}

// A redirect to another host carries no key.
func TestARedirectDoesNotCarryTheKey(t *testing.T) {
	elsewhere := newFake(t, func(int, seen) (int, any) { return 200, textReply("A") })
	own := newFake(t, func(int, seen) (int, any) { return 0, nil }, func(f *fake, _ http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.reqs = append(f.reqs, seen{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()})
			f.mu.Unlock()
			http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
		})
	})
	// Another host name for the same listener: a different host, as far as the key goes.
	base := strings.Replace(own.URL, "127.0.0.1", "localhost", 1)
	cl, _ := newClient(t, base, testKey, jev.Capabilities{})
	_, _ = ask(t, cl, choiceOf("x", "y"))
	_, _ = cl.Models(context.Background())
	if len(own.requests()) == 0 || own.requests()[0].Header.Get("Authorization") != "Bearer "+testKey {
		t.Fatal("the provider's own host did not get the key")
	}
	if len(elsewhere.requests()) == 0 {
		t.Fatal("the redirect was not followed: the test proves nothing")
	}
	for _, r := range elsewhere.requests() {
		if strings.Contains(fmt.Sprint(r.Header), testKey) {
			t.Fatalf("the redirect target got the key: %v", r.Header)
		}
	}
}

// A 408, 429 or 5xx is retried twice, honouring Retry-After.
func TestRetryableStatusesAreRetriedTwice(t *testing.T) {
	statuses := []int{429, 503, 200}
	f := newFake(t, func(n int, r seen) (int, any) {
		if s := statuses[min(n, len(statuses)-1)]; s != 200 {
			return s, `{"error":"busy"}`
		}
		return oracle(t, "A", 0)(n, r)
	}, retryAfterZero)
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	if _, err := ask(t, cl, choiceOf("x", "y")); err != nil || len(f.requests()) != 3 {
		t.Fatalf("%v (%d requests)", err, len(f.requests()))
	}
	statuses = []int{500, 500, 500, 500, 500, 500, 500}
	before := len(f.requests())
	if _, err := ask(t, cl, choiceOf("x", "y")); err == nil || len(f.requests())-before != 3 {
		t.Fatalf("%v (%d requests)", err, len(f.requests())-before)
	}
}

func retryAfterZero(_ *fake, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		h.ServeHTTP(w, r)
	})
}

// Probe asks one tiny noul and records whether log-probabilities came back.
func TestProbeFindsWhatTheProviderGives(t *testing.T) {
	for _, c := range []struct {
		name  string
		reply func(int, seen) (int, any)
		want  jev.ProbabilityKind
		reqs  int
		k     int
	}{
		{"logprobs", func(int, seen) (int, any) {
			return 200, logprobReply(alt{"Yes", 0.8}, alt{"No", 0.1}, alt{"yes", 0.05})
		}, jev.ProbLogprob, 1, 3},
		{"no logprobs", func(int, seen) (int, any) { return 200, textReply("Yes") }, jev.ProbSampled, 1, defaultTopK},
		{"refuses logprobs", func(_ int, r seen) (int, any) {
			if r.Body.Logprobs {
				return 400, `{"error":"logprobs unsupported"}`
			}
			return 200, textReply("Yes")
		}, jev.ProbSampled, 2, defaultTopK},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, c.reply)
			cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
			p, ok := cl.Backend.(Prober)
			if !ok {
				t.Fatal("the backend is not a Prober")
			}
			caps, err := p.Probe(context.Background())
			if err != nil || caps.Probability != c.want || len(f.requests()) != c.reqs || cl.Backend.(*Backend).k() != c.k {
				t.Fatalf("caps = %+v %v (%d requests, k %d)", caps, err, len(f.requests()), cl.Backend.(*Backend).k())
			}
			if r := f.requests()[0]; r.Body.MaxTokens != 1 || r.Body.TopLogprobs != defaultTopK || !strings.Contains(r.user(), "Yes") {
				t.Fatalf("probe request = %+v", r.Body)
			}
		})
	}
	f := newFake(t, func(int, seen) (int, any) { return 401, `{"error":"no"}` })
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	if _, err := cl.Backend.(Prober).Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

// llama.cpp and older servers shape log-probabilities differently; any
// shape that cannot be read is "none", never an error.
func TestLogprobShapesAreReadDefensively(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"openai", `{"choices":[{"logprobs":{"content":[{"token":"A","logprob":-0.1,"top_logprobs":[{"token":"A","logprob":-0.1},{"token":"B","logprob":-2}]}]}}]}`, 2},
		{"tok_str and prob", `{"choices":[{"logprobs":{"content":[{"top_logprobs":[{"tok_str":"A","prob":0.9},{"tok_str":"B","prob":0.1}]}]}}]}`, 2},
		{"only the token", `{"choices":[{"logprobs":{"content":[{"token":"A","logprob":-0.1,"top_logprobs":[]}]}}]}`, 1},
		{"legacy map", `{"choices":[{"logprobs":{"top_logprobs":[{"A":-0.1,"B":-3}]}}]}`, 2},
		{"llama.cpp completion_probabilities", `{"completion_probabilities":[{"content":"A","probs":[{"tok_str":"A","prob":0.7},{"tok_str":"B","prob":0.3}]}]}`, 2},
		{"null", `{"choices":[{"logprobs":null}]}`, 0},
		{"a string", `{"choices":[{"logprobs":"nope"}]}`, 0},
		{"empty content", `{"choices":[{"logprobs":{"content":[]}}]}`, 0},
		{"no choices", `{}`, 0},
	} {
		var r chatResponse
		if err := json.Unmarshal([]byte(c.body), &r); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := r.top(); len(got) != c.want {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

// D5: the provider's key is read from its machine-vault entry under the
// same gate as the TypeSafe key and reaches only the provider's own host —
// never the TypeSafe host or one TYPESAFE_BASE_URL names — and the TypeSafe
// key never reaches the provider.
func TestTheKeyGoesOnlyToTheProvidersHost(t *testing.T) {
	const typesafeKey = "ts-STORED-KEY-0123456789"
	for _, c := range callers.All() {
		t.Run(c.Name, func(t *testing.T) {
			ctx := context.Background()
			db := testdb.Open(t).DB
			for name, key := range map[string]string{jevconf.SecretName: typesafeKey, "openai": testKey} {
				if _, err := secrets.Add(ctx, db, jevconf.VaultScope, "secret", name, map[string]string{"secret": key}, "", "", ""); err != nil {
					t.Fatal(err)
				}
			}
			typesafe, envHost := jevtest.TwoHosts(t)
			own := &recorder{}
			srv := httptest.NewServer(own)
			t.Cleanup(srv.Close)
			p := jevconf.Provider{Name: "own", Kind: Kind, BaseURL: srv.URL + "/v1", Model: testModel, SecretName: "openai"}
			if err := jevconf.SaveProvider(ctx, db, p); err != nil {
				t.Fatal(err)
			}
			if err := jevconf.SetDefaultProvider(ctx, db, "own"); err != nil {
				t.Fatal(err)
			}
			c.Prepare(t)
			cctx := c.Ctx(t, db, ctx, "default", "ai.choose")
			cl, err := jevconf.NewClient(cctx, db, "default", "", "", jevconf.NodeSurface("ai.choose"))
			if c.Refused {
				if !errors.Is(err, secrets.ErrResolveRefused) || cl != nil {
					t.Fatalf("a refused step got the provider's key: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := cl.Ask(cctx, map[string]any{}, map[string]jev.Question{"q": choiceOf("x", "y")}); err != nil {
					t.Fatal(err)
				}
				if !own.saw("Bearer "+testKey) || !own.saw("/v1/chat/completions") {
					t.Error("the provider's key did not reach its own host")
				}
			}
			if typesafe.Calls()+envHost.Calls() != 0 {
				t.Errorf("something went to the TypeSafe host (%d) or the environment's host (%d)", typesafe.Calls(), envHost.Calls())
			}
			if own.saw(typesafeKey) {
				t.Error("the TypeSafe key reached the provider")
			}
			if c.Refused && own.calls() != 0 {
				t.Errorf("a refused step sent %d request(s)", own.calls())
			}
			c.Audited(t, db, "default", "openai")
		})
	}
}

// recorder answers every chat completion with A and keeps each request's
// path and headers.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.seen = append(r.seen, req.URL.Path+"\n"+fmt.Sprint(req.Header))
	r.mu.Unlock()
	_ = json.NewEncoder(w).Encode(logprobReply(alt{"A", 0.9}, alt{"B", 0.1}))
}

func (r *recorder) saw(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.seen {
		if strings.Contains(x, s) {
			return true
		}
	}
	return false
}

func (r *recorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}
