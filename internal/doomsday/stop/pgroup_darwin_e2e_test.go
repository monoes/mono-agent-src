//go:build darwin

package stop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The real-kernel proof of the macOS unit (#1582): the run's process group is made by the host, in the host's session, and a
// turn the host places in it joins it. The test plays `doomsday start` (its own session); the host is the test binary
// run again in a session of its own, so the two sessions differ as they do in the app.

const helperEnv = "MONOAGENT_PGROUP_E2E_HOST"

// TestHelperAnchorHost is the host: it serves anchors, then places a `sleep` in the unit whose record it is handed.
func TestHelperAnchorHost(t *testing.T) {
	dir := os.Getenv(helperEnv)
	if dir == "" {
		t.Skip("helper process")
	}
	rd := RunDir{Dir: dir}
	if err := ServeAnchors(context.Background(), rd); err != nil {
		fmt.Println("ERR serve:", err)
		os.Exit(1)
	}
	fmt.Println("ready")
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	for in.Scan() {
		var rec UnitRecord
		if err := json.Unmarshal(in.Bytes(), &rec); err != nil {
			fmt.Println("ERR rec:", err)
			continue
		}
		pl, err := (pgContainment{os: darwinPG{}}).Place(rec)
		if err != nil {
			fmt.Println("ERR place:", err)
			continue
		}
		cmd := exec.Command("sleep", "120")
		if _, err := pl.(Placement).Place(cmd); err != nil {
			fmt.Println("ERR place:", err)
			continue
		}
		if err := cmd.Start(); err != nil {
			fmt.Println("ERR start:", err)
			continue
		}
		go func() { _ = cmd.Wait() }()
		fmt.Println("placed", cmd.Process.Pid)
	}
}

func startHost(t *testing.T, dir string) (send func(string) string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperAnchorHost$")
	cmd.Env = append(os.Environ(), helperEnv+"="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	rd := bufio.NewReader(out)
	read := func() string {
		for {
			l, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("the host ended: %v", err)
			}
			l = strings.TrimSpace(l)
			if l == "ready" || strings.HasPrefix(l, "placed") || strings.HasPrefix(l, "ERR") {
				return l
			}
		}
	}
	if l := read(); l != "ready" {
		t.Fatalf("host: %s", l)
	}
	return func(line string) string { fmt.Fprintln(in, line); return read() }
}

func TestDarwinRealUnitIsMadeByTheHostAndTurnsJoinIt(t *testing.T) {
	if os.Getenv(helperEnv) != "" {
		t.Skip("helper process")
	}
	dir, err := os.MkdirTemp("/tmp", "pgu")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	rd := RunDir{Dir: dir}
	host := startHost(t, dir)

	// the creator: this process, in another session than the host
	rec, err := (pgContainment{os: darwinPG{}, viaHost: true}).Create(context.Background(), CreateArgs{Dir: rd}, 1)
	if err != nil {
		t.Fatalf("the unit was not made: %v", err)
	}
	t.Cleanup(func() { _ = unix.Kill(-rec.AnchorPID, unix.SIGKILL) })
	var h pgHandle
	_ = json.Unmarshal([]byte(rec.Handle), &h)
	mine, _ := unix.Getsid(0)
	if h.Sid == mine || h.Sid <= 1 {
		t.Fatalf("the group lives in session %d, which is the creator's (%d) or unset: it must be the host's", h.Sid, mine)
	}
	// the creator cannot place into it (the old, broken path), the host can
	if _, err := (pgContainment{os: darwinPG{}}).Place(rec); err != nil {
		t.Fatal(err)
	}
	pl, _ := (pgContainment{os: darwinPG{}}).Place(rec)
	if _, err := pl.(Placement).Place(exec.Command("sleep", "1")); err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("the creator's placement was not refused for its session: %v", err)
	}
	b, _ := json.Marshal(rec)
	ans := host(string(b))
	var pid int
	if _, err := fmt.Sscanf(ans, "placed %d", &pid); err != nil {
		t.Fatalf("the host could not place a turn: %s", ans)
	}

	// the proof still holds from this process, and the placed turn is a member
	if p, err := proveGroup(darwinPG{}, rec); err != nil || p.Cg == nil {
		t.Fatalf("the unit fails its identity proof: %v", err)
	}
	if pg, _ := unix.Getpgid(pid); pg != rec.AnchorPID {
		t.Fatalf("the placed turn is in group %d, want %d", pg, rec.AnchorPID)
	}
	ms, _ := darwinPG{}.Members(rec.AnchorPID)
	found := false
	for _, m := range ms {
		found = found || m == pid
	}
	if !found {
		t.Fatalf("the group %v does not list the placed turn %d", ms, pid)
	}
	// Stop ends the group, turn and anchor, and the signaller is not in it
	if err := (darwinPG{}).SignalGroup(rec.AnchorPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		if ms, _ := (darwinPG{}).Members(rec.AnchorPID); len(ms) == 0 {
			break
		}
		if i > 100 {
			t.Fatal("the group still holds a process after the kill")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDarwinNoHostMeansNoUnit(t *testing.T) {
	oldT, oldE := anchorDialTries, anchorDialEvery
	anchorDialTries, anchorDialEvery = 2, time.Millisecond
	t.Cleanup(func() { anchorDialTries, anchorDialEvery = oldT, oldE })
	dir, _ := os.MkdirTemp("/tmp", "pgn")
	t.Cleanup(func() { os.RemoveAll(dir) })
	_, err := (pgContainment{os: darwinPG{}, viaHost: true}).Create(context.Background(), CreateArgs{Dir: RunDir{Dir: dir}}, 1)
	if err == nil || !strings.Contains(err.Error(), "coder host is not running") {
		t.Fatalf("a unit was made without a host (or the refusal does not say why): %v", err)
	}
}

// A unit the store refused the entry for is discarded: the host link ends its group by name and the record goes, so the
// generation's next attempt starts clean.
func TestDarwinDiscardEndsTheHostMadeGroupAndRemovesItsRecord(t *testing.T) {
	if os.Getenv(helperEnv) != "" {
		t.Skip("helper process")
	}
	dir, _ := os.MkdirTemp("/tmp", "pgd")
	t.Cleanup(func() { os.RemoveAll(dir) })
	rd := RunDir{Dir: dir}
	startHost(t, dir)
	rec, err := (pgContainment{os: darwinPG{}, viaHost: true}).Create(context.Background(), CreateArgs{Dir: rd}, 7)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Kill(-rec.AnchorPID, unix.SIGKILL) })
	if err := (UnitCreator{Dir: rd, Host: SystemHost()}).Discard(context.Background(), rec); err != nil {
		t.Fatalf("discard: %v", err)
	}
	for i := 0; ; i++ {
		if ms, _ := (darwinPG{}).Members(rec.AnchorPID); len(ms) == 0 {
			break
		}
		if i > 100 {
			t.Fatal("the discarded unit's group still holds a process")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok, _ := rd.ReadUnitFile(7); ok {
		t.Fatal("the unit record is still there")
	}
}

// A host that is started just as the run is, and serves its anchors a moment after the creator first looks, is waited for.
func TestDarwinCreatorWaitsForAHostThatIsStillStarting(t *testing.T) {
	if os.Getenv(helperEnv) != "" {
		t.Skip("helper process")
	}
	dir, _ := os.MkdirTemp("/tmp", "pgw")
	t.Cleanup(func() { os.RemoveAll(dir) })
	go func() { time.Sleep(600 * time.Millisecond); startHost(t, dir) }()
	rec, err := (pgContainment{os: darwinPG{}, viaHost: true}).Create(context.Background(), CreateArgs{Dir: RunDir{Dir: dir}}, 3)
	if err != nil {
		t.Fatalf("the creator did not wait for the host: %v", err)
	}
	_ = unix.Kill(-rec.AnchorPID, unix.SIGKILL)
}
