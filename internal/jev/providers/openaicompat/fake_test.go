package openaicompat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

// seen is one request the fake received.
type seen struct {
	Method, Path string
	Header       http.Header
	Body         chatRequest
	Raw          map[string]any // the body as sent, to tell a missing field from a zero one
}

func (s seen) user() string {
	if len(s.Body.Messages) < 2 {
		return ""
	}
	return s.Body.Messages[1].Content
}

// fake is an OpenAI-compatible server whose reply is scripted per request.
type fake struct {
	*httptest.Server
	mu    sync.Mutex
	reqs  []seen
	reply func(n int, r seen) (status int, body any)
}

// newFake starts a fake answering through reply; wrap, when given, wraps
// its handler (to set headers or redirect).
func newFake(t *testing.T, reply func(n int, r seen) (int, any), wrap ...func(*fake, http.Handler) http.Handler) *fake {
	t.Helper()
	f := &fake{reply: reply}
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s := seen{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()}
		_ = json.Unmarshal(raw, &s.Body)
		_ = json.Unmarshal(raw, &s.Raw)
		f.mu.Lock()
		n := len(f.reqs)
		f.reqs = append(f.reqs, s)
		f.mu.Unlock()
		status, body := f.reply(n, s)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if str, ok := body.(string); ok {
			_, _ = io.WriteString(w, str)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	for _, w := range wrap {
		h = w(f, h)
	}
	f.Server = httptest.NewServer(h)
	t.Cleanup(f.Close)
	return f
}

func (f *fake) requests() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.reqs...)
}

// alt is one alternative of the first token, as the fake sends it.
type alt struct {
	tok string
	p   float64
}

// logprobReply is an OpenAI chat completion whose first token has alts as
// its alternatives (most probable first) and whose text is the first of them.
func logprobReply(alts ...alt) map[string]any {
	sort.SliceStable(alts, func(i, j int) bool { return alts[i].p > alts[j].p })
	top := make([]map[string]any, len(alts))
	for i, a := range alts {
		top[i] = map[string]any{"token": a.tok, "logprob": math.Log(a.p), "bytes": []byte(a.tok)}
	}
	content := ""
	if len(alts) > 0 {
		content = alts[0].tok
	}
	return map[string]any{
		"choices": []any{map[string]any{
			"message":  map[string]any{"role": "assistant", "content": content},
			"logprobs": map[string]any{"content": []any{map[string]any{"token": content, "logprob": -0.1, "top_logprobs": top}}},
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1},
	}
}

// textReply is a chat completion of text with no log-probabilities.
func textReply(text string) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text}}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 1},
	}
}

var (
	labelLine = regexp.MustCompile(`(?m)^(\S+)\) (.*)$`)
	replyLine = regexp.MustCompile(`Reply with exactly one of: (.*)\.$`)
)

// labelsOf reads the labels a request offers and each one's line.
func labelsOf(user string) (labels []string, lines map[string]string) {
	m := replyLine.FindStringSubmatch(user)
	if m == nil {
		return nil, nil
	}
	labels = strings.Split(m[1], ", ")
	lines = map[string]string{}
	for _, l := range labelLine.FindAllStringSubmatch(user, -1) {
		lines[l[1]] = l[2]
	}
	return labels, lines
}

// oracle is a model that knows the answer: it puts 0.9 on the label that is
// target, or whose line names target as a word, and spreads 0.1 over the
// other labels, sending at most maxK alternatives (0 = as many as asked).
func oracle(t *testing.T, target string, maxK int) func(int, seen) (int, any) {
	return func(_ int, r seen) (int, any) {
		labels, lines := labelsOf(r.user())
		word := regexp.MustCompile(`(^|[^\w-])` + regexp.QuoteMeta(target) + `($|[^\w-])`)
		pick := ""
		for _, l := range labels {
			if l == target || word.MatchString(lines[l]) {
				pick = l
				break
			}
		}
		if pick == "" {
			t.Errorf("the oracle found %q in no label of %q", target, r.user())
			return 500, `{"error":"lost"}`
		}
		alts := []alt{{pick, 0.9}}
		for _, l := range labels {
			if l != pick {
				alts = append(alts, alt{l, 0.1 / float64(len(labels)-1)})
			}
		}
		k := r.Body.TopLogprobs
		if maxK > 0 && k > maxK {
			k = maxK
		}
		if !r.Body.Logprobs {
			return 200, textReply(pick)
		}
		// A real server fills the k alternatives, with tokens that are no label if need be.
		for i := 0; len(alts) < k; i++ {
			alts = append(alts, alt{fmt.Sprintf("~%d", i), 1e-9})
		}
		sort.SliceStable(alts, func(i, j int) bool { return alts[i].p > alts[j].p })
		return 200, logprobReply(alts[:k]...)
	}
}

const testModel = "m1"

// newClient saves a provider of kind openai at url (with key in its machine
// vault entry, or keyless) and builds its client through jevconf, as every
// surface does.
func newClient(t *testing.T, url, key string, caps jev.Capabilities) (*jev.Client, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db := testdb.Open(t).DB
	secret := ""
	if key != "" {
		secret = "openai-test"
		if _, err := secrets.Add(ctx, db, jevconf.VaultScope, "secret", secret, map[string]string{"secret": key}, "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	p := jevconf.Provider{Name: "local", Kind: Kind, BaseURL: url, Model: testModel, SecretName: secret, Caps: caps}
	if err := jevconf.SaveProvider(ctx, db, p); err != nil {
		t.Fatal(err)
	}
	c, err := jevconf.NewClientFor(ctx, db, "default", "local", "", "", jevconf.CLI)
	if err != nil {
		t.Fatal(err)
	}
	return c, db
}

func choiceOf(ids ...string) jev.Question {
	crit := map[string]any{}
	for _, id := range ids {
		crit[id] = "Option " + id
	}
	return jev.Question{Type: jev.TypeChoice, Criteria: crit, Instructions: "Pick one."}
}

func ask(t *testing.T, c *jev.Client, q jev.Question) (*jev.Response, error) {
	t.Helper()
	return c.Ask(context.Background(), map[string]any{"ticket": "refund please"}, map[string]jev.Question{"q": q})
}
