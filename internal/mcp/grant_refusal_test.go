package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orggrant"
)

// brokenDBServer is a grant-mode server whose database cannot be opened: any
// path through a grant-list read or runtime bootstrap fails with a
// non-refusal error, so a refusal that still comes back made no database
// access.
func brokenDBServer(t *testing.T) *Server {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(Options{DBPath: filepath.Join(blocker, "x.db"), Profile: "default", Grant: "g-1"})
	t.Cleanup(func() { s.closeRuntime() })
	return s
}

// The grant-mode refusal of a tool the grant can never serve is decided from
// the name alone: the answer is refused_grant for every environment, every
// grant state and a live or canceled context, and it never reads the database.
func TestGrantModeRefusalIsDecidedBeforeAnyGrantRead(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	ctxs := map[string]context.Context{"live": context.Background(), "canceled": canceled}
	envs := map[string][2]string{"both": {"growth", "writer"}, "no-org": {"", "writer"}, "no-role": {"growth", ""}, "none": {"", ""}}
	notServed := []string{"task_list", "task_next", "api_status", "workflow_list", "doctor", "automation", "no_such_tool", ""}
	for ctxName, ctx := range ctxs {
		for envName, env := range envs {
			for _, name := range notServed {
				t.Run(ctxName+"/"+envName+"/"+name, func(t *testing.T) {
					t.Setenv("MONOMIND_ORG_NAME", env[0])
					t.Setenv("MONOMIND_ORG_ROLE", env[1])
					// grants absent: the database cannot even be opened.
					if _, err := brokenDBServer(t).callGrantTool(ctx, name, nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
						t.Fatalf("grants absent: %q = %v, want a %s refusal", name, err, codeRefusedGrant)
					}
					// grants present
					f := newGrantFixture(t, orggrant.Tool{Wait: true})
					t.Setenv("MONOMIND_ORG_NAME", env[0])
					t.Setenv("MONOMIND_ORG_ROLE", env[1])
					if _, err := f.server.callGrantTool(ctx, name, nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
						t.Fatalf("grants present: %q = %v, want a %s refusal", name, err, codeRefusedGrant)
					}
				})
			}
		}
	}
}

// A grant tool with the environment missing is refused before the database is
// read too, canceled context or not; with the environment present and the
// context canceled the call still fails closed (an error, nothing served).
func TestGrantModeServedNameStaysRefusedOrFailsClosed(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range []string{"automation_publish", "automation_status", "automation_output", "decision_list", "org_start"} {
		t.Run(name, func(t *testing.T) {
			for ctxName, ctx := range map[string]context.Context{"live": context.Background(), "canceled": canceled} {
				for envName, env := range map[string][2]string{"no-org": {"", "writer"}, "no-role": {"growth", ""}} {
					t.Setenv("MONOMIND_ORG_NAME", env[0])
					t.Setenv("MONOMIND_ORG_ROLE", env[1])
					if _, err := brokenDBServer(t).callGrantTool(ctx, name, nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
						t.Errorf("%s/%s %s: %v, want a %s refusal without a database", ctxName, envName, name, err, codeRefusedGrant)
					}
				}
			}
			f := newGrantFixture(t, orggrant.Tool{Wait: true})
			if out, err := f.server.callGrantTool(canceled, name, nil); err == nil {
				t.Errorf("canceled context served %s: %s", name, out)
			}
		})
	}
}
