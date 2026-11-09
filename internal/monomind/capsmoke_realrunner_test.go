//go:build realrunner

package monomind

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/testhome"
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
// hermes, ...): OUR code never reads, stores or passes a key. But the runner inherits the ambient environment (monomind's
// FilteredEnviron strips only CLAUDE_CODE_* and MONOMIND_*), so the harness REMOVES every provider key variable (smokeProviderKeyVars: OPENROUTER, ANTHROPIC, OPENAI, GEMINI, GOOGLE, MISTRAL,
// GROQ, XAI, AZURE_OPENAI, AWS key/secret/session/bedrock-bearer, CLAUDE_CODE_USE_BEDROCK/VERTEX) from its own environment before the
// runner starts, so that only the runtime's own login can be used, and prints the NAMES removed (never a value). Everything the runner
// says that is logged goes through redactSecrets (key, bearer, JWT, long hex/base64 shapes). It opens no monoagent database (the turn runs in a fresh
// temporary folder), so it touches nothing of ~/.monoagent. It never runs by default and never in CI: it needs the build tag
// and the variables below, and it spends real money on the login's account.
//
//	MONOAGENT_SMOKE_CONFIRM=spend-real-money
//	MONOAGENT_SMOKE_RUNTIME=claude|codex|opencode|hermes|...   the runtime to drive (its own login is used)
//	MONOAGENT_SMOKE_MODEL=<model>                              optional; the runtime's default when empty
//	MONOAGENT_SMOKE_CAP_USD=0.05                               optional; default 0.05, at most 0.10
//	MONOAGENT_SMOKE_RUNTIME_HOME=scratch|real                  default scratch (see below)
//	MONOAGENT_SMOKE_RUNTIME_CONFIG_DIR=<dir>                   optional with real: the runtime's config dir
//	MONOAGENT_SMOKE_RUNTIME_CONFIG_VAR=CLAUDE_CONFIG_DIR       optional: the variable that names it (default for claude)
//	MONOAGENT_SMOKE_ALLOW_INSTALL=1                            optional: let monomind download its SDK (default: refused)
//
// WHICH LOGIN: test binaries of this package run under internal/testhome, which points HOME at a fresh empty folder, so by
// default (scratch) the runtime cannot find its own login and answers "Not logged in" (first attempt: $0 spent, inconclusive).
// With MONOAGENT_SMOKE_RUNTIME_HOME=real only the CHILD process gets the user's real HOME (and the config dir above, if any),
// so the runtime finds its existing login; the test prints that path loudly, and the child process can read everything in it.
// Monoagent state stays scratch either way, and OUR code reads and passes no key or token.
//
// WHICH MONOMIND: the test finds monomind like the product, except that under test only a monomind inside the temp dir, GOTMPDIR
// or the test HOME counts (UnderTestEnv); set MONOMIND_BIN to the monomind to use (it is always honoured; without it the first
// attempt ended in "monomind not found"). For the claude runtime monomind uses an installed Claude Code only if the binary is
// root-owned and named claude or versions/<x.y.z>; a mise shim or a user-owned install (uid 1000) is not run ("so org roles
// may write it"), and monomind then wants to download @anthropic-ai/claude-agent-sdk (about 300 MB). This harness sets
// MONOMIND_NO_AUTO_INSTALL=1 for the child so nothing is downloaded; set MONOMIND_CLAUDE_PATH to a Claude Code binary monomind
// accepts, or MONOAGENT_SMOKE_ALLOW_INSTALL=1 to allow the download. (FilteredEnviron drops ambient MONOMIND_* except
// MONOMIND_CLAUDE_PATH, so these are passed to the child explicitly.)
//
// VERDICT: only a completed turn (exit 0, no error result, a done event, some assistant output) gets a verdict; anything else
// is INCONCLUSIVE and fails the test.
//
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
	// Only the runtime's own login may be used: every provider key variable is removed from this process (the runner inherits
	// the environment) for the duration of the test, and the NAMES removed are printed, never a value.
	removed, old := scrubProviderEnv(os.LookupEnv, os.Unsetenv)
	t.Cleanup(func() {
		for k, v := range old {
			os.Setenv(k, v)
		}
	})
	if len(removed) > 0 {
		t.Logf("WARNING these provider key variables were set in this environment and are REMOVED for the runner, so that only the runtime's own login is used: %s", strings.Join(removed, ", "))
	}
	t.Logf("LOGIN this run relies on the %q runtime's own login on this machine; no key is read or passed by OUR code (provider key variables are removed from the child's environment). The cap $%.4f is a REQUEST to the runner and may be overshot by one call.", runtime, capUSD)
	pin := runtime + "/" + model
	opts := ExecOptions{Env: map[string]string{}}
	if os.Getenv("MONOAGENT_SMOKE_ALLOW_INSTALL") != "1" {
		opts.Env["MONOMIND_NO_AUTO_INSTALL"] = "1" // never download an SDK during a smoke
	}
	switch mode := strings.TrimSpace(os.Getenv("MONOAGENT_SMOKE_RUNTIME_HOME")); mode {
	case "", "scratch":
		t.Logf("RUNTIME HOME scratch: the runtime sees the test's empty HOME and will most likely say it is not logged in")
	case "real":
		rh := testhome.RealHome()
		if rh == "" {
			t.Fatal("the user's real home is not known")
		}
		opts.Env["HOME"] = rh
		t.Logf("RUNTIME HOME REAL: the child process (the runtime) gets HOME=%s and can read everything in it", rh)
		if dir := strings.TrimSpace(os.Getenv("MONOAGENT_SMOKE_RUNTIME_CONFIG_DIR")); dir != "" {
			name := strings.TrimSpace(os.Getenv("MONOAGENT_SMOKE_RUNTIME_CONFIG_VAR"))
			if name == "" {
				name = "CLAUDE_CONFIG_DIR"
			}
			opts.Env[name] = dir
			t.Logf("RUNTIME CONFIG DIR REAL: %s=%s is passed to the child process, which can read everything in it", name, dir)
		}
	default:
		t.Fatalf("MONOAGENT_SMOKE_RUNTIME_HOME %q is not scratch or real", mode)
	}
	cwd := t.TempDir()
	events := map[string]int{}
	var costs []float64
	assistantBytes := 0
	start := time.Now()
	opts.Runtime, opts.Model, opts.Cwd, opts.BudgetUSD, opts.MaxTurns, opts.Timeout = runtime, model, cwd, capUSD, 1, 4*time.Minute
	opts.Prompt = "Write an extremely long, detailed essay of at least 4000 words about the history of the bicycle. Do not stop early."
	res, err := Exec(context.Background(), opts, func(ev Event) {
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
		t.Fatalf("the turn did not run at all: %s", redactSecrets(fmt.Sprint(err)))
	}
	t.Logf("RECORD pin=%s cap=$%.4f elapsed=%s events=%v assistant_bytes=%d usage_costs=%v", pin, capUSD, elapsed, events, assistantBytes, costs)
	if res == nil {
		t.Logf("RECORD no result (err=%s)", redactSecrets(fmt.Sprint(err)))
		return
	}
	t.Logf("RECORD exit=%d stop_reason=%q saw_done=%v has_cost=%v cost=$%.6f err=%v result_bytes=%d", res.ExitCode, res.StopReason, res.SawDone, res.HasCostUSD, res.CostUSD, redactSecrets(fmt.Sprint(res.Err)), len(res.ResultText))
	// A verdict needs a real, completed turn: an error result (for example "Not logged in") reports cost 0 and says nothing about the cap.
	if why := incompleteTurn(res, err, assistantBytes); why != "" {
		t.Errorf("VERDICT INCONCLUSIVE: the turn did not complete (%s); nothing is learned about the cap", why)
		return
	}
	switch {
	case !res.HasCostUSD:
		t.Logf("VERDICT the runner reported NO cost: the host would charge the whole cap ($%.4f)", capUSD)
	case res.CostUSD > capUSD:
		t.Logf("VERDICT OVERSHOOT: reported $%.6f against a cap of $%.4f (%.1fx)", res.CostUSD, capUSD, res.CostUSD/capUSD)
	default:
		t.Logf("VERDICT within the cap: reported $%.6f against $%.4f (stop_reason %q)", res.CostUSD, capUSD, res.StopReason)
	}
}

// incompleteTurn says why a turn is not a completed one, or "".
func incompleteTurn(res *TurnResult, err error, assistantBytes int) string {
	switch {
	case res == nil:
		return redactSecrets(fmt.Sprintf("no result, err=%v", err))
	case err != nil:
		return redactSecrets(fmt.Sprintf("exec error: %v", err))
	case res.Err != nil:
		return redactSecrets(fmt.Sprintf("the runner returned an error result: %v", res.Err))
	case res.ExitCode != 0:
		return fmt.Sprintf("exit code %d", res.ExitCode)
	case !res.SawDone:
		return "no done event"
	case assistantBytes == 0:
		return "no assistant output"
	}
	return ""
}
