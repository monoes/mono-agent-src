package browserctl

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

// Two commands of one context load the same state, each adds a mark of its
// own and saves: both marks must survive, whichever saves last.
func TestSaveStateFromKeepsAnotherCommandsUpdates(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if err := SaveState("ctx", State{TabID: 1, Instance: "i", Reported: map[string]int{"i/1": 1}}); err != nil {
		t.Fatal(err)
	}
	baseA, baseB := LoadState("ctx"), LoadState("ctx")
	a, b := LoadState("ctx"), LoadState("ctx")
	a.Reported["i/2"] = 5
	b.Loads = map[string]LoadMark{"i/1": {Console: 3, Network: 4}}
	if err := SaveStateFrom("ctx", baseA, a); err != nil {
		t.Fatal(err)
	}
	if err := SaveStateFrom("ctx", baseB, b); err != nil {
		t.Fatal(err)
	}
	got := LoadState("ctx")
	if got.Reported["i/2"] != 5 || got.Reported["i/1"] != 1 || got.Loads["i/1"].Network != 4 {
		t.Fatalf("an update was lost: %+v", got)
	}
}

// A key one command deleted stays deleted; a key it left alone follows the file.
func TestSaveStateFromHonoursDeletes(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	if err := SaveState("ctx", State{Reported: map[string]int{"x": 1, "y": 2}}); err != nil {
		t.Fatal(err)
	}
	base := LoadState("ctx")
	mine := LoadState("ctx")
	delete(mine.Reported, "x")
	other := LoadState("ctx")
	other.Reported["y"] = 9
	if err := SaveState("ctx", other); err != nil {
		t.Fatal(err)
	}
	if err := SaveStateFrom("ctx", base, mine); err != nil {
		t.Fatal(err)
	}
	got := LoadState("ctx").Reported
	if _, ok := got["x"]; ok || got["y"] != 9 {
		t.Fatalf("reported = %v, want x deleted and y=9", got)
	}
}

func TestConcurrentSavesLoseNothing(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			base := LoadState("ctx")
			st := LoadState("ctx")
			if st.Reported == nil {
				st.Reported = map[string]int{}
			}
			st.Reported[fmt.Sprintf("t/%d", i)] = i + 1
			if err := SaveStateFrom("ctx", base, st); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if got := LoadState("ctx").Reported; len(got) != n {
		t.Fatalf("%d of %d concurrent updates survived: %v", len(got), n, got)
	}
}
