package orgproposal

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

var t0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := t0
	s := NewStore(db.DB)
	s.Now = func() time.Time { return now }
	return s, &now
}

func in(org, target string) Input {
	return Input{ProfileID: "p1", Org: org, ConversationID: "c1", Verb: VerbGrantAdd, Target: target,
		ArgsJSON: `{"role":"lead","automation":"` + target + `"}`, PreviewText: "grant " + target, StateDigest: "d-" + target, CreatedBy: "chat"}
}

func mustCreate(t *testing.T, s *Store, i Input) *Proposal {
	t.Helper()
	p, err := s.Create(context.Background(), i)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return p
}

func TestCreateStoresAPendingRowThatExpiresInThirtyMinutes(t *testing.T) {
	s, _ := newStore(t)
	p := mustCreate(t, s, in("growth", "a"))
	if len(p.ID) != 32 || strings.Trim(p.ID, "0123456789abcdef") != "" {
		t.Errorf("id %q is not 128 random bits in hex", p.ID)
	}
	q := mustCreate(t, s, in("growth", "b"))
	if q.ID == p.ID {
		t.Error("two proposals share an id")
	}
	if p.Status != StatusPending || !p.ExpiresAt.Equal(t0.Add(30*time.Minute)) || !p.CreatedAt.Equal(t0) {
		t.Errorf("status %s created %v expires %v", p.Status, p.CreatedAt, p.ExpiresAt)
	}
	got, err := s.Get(context.Background(), "p1", p.ID)
	if err != nil || got.Hash == "" || got.Hash != got.RecomputedHash() || got.PreviewText != "grant a" || got.CreatedBy != "chat" {
		t.Fatalf("Get: %+v %v", got, err)
	}
}

func TestTheHashBindsEveryFieldTheUserSees(t *testing.T) {
	base := []string{"id", "p1", "growth", VerbGrantAdd, `{"a":1}`, "preview", "digest"}
	h := func(f []string) string { return ComputeHash(f[0], f[1], f[2], f[3], f[4], f[5], f[6]) }
	want := h(base)
	if want == "" || len(want) != 64 {
		t.Fatalf("hash %q", want)
	}
	for i := range base {
		f := append([]string(nil), base...)
		f[i] += "x"
		if h(f) == want {
			t.Errorf("field %d does not change the hash", i)
		}
	}
	// a value moved between two fields is not the same input
	a := []string{"id", "p1", "growth", VerbGrantAdd, "ab", "c", "digest"}
	b := []string{"id", "p1", "growth", VerbGrantAdd, "a", "bc", "digest"}
	if h(a) == h(b) {
		t.Error("the hash does not separate its fields")
	}
}

func TestAnOrgHoldsAtMostFivePendingProposals(t *testing.T) {
	s, _ := newStore(t)
	for _, tg := range []string{"a", "b", "c", "d", "e"} {
		mustCreate(t, s, in("growth", tg))
	}
	if _, err := s.Create(context.Background(), in("growth", "f")); !errors.Is(err, ErrTooMany) {
		t.Fatalf("sixth pending proposal: %v, want ErrTooMany", err)
	}
	list, _ := s.List(context.Background(), "p1", "growth", "")
	if len(list) != 5 {
		t.Errorf("%d rows after a refused sixth, want 5 (a refusal supersedes nothing)", len(list))
	}
	mustCreate(t, s, in("other-org", "f")) // another org has its own five
	// a decided proposal frees a slot
	if err := s.Decline(context.Background(), "p1", list[0].ID); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, in("growth", "f"))
}

func TestACapOfFiveHoldsUnderConcurrentCreates(t *testing.T) {
	s, _ := newStore(t)
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Create(context.Background(), in("growth", string(rune('a'+i)))); err == nil {
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	list, _ := s.List(context.Background(), "p1", "growth", "")
	if ok.Load() > 5 || len(list) > 5 {
		t.Fatalf("%d creates succeeded, %d rows: the cap of five was passed", ok.Load(), len(list))
	}
}

func TestANewerProposalSupersedesTheOlderPendingOneForTheSameTarget(t *testing.T) {
	s, _ := newStore(t)
	older := mustCreate(t, s, in("growth", "a"))
	i := in("growth", "a")
	i.PreviewText = "grant a, again"
	newer := mustCreate(t, s, i)
	o, _ := s.Get(context.Background(), "p1", older.ID)
	if o.Status != StatusDeclined || !strings.Contains(o.ResultJSON, "superseded") {
		t.Errorf("older: status %s result %q, want declined by supersession", o.Status, o.ResultJSON)
	}
	if _, err := s.Claim(context.Background(), "p1", older.ID, o.RecomputedHash(), o.StateDigest); !errors.Is(err, ErrAlreadyDone) {
		t.Errorf("claiming the superseded one: %v, want ErrAlreadyDone", err)
	}
	if n, _ := s.List(context.Background(), "p1", "growth", ""); len(n) != 2 {
		t.Errorf("%d rows, want the two kept", len(n))
	}
	// five supersessions of one target never fill the cap
	for k := 0; k < 8; k++ {
		mustCreate(t, s, in("growth", "a"))
	}
	if _, err := s.Get(context.Background(), "p1", newer.ID); err != nil {
		t.Fatal(err)
	}
}

func claim(s *Store, p *Proposal) (*Proposal, error) {
	return s.Claim(context.Background(), "p1", p.ID, p.RecomputedHash(), p.StateDigest)
}

func TestAProposalIsClaimedOnce(t *testing.T) {
	s, _ := newStore(t)
	p := mustCreate(t, s, in("growth", "a"))
	c, err := claim(s, p)
	if err != nil || c.Status != StatusApplying {
		t.Fatalf("first claim: %+v %v", c, err)
	}
	if _, err := claim(s, p); !errors.Is(err, ErrAlreadyDone) {
		t.Fatalf("second claim: %v, want ErrAlreadyDone", err)
	}
	if err := s.Finish(context.Background(), "p1", p.ID, StatusApplied, `{"ok":true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := claim(s, p); !errors.Is(err, ErrAlreadyDone) {
		t.Fatalf("claim after applied: %v, want ErrAlreadyDone", err)
	}
	got, _ := s.Get(context.Background(), "p1", p.ID)
	if got.Status != StatusApplied || got.ResultJSON != `{"ok":true}` || got.DecidedAt == nil {
		t.Errorf("after finish: %+v", got)
	}
	if err := s.Finish(context.Background(), "p1", p.ID, StatusFailed, "{}"); err == nil {
		t.Error("an applied row was finished again")
	}
}

func TestConcurrentClaimsApplyOnce(t *testing.T) {
	s, _ := newStore(t)
	p := mustCreate(t, s, in("growth", "a"))
	var wg sync.WaitGroup
	var won, done atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch _, err := claim(s, p); {
			case err == nil:
				won.Add(1)
			case errors.Is(err, ErrAlreadyDone):
				done.Add(1)
			default:
				t.Errorf("claim: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 || done.Load() != 11 {
		t.Fatalf("%d claims won, %d answered already decided; want 1 and 11", won.Load(), done.Load())
	}
}

func TestClaimRefusalsAreDistinctAndChangeNothingElse(t *testing.T) {
	ctx := context.Background()
	t.Run("hash of another proposal with the same content", func(t *testing.T) {
		s, _ := newStore(t)
		a := mustCreate(t, s, in("growth", "a"))
		b := mustCreate(t, s, in("other", "a"))
		b2 := mustCreate(t, s, Input{ProfileID: "p1", Org: "growth", Verb: VerbGrantAdd, Target: "z", ArgsJSON: a.ArgsJSON, PreviewText: a.PreviewText, StateDigest: a.StateDigest})
		for _, other := range []*Proposal{b, b2} {
			if _, err := s.Claim(ctx, "p1", a.ID, other.RecomputedHash(), a.StateDigest); !errors.Is(err, ErrHashMismatch) {
				t.Errorf("hash of %s for %s: %v, want ErrHashMismatch", other.ID, a.ID, err)
			}
		}
		if got, _ := s.Get(ctx, "p1", a.ID); got.Status != StatusPending {
			t.Errorf("a hash mismatch left status %s", got.Status)
		}
	})
	t.Run("row rewritten after the card was drawn", func(t *testing.T) {
		s, _ := newStore(t)
		a := mustCreate(t, s, in("growth", "a"))
		shown := a.RecomputedHash()
		if _, err := s.DB.Exec(`UPDATE org_proposals SET args_json = ? WHERE id = ?`, `{"role":"lead","automation":"other"}`, a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Claim(ctx, "p1", a.ID, shown, a.StateDigest); !errors.Is(err, ErrHashMismatch) {
			t.Fatalf("claim of a rewritten row with the shown hash: %v, want ErrHashMismatch", err)
		}
		// rewritten together with the stored hash column: the shown hash no longer matches the content
		if _, err := s.DB.Exec(`UPDATE org_proposals SET hash = ? WHERE id = ?`, ComputeHash(a.ID, "p1", "growth", VerbGrantAdd, `{"role":"lead","automation":"other"}`, a.PreviewText, a.StateDigest), a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Claim(ctx, "p1", a.ID, shown, a.StateDigest); !errors.Is(err, ErrHashMismatch) {
			t.Fatalf("claim with the old shown hash: %v, want ErrHashMismatch", err)
		}
		if got, _ := s.Get(ctx, "p1", a.ID); got.Status != StatusPending {
			t.Errorf("status %s", got.Status)
		}
	})
	t.Run("stale", func(t *testing.T) {
		s, _ := newStore(t)
		a := mustCreate(t, s, in("growth", "a"))
		if _, err := s.Claim(ctx, "p1", a.ID, a.RecomputedHash(), "a-different-live-digest"); !errors.Is(err, ErrStale) {
			t.Fatalf("stale claim: %v", err)
		}
		if got, _ := s.Get(ctx, "p1", a.ID); got.Status != StatusStale {
			t.Errorf("status %s, want stale", got.Status)
		}
		if _, err := claim(s, a); !errors.Is(err, ErrAlreadyDone) {
			t.Errorf("claim after stale: %v, want ErrAlreadyDone", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		s, now := newStore(t)
		a := mustCreate(t, s, in("growth", "a"))
		*now = t0.Add(30*time.Minute + time.Second)
		if g, _ := s.Get(ctx, "p1", a.ID); g.Status != StatusExpired {
			t.Errorf("a read after the expiry shows %s", g.Status)
		}
		if _, err := claim(s, a); !errors.Is(err, ErrExpired) {
			t.Fatalf("expired claim: %v", err)
		}
		*now = t0 // the clock going back does not revive it
		if _, err := claim(s, a); !errors.Is(err, ErrAlreadyDone) && !errors.Is(err, ErrExpired) {
			t.Fatalf("claim after the clock went back: %v", err)
		}
		if g, _ := s.Get(ctx, "p1", a.ID); g.Status != StatusExpired {
			t.Errorf("status %s, want expired for good", g.Status)
		}
	})
	t.Run("last second", func(t *testing.T) {
		s, now := newStore(t)
		a := mustCreate(t, s, in("growth", "a"))
		*now = t0.Add(30*time.Minute - time.Second)
		if _, err := claim(s, a); err != nil {
			t.Fatalf("claim a second before the expiry: %v", err)
		}
	})
	t.Run("declined, applied and not found", func(t *testing.T) {
		s, _ := newStore(t)
		a := mustCreate(t, s, in("growth", "a"))
		if err := s.Decline(ctx, "p1", a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := claim(s, a); !errors.Is(err, ErrAlreadyDone) {
			t.Errorf("claim of a declined one: %v", err)
		}
		if err := s.Decline(ctx, "p1", a.ID); !errors.Is(err, ErrAlreadyDone) {
			t.Errorf("second decline: %v", err)
		}
		if _, err := s.Claim(ctx, "p1", "00000000000000000000000000000000", "h", "d"); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown id: %v", err)
		}
		b := mustCreate(t, s, in("growth", "b"))
		if _, err := s.Claim(ctx, "another-profile", b.ID, b.RecomputedHash(), b.StateDigest); !errors.Is(err, ErrNotFound) {
			t.Errorf("another profile's claim: %v, want ErrNotFound", err)
		}
		if err := s.Decline(ctx, "another-profile", b.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("another profile's decline: %v, want ErrNotFound", err)
		}
	})
}

func TestFinishOnlyEndsAClaimedProposal(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	p := mustCreate(t, s, in("growth", "a"))
	if err := s.Finish(ctx, "p1", p.ID, StatusApplied, "{}"); err == nil {
		t.Fatal("a pending proposal was marked applied without a claim")
	}
	if _, err := claim(s, p); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, "p1", p.ID, StatusDeclined, "{}"); err == nil {
		t.Error("Finish accepts a status that is not applied or failed")
	}
	if err := s.Finish(ctx, "another-profile", p.ID, StatusApplied, "{}"); err == nil {
		t.Error("another profile finished the proposal")
	}
}

func TestDecidedRowsAreKeptThirtyDaysThenDropped(t *testing.T) {
	s, now := newStore(t)
	ctx := context.Background()
	old := mustCreate(t, s, in("growth", "a"))
	if err := s.Decline(ctx, "p1", old.ID); err != nil {
		t.Fatal(err)
	}
	pend := mustCreate(t, s, in("growth", "b"))
	*now = t0.Add(Retention - time.Hour)
	mustCreate(t, s, in("growth", "c"))
	if _, err := s.Get(ctx, "p1", old.ID); err != nil {
		t.Fatalf("a row decided 29 days ago is gone: %v", err)
	}
	*now = t0.Add(Retention + 10*time.Minute) // past the decided row's 30 days, before the pending one's expiry plus 30 days
	mustCreate(t, s, in("growth", "d"))
	if _, err := s.Get(ctx, "p1", old.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a row decided 30 days ago is kept: %v", err)
	}
	// the pending one expired at t0+30m; it is decided (expired) from then on and is dropped 30 days after that
	if g, err := s.Get(ctx, "p1", pend.ID); err != nil || g.Status != StatusExpired {
		t.Errorf("expired row: %+v %v", g, err)
	}
}

func TestListIsScopedToTheProfileOrgAndConversation(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	a := in("growth", "a")
	a.ConversationID = "c1"
	b := in("growth", "b")
	b.ConversationID = "c2"
	c := in("sales", "c")
	d := in("growth", "d")
	d.ProfileID = "p2"
	for _, i := range []Input{a, b, c, d} {
		mustCreate(t, s, i)
	}
	count := func(profile, org, conv string) int {
		l, err := s.List(ctx, profile, org, conv)
		if err != nil {
			t.Fatal(err)
		}
		return len(l)
	}
	if n := count("p1", "", ""); n != 3 {
		t.Errorf("profile p1: %d rows", n)
	}
	if n := count("p1", "growth", ""); n != 2 {
		t.Errorf("org growth: %d", n)
	}
	if n := count("p1", "growth", "c2"); n != 1 {
		t.Errorf("conversation c2: %d", n)
	}
	if n := count("p2", "", ""); n != 1 {
		t.Errorf("profile p2: %d", n)
	}
}
