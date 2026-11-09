//go:build realrunner

package monomind

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// OPT-IN smoke test of the one thing this repository cannot show by itself: what the external `monomind agent exec` does with
// --budget-usd. The pursuit's per-wake cap X and the judge's per-call cap are REQUESTS to that runner; the host reads the cost
// the runner reports afterwards. This test starts one real turn under a tiny cap, with a prompt that would cost far more, and
// RECORDS what happened (it asserts nothing about the runner, only that the turn ran):
//
//	did the turn stop before it finished, and with what stop reason?
//	what cost did the runner report, and was it above the cap (an overshoot)?
//	did any usage event report a cost, or did the turn report none (the host then charges the whole cap)?
//
// It never runs by default: it needs the build tag and a pin, and it spends real money on a real account.
//
//	HOME=<a fresh scratch dir, never your real home> \
//	MONOAGENT_SMOKE_PIN=<runtime/model>   a model that costs a little, so that the cap can bind (a :free model reports $0)
//	MONOAGENT_SMOKE_CAP_USD=0.01          optional, default 0.01
//	go test -tags realrunner -count=1 -v -timeout 300s -run TestRealRunnerRecordsWhatItDoesUnderATinyCap ./internal/monomind
//
// The runner needs its provider's credential in the environment (for an OpenRouter-backed runtime OPENROUTER_API_KEY).
func TestRealRunnerRecordsWhatItDoesUnderATinyCap(t *testing.T) {
	pin := strings.TrimSpace(os.Getenv("MONOAGENT_SMOKE_PIN"))
	if pin == "" {
		t.Skip("MONOAGENT_SMOKE_PIN (runtime/model) is not set: this smoke test spends real money and is opt-in")
	}
	runtime, model, ok := strings.Cut(pin, "/")
	if !ok || runtime == "" || model == "" {
		t.Fatalf("MONOAGENT_SMOKE_PIN %q is not runtime/model", pin)
	}
	capUSD := 0.01
	if raw := os.Getenv("MONOAGENT_SMOKE_CAP_USD"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || !(v > 0) || v > 1 {
			t.Fatalf("MONOAGENT_SMOKE_CAP_USD %q must be a number above 0 and at most 1", raw)
		}
		capUSD = v
	}
	cwd := t.TempDir()
	events := map[string]int{}
	var costs []float64
	assistantBytes := 0
	start := time.Now()
	res, err := Exec(context.Background(), ExecOptions{
		Runtime: runtime, Model: model, Cwd: cwd, BudgetUSD: capUSD, MaxTurns: 1, Timeout: 4 * time.Minute,
		Prompt: "Write an extremely long, detailed essay of at least 4000 words about the history of the bicycle. Do not stop early.",
	}, func(ev Event) {
		events[ev.Type]++
		if ev.Type == "assistant" {
			assistantBytes += len(ev.Text)
		}
		if ev.HasCostUSD {
			costs = append(costs, ev.CostUSD)
		}
	})
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil && res == nil {
		t.Fatalf("the turn did not run at all: %v", err)
	}
	t.Logf("RECORD pin=%s cap=$%.4f elapsed=%s events=%v assistant_bytes=%d usage_costs=%v", pin, capUSD, elapsed, events, assistantBytes, costs)
	if res == nil {
		t.Logf("RECORD no result (err=%v)", err)
		return
	}
	t.Logf("RECORD exit=%d stop_reason=%q saw_done=%v has_cost=%v cost=$%.6f err=%v result_bytes=%d", res.ExitCode, res.StopReason, res.SawDone, res.HasCostUSD, res.CostUSD, res.Err, len(res.ResultText))
	switch {
	case !res.HasCostUSD:
		t.Logf("VERDICT the runner reported NO cost: the host would charge the whole cap ($%.4f)", capUSD)
	case res.CostUSD > capUSD:
		t.Logf("VERDICT OVERSHOOT: reported $%.6f against a cap of $%.4f (%.1fx)", res.CostUSD, capUSD, res.CostUSD/capUSD)
	default:
		t.Logf("VERDICT within the cap: reported $%.6f against $%.4f (stop_reason %q)", res.CostUSD, capUSD, res.StopReason)
	}
}
