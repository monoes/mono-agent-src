//go:build windows

package procbind

import (
	"net"
	"sync"
	"testing"
	"time"
)

// lateRegistry names whichever process the stand-in peer lookup pinned last as the worker.
type lateRegistry struct {
	mu sync.Mutex
	h  Handle
}

func (r *lateRegistry) set(h Handle) { r.mu.Lock(); r.h = h; r.mu.Unlock() }
func (r *lateRegistry) Lookup(string, string, string) (Handle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.h, r.h != nil
}

// A process the kernel names as the peer, but that was created after the service accepted the connection, cannot be the
// connector: it is a recycled number. The control is the same process pinned when it was created before the accept.
func TestAPeerCreatedAfterTheAcceptIsNotServed(t *testing.T) {
	for _, late := range []bool{false, true} {
		name := "created before the accept is served"
		if late {
			name = "created after the accept is refused"
		}
		t.Run(name, func(t *testing.T) {
			sid, err := OwnSID()
			if err != nil {
				t.Fatal(err)
			}
			var early *ProcHandle
			if !late {
				cmd, r := spawn(t, "sleep")
				readLine(t, r, "READY")
				if early, err = OpenProcessHandle(cmd.Process.Pid); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { early.Close() })
			}
			reg := &lateRegistry{}
			s := startService(t, nil, func(s *Server) {
				s.Registry = reg
				s.peerOf = func(*net.UnixConn) (peerInfo, error) {
					var h *ProcHandle
					if late {
						time.Sleep(3 * peerClockSlack) // the connection is accepted; the stand-in peer is made after it
						cmd, r := spawn(t, "sleep")
						readLine(t, r, "READY")
						var err error
						if h, err = OpenProcessHandle(cmd.Process.Pid); err != nil {
							return peerInfo{}, err
						}
					} else if h, err = OpenProcessHandle(early.pidInt()); err != nil {
						return peerInfo{}, err
					}
					w, err := OpenProcessHandle(h.pidInt())
					if err != nil {
						return peerInfo{}, err
					}
					reg.set(w)
					return peerInfo{proc: h, sid: sid}, nil
				}
			})
			v, err := s.verify()
			if late {
				if err == nil && v == VerdictAllow {
					t.Fatal("a peer created after the accept was served")
				}
				return
			}
			if err != nil || v != VerdictAllow {
				t.Fatalf("control: a peer created before the accept was not served: %v %v", v, err)
			}
		})
	}
}

func (p *ProcHandle) pidInt() int { n, _ := p.Pid(); return n }
