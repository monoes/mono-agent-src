package jevcli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	_ "github.com/monoes/mono-agent/internal/jev/providers/openaicompat" // registers the kind "openai", as the binary does
)

var (
	e2eReply = regexp.MustCompile(`Reply with exactly one of: (.*)\.$`)
	e2eLine  = regexp.MustCompile(`(?m)^(\S+)\) (.*)$`)
)

// openAIServer is a loopback server speaking OpenAI's chat completions with
// top log-probabilities. It answers Yes to a yes/no question, the option
// whose line names "tech" to a choice, and the last level to a score, with
// 0.9 on that label, 0.1 over the other labels and the rest of the k
// alternatives asked filled with tokens that are no label.
func openAIServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			t.Errorf("a keyless provider sent Authorization")
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			Logprobs    bool `json:"logprobs"`
			TopLogprobs int  `json:"top_logprobs"`
		}
		raw, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" || json.Unmarshal(raw, &body) != nil || len(body.Messages) < 2 {
			http.Error(w, `{"error":{"code":"not_found"}}`, http.StatusNotFound)
			return
		}
		if body.Model != "m" {
			t.Errorf("model sent = %q, want the record's %q", body.Model, "m")
		}
		user := body.Messages[len(body.Messages)-1].Content
		m := e2eReply.FindStringSubmatch(user)
		if m == nil {
			t.Errorf("no labels in %q", user)
			http.Error(w, `{}`, http.StatusBadRequest)
			return
		}
		labels := strings.Split(m[1], ", ")
		pick := labels[len(labels)-1] // a score: the top level
		for _, l := range e2eLine.FindAllStringSubmatch(user, -1) {
			if strings.HasPrefix(l[2], "tech") {
				pick = l[1]
			}
		}
		for _, l := range labels {
			if l == "Yes" {
				pick = l
			}
		}
		type alt struct {
			Token   string  `json:"token"`
			Logprob float64 `json:"logprob"`
		}
		alts := []alt{{pick, math.Log(0.9)}}
		for _, l := range labels {
			if l != pick {
				alts = append(alts, alt{l, math.Log(0.1 / float64(len(labels)-1))})
			}
		}
		for i := 0; len(alts) < body.TopLogprobs; i++ {
			alts = append(alts, alt{fmt.Sprintf("~%d", i), math.Log(1e-9)})
		}
		if body.TopLogprobs > 0 && len(alts) > body.TopLogprobs {
			alts = alts[:body.TopLogprobs]
		}
		reply := map[string]any{
			"model": "m",
			"choices": []any{map[string]any{
				"message":  map[string]any{"role": "assistant", "content": pick},
				"logprobs": map[string]any{"content": []any{map[string]any{"token": pick, "logprob": math.Log(0.9), "top_logprobs": alts}}},
			}},
			"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 1},
		}
		if !body.Logprobs {
			t.Errorf("a logprob provider was asked without logprobs")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// The whole chain on the real adapter: `jev provider add` (keyless, on this
// computer), `probe`, `use`, then `jev ask` of each question kind through
// jevconf and the openai adapter, recorded in usage under the provider; then
// back to TypeSafe and `remove`.
func TestJevProviderEndToEndOnTheOpenAIAdapter(t *testing.T) {
	cfg := jevTestCfg(t, true)
	srv, paths := openAIServer(t)
	base := srv.URL + "/v1"

	out, errOut, err := runJev(t, cfg, "", "provider", "add", "local", "--kind", "openai", "--base-url", base, "--model", "m")
	if err != nil {
		t.Fatalf("add: %v\n%s%s", err, out, errOut)
	}
	if m := jsonMap(t, out); m["probe"] == nil || m["probe"].(map[string]any)["ok"] != true {
		t.Fatalf("add's probe = %s", out)
	}

	out, _, err = runJev(t, cfg, "", "provider", "probe", "local")
	if err != nil {
		t.Fatalf("probe: %v\n%s", err, out)
	}
	pr := jsonMap(t, out)
	caps, _ := pr["caps"].(map[string]any)
	if pr["ok"] != true || caps["probability"] != "logprob" {
		t.Fatalf("probe = %s", out)
	}

	if out, errOut, err = runJev(t, cfg, "", "provider", "use", "local", "--yes"); err != nil {
		t.Fatalf("use: %v\n%s%s", err, out, errOut)
	}

	ask := func(q string) (map[string]any, map[string]any) {
		t.Helper()
		req := `{"state":{"text":"the app crashes on login, please help"},"questions":{"q":` + q + `}}`
		out, errOut, err := runJev(t, cfg, req, "ask", "--request", "-")
		if err != nil {
			t.Fatalf("ask %s: %v\n%s%s", q, err, out, errOut)
		}
		m := jsonMap(t, out)
		if m["provider"] != "local" || m["probability"] != "logprob" || m["model"] != "m" {
			t.Fatalf("ask %s = %s", q, out)
		}
		answers, _ := m["answers"].(map[string]any)
		a, _ := answers["q"].(map[string]any)
		if a == nil {
			t.Fatalf("no answer in %s", out)
		}
		return m, a
	}
	near := func(got any, want float64) bool {
		f, ok := got.(float64)
		return ok && math.Abs(f-want) < 1e-3
	}

	_, a := ask(`{"type":"choice","criteria":{"billing":"money","tech":"bugs and crashes"}}`)
	probs, _ := a["probabilities"].(map[string]any)
	if a["choice"] != "tech" || !near(probs["tech"], 0.9) {
		t.Fatalf("choice answer = %v", a)
	}
	_, a = ask(`{"type":"noul","instructions":"Is it a bug report?"}`)
	if !near(a["noul"], 0.9) {
		t.Fatalf("noul answer = %v", a)
	}
	_, a = ask(`{"type":"score","criteria":["low","medium","high"],"instructions":"How urgent?"}`)
	probs, _ = a["probabilities"].(map[string]any)
	if !near(a["score"], 0.9*2+0.05) || !near(probs["2"], 0.9) {
		t.Fatalf("score answer = %v", a)
	}

	db := openJevDB(t, cfg)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM jev_usage WHERE surface = 'cli' AND provider = 'local'
		AND probability_kind = 'logprob' AND model = 'm' AND ok = 1`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("usage rows naming the provider = %d (%v)", n, err)
	}
	for _, p := range paths() {
		if p != "POST /v1/chat/completions" {
			t.Fatalf("the adapter sent %s", p)
		}
	}

	if out, errOut, err = runJev(t, cfg, "", "provider", "use", "typesafe", "--yes"); err != nil {
		t.Fatalf("use typesafe: %v\n%s%s", err, out, errOut)
	}
	if out, _, err = runJev(t, cfg, "", "provider", "remove", "local"); err != nil || jsonMap(t, out)["removed"] != true {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	out, _, err = runJev(t, cfg, "", "provider", "list")
	if ps, _ := jsonMap(t, out)["providers"].([]any); err != nil || len(ps) != 1 || jsonMap(t, out)["default"] != "typesafe" {
		t.Fatalf("list after remove: %v\n%s", err, out)
	}
}
