//go:build linux

package procbind

import (
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A peer of another uid is refused before it is pinned, and the pidfd the kernel gave for it is closed on that path.
func TestServiceClosesThePidfdOfAPeerOfAnotherUID(t *testing.T) {
	self := open(t, os.Getpid())
	var seen atomic.Int64
	seen.Store(-1)
	path, _ := startServer(t, register(t, self), func(s *Server) {
		s.UID = os.Getuid() + 1
		s.peerOf = func(c *net.UnixConn) (unix.Ucred, int, error) {
			cred, fd, err := peer(c)
			if err == nil {
				seen.Store(int64(fd))
			}
			return cred, fd, err
		}
	})
	if _, err := verifyOnce(t, path, goodClient()); err == nil {
		t.Fatal("a peer of another uid was answered")
	}
	fd := int(seen.Load())
	if fd < 0 {
		t.Fatal("the service never looked up the peer")
	}
	stillPidfd := func() bool {
		target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		return err == nil && strings.Contains(target, "pidfd")
	}
	for deadline := time.Now().Add(3 * time.Second); stillPidfd() && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	if stillPidfd() {
		t.Errorf("the pidfd %d of a refused peer is still open", fd)
	}
}
