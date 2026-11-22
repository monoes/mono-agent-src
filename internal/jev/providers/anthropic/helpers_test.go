package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
const testKey = "fake-anthropic-key-for-tests"

// seen is one request the fake got.
type seen struct {
	method, path string
	header       http.Header
	body         messagesRequest
	raw          string
}

// fake is a scripted Messages API: reply decides every answer from the
// request and the number of requests before it.
type fake struct {
	*httptest.Server
	mu    sync.Mutex
	got   []seen
	reply func(n int, r messagesRequest) (status int, body string)
}

func newFake(t *testing.T, reply func(n int, r messagesRequest) (int, string)) *fake {
	t.Helper()
	f := &fake{reply: reply}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body messagesRequest
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		n := len(f.got)
		f.got = append(f.got, seen{r.Method, r.URL.RequestURI(), r.Header.Clone(), body, string(raw)})
		f.mu.Unlock()
		status, out := f.reply(n, body)
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

// toolReply is a 200 whose tool call answers answer.
func toolReply(answer string) (int, string) {
	return http.StatusOK, fmt.Sprintf(`{"model":"claude-test","content":[{"type":"tool_use","id":"t1","name":"answer","input":{"answer":%q}}],`+
		`"usage":{"input_tokens":10,"output_tokens":2}}`, answer)
}

// sequence answers the i-th request with answers[i] ("" = a reply without a tool call).
func sequence(answers ...string) func(int, messagesRequest) (int, string) {
	return func(n int, _ messagesRequest) (int, string) {
		if n >= len(answers) {
			return http.StatusInternalServerError, `{"error":"script exhausted"}`
		}
		if answers[n] == "" {
			return http.StatusOK, `{"content":[{"type":"text","text":"I think yes"}],"usage":{"input_tokens":10,"output_tokens":2}}`
		}
		return toolReply(answers[n])
	}
}

// enum is the tool's enum in r.
func enum(r messagesRequest) []string {
	if len(r.Tools) != 1 {
		return nil
	}
	props, _ := r.Tools[0].InputSchema["properties"].(map[string]any)
	ans, _ := props["answer"].(map[string]any)
	list, _ := ans["enum"].([]any)
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = fmt.Sprint(v)
	}
	return out
}

// providerClient saves an anthropic provider at base (with key in the
// machine vault when non-empty) and builds its client through jevconf.
func providerClient(t *testing.T, base, key, model string) *jev.Client {
	t.Helper()
	ctx := context.Background()
	db := testdb.Open(t).DB
	secret := ""
	if key != "" {
		secret = "anthropic-key"
		if _, err := secrets.Add(ctx, db, jevconf.VaultScope, "secret", secret, map[string]string{"secret": key}, "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := jevconf.SaveProvider(ctx, db, jevconf.Provider{Name: "claude", Kind: Kind, BaseURL: base, Model: model, SecretName: secret}); err != nil {
		t.Fatal(err)
	}
	c, err := jevconf.NewClientFor(ctx, db, "default", "claude", "", "", jevconf.CLI)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func noul(instructions string) jev.Question {
	return jev.Question{Type: jev.TypeNoul, Instructions: instructions}
}

func choice(ids ...string) jev.Question {
	c := map[string]any{}
	for _, id := range ids {
		c[id] = "the " + id + " option"
	}
	return jev.Question{Type: jev.TypeChoice, Instructions: "Pick one.", Criteria: c}
}
