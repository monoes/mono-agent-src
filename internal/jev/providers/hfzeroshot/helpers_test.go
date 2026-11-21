package hfzeroshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

// testKey is a placeholder credential the fakes look for.
const testKey = "fake-hf-token-for-tests-0123"

type seen struct {
	method, path string
	header       http.Header
	body         classifyRequest
}

// fake is a scripted zero-shot endpoint: reply decides the answer from the
// labels sent.
type fake struct {
	*httptest.Server
	mu    sync.Mutex
	got   []seen
	reply func(r classifyRequest) (status int, body string)
}

func newFake(t *testing.T, reply func(r classifyRequest) (int, string)) *fake {
	t.Helper()
	f := &fake{reply: reply}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body classifyRequest
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.got = append(f.got, seen{r.Method, r.URL.RequestURI(), r.Header.Clone(), body})
		f.mu.Unlock()
		status, out := f.reply(body)
		w.Header().Set("Content-Type", "application/json")
		if status >= 300 {
			w.Header().Set("Retry-After", "0") // retries in tests do not sleep
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) requests() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.got...)
}

// pairs answers [{"label","score"},…] giving the labels the scores in order.
func pairs(scores ...float64) func(classifyRequest) (int, string) {
	return func(r classifyRequest) (int, string) {
		out := make([]string, 0, len(r.Parameters.CandidateLabels))
		for i, l := range r.Parameters.CandidateLabels {
			s := 0.0
			if i < len(scores) {
				s = scores[i]
			}
			lb, _ := json.Marshal(l)
			out = append(out, fmt.Sprintf(`{"label":%s,"score":%v}`, lb, s))
		}
		return http.StatusOK, "[" + strings.Join(out, ",") + "]"
	}
}

// providerClient saves an hf-zeroshot provider at url (with key in the
// machine vault when non-empty) and builds its client through jevconf.
func providerClient(t *testing.T, url, key string) *jev.Client {
	t.Helper()
	ctx := context.Background()
	db := testdb.Open(t).DB
	secret := ""
	if key != "" {
		secret = "hf-token"
		if _, err := secrets.Add(ctx, db, jevconf.VaultScope, "secret", secret, map[string]string{"secret": key}, "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := jevconf.SaveProvider(ctx, db, jevconf.Provider{Name: "hf", Kind: Kind, BaseURL: url, Model: "bart-mnli", SecretName: secret}); err != nil {
		t.Fatal(err)
	}
	c, err := jevconf.NewClientFor(ctx, db, "default", "hf", "", "", jevconf.CLI)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
