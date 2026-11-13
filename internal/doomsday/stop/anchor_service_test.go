package stop

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

type countingAnchorOS struct {
	PGroupOS
	starts int
}

func (c *countingAnchorOS) StartAnchor(string) (int, string, error) {
	c.starts++
	return 0, "", errNoPGroupOS
}

func askAnchor(t *testing.T, dir RunDir, req string) string {
	t.Helper()
	c, err := net.DialTimeout("unix", dir.AnchorSockPath(), 2*time.Second)
	mustNoErr(t, err)
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = c.Write([]byte(req))
	mustNoErr(t, err)
	line, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(line)
}

// The anchor socket serves peers of the host's uid only and starts nothing for anyone else or for a malformed request:
// it holds no account state and runs no work of its own, so a locked account gains nothing through it.
func TestAnchorServiceRefusesForeignPeerAndStartsNothingUnasked(t *testing.T) {
	dir := newRunDir(t, nil)
	anchorOS := &countingAnchorOS{}
	peer := uint32(1000)
	srv := &AnchorServer{Dir: dir, UID: 1000, OS: anchorOS, Peer: func(net.Conn) (uint32, error) { return peer, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mustNoErr(t, srv.Start(ctx))

	peer = 1001
	if got := askAnchor(t, dir, "ANCHOR monoagent-run-1-"+strings.Repeat("a", 32)+".service\n"); got != "ERR peer" {
		t.Fatalf("a foreign uid must be refused, got %q", got)
	}
	peer = 1000
	if got := askAnchor(t, dir, "ANCHOR not-a-run-unit\n"); got != "ERR name" {
		t.Fatalf("a name that is not a run unit must be refused, got %q", got)
	}
	if got := askAnchor(t, dir, "HELLO\n"); got != "ERR request" {
		t.Fatalf("a malformed request must be refused, got %q", got)
	}
	if anchorOS.starts != 0 {
		t.Fatalf("nothing may be started for a refused request, started %d", anchorOS.starts)
	}
}
