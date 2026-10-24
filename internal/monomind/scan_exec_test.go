package monomind

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// `agent scan` (monomind 2.24.1) says per runtime whether it can be run.
func TestScanEntryExecutionSupport(t *testing.T) {
	var res ScanResult
	raw := `{"v":1,"agents":[
	 {"id":"tui-only","installed":false,"execution_supported":false,"execution_unsupported_reason":"interactive-only"},
	 {"id":"kilo","installed":false,"execution_supported":true,"execution_unsupported_reason":null},
	 {"id":"old","installed":true}]}`
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	if r, ok := res.Find("tui-only").UnsupportedReason(); !ok || r != "interactive-only" {
		t.Errorf("tui-only = %q %v", r, ok)
	}
	for _, id := range []string{"kilo", "old"} {
		if _, ok := res.Find(id).UnsupportedReason(); ok {
			t.Errorf("%s reported unsupported", id)
		}
	}
	out, _ := json.Marshal(res.Find("tui-only"))
	var back map[string]any
	_ = json.Unmarshal(out, &back)
	if back["execution_supported"] != false || back["execution_unsupported_reason"] != "interactive-only" {
		t.Errorf("re-encoded = %s", out)
	}
}

// freebuff cannot run under monomind, so Scan never lists it.
func TestScanDropsFreebuff(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo '{"v":1,"version":"2.24.1","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi
if [ "$1 $2" = "agent scan" ]; then echo '{"v":1,"agents":[{"id":"claude","installed":true},{"id":"freebuff","installed":true}]}'; exit 0; fi
exit 2
`
	p := filepath.Join(t.TempDir(), "monomind")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvOverride, p)
	res, err := Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Agents) != 1 || res.Agents[0].ID != "claude" {
		t.Errorf("agents = %+v", res.Agents)
	}
}
