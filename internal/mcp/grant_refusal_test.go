package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func refusalRows(t *testing.T, f *grantFixture) int {
	t.Helper()
	f.server.refusals.wait()
	var n int
	if err := f.db.DB.QueryRow(`SELECT COUNT(*) FROM org_bridge_calls WHERE direction = 'role_tool' AND status = 'refused_grant' AND grant_id = ? AND org_name = 'growth' AND role_id = 'writer'`, f.grant.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func slowLedger(t *testing.T) {
	t.Helper()
	old := grantRefusalLedgerTimeout
	grantRefusalLedgerTimeout = 10 * time.Second
	t.Cleanup(func() { grantRefusalLedgerTimeout = old })
}

func refuseOnce(t *testing.T, s *Server, ctx context.Context, name string) {
	t.Helper()
	if _, err := s.callGrantTool(ctx, name, nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
		t.Fatalf("%q = %v, want a %s refusal", name, err, codeRefusedGrant)
	}
}

// A never-served name still leaves its refusal row in the ledger, written
// after the refusal is decided and on its own context, so a canceled request
// context does not lose it.
func TestGrantModeNeverServedNameLeavesALedgerRow(t *testing.T) {
	slowLedger(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for ctxName, ctx := range map[string]context.Context{"live": context.Background(), "canceled": canceled} {
		t.Run(ctxName, func(t *testing.T) {
			f := newGrantFixture(t, orggrant.Tool{Wait: true})
			refuseOnce(t, f.server, ctx, "task_list")
			if n := refusalRows(t, f); n != 1 {
				t.Fatalf("%d refusal rows, want 1", n)
			}
		})
	}
}

// The first refusal per name per minute is recorded, on a fake clock; a flood
// of distinct names stays bounded.
func TestGrantModeRefusalRowsAreLimitedPerNamePerMinute(t *testing.T) {
	slowLedger(t)
	now := time.Now()
	old := grantRefusalNow
	grantRefusalNow = func() time.Time { return now }
	t.Cleanup(func() { grantRefusalNow = old })
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	for i := 0; i < 5; i++ {
		refuseOnce(t, f.server, context.Background(), "task_list")
		f.server.refusals.wait()
	}
	if n := refusalRows(t, f); n != 1 {
		t.Fatalf("%d rows after 5 refusals of one name in a minute, want 1", n)
	}
	refuseOnce(t, f.server, context.Background(), "task_next")
	if n := refusalRows(t, f); n != 2 {
		t.Fatalf("%d rows after a second name, want 2", n)
	}
	now = now.Add(61 * time.Second)
	refuseOnce(t, f.server, context.Background(), "task_list")
	if n := refusalRows(t, f); n != 3 {
		t.Fatalf("%d rows a minute later, want 3", n)
	}
	for i := 0; i < grantRefusalMaxNames*2; i++ {
		refuseOnce(t, f.server, context.Background(), "flood_"+strconv.Itoa(i))
		f.server.refusals.wait()
	}
	if n := refusalRows(t, f); n > 3+grantRefusalMaxNames {
		t.Fatalf("%d rows after a flood of distinct names, want at most %d", n, 3+grantRefusalMaxNames)
	}
}

// A process whose environment does not match the stored grant writes no row,
// and a refusal is still returned.
func TestGrantModeMismatchedEnvironmentWritesNoRefusalRow(t *testing.T) {
	slowLedger(t)
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	t.Setenv("MONOMIND_ORG_NAME", "other")
	refuseOnce(t, f.server, context.Background(), "task_list")
	f.server.refusals.wait()
	t.Setenv("MONOMIND_ORG_NAME", "growth")
	t.Setenv("MONOMIND_ORG_ROLE", "other")
	refuseOnce(t, f.server, context.Background(), "task_next")
	if n := refusalRows(t, f); n != 0 {
		t.Fatalf("%d rows written for a mismatched environment", n)
	}
}

// The refusal is not held by the ledger write: it returns at once while the
// insert is blocked, and the write itself gives up within its timeout.
func TestGrantModeRefusalIsNotHeldByTheLedgerWrite(t *testing.T) {
	old := grantRefusalLedgerTimeout
	grantRefusalLedgerTimeout = 100 * time.Millisecond
	t.Cleanup(func() { grantRefusalLedgerTimeout = old })
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	tx, err := f.db.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.Exec(`UPDATE org_bridge_calls SET status = status`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	refuseOnce(t, f.server, context.Background(), "task_list")
	returned := time.Since(start)
	f.server.refusals.wait()
	if returned > 50*time.Millisecond {
		t.Fatalf("refusal took %v with the ledger write blocked", returned)
	}
	if total := time.Since(start); total > 5*time.Second {
		t.Fatalf("the ledger write outlived its timeout: %v", total)
	}
}

// Names that only look like a grant tool are never served.
func TestGrantModeNearMissNamesAreRefused(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range []string{"Automation_x", "AUTOMATION_PUBLISH", "automation_", "automation", " automation_x", "\u0430utomation_x", "automation\u2010x", "automation-x", "decision_list ", "Decision_list", "org_"} {
		if grantServesName(name) {
			t.Errorf("grantServesName(%q) = true", name)
		}
		refuseOnce(t, f.server, canceled, name)
	}
}

// An unset environment with an unknown grant gives the environment refusal
// (the environment is checked before the grant is read).
func TestGrantModeUnsetEnvironmentBeatsUnknownGrant(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	f.server.opts.Grant = "no-such-grant"
	t.Setenv("MONOMIND_ORG_NAME", "")
	_, err := f.server.callGrantTool(context.Background(), "automation_publish", nil)
	if err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) || !strings.Contains(err.Error(), "MONOMIND_ORG_NAME") {
		t.Fatalf("got %v, want the MONOMIND_ORG_NAME refusal", err)
	}
}
