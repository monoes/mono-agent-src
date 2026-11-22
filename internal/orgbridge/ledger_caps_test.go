package orgbridge

import (
	"context"
	"sync"
	"testing"
)

// Parallel granted calls are bounded by the grant's per-run and per-day
// caps exactly: the counts are read in the same transaction that records the
// call, so 16 callers cannot all read n < limit.
func TestLedgerGrantCapsHoldUnderParallelCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps GrantCaps
		run  string
	}{
		{"per run", GrantCaps{PerRun: 5, PerDay: 1000}, "run-1"},
		{"per day", GrantCaps{PerRun: 1000, PerDay: 5}, "run-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := NewLedger(newTestDB(t))
			ctx := context.Background()
			var mu sync.Mutex
			admitted := 0
			var wg sync.WaitGroup
			for w := 0; w < 16; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					c := tc.caps
					call := Call{ProfileID: "p", Direction: DirRoleTool, OrgName: "g", RoleID: "writer", WorkflowID: "wf",
						GrantID: "grant-1", RunID: tc.run, Caps: &c}
					adm, err := l.Admit(ctx, call, Limits{MaxRepeats: 200})
					if err != nil {
						t.Error(err)
						return
					}
					if adm.OK() {
						mu.Lock()
						admitted++
						mu.Unlock()
					} else if adm.Status != StatusRefusedCap {
						t.Errorf("refused as %q, want %q", adm.Status, StatusRefusedCap)
					}
				}()
			}
			wg.Wait()
			if admitted != 5 {
				t.Fatalf("admitted %d of 16 parallel calls, want exactly 5", admitted)
			}
		})
	}
}
