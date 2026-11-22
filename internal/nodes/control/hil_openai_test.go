package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/providers/openaicompat"
	"github.com/monoes/mono-agent/internal/workflow"
)

var (
	hilReply = regexp.MustCompile(`Reply with exactly one of: (.*)\.$`)
	hilLine  = regexp.MustCompile(`(?m)^(\S+)\) (.*)$`)
)

// hilOpenAI is a loopback OpenAI-compatible server that approves every item
// at low risk: with logprobs it puts 0.95 on that label; with logprobs false
// it replies with the label as text only, so the adapter samples.
func hilOpenAI(t *testing.T, logprobs bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		m := hilReply.FindStringSubmatch(user)
		if m == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		labels := strings.Split(m[1], ", ")
		pick := labels[0]
		for _, l := range hilLine.FindAllStringSubmatch(user, -1) {
			if strings.HasPrefix(l[2], "approve") || strings.HasPrefix(l[2], "low") {
				pick = l[1]
			}
		}
		choice := map[string]any{"message": map[string]any{"role": "assistant", "content": pick}}
		if logprobs {
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "m", "choices": []any{choice},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// R2-A × R2-B on the real openai adapter: log-probabilities of 0.95 approve
// at approve_above 0.9; a unanimous sample (add-one smoothed over the three
// decisions: 6/8 = 0.75) never does, and every item waits for a person with
// the suggestion and probability_kind "sampled".
func TestHILAuto_SampledProviderNeverApprovesAtNinety(t *testing.T) {
	for _, c := range []struct {
		logprobs bool
		status   string
		kind     string
	}{{true, "approved", "logprob"}, {false, "pending", "sampled"}} {
		t.Run(c.kind, func(t *testing.T) {
			db := newAutoTestDB(t)
			ts := jevByCaption(t, nil)
			enable(t, db)
			srv := hilOpenAI(t, c.logprobs)
			if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{Name: "local", Kind: openaicompat.Kind, BaseURL: srv.URL, Model: "m"}); err != nil {
				t.Fatal(err)
			}
			if err := jevconf.SetDefaultProvider(context.Background(), db, "local"); err != nil {
				t.Fatal(err)
			}
			_, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), autoConfig(map[string]interface{}{"policy": "ok", "approve_above": 0.9}))
			if c.logprobs && err != nil || !c.logprobs && !errors.Is(err, workflow.ErrNodePaused) {
				t.Fatalf("err = %v", err)
			}
			if ts.Calls() != 0 {
				t.Fatalf("TypeSafe got %d calls", ts.Calls())
			}
			for _, r := range rowsOf(t, db) {
				cfg := cfgOf(t, r)
				sug, _ := cfg["suggestion"].(map[string]interface{})
				p, _ := sug["p"].(float64)
				if r.Status != c.status || cfg["probability_kind"] != c.kind || sug["choice"] != "approve" || sug["risk"] != "low" {
					t.Fatalf("row = %s %v", r.Status, cfg)
				}
				if want := map[bool]float64{true: 0.95, false: 0.75}[c.logprobs]; math.Abs(p-want) > 1e-9 {
					t.Fatalf("suggestion p = %v, want %v", p, want)
				}
			}
		})
	}
}
