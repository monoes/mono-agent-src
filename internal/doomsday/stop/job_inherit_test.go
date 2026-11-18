package stop

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/inheritenv"
)

// fakeAnchorDup duplicates into a fake anchor: handle h becomes h+1000, and what is still open there is tracked.
type fakeAnchorDup struct {
	open   map[uintptr]bool
	failOn uintptr
}

func (f *fakeAnchorDup) dup(h uintptr) (uintptr, error) {
	if h == f.failOn {
		return 0, errors.New("access denied")
	}
	if f.open == nil {
		f.open = map[uintptr]bool{}
	}
	f.open[h+1000] = true
	return h + 1000, nil
}
func (f *fakeAnchorDup) close(d uintptr) { delete(f.open, d) }

// Each handle is replaced by its duplicate in the same order, and the variable that announces it now carries the
// duplicate's number; nothing else in the environment changes and the caller's slices are not written to (#1723).
func TestPassInheritedSubstitutesTheDuplicatesAndRewritesTheirVariables(t *testing.T) {
	handles := []uintptr{12, 40}
	env := []string{"PATH=/bin", inheritenv.Mission + "=40", "X=12", strings.ToLower(inheritenv.Cred) + "=12"}
	keepH, keepE := slices.Clone(handles), slices.Clone(env)
	var f fakeAnchorDup
	dups, out, err := passInherited(handles, env, f.dup, f.close)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dups, []uintptr{1012, 1040}) {
		t.Fatalf("duplicates %v", dups)
	}
	want := []string{"PATH=/bin", inheritenv.Mission + "=1040", "X=12", strings.ToLower(inheritenv.Cred) + "=1012"}
	if !slices.Equal(out, want) {
		t.Fatalf("env %q, want %q", out, want)
	}
	if !slices.Equal(handles, keepH) || !slices.Equal(env, keepE) {
		t.Fatal("the caller's list or environment was written to")
	}
	if len(f.open) != 2 {
		t.Fatalf("the duplicates are not left open for the start: %v", f.open)
	}
}

// A command that passes no handle is untouched and nothing is duplicated.
func TestPassInheritedLeavesACommandWithoutHandlesAlone(t *testing.T) {
	env := []string{inheritenv.Cred + "=7"} // a stale announcement is not this placement's business
	f := fakeAnchorDup{failOn: 7}
	dups, out, err := passInherited(nil, env, f.dup, f.close)
	if err != nil || dups != nil || !slices.Equal(out, env) || len(f.open) != 0 {
		t.Fatalf("%v %v %q %v", err, dups, out, f.open)
	}
}

// Anything but one announcement per handle is refused before anything is duplicated.
func TestPassInheritedRefusesABrokenContractBeforeDuplicating(t *testing.T) {
	cred, mission := inheritenv.Cred, inheritenv.Mission
	for name, c := range map[string]struct {
		handles []uintptr
		env     []string
	}{
		"no environment":         {[]uintptr{5}, nil},
		"handle not announced":   {[]uintptr{5, 6}, []string{cred + "=5"}},
		"announces another one":  {[]uintptr{5}, []string{cred + "=9"}},
		"not a number":           {[]uintptr{5}, []string{cred + "=five"}},
		"set twice":              {[]uintptr{5}, []string{cred + "=5", strings.ToLower(cred) + "=5"}},
		"one handle, two names":  {[]uintptr{5}, []string{cred + "=5", mission + "=5"}},
		"the same handle twice":  {[]uintptr{5, 5}, []string{cred + "=5"}},
		"announced by the value": {[]uintptr{5}, []string{"OTHER=5"}},
	} {
		var f fakeAnchorDup
		if _, _, err := passInherited(c.handles, c.env, f.dup, f.close); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if len(f.open) != 0 {
			t.Errorf("%s: duplicated before refusing: %v", name, f.open)
		}
	}
}

// A duplicate that fails closes the ones already made in the anchor.
func TestPassInheritedClosesItsDuplicatesWhenOneFails(t *testing.T) {
	f := fakeAnchorDup{failOn: 6}
	_, _, err := passInherited([]uintptr{5, 6}, []string{inheritenv.Cred + "=5", inheritenv.Mission + "=6"}, f.dup, f.close)
	if err == nil {
		t.Fatal("a failed duplicate was not reported")
	}
	if len(f.open) != 0 {
		t.Fatalf("duplicates left in the anchor: %v", f.open)
	}
}

// A leased placement is released when the command is collected, long after its start: one that announces an inherited
// handle is refused before anything is prepared, a plain one is placed as before.
func TestJobPlaceLeasedRefusesACommandThatPassesAHandle(t *testing.T) {
	w := newFakeWin()
	rec, _, _ := createWin(t, w, 7)
	pl, _ := jobContainment{os: w}.Place(rec)
	cmd := exec.Command("monoagentcli")
	cmd.Env = []string{"PATH=/bin", strings.ToLower(inheritenv.Cred) + "=5"}
	if err := pl.PlaceLeased(cmd); !errors.Is(err, ErrNotPlaced) || len(w.parentFor) != 0 {
		t.Fatalf("a leased command passing a handle was placed: %v %v", err, w.parentFor)
	}
	cmd.Env = []string{"PATH=/bin"}
	if err := pl.PlaceLeased(cmd); err != nil || len(w.parentFor) != 1 {
		t.Fatalf("a plain leased command: %v %v", err, w.parentFor)
	}
}
