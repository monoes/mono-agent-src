package monomind

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This verifies the client boundary, not whether monomind's pause-file
// resume command performs R6. The recorded checkpoint-resume wording must
// survive either output stream if a monomind returns it here.
func TestIssue378ResumeRefusalStreams(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			argv := filepath.Join(t.TempDir(), "argv")
			body := "printf '%s\\n' \"$@\" > '" + argv + "'\n" + sh(r6ResumeRefusal)
			if stream == "stderr" {
				body = "printf '%s\\n' \"$@\" > '" + argv + "'\n{\n" + sh(r6ResumeRefusal) + "} >&2\n"
			}
			root := fakeOrgMonomind(t, body+"echo '[ERROR] org start failed' >&2\nexit 1")
			_, err := OrgResume(context.Background(), root, "sec")
			ref, ok := AsStartRefusal(err)
			if !ok || ref.Kind != RefusalPreflight || ref.Org != "sec" || !strings.Contains(ref.Text, `cannot resume on this host`) || !strings.Contains(err.Error(), r6ResumeRefusal) || ref.Hint == "" {
				t.Fatalf("resume refusal = %+v, %v", ref, err)
			}
			got, err := os.ReadFile(argv)
			if err != nil || string(got) != "org\nresume\nsec\n" {
				t.Fatalf("argv = %q, %v", got, err)
			}
		})
	}
}

// Exercise both the native sign-check and the local verification fallback
// using a real unsigned definition. Neither may reach the run subcommand.
func TestIssue378UnsignedEstimateNeverRuns(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "local fallback"
		if native {
			name = "native sign check"
		}
		t.Run(name, func(t *testing.T) {
			calls := filepath.Join(t.TempDir(), "calls")
			caps := `"agent-exec","agent-scan","org-json-v1"`
			if native {
				caps += `,"org-sign-check"`
			}
			root := fakeOrgMonomind(t, "exit 99")
			bin := os.Getenv(EnvOverride)
			script := "#!/bin/sh\n" +
				`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"2.24.1","min_caller":"1.0.0","capabilities":[` + caps + `]}'; exit 0; fi` + "\n" +
				"echo \"$@\" >> '" + calls + "'\n" +
				`if [ "$1 $2" = "org validate" ]; then echo ok; exit 0; fi` + "\n" +
				`if [ "$1 $2" = "org sign" ]; then echo '{"orgs":[{"org":"sec","state":"unsigned","message":"no operator signature"}]}'; exit 1; fi` + "\n" +
				"echo 'unexpected command' >&2\nexit 99\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, ".monomind", "orgs")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			def := []byte(`{"name":"sec","goal":"test","roles":[{"id":"boss","type":"boss","reports_to":null}]}`)
			path := filepath.Join(dir, "sec.json")
			if err := os.WriteFile(path, def, 0o600); err != nil {
				t.Fatal(err)
			}
			est, err := OrgCostEstimate(context.Background(), root, "sec")
			if est != nil || !errors.Is(err, ErrEstimateUnavailable) || !strings.Contains(err.Error(), "monoagentcli org sign sec") {
				t.Fatalf("unsigned estimate = %+v, %v", est, err)
			}
			got, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			want := "org validate sec\n"
			if native {
				want += "org sign sec --check --format json\n"
			}
			if string(got) != want {
				t.Fatalf("commands = %q, want %q; run must never be invoked", got, want)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(def) {
				t.Fatalf("definition changed: %q, %v", after, err)
			}
		})
	}
}

// Costs are incremental usage events, partitioned by role. Audit snapshots
// annotate warnings/closure and must not count the same spend a second time.
func TestIssue378SectionSpendFromUsage(t *testing.T) {
	def := []byte(`{"roles":[{"id":"boss","type":"boss","reports_to":null},{"id":"writer","reports_to":"boss"},{"id":"checker","reports_to":"boss"}],"sections":{"drafting":{"lead":"writer","members":["writer"],"budget":{"usd":1}},"review":{"members":["checker"],"budget":{"usd":1}}},"run_config":{"budget_usd":3}}`)
	events := SplitEventLines([]byte(`{"type":"usage","from":"writer","data":{"tokens":10,"cost_usd":0.2}}
{"type":"usage","from":"writer","data":{"tokens":20,"cost_usd":0.65}}
{"type":"usage","from":"checker","data":{"tokens":30,"cost_usd":0}}
{"type":"usage","from":"boss","data":{"tokens":40,"cost_usd":0.1}}
{"type":"usage","from":"removed","data":{"tokens":50,"cost_usd":0.05}}
{"type":"audit","ts":123,"reason":"section-budget-warning","data":{"scope":"section:drafting","spentUsd":0.85,"allocationUsd":1}}
`))
	rep, err := SectionBudgets(def, events)
	if err != nil {
		t.Fatal(err)
	}
	draft := scopeByName(t, rep, "drafting")
	review := scopeByName(t, rep, "review")
	if !near(draft.SpentUSD, 0.85) || draft.State != BudgetWarn || draft.WarnedAt != 123 || len(draft.Roles) != 1 || draft.Roles[0].Tokens != 30 {
		t.Fatalf("drafting (lead/member counted once) = %+v", draft)
	}
	if review.CostUnknown || review.SpentUSD != 0 || !rep.CostComplete {
		t.Fatalf("explicit zero is known: %+v", rep)
	}
	if rep.Reserve == nil || !near(rep.Reserve.SpentUSD, 0.1) || !near(rep.UnattributedUSD, 0.05) || !near(rep.Total.SpentUSD, 1) || rep.TotalTokens != 150 {
		t.Fatalf("partition totals = %+v", rep)
	}
	if sum := draft.SpentUSD + review.SpentUSD + rep.Reserve.SpentUSD + rep.UnattributedUSD; !near(sum, rep.Total.SpentUSD) {
		t.Fatalf("partition sum %g != total %g", sum, rep.Total.SpentUSD)
	}
	events = append(events, []byte(`{"type":"usage","from":"checker","data":{"tokens":7,"cost_usd":null}}`))
	rep, err = SectionBudgets(def, events)
	if err != nil {
		t.Fatal(err)
	}
	if rep.CostComplete || !rep.Total.CostUnknown || !scopeByName(t, rep, "review").CostUnknown || scopeByName(t, rep, "drafting").CostUnknown || !near(rep.Total.SpentUSD, 1) || rep.TotalTokens != 157 {
		t.Fatalf("unknown cost must preserve known totals and its scope: %+v", rep)
	}
}
