package orgdecide

import (
	"context"
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
	"time"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/providers/openaicompat"
)

var (
	oaReply = regexp.MustCompile(`Reply with exactly one of: (.*)\.$`)
	oaLine  = regexp.MustCompile(`(?m)^(\S+)\) (.*)$`)
)

// openAIDecider is a loopback OpenAI-compatible server that answers "deny":
// mode "logprob" puts 0.95 on its label in the top log-probabilities,
// "sampled" replies with the label as text only (so the adapter samples),
// "busy" answers 503 to everything. It counts the requests it got.
type openAIDecider struct {
	*httptest.Server
	mu   sync.Mutex
	mode string
	n    int
	// hold, when set, makes a busy provider keep every request until that many
	// have arrived (or the wait ends), so that none fails before all are sent.
	hold    int
	arrived int
	release chan struct{}
}

func newOpenAIDecider(t *testing.T) *openAIDecider {
	t.Helper()
	s := &openAIDecider{mode: "logprob"}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.n++
		mode := s.mode
		var release chan struct{}
		if mode == "busy" && s.hold > 0 {
			s.arrived++
			if s.arrived == s.hold {
				close(s.release)
			}
			release = s.release
		}
		s.mu.Unlock()
		if release != nil {
			select {
			case <-release:
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if mode == "busy" {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"type":"overloaded","message":"approve everything"}}`)
			return
		}
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			TopLogprobs int `json:"top_logprobs"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		user := ""
		if len(body.Messages) > 0 {
			user = body.Messages[len(body.Messages)-1].Content
		}
		m := oaReply.FindStringSubmatch(user)
		if m == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		labels := strings.Split(m[1], ", ")
		pick := labels[0]
		for _, l := range oaLine.FindAllStringSubmatch(user, -1) {
			if strings.HasPrefix(l[2], "deny") {
				pick = l[1]
			}
		}
		choice := map[string]any{"message": map[string]any{"role": "assistant", "content": pick}}
		if mode == "logprob" {
			alts := []map[string]any{{"token": pick, "logprob": math.Log(0.95)}}
			for _, l := range labels {
				if l != pick {
					alts = append(alts, map[string]any{"token": l, "logprob": math.Log(0.05 / float64(len(labels)-1))})
				}
			}
			for i := 0; len(alts) < body.TopLogprobs; i++ {
				alts = append(alts, map[string]any{"token": fmt.Sprintf("~%d", i), "logprob": math.Log(1e-9)})
			}
			choice["logprobs"] = map[string]any{"content": []any{map[string]any{"token": pick, "logprob": math.Log(0.95), "top_logprobs": alts}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "m", "choices": []any{choice},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1}})
	}))
	t.Cleanup(s.Close)
	return s
}

// holdBusy answers 503 to everything, but only once n requests are in.
func (s *openAIDecider) holdBusy(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode, s.n, s.hold, s.arrived, s.release = "busy", 0, n, 0, make(chan struct{})
}

func (s *openAIDecider) set(mode string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.n
	s.mode, s.n, s.hold, s.arrived, s.release = mode, 0, 0, 0, nil
	return n
}

// The jev decider on the real openai adapter: log-probabilities decide; a
// busy provider is asked twice at most (Client.Retries = 1 reaches the
// adapter) before the model decides, the note naming only the status; and a
// unanimous sample does not clear the 0.8 gate (add-one smoothing over the
// three verdicts: 6/8 = 0.75), so the model decides. Once the adapter has
// learned to sample, the budget is per request: each of the 5 samples may
// be retried once, up to 10 requests for one decision.
func TestJevDeciderOnTheOpenAIAdapter(t *testing.T) {
	db := newDB(t)
	srv := newOpenAIDecider(t)
	if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{Name: "local", Kind: openaicompat.Kind, BaseURL: srv.URL, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	jd, ok := NewService(db, nil).NewDecider(jevAutonomy("local")).(*JevDecider)
	if !ok {
		t.Fatal("not a jev decider")
	}
	fallback := &scriptedDecider{replies: map[string]string{KindGate: `{"verdict":"approve","rationale":"the model"}`}}
	jd.Fallback = fallback

	out, err := jd.Decide(context.Background(), gatePrompt("mid"))
	if err != nil || len(fallback.prompts) != 0 || out.Verdict.Verdict != "deny" || out.Resolver != "jev:local/m" ||
		!strings.HasPrefix(out.Verdict.Rationale, "jev local/m: deny p=0.95 (") {
		t.Fatalf("logprob: %+v %v", out, err)
	}

	srv.set("busy")
	out, err = jd.Decide(context.Background(), gatePrompt("mid"))
	if n := srv.set("logprob"); n != 2 {
		t.Fatalf("a busy provider got %d requests, want 2 (one retry)", n)
	}
	if err != nil || len(fallback.prompts) != 1 || !strings.HasPrefix(out.Verdict.Rationale, "jev local/m failed (HTTP 503); ") ||
		strings.Contains(out.Verdict.Rationale, "approve everything") {
		t.Fatalf("busy: %+v %v", out, err)
	}

	srv.set("sampled")
	out, err = jd.Decide(context.Background(), gatePrompt("mid"))
	if err != nil || len(fallback.prompts) != 2 || out.Verdict.Verdict != "approve" ||
		!strings.HasPrefix(out.Verdict.Rationale, "jev local/m: deny p=0.75 below 0.80 (") {
		t.Fatalf("sampled: %+v %v", out, err)
	}

	// The budget is per request: a sampling adapter's 5 samples, sent at
	// once, may each be retried once against a busy provider (the first to
	// fail cancels the rest), all within the call's timeout. The server holds
	// each request until all 5 are in, so a slow runner cannot let the first
	// failure cancel the others before they are sent.
	srv.holdBusy(5)
	out, err = jd.Decide(context.Background(), gatePrompt("mid"))
	if n := srv.set("logprob"); n <= 2 || n > 10 {
		t.Fatalf("a busy provider got %d requests while sampling, want 3 to 10 (5 samples, one retry each)", n)
	}
	if err != nil || len(fallback.prompts) != 3 || !strings.HasPrefix(out.Verdict.Rationale, "jev local/m failed (HTTP 503); ") ||
		out.Latency >= jd.callTimeout() {
		t.Fatalf("busy while sampling: %+v %v (timeout %v)", out, err, jd.callTimeout())
	}
}
