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
// --budget-usd. The pursuit's per-wake cap X and the judge's per-call cap are REQUESTS to that runner (it may overshoot by one
// model call); the host reads the cost the runner reports afterwards. This test starts one real turn under a tiny cap, with a
// prompt that would cost far more, and RECORDS what happened (it asserts nothing about the runner, only that the turn ran):
//
//	did the turn stop before it finished, and with what stop reason?
//	what cost did the runner report, and was it above the cap (an overshoot)?
//	did the turn report a cost at all (if not, the host charges the whole cap)?
//
// It works with any installed runtime and uses THAT RUNTIME'S OWN EXISTING LOGIN on this machine (claude, codex, opencode,
// hermes, ...): this code never reads, stores or passes a key. It opens no monoagent database (the turn runs in a fresh
// temporary folder), so it touches nothing of ~/.monoagent. It never runs by default and never in CI: it needs the build tag
// and the variables below, and it spends real money on the login's account.
//
//	MONOAGENT_SMOKE_CONFIRM=spend-real-money
//	MONOAGENT_SMOKE_RUNTIME=claude|codex|opencode|hermes|...   the runtime to drive (its own login is used)
//	MONOAGENT_SMOKE_MODEL=<model>                              optional; the runtime's default when empty
//	MONOAGENT_SMOKE_CAP_USD=0.05                               optional; default 0.05, at most 0.10
//	go test -tags realrunner -count=1 -v -timeout 300s -run TestRealRunnerRecordsWhatItDoesUnderATinyCap ./internal/monomind
func TestRealRunnerRecordsWhatItDoesUnderATinyCap(t *testing.T) {
	runtime := strings.TrimSpace(os.Getenv("MONOAGENT_SMOKE_RUNTIME"))
	if runtime == "" || os.Getenv("MONOAGENT_SMOKE_CONFIRM") != "spend-real-money" {
		t.Skip("MONOAGENT_SMOKE_RUNTIME and MONOAGENT_SMOKE_CONFIRM=spend-real-money are not both set: this smoke test spends real money and is opt-in")
	}
	model := strings.TrimSpace(os.Getenv("MONOAGENT_SMOKE_MODEL"))
	capUSD := 0.05
	if raw := os.Getenv("MONOAGENT_SMOKE_CAP_USD"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || !(v > 0) || v > 0.10 {
			t.Fatalf("MONOAGENT_SMOKE_CAP_USD %q must be a number above 0 and at most 0.10", raw)
		}
		capUSD = v
	}
	t.Logf("LOGIN this run relies on the %q runtime's own login on this machine; no key is read or passed by this test. The cap $%.4f is a REQUEST to the runner and may be overshot by one call.", runtime, capUSD)
	pin := runtime + "/" + model
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
