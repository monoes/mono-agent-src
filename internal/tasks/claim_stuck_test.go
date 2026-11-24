package tasks

import "testing"

// R16-D5: a held task whose history is too long to be claimed or renewed is
// flagged in the list, so the operator can find it.
func TestListFlagsAClaimStuckByEventCount(t *testing.T) {
	s, db, _ := newTestStore(t)
	stuck := mustAdd(t, s, "default", "long running", true).ID
	fine := mustAdd(t, s, "default", "short", true).ID
	claimsClaim(t, s, stuck, "one", 0)
	claimsClaim(t, s, fine, "one", 0)
	claimsSeedEvents(t, db, stuck, MaxEventsToClaim)
	claimsSeedEvents(t, db, fine, 5)
	got, err := s.List(bg, "default", Filter{}, human)
	if err != nil {
		t.Fatal(err)
	}
	flags := map[int64]bool{}
	for _, tk := range got {
		flags[tk.ID] = tk.ClaimStuck
	}
	if !flags[stuck] || flags[fine] {
		t.Fatalf("claim_stuck by id = %v, want only #%d", flags, stuck)
	}
}
