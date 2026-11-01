//go:build linux

package procbind

import (
	"os"
	"testing"
	"time"
)

// hookHandle is a worker handle that runs hook once, the first time the walk reads its pid.
type hookHandle struct {
	Handle
	hook func()
	done bool
}

func (h *hookHandle) Pid() (int, error) {
	if !h.done && h.hook != nil {
		h.done = true
		h.hook()
	}
	return h.Handle.Pid()
}

func leaseSpec() EntrySpec {
	return EntrySpec{TokenID: tToken, Kind: KindSession, Profile: tProfile, Scope: tScope, Session: "conv", Turn: "turn", Expires: time.Now().Add(time.Hour)}
}

func leaseServer(t *testing.T, b *Binder) string {
	t.Helper()
	path, _ := startServer(t, nil, func(s *Server) { s.Registry, s.Leases = nil, b })
	return path
}

func activeBinder(t *testing.T, activate bool, hook func(b *Binder)) *Binder {
	t.Helper()
	b := NewBinder(time.Now)
	var h Handle = open(t, os.Getpid())
	if hook != nil {
		h = &hookHandle{Handle: h, hook: func() { hook(b) }}
	}
	gen, err := b.Register(leaseSpec(), h)
	if err != nil {
		t.Fatal(err)
	}
	if activate {
		if err := b.Activate(tProfile, tToken, gen); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(b.Close)
	return b
}

// The service reads the Binder through a lease: an active registration of the caller's own process is allowed; a
// pending one, a revoked one and an unknown token are denied; and a revoke that lands while the walk runs makes the
// allow a deny, because the lease is checked again right before the answer is signed.
func TestTheServiceUsesALeaseAndRechecksItBeforeAllowing(t *testing.T) {
	cases := map[string]struct {
		activate bool
		before   func(b *Binder)
		during   func(b *Binder)
		want     Verdict
	}{
		"active":                     {activate: true, want: VerdictAllow},
		"pending":                    {activate: false, want: VerdictDeny},
		"revoked before":             {activate: true, before: func(b *Binder) { b.Revoke(Selector{Profile: tProfile, TokenID: tToken}) }, want: VerdictDeny},
		"revoked while the walk ran": {activate: true, during: func(b *Binder) { b.Revoke(Selector{Profile: tProfile, TokenID: tToken}) }, want: VerdictDeny},
	}
	for name, c := range cases {
		b := activeBinder(t, c.activate, c.during)
		if c.before != nil {
			c.before(b)
		}
		path := leaseServer(t, b)
		v, err := verifyOnce(t, path, goodClient())
		if err != nil || v != c.want {
			t.Errorf("%s: %v %v, want %v", name, v, err, c.want)
		}
	}
}
