package stop

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// maxAnchors is how many anchors this host keeps alive at once: one run has one, and a peer of the operator's uid
	// must not be able to fill the process table through the socket.
	maxAnchors = 16
	// maxAnchorRequest bounds the one request line (a unit name is under 80 bytes).
	maxAnchorRequest = 256
)

// The anchor service is how a macOS run's process group is made in the host's own session.
//
// A process can only join a process group of its own session (setpgid is refused with EPERM across sessions), and the
// host places the boss's turn into the run's group. The group's anchor must therefore be started by the host, not by the
// `doomsday start` process that creates the run (a different session). That process asks the host over a socket in the run
// directory: it sends the unit's name and gets back the anchor's pid, start stamp and session. Everything else about the
// unit (the journal's intent and binding, the identity proof, the unit file) stays where it was.
//
// The service starts nothing but the anchor: a blocking system binary under a unit name of the form createPGroup makes. A
// peer of the operator's UID is the only one served, and a peer that is anyone else is refused, as on the emergency
// socket. A request without a run behind it leaves an idle process that ends with the host's kill or the user's (it is
// named monoagent-run-<generation>-<nonce>.service, and the proof of any group refuses a name that is not the record's).

// AnchorSockPath is the anchor service's socket, in the private run directory.
func (r RunDir) AnchorSockPath() string { return r.Dir + "/anchor.sock" }

// ErrNoAnchorService means no host serves anchors for the run directory: the unit cannot be made, so the run is not.
var ErrNoAnchorService = errors.New("the coder host is not running (nothing serves the run's process group): start it (an older host does not serve this: `monoagentcli coder host stop` and start again)")

// AnchorServer is the host's end: it starts anchors in the host's session through OS.
type AnchorServer struct {
	Dir  RunDir
	UID  uint32
	OS   PGroupOS
	Peer func(net.Conn) (uint32, error) // defaults to the platform implementation

	mu      sync.Mutex
	started []startedAnchor
}

type startedAnchor struct {
	pid   int
	stamp string
}

// reserve counts the anchors this server started that are still the same live process, and says whether one more may start.
func (s *AnchorServer) reserve() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.started[:0]
	for _, a := range s.started {
		if st, _, err := s.OS.ProcInfo(a.pid); err == nil && st == a.stamp {
			live = append(live, a)
		}
	}
	s.started = live
	return len(s.started) < maxAnchors
}

func (s *AnchorServer) record(pid int, stamp string) {
	s.mu.Lock()
	s.started = append(s.started, startedAnchor{pid, stamp})
	s.mu.Unlock()
}

// Start binds the socket and serves in the background until ctx ends.
func (s *AnchorServer) Start(ctx context.Context) error {
	if s.OS == nil {
		return errNoPGroupOS
	}
	if err := s.Dir.fsys().MkdirAll(s.Dir.Dir, 0o700); err != nil {
		return err
	}
	// The directory may exist already with looser rights; the socket is only reachable through a directory of the operator's,
	// so there is no moment at which it is open to anyone else between its creation and its chmod.
	if err := os.Chmod(s.Dir.Dir, 0o700); err != nil {
		return err
	}
	path := s.Dir.AnchorSockPath()
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	go func() { <-ctx.Done(); ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	return nil
}

func (s *AnchorServer) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	peer := s.Peer
	if peer == nil {
		peer = peerUID
	}
	if uid, err := peer(c); err != nil || uid != s.UID {
		fmt.Fprintln(c, "ERR peer")
		return
	}
	line, err := bufio.NewReader(io.LimitReader(c, maxAnchorRequest)).ReadString('\n')
	if err != nil {
		return
	}
	f := strings.Fields(line)
	if len(f) != 2 || f[0] != "ANCHOR" {
		fmt.Fprintln(c, "ERR request")
		return
	}
	if _, _, ok := UnitGeneration(f[1]); !ok {
		fmt.Fprintln(c, "ERR name")
		return
	}
	if !s.reserve() {
		fmt.Fprintln(c, "ERR busy")
		return
	}
	pid, stamp, err := s.OS.StartAnchor(f[1])
	if err != nil {
		if pid > 0 {
			_ = s.OS.KillAnchor(pid, stamp)
		}
		fmt.Fprintf(c, "ERR start: %s\n", strings.ReplaceAll(err.Error(), "\n", " "))
		return
	}
	sid := 0
	if so, ok := s.OS.(interface{ SessionOf(int) (int, error) }); ok {
		if v, serr := so.SessionOf(pid); serr == nil {
			sid = v
		}
	}
	if sid <= 0 {
		_ = s.OS.KillAnchor(pid, stamp)
		fmt.Fprintln(c, "ERR session")
		return
	}
	s.record(pid, stamp)
	fmt.Fprintf(c, "OK %d %s %d\n", pid, stamp, sid)
}

var (
	anchorDialTries = 100
	anchorDialEvery = 100 * time.Millisecond
)

// hostAnchorOS is the PGroupOS of the process that creates the unit (`doomsday start`): every operation is the local one,
// except that the anchor is started by the host, and the session its group lives in is the one the host reported.
type hostAnchorOS struct {
	PGroupOS
	dir  RunDir
	sids map[int]int
}

func newHostAnchorOS(o PGroupOS, dir RunDir) *hostAnchorOS {
	return &hostAnchorOS{PGroupOS: o, dir: dir, sids: map[int]int{}}
}

func (h *hostAnchorOS) StartAnchor(argv0 string) (int, string, error) {
	// A host that was just started takes its lock before it serves anchors: give it a few seconds.
	var c net.Conn
	var err error
	for i := 0; i < anchorDialTries; i++ {
		if c, err = net.DialTimeout("unix", h.dir.AnchorSockPath(), 2*time.Second); err == nil {
			break
		}
		time.Sleep(anchorDialEvery)
	}
	if err != nil {
		return 0, "", fmt.Errorf("%w (%v)", ErrNoAnchorService, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintf(c, "ANCHOR %s\n", argv0); err != nil {
		return 0, "", fmt.Errorf("%w (%v)", ErrNoAnchorService, err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return 0, "", fmt.Errorf("the coder host did not answer for the run's process group: %w", err)
	}
	f := strings.Fields(line)
	if len(f) != 4 || f[0] != "OK" {
		return 0, "", fmt.Errorf("the coder host refused to start the run's process group: %s", strings.TrimSpace(line))
	}
	pid, perr := strconv.Atoi(f[1])
	sid, serr := strconv.Atoi(f[3])
	if perr != nil || serr != nil || pid <= 1 || sid <= 1 {
		return 0, "", fmt.Errorf("the coder host's answer for the run's process group is malformed: %s", strings.TrimSpace(line))
	}
	h.sids[pid] = sid
	return pid, f[2], nil
}

func (h *hostAnchorOS) SessionOf(pid int) (int, error) {
	if sid, ok := h.sids[pid]; ok {
		return sid, nil
	}
	return 0, errors.New("the session of that process is not known")
}
