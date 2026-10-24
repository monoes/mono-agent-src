package monomind

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real `monomind org validate` (2.24.1) lines for a sections org.
const sectionsValidateSample = `t: policy.git below 'push' has no OS sandbox on these runtimes: a (kilo)
t: roles.c: runtime "kimicode" uses the generic "config-env" isolation, not probed on a host that has the CLI (kimi is not installed on the host that wrote this table)
t: roles.a: runtime "kilo" is refused for sections orgs: Kilo supports full access only, so no role can run on it
[ERROR] 1 of 1 org config(s) failed validation`

func TestParseSectionsRuntimeFindings(t *testing.T) {
	got := ParseSectionsRuntimeFindings(sectionsValidateSample)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if k := got["kilo"]; k.Status != SectionsRuntimeRefused || k.Reason != "Kilo supports full access only, so no role can run on it" {
		t.Errorf("kilo = %+v", k)
	}
	if k := got["kimicode"]; k.Status != SectionsRuntimeUnverified || !strings.HasPrefix(k.Reason, `runtime "kimicode" uses the generic`) {
		t.Errorf("kimicode = %+v", k)
	}
}

// The fallback names exactly the runtimes monomind 2.24.1 refuses or leaves unverified.
func TestSectionsFallbackPinned(t *testing.T) {
	refused, unverified := map[string]bool{}, map[string]bool{}
	for _, r := range sectionsFallback {
		if r.Status == SectionsRuntimeRefused {
			refused[r.Runtime] = true
		} else {
			unverified[r.Runtime] = true
		}
	}
	for _, id := range []string{"kilo"} {
		if !refused[id] {
			t.Errorf("%s not refused", id)
		}
	}
	for _, id := range []string{"kimicode", "qwen", "qwen-rpc", "dsh", "aider", "cline", "vercel"} {
		if !unverified[id] {
			t.Errorf("%s not unverified", id)
		}
	}
	if len(refused) != 1 || len(unverified) != 7 {
		t.Errorf("refused=%v unverified=%v", refused, unverified)
	}
}

func fakeSectionsMonomind(t *testing.T, validateOut string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo '{"v":1,"version":"2.24.1","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi
if [ "$1 $2" = "agent scan" ]; then echo '{"v":1,"agents":[{"id":"claude","installed":true},{"id":"kilo","installed":false,"execution_supported":true,"execution_unsupported_reason":null},{"id":"tui-only","installed":false,"execution_supported":false,"execution_unsupported_reason":"tui-only is interactive-only (scan)"}]}'; exit 0; fi
if [ "$1 $2" = "org validate" ]; then
cat <<'OUT'
` + validateOut + `
OUT
exit 1
fi
exit 2
`
	p := filepath.Join(t.TempDir(), "monomind")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvOverride, p)
}

func TestSectionsRuntimesFromValidateAndScan(t *testing.T) {
	fakeSectionsMonomind(t, sectionsValidateSample)
	p, err := SectionsRuntimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != "monomind" {
		t.Errorf("source = %q", p.Source)
	}
	by := map[string]SectionsRuntime{}
	for _, r := range p.Runtimes {
		by[r.Runtime] = r
	}
	if by["kilo"].Reason != "Kilo supports full access only, so no role can run on it" {
		t.Errorf("kilo = %+v", by["kilo"])
	}
	// scan's own reason wins for a runtime it reports as not executable
	if by["tui-only"].Status != SectionsRuntimeRefused || by["tui-only"].Reason != "tui-only is interactive-only (scan)" {
		t.Errorf("tui-only = %+v", by["tui-only"])
	}
	if by["kimicode"].Status != SectionsRuntimeUnverified {
		t.Errorf("kimicode = %+v", by["kimicode"])
	}
}

func TestSectionsRuntimesFallsBackWhenValidateSaysNothing(t *testing.T) {
	fakeSectionsMonomind(t, "t: valid")
	p, err := SectionsRuntimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// the fake scan adds tui-only, which the fallback does not list
	if p.Source != "fallback" || len(p.Runtimes) != len(sectionsFallback)+1 {
		t.Errorf("policy = %+v", p)
	}
}
