//go:build darwin

package procbind

import (
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The checks on what the kernel answers, on answers built to be wrong. The real peer() calls exactly these.

func goodXucred() (*unix.Xucred, uintptr) {
	x := &unix.Xucred{Version: 0, Uid: 501, Ngroups: 1}
	x.Groups[0] = 20
	return x, unsafe.Sizeof(*x)
}

func TestDarwinCheckXucredRefusesWhatIsNotAFilledInCredential(t *testing.T) {
	x, n := goodXucred()
	if err := checkXucred(x, n); err != nil {
		t.Fatalf("a good credential: %v", err)
	}
	if err := checkXucred(x, n-4); err == nil {
		t.Error("a short answer passed")
	}
	if err := checkXucred(x, n+4); err == nil {
		t.Error("a long answer passed")
	}
	bad := *x
	bad.Version = 1
	if err := checkXucred(&bad, n); err == nil {
		t.Error("another version passed")
	}
	zero := &unix.Xucred{} // untouched: uid 0, no groups
	if err := checkXucred(zero, n); err == nil {
		t.Error("a zeroed credential passed")
	}
}

func TestDarwinJoinPeerRefusesWhatIsNotAProcessNumber(t *testing.T) {
	x, _ := goodXucred()
	cred, err := joinPeer(x, 4242)
	if err != nil || cred.Pid != 4242 || cred.Uid != 501 {
		t.Fatalf("good answers: %+v %v", cred, err)
	}
	for _, pid := range []int{0, -5, 1<<30 + 1} {
		if _, err := joinPeer(x, pid); err == nil {
			t.Errorf("pid %d accepted", pid)
		}
	}
}

func TestDarwinSocketPCBsRefuseWhatIsNotAConnectedUnixSocket(t *testing.T) {
	good := make([]byte, socketFDInfoSize)
	*(*uint64)(unsafe.Pointer(&good[offPCB])) = 7
	*(*uint64)(unsafe.Pointer(&good[offConnPCB])) = 9
	*(*int32)(unsafe.Pointer(&good[offFamily])) = afUnix
	if own, conn, err := socketPCBs(good, socketFDInfoSize); err != nil || own != 7 || conn != 9 {
		t.Fatalf("good: %d %d %v", own, conn, err)
	}
	if _, _, err := socketPCBs(good, socketFDInfoSize-8); err == nil {
		t.Error("a short answer passed")
	}
	if _, _, err := socketPCBs(good[:100], socketFDInfoSize); err == nil {
		t.Error("a short buffer passed")
	}
	inet := append([]byte(nil), good...)
	*(*int32)(unsafe.Pointer(&inet[offFamily])) = 2
	if _, _, err := socketPCBs(inet, socketFDInfoSize); err == nil {
		t.Error("an inet socket passed")
	}
	for _, off := range []int{offPCB, offConnPCB} {
		z := append([]byte(nil), good...)
		*(*uint64)(unsafe.Pointer(&z[off])) = 0
		if _, _, err := socketPCBs(z, socketFDInfoSize); err == nil {
			t.Errorf("a zero control block at %d passed", off)
		}
	}
}

// On the real table: the process that was connected to holds the connection; another process does not.
func TestDarwinHoldsConnectionOnTheRealTable(t *testing.T) {
	path := filepath.Join(shortDir(t), "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	nc := exec.Command("nc", "-U", path)
	in, _ := nc.StdinPipe()
	if err := nc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = nc.Process.Kill(); _ = nc.Wait() }()
	c, err := l.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := holdsConnection(c, nc.Process.Pid); err != nil {
		t.Errorf("the connector does not hold its connection: %v", err)
	}
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Process.Kill(); _ = other.Wait() }()
	if err := holdsConnection(c, other.Process.Pid); err == nil {
		t.Error("an unrelated process was taken to hold the connection")
	}
	if err := holdsConnection(c, 0x7ffffff0); err == nil {
		t.Error("a process that does not exist was taken to hold the connection")
	}
}

func TestDarwinInfoOfATableEntry(t *testing.T) {
	var k unix.KinfoProc
	k.Proc.P_starttime = unix.Timeval{Sec: 5, Usec: 3}
	k.Eproc.Ppid = 42
	if pp, st, err := infoOf(&k); err != nil || pp != 42 || st != 5_000_003 {
		t.Fatalf("good: %d %d %v", pp, st, err)
	}
	if _, _, err := infoOf(nil); err == nil {
		t.Error("a missing entry passed")
	}
	z := k
	z.Proc.P_stat = sZOMB
	if _, _, err := infoOf(&z); err == nil {
		t.Error("a zombie passed")
	}
	k.Proc.P_starttime = unix.Timeval{}
	if _, _, err := infoOf(&k); err == nil {
		t.Error("an entry with no start time passed")
	}
}
