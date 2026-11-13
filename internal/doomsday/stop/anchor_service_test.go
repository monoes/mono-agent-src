package stop

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// anchorAsk sends one raw request line to the anchor socket and returns the answer line.
func anchorAsk(t *testing.T, dir RunDir, req string) string {
	t.Helper()
	c, err := net.DialTimeout("unix", dir.AnchorSockPath(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(c, req)
	line, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(line)
}

type sessionFakePG struct{ *fakePG }

func (s sessionFakePG) SessionOf(pid int) (int, error) { return s.procs[pid].sid, nil }

func startAnchorServer(t *testing.T, peer uint32) (RunDir, *fakePG) {
	t.Helper()
	d, err := os.MkdirTemp("", "as")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	dir := RunDir{Dir: d}
	pg := newFakePG()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := &AnchorServer{Dir: dir, UID: 501, OS: sessionFakePG{pg}, Peer: func(net.Conn) (uint32, error) { return peer, nil }}
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return dir, pg
}

const goodUnit = "monoagent-run-7-0123456789abcdef0123456789abcdef.service"

func TestAnchorServiceServesTheOperatorAndStartsOneAnchor(t *testing.T) {
	dir, pg := startAnchorServer(t, 501)
	got := anchorAsk(t, dir, "ANCHOR "+goodUnit+"\n")
	if !strings.HasPrefix(got, "OK ") {
		t.Fatalf("the operator's request was not served: %q", got)
	}
	if len(pg.procs) != 2 {
		t.Fatalf("one anchor expected, processes: %d", len(pg.procs))
	}
}

func TestAnchorServiceRefusesAPeerThatIsNotTheOperatorAndStartsNothing(t *testing.T) {
	dir, pg := startAnchorServer(t, 502)
	got := anchorAsk(t, dir, "ANCHOR "+goodUnit+"\n")
	if got != "ERR peer" {
		t.Fatalf("a foreign peer was answered %q", got)
	}
	if len(pg.procs) != 1 {
		t.Fatalf("a refused peer started a process: %d", len(pg.procs))
	}
}

func TestAnchorServiceRefusesAnythingButAUnitNameAndStartsNothing(t *testing.T) {
	dir, pg := startAnchorServer(t, 501)
	for _, req := range []string{"ANCHOR /bin/sh\n", "ANCHOR ../x\n", "ANCHOR\n", "RUN monoagent-run-7-0123456789abcdef0123456789abcdef.service\n", "ANCHOR " + goodUnit + " extra\n"} {
		if got := anchorAsk(t, dir, req); !strings.HasPrefix(got, "ERR") {
			t.Errorf("%q was answered %q", req, got)
		}
	}
	if len(pg.procs) != 1 {
		t.Fatalf("a refused request started a process: %d", len(pg.procs))
	}
}

func TestAnchorServiceSocketIsPrivate(t *testing.T) {
	dir, _ := startAnchorServer(t, 501)
	fi, err := os.Stat(dir.AnchorSockPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("anchor socket mode %v", fi.Mode().Perm())
	}
}
