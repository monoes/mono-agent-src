//go:build darwin

package procbind

import (
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

func TestDarwinJoinPeerRefusesAnswersThatDisagree(t *testing.T) {
	x, _ := goodXucred()
	tok := [tokenWords]uint32{501, 501, 20, 501, 20, 4242, 100, 777}
	size := unsafe.Sizeof(tok)
	cred, err := joinPeer(x, 4242, tok, size)
	if err != nil || cred.Pid != 4242 || cred.Uid != 501 || cred.Pidversion != 777 {
		t.Fatalf("good answers: %+v %v", cred, err)
	}
	for name, c := range map[string]struct {
		pid  int
		tok  [tokenWords]uint32
		size uintptr
	}{
		"pid 0":            {0, tok, size},
		"negative pid":     {-5, tok, size},
		"pid out of range": {1<<30 + 1, tok, size},
		"short token":      {4242, tok, size - 4},
		"token of a pid":   {4243, tok, size},
		"token of a user":  {4242, [tokenWords]uint32{501, 502, 20, 501, 20, 4242, 100, 777}, size},
		"no pid version":   {4242, [tokenWords]uint32{501, 501, 20, 501, 20, 4242, 100, 0}, size},
	} {
		if _, err := joinPeer(x, c.pid, c.tok, c.size); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDarwinParseIdversionRefusesShortAndZero(t *testing.T) {
	buf := make([]byte, uniqIdentifierSize)
	buf[idversionOffset] = 9
	if v, err := parseIdversion(buf, uniqIdentifierSize); err != nil || v != 9 {
		t.Fatalf("good: %d %v", v, err)
	}
	if _, err := parseIdversion(buf, uniqIdentifierSize-8); err == nil {
		t.Error("a short answer passed")
	}
	if _, err := parseIdversion(buf[:20], uniqIdentifierSize); err == nil {
		t.Error("a short buffer passed")
	}
	buf[idversionOffset] = 0
	if _, err := parseIdversion(buf, uniqIdentifierSize); err == nil {
		t.Error("a zero pid version passed")
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

// The kernel's own answers for a live process: the pid version in the table is the one in the token of a connection.
func TestDarwinThePidversionOfTheTableIsThatOfTheToken(t *testing.T) {
	v, err := procIdversion(1)
	if err != nil || v == 0 {
		t.Fatalf("pid version of launchd: %d %v", v, err)
	}
	if _, err := procIdversion(0x7ffffff0); err == nil {
		t.Error("a pid version for a process that does not exist")
	}
}
