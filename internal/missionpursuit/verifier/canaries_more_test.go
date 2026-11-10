//go:build !windows

package verifier

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

var _ = finish.ResPass

// planterConf is a confiner that can put a variable in the manager that starts its programs (the systemd one).
type planterConf struct {
	FakeConfiner
	mu               sync.Mutex
	planted, cleaned []string
	err              error
}

func (p *planterConf) PlantManagerEnv(_ context.Context, name, value string) (func(), error) {
	if p.err != nil {
		return nil, p.err
	}
	p.mu.Lock()
	p.planted = append(p.planted, name+"="+value)
	p.mu.Unlock()
	return func() { p.mu.Lock(); p.cleaned = append(p.cleaned, name); p.mu.Unlock() }, nil
}

// C12: what the manager's own environment holds must not reach a program. The host puts a variable there (where the confiner can)
// and the program must see none that the sandbox did not give it.
func TestTheManagerEnvironmentIsPlantedAndRemovedAndAnyUnknownVariableIsBroken(t *testing.T) {
	w := newTestWorld(t)
	pc := &planterConf{}
	s := &scripted{}
	rs := RunCanariesWith(context.Background(), pc, w.base, w.opts(CanaryOptions{Exec: s.exec, Leftovers: noLeftovers, HasTool: func(string) bool { return true }, Only: []string{"C12"}}))
	if CanariesHold(rs) != nil || len(pc.planted) != 1 || !strings.HasPrefix(pc.planted[0], "MONOAGENT_CANARY_MGR_") || len(pc.cleaned) != 1 || !strings.HasPrefix(pc.cleaned[0], "MONOAGENT_CANARY_MGR_") {
		t.Fatalf("%+v planted %v cleaned %v", rs, pc.planted, pc.cleaned)
	}
	spec := s.specs["C12"]
	for _, want := range []string{"env | cut -d= -f1", "grep -v -x -E", "PATH", "INVOCATION_ID"} {
		if !strings.Contains(spec, want) {
			t.Errorf("C12 does not say %q: %s", want, spec)
		}
	}
	// a planting that fails is an error verdict, never a pass; the cleanup still ran for what was planted
	pc = &planterConf{err: errors.New("manager refused")}
	rs = RunCanariesWith(context.Background(), pc, w.base, w.opts(CanaryOptions{Exec: s.exec, Leftovers: noLeftovers, HasTool: func(string) bool { return true }, Only: []string{"C12"}}))
	if v := verdicts(rs)["C12"]; v != CanaryError {
		t.Fatalf("planting failed: %q", v)
	}
}

// Stale plants of a run that crashed are removed at the next run; fresh ones (a run in progress) are left alone.
func TestStaleCanaryFilesOfACrashedRunAreSweptAndFreshOnesKept(t *testing.T) {
	w := newTestWorld(t)
	old := time.Now().Add(-3 * time.Hour)
	mk := func(p string, isDir bool, mt time.Time) string {
		var err error
		if isDir {
			err = os.Mkdir(p, 0o700)
		} else {
			err = os.WriteFile(p, []byte("x"), 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, mt, mt)
		return p
	}
	stale := []string{mk(filepath.Join(w.base, "canary-old"), true, old), mk(filepath.Join(w.parent, ".canary-r-old"), false, old), mk(filepath.Join(w.parent, ".canary-w-old"), false, old),
		mk(filepath.Join(w.home, ".canary-h-old"), false, old), mk(filepath.Join(w.tmp, "monoagent-canary-old.sock"), false, old), mk(filepath.Join(w.shm, "monoagent-canary-old"), false, old)}
	fresh := []string{mk(filepath.Join(w.base, "canary-new"), true, time.Now()), mk(filepath.Join(w.parent, ".canary-r-new"), false, time.Now()),
		mk(filepath.Join(w.base, "check-keep"), true, old), mk(filepath.Join(w.home, "important"), false, old)}
	runWith2(t, w, &scripted{})
	for _, p := range stale {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("stale %s was not swept", p)
		}
	}
	for _, p := range fresh {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
}

// Two runs that overlap in one process do not see each other's secret: the variable name is per run.
func TestTwoOverlappingRunsUseDifferentSecretNames(t *testing.T) {
	names := map[string]bool{}
	for i := 0; i < 3; i++ {
		s := &scripted{}
		runWith(t, s, func(o *CanaryOptions) { o.Only = []string{"C5"} })
		n := regexp.MustCompile(`MONOAGENT_CANARY_SECRET_[0-9a-f]+`).FindString(s.specs["C5"])
		if n == "" || names[n] {
			t.Fatalf("name %q reused or missing", n)
		}
		names[n] = true
	}
}

// C13: a program that writes in the host's /dev/shm and is "refused" on its exit code anyway is broken if the file persists.
func TestC13IsBrokenWhenTheFileTheProgramWroteSurvivesOnTheHost(t *testing.T) {
	s := &scripted{check: func(id, spec string) {
		if id == "C13" {
			if err := os.WriteFile(quotedPaths(spec)[1], []byte("x"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}}
	if v := verdicts(runWith(t, s, nil))["C13"]; v != CanaryBroken {
		t.Fatalf("C13 = %q with the write persisting", v)
	}
}
