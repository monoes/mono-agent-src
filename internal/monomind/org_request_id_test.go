package monomind

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A request id narrows an approve or deny to that one request. Without it
// monomind resolves every pending request of the (role, action) pair, so the
// id is never dropped: a monomind that cannot take it gets no call at all.
func TestOrgResolveNeverDropsTheRequestID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	for _, tc := range []struct {
		name, caps string
		sent       bool
	}{
		{"with " + CapOrgDecisionAttribution, `,"` + CapOrgDecisionAttribution + `"`, true},
		{"without it", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argsFile := filepath.Join(dir, "args")
			bin := filepath.Join(t.TempDir(), "monomind") // outside the project: a project-local one is refused
			script := "#!/bin/sh\n" +
				`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"` + tc.caps + `]}'; exit 0; fi` + "\n" +
				`echo "$*" >> '` + argsFile + "'\necho '{\"v\":1}'\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv(EnvOverride, bin)
			ResetCapabilityCache()
			t.Cleanup(ResetCapabilityCache)
			ctx := context.Background()
			opts := ResolveOptions{RequestID: "apr-1"}
			_, approveErr := OrgApproveWith(ctx, dir, "acme", "dev", "Bash", opts)
			_, denyErr := OrgDenyWith(ctx, dir, "acme", "dev", "Bash", opts)
			b, _ := os.ReadFile(argsFile)
			if !tc.sent {
				for _, err := range []error{approveErr, denyErr} {
					if err == nil || !strings.Contains(err.Error(), CapOrgDecisionAttribution) {
						t.Errorf("want a refusal naming the capability, got %v", err)
					}
				}
				if len(b) != 0 {
					t.Errorf("monomind was called without the request id:\n%s", b)
				}
				return
			}
			if approveErr != nil || denyErr != nil {
				t.Fatal(approveErr, denyErr)
			}
			want := "org approve acme dev Bash --request apr-1 --format json\norg deny acme dev Bash --request apr-1 --format json\n"
			if string(b) != want {
				t.Errorf("monomind got\n%s\nwant\n%s", b, want)
			}
		})
	}
}
