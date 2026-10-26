//go:build !windows

package chat

import (
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// handOffMissionToken does what the host does for the chat child: the token on a
// pipe whose descriptor the marker names. An empty token hands nothing over.
func handOffMissionToken(t *testing.T, token string) {
	t.Helper()
	_ = coderhost.TakeMissionToken()
	t.Setenv(coderhost.MissionFDEnv, "")
	if token == "" {
		return
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString(token + "\n")
	w.Close()
	fd, err := syscall.Dup(int(r.Fd())) // the code under test takes ownership of this descriptor
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(coderhost.MissionFDEnv, strconv.Itoa(fd))
}
