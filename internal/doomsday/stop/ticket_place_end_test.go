package stop

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

// fakeSub stands in for the kernel's ticket cgroup: it records the name the Placer chose and registers an empty
// sub-cgroup in the fake unit; the test adds the "born" child to it.
type fakeSub struct{ name string }

func (s fakeSub) Name() string  { return s.name }
func (fakeSub) Apply(*exec.Cmd) {}
func (fakeSub) Close() error    { return nil }
func fakeOpener(cg *fakeCg, last *string) func(Cgroup, string) (subCgroup, error) {
	return func(_ Cgroup, name string) (subCgroup, error) {
		cg.subs[name] = nil
		*last = name
		return fakeSub{name}, nil
	}
}

// P1 of the containment review: Stop spares the cgroup Place really created for a begun operation's ticket, and
// ends the one made for no ticket. Nothing here writes a sub-cgroup name by hand; the names come from Place.
func TestPlaceThenEndSparesTheBegunOperationsCgroup(t *testing.T) {
	k := newFakeKernel()
	var last string
	p := &Placer{K: k, Rec: testRecord(), openSub: fakeOpener(k.cg, &last)}

	f := newGateFixture(t)
	begun := startOp(t, f, "a", "b") // a merge whose first step started: CONTINUING

	rel, err := p.PlaceTicket(begun.ID(), exec.Command("true"))
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	begunSub := last
	k.cg.subs[begunSub] = []*fakeProc{{pid: 20}} // the git child, born there; it ignores SIGTERM

	rel2, err := p.Place(exec.Command("true"))
	if err != nil {
		t.Fatal(err)
	}
	defer rel2()
	otherSub := last
	k.cg.subs[otherSub] = []*fakeProc{{pid: 21, dieOnTerm: true}}

	u := newEnder(t, k, testRecord())
	out := u.End(bg, 7, f.g.ContinuingTickets())
	if out.Kind != UnitEnded {
		t.Fatalf("%+v", out)
	}
	if len(k.cg.subs[begunSub]) != 1 || k.cg.removed[begunSub] {
		t.Fatalf("the begun operation's cgroup %q was ended", begunSub)
	}
	for _, s := range k.cg.signals {
		if s == begunSub {
			t.Fatalf("the begun operation's cgroup %q was signalled", begunSub)
		}
	}
	if !k.cg.removed[otherSub] {
		t.Fatalf("the unticketed cgroup %q was spared", otherSub)
	}
}

// A second command of the same ticket does not collide with the first, and is spared too.
func TestPlaceTicketTwiceSameTicket(t *testing.T) {
	k := newFakeKernel()
	var last string
	p := &Placer{K: k, Rec: testRecord(), openSub: fakeOpener(k.cg, &last)}
	f := newGateFixture(t)
	begun := startOp(t, f, "a", "b")
	var subs []string
	for i := 0; i < 2; i++ {
		rel, err := p.PlaceTicket(begun.ID(), exec.Command("true"))
		if err != nil {
			t.Fatal(err)
		}
		defer rel()
		subs = append(subs, last)
		k.cg.subs[last] = []*fakeProc{{pid: 30 + i}}
	}
	if subs[0] == subs[1] {
		t.Fatal("two placements share one sub-cgroup name")
	}
	newEnder(t, k, testRecord()).End(bg, 7, f.g.ContinuingTickets())
	for _, s := range subs {
		if len(k.cg.subs[s]) != 1 {
			t.Fatalf("%q was ended", s)
		}
	}
}

// P2: a unit record of another generation is refused by End and by NewPlacer.
func TestUnitRecordOfAnotherGenerationIsRefused(t *testing.T) {
	k := newFakeKernel()
	k.cg.subs["t-1-a"] = []*fakeProc{{pid: 50, dieOnTerm: true}}
	k.names = []string{testRecord().Name}
	// a generation-7 record planted as the generation-8 file (a copied or restored run directory)
	dir2 := newRunDir(t, nil)
	mustNoErr(t, dir2.WriteUnitFile(testRecord()))
	data, err := os.ReadFile(dir2.UnitPath(7))
	if err != nil {
		t.Fatal(err)
	}
	mustNoErr(t, os.WriteFile(dir2.UnitPath(8), data, 0o600))

	u := UnitEnder{K: k, Dir: dir2, Cfg: endCfg(allKnown)}
	out := u.End(bg, 8, nil)
	if out.Kind != UnitNotProven || len(k.cg.signals) != 0 || k.cg.removed["t-1-a"] {
		t.Fatalf("a generation-7 record ended generation 8: %+v signals=%v", out, k.cg.signals)
	}
	if _, err := NewPlacer(k, dir2, 8); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("a generation-7 record made a placer for generation 8: %v", err)
	}
	if _, err := NewPlacer(k, dir2, 7); err != nil {
		t.Fatalf("the matching generation is refused: %v", err)
	}
}
