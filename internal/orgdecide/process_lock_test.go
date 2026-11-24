package orgdecide

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// While another process (the CLI's `org process`, the daemon's tick) is
// processing an org, a second pass decides nothing: neither asks the
// decider again nor applies to monomind a second time.
func TestProcessOrgSkipsWhileAnotherProcessHoldsTheOrg(t *testing.T) {
	org := standardItems()
	dec := &scriptedDecider{}
	s := newTestService(t, org, dec)
	setLevel(t, s, orgdesign.LevelFull)
	root := t.TempDir()

	release, err := daemonhb.LockFile(processLockPath(root, "growth"))
	if err != nil {
		t.Fatal(err)
	}
	ds, err := s.ProcessOrg(context.Background(), "p", root, "growth")
	if err != nil || len(ds) != 0 || len(org.resolved) != 0 || len(dec.prompts) != 0 {
		t.Fatalf("decided under another process's claim: %v %v resolved=%v prompts=%d", ds, err, org.resolved, len(dec.prompts))
	}
	release()

	ds, err = s.ProcessOrg(context.Background(), "p", root, "growth")
	if err != nil || len(ds) == 0 {
		t.Fatalf("after the claim was released: %v %v", ds, err)
	}
}
