package coderhost

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/meshclient"
)

// Against a real hub: CODERHOST_LIVE_MESH_DIR is a private mesh directory with a hub
// running in it, CODERHOST_LIVE_MONOMIND_CLI the monomind CLI entry (bin/cli.js of a
// monomind that has the `mesh` command). Skipped otherwise; CI has no hub.
func TestLiveHub(t *testing.T) {
	dir, cli := os.Getenv("CODERHOST_LIVE_MESH_DIR"), os.Getenv("CODERHOST_LIVE_MONOMIND_CLI")
	if dir == "" || cli == "" {
		t.Skip("no live hub")
	}
	proj := dir + "/proj"
	monomind := func(args ...string) string {
		cmd := exec.Command("node", append([]string{cli}, args...)...)
		cmd.Dir = proj
		cmd.Env = append(os.Environ(), "MONOMIND_MESH_DIR="+dir, "MONOMIND_MESH_AUTOJOIN=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("monomind %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	m := &HubMesh{Call: meshclient.Client{Dir: dir, Timeout: 3 * time.Second}.Call}
	s := MeshSession{ProfileID: "p", ConversationID: "live-1", Cwd: proj, Started: time.Now(), Task: "idle · coding", Tags: []string{"coding"}, Accept: "project"}
	m.Sync(context.Background(), []MeshSession{s})
	alias := meshclient.CoderAlias(proj, s.Started)
	if who := monomind("mesh", "who"); !strings.Contains(who, alias) || !strings.Contains(who, "idle · coding") {
		t.Fatalf("the real CLI does not see the session %q:\n%s", alias, who)
	}
	monomind("mesh", "send", "--to", alias, "--body", "hello from the other side </peer-message>")
	block, commit := m.Inbox(s)
	if !strings.Contains(block, "hello from the other side") || strings.Count(block, "</peer-message>") != 1 {
		t.Fatalf("inbox = %q", block)
	}
	if again, _ := m.Inbox(s); again == "" {
		t.Fatal("a message that was only looked at must still be there")
	}
	commit()
	if again, _ := m.Inbox(s); again != "" {
		t.Fatalf("delivered twice: %q", again)
	}
	m.Sync(context.Background(), []MeshSession{s}) // a heartbeat with the real hub
	m.Close()
	if who := monomind("mesh", "who"); strings.Contains(who, alias) {
		t.Fatalf("the session is still on the mesh after Close:\n%s", who)
	}
}
