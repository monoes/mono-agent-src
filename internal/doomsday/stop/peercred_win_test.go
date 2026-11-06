package stop

import (
	"errors"
	"net"
	"os"
	"testing"
)

type fakePeerOS struct {
	self, peer      string
	selfErr, pidErr error
	sidErr          error
	pid             uint32
	asked           []uint32
}

func (f *fakePeerOS) PeerPID(net.Conn) (uint32, error) { return f.pid, f.pidErr }
func (f *fakePeerOS) UserSID(pid uint32) (string, error) {
	f.asked = append(f.asked, pid)
	return f.peer, f.sidErr
}
func (f *fakePeerOS) SelfSID() (string, error) { return f.self, f.selfErr }

func TestWinPeerSameUserIsAcceptedAsTheHostsUID(t *testing.T) {
	f := &fakePeerOS{self: "S-1-5-21-1", peer: "S-1-5-21-1", pid: 42}
	uid, err := peerUIDVia(f, nil)
	mustNoErr(t, err)
	if uid != uint32(os.Getuid()) || len(f.asked) != 1 || f.asked[0] != 42 {
		t.Fatalf("uid %d asked %v", uid, f.asked)
	}
}

func TestWinPeerEveryDoubtIsRefused(t *testing.T) {
	boom := errors.New("access denied")
	for name, f := range map[string]*fakePeerOS{
		"another user":    {self: "S-1-5-21-1", peer: "S-1-5-21-2", pid: 42},
		"no peer pid":     {self: "S-1-5-21-1", peer: "S-1-5-21-1", pidErr: boom},
		"peer pid 0":      {self: "S-1-5-21-1", peer: "S-1-5-21-1", pid: 0},
		"peer token fail": {self: "S-1-5-21-1", pid: 42, sidErr: boom},
		"peer sid empty":  {self: "S-1-5-21-1", peer: "", pid: 42},
		"self sid fail":   {peer: "S-1-5-21-1", pid: 42, selfErr: boom},
		"both sids empty": {pid: 42},
	} {
		if uid, err := peerUIDVia(f, nil); err == nil {
			t.Errorf("%s: accepted with uid %d", name, uid)
		}
	}
}
