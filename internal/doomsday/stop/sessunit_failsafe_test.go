package stop

import (
	"errors"
	"strings"
	"syscall"
	"testing"
)

// A member whose Info read fails is NOT a gone member: it is checked by its stamp alone and still signalled, and a read that
// cannot be checked at all is an error, never a silent skip (the kill switch must not fail open).
func TestSignalSkipsOnlyAGoneOrReusedMember(t *testing.T) {
	errRead := errors.New("the process keeps changing")
	for name, c := range map[string]struct {
		prep    func(r *sessRig, m SessProc)
		wantKil bool
		wantErr bool
	}{
		"read fails, stamp still equal: signalled": {func(r *sessRig, m SessProc) { r.w.infoErrs[m.PID] = errRead }, true, false},
		"gone: skipped without error":              {func(r *sessRig, m SessProc) { delete(r.w.procs, m.PID) }, false, false},
		"number reused (stamp differs): skipped": {func(r *sessRig, m SessProc) {
			r.w.procs[m.PID].Stamp = "1.000001"
		}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSessRig(t)
			l := r.w.leader(500)
			r.record(l)
			g := &sessGroup{c: r.c, rec: r.rec}
			m := l.SessProc
			c.prep(r, m)
			err := g.signal(m, syscall.SIGKILL)
			if (err != nil) != c.wantErr {
				t.Fatalf("err %v", err)
			}
			if got := r.w.count("kill:500") > 0; got != c.wantKil {
				t.Fatalf("killed=%v want %v (%v)", got, c.wantKil, r.w.log)
			}
		})
	}
}

// With no stamp reader a failed read is returned: the caller sees it, nothing is hidden.
func TestSignalReturnsAReadThatCannotBeChecked(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	r.w.infoErrs[500] = errors.New("unreadable")
	c := r.c
	c.os = noStampOS{r.w}
	g := &sessGroup{c: c, rec: r.rec}
	if err := g.signal(l.SessProc, syscall.SIGKILL); err == nil {
		t.Fatal("an unreadable member was skipped silently")
	}
}

// noStampOS hides the optional StampOf.
type noStampOS struct{ SessionOS }

// When the scan fails as a whole, Stop does not do nothing: the members it has seen and the recorded leaders are signalled by
// pid and start stamp, and the answer is NOT PROVEN.
func TestKillFallsBackToRecordedMembersWhenTheScanFails(t *testing.T) {
	r := newSessRig(t)
	l := r.w.leader(500)
	r.record(l)
	r.w.put(501, 500, 501, 500, "kid")
	g := &sessGroup{c: r.c, rec: r.rec}
	if _, err := g.Procs(""); err != nil { // the members become known
		t.Fatal(err)
	}
	r.w.errs["Scan"] = errors.New("the table cannot be read")
	err := g.Kill("")
	if !errors.Is(err, ErrNotProven) {
		t.Fatalf("a failed scan must answer NOT PROVEN: %v", err)
	}
	if !strings.Contains(err.Error(), "table cannot be read") {
		t.Fatalf("the cause is lost: %v", err)
	}
	for _, pid := range []int{500, 501} {
		if r.w.has(pid) {
			t.Fatalf("recorded member %d was not killed after a failed scan (%v)", pid, r.w.log)
		}
	}
	// a fresh group that has seen nothing still kills the recorded leader (the ledger is not the scan)
	r2 := newSessRig(t)
	l2 := r2.w.leader(500)
	r2.record(l2)
	r2.w.errs["Scan"] = errors.New("boom")
	if err := (&sessGroup{c: r2.c, rec: r2.rec}).Kill(""); !errors.Is(err, ErrNotProven) || r2.w.has(500) {
		t.Fatalf("recorded leader survives a failed scan: %v %v", err, r2.w.log)
	}
}
