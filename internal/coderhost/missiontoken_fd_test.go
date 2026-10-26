//go:build !windows

package coderhost

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const helperEnv = "CODERHOST_MISSION_HELPER"

// The child side of the end-to-end test: this test binary started again by the
// host side of the test. It reads the token the way the chat child does, then
// reports what a process of the same user could see of it.
func TestMissionTokenChildHelper(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("child half of TestMissionTokenOverPipeEndToEnd")
	}
	StashMissionToken()
	out := os.Stdout
	_, markerLeft := os.LookupEnv(MissionFDEnv)
	tok := TakeMissionToken()
	out.WriteString("TOKEN=" + tok + "\n")
	out.WriteString("TAKE_AGAIN=" + TakeMissionToken() + "\n")
	out.WriteString("MARKER_LEFT=" + strconv.FormatBool(markerLeft) + "\n")
	_, ferr := os.Stat("/proc/self/fd/3")
	out.WriteString("FD3_OPEN=" + strconv.FormatBool(ferr == nil) + "\n")
	env, _ := os.ReadFile("/proc/self/environ")
	cmdline, _ := os.ReadFile("/proc/self/cmdline")
	out.WriteString("IN_ENVIRON=" + strconv.FormatBool(strings.Contains(string(env), "secret-nonce")) + "\n")
	out.WriteString("IN_CMDLINE=" + strconv.FormatBool(strings.Contains(string(cmdline), "secret-nonce")) + "\n")
	// a process the child starts sees neither the marker nor an open descriptor 3
	g, _ := exec.Command("sh", "-c", `[ -e /proc/self/fd/3 ] && echo open || echo closed; env | grep -c -e MONOAGENT_MISSION -e secret-nonce`).Output()
	out.WriteString("GRANDCHILD=" + strings.ReplaceAll(string(g), "\n", "|") + "\n")
}

func TestMissionTokenOverPipeEndToEnd(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMissionTokenChildHelper$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	closeToken, err := AttachMissionToken(cmd, "7.secret-nonce")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	closeToken()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{"TOKEN=7.secret-nonce\n", "TAKE_AGAIN=\n", "MARKER_LEFT=false\n", "FD3_OPEN=false\n", "IN_ENVIRON=false\n", "IN_CMDLINE=false\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("child report lacks %q:\n%s", want, got)
		}
	}
	line := got[strings.Index(got, "GRANDCHILD="):]
	if !strings.Contains(line, "closed|0|") {
		t.Errorf("a process the child starts sees the handoff: %s", line)
	}
}

// The host keeps no copy of the read end and no open write end.
func TestAttachMissionTokenHostSideLeaks(t *testing.T) {
	count := func() int { d, _ := os.ReadDir("/proc/self/fd"); return len(d) }
	before := count()
	cmd := exec.Command("true")
	closeToken, err := AttachMissionToken(cmd, "7.n")
	if err != nil {
		t.Fatal(err)
	}
	if count() != before+1 {
		t.Fatalf("expected exactly the read end open, fds %d -> %d", before, count())
	}
	closeToken()
	if count() != before {
		t.Fatalf("descriptors leaked: %d -> %d", before, count())
	}
	if _, err := AttachMissionToken(exec.Command("true"), "7."+strings.Repeat("a", 300)); err == nil {
		t.Fatal("an oversized token was accepted")
	}
	if count() != before {
		t.Fatalf("descriptors leaked on the error path: %d -> %d", before, count())
	}
}

// stash puts content on a pipe (writer closed unless keepOpen) and points the marker at it.
func stash(t *testing.T, content string, keepOpen bool) {
	t.Helper()
	_ = TakeMissionToken()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString(content)
	if keepOpen {
		t.Cleanup(func() { w.Close() })
	} else {
		w.Close()
	}
	fd, err := syscall.Dup(int(r.Fd()))
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(MissionFDEnv, strconv.Itoa(fd))
}

func TestStashMissionTokenFailsClosed(t *testing.T) {
	for name, in := range map[string]string{
		"no newline":    "7.nonce",
		"empty":         "",
		"no dot":        "7nonce\n",
		"space":         "7.no nce\n",
		"two lines":     "7.a\n7.b\n",
		"oversized":     "7." + strings.Repeat("a", 400) + "\n",
		"negative id":   "-1.n\n",
		"control bytes": "7.n\x01\n",
	} {
		stash(t, in, false)
		if got := TakeMissionToken(); got != "" {
			t.Errorf("%s: accepted %q", name, got)
		}
	}
	stash(t, "7.good\n", false)
	if got := TakeMissionToken(); got != "7.good" {
		t.Errorf("good token = %q", got)
	}
}

func TestStashMissionTokenRefusesNonPipesAndStuckPipes(t *testing.T) {
	_ = TakeMissionToken()
	f, err := os.CreateTemp(t.TempDir(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("7.n\n")
	f.Seek(0, 0)
	fd, _ := syscall.Dup(int(f.Fd()))
	f.Close()
	t.Setenv(MissionFDEnv, strconv.Itoa(fd))
	if got := TakeMissionToken(); got != "" {
		t.Errorf("a regular file was accepted: %q", got)
	}
	if err := syscall.Close(fd); err == nil {
		t.Error("the descriptor named by the marker was left open")
	}
	t.Setenv(MissionFDEnv, "abc")
	if TakeMissionToken() != "" {
		t.Error("a bad marker produced a token")
	}
	// a pipe whose writer never writes nor closes cannot hang the start
	old := missionTokenTimeoutForTest(50 * time.Millisecond)
	defer old()
	stash(t, "", true)
	start := time.Now()
	if TakeMissionToken() != "" || time.Since(start) > 2*time.Second {
		t.Error("a stuck pipe produced a token or hung")
	}
}
