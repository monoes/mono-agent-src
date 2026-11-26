package orggrant

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

// together runs n calls of fn at the same moment and fails on any error.
func together(t *testing.T, n int, fn func(i int) error) {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

// Grants written at the same moment end up in one live row per slot, none of
// the writers failing and no addition lost.
func TestConcurrentGrantWritersKeepOneRowPerSlot(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	for round := 0; round < 5; round++ {
		role := fmt.Sprintf("r%d", round)
		together(t, 8, func(int) error {
			_, err := s.UpsertGrant(ctx, GrantInput{ProfileID: "p", OrgName: "o", RoleID: role,
				Tool: Tool{Alias: "x", WorkflowID: "wf1"}})
			return err
		})
		together(t, 8, func(i int) error {
			_, err := s.MergeOrgTools(ctx, "p", "o", role, []OrgTool{{Tool: "org_start", Orgs: []string{fmt.Sprintf("c%d", i)}}})
			return err
		})
		grants, err := s.ListGrants(ctx, "p", "o", role)
		if err != nil {
			t.Fatal(err)
		}
		if len(grants) != 2 {
			t.Fatalf("round %d: %d live rows, want one automation and one org-tool row", round, len(grants))
		}
		for _, g := range grants {
			if len(g.OrgTools) == 0 {
				continue
			}
			orgs := append([]string(nil), g.OrgTools[0].Orgs...)
			sort.Strings(orgs)
			if got := strings.Join(orgs, ","); got != "c0,c1,c2,c3,c4,c5,c6,c7" {
				t.Fatalf("round %d: orgs = %s, an addition was lost", round, got)
			}
		}
		together(t, 8, func(i int) error {
			_, err := s.SetOrgTools(ctx, "p", "o", role+"-set", []OrgTool{{Tool: "org_stop", Orgs: []string{"a"}}})
			return err
		})
		// A narrowing and a removal leave nothing of the old scope live.
		if _, err := s.SetOrgToolScope(ctx, "p", "o", role, []OrgTool{{Tool: "org_start"}}); err != nil {
			t.Fatal(err)
		}
		grants, err = s.ListGrants(ctx, "p", "o", role)
		if err != nil || len(grants) != 1 || grants[0].Automation() == nil {
			t.Fatalf("round %d: after narrowing to nothing: %+v, %v", round, grants, err)
		}
		if set, err := s.ListGrants(ctx, "p", "o", role+"-set"); err != nil || len(set) != 1 {
			t.Fatalf("round %d: concurrent set left %d rows (%v)", round, len(set), err)
		}
	}
}
