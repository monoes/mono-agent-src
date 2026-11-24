package chat

import (
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
)

// The org-design write tools hold the profile's org rows lock for their
// load-write-save, as `org` commands do, so a stale document cannot overwrite
// a concurrent edit (r06-R06-13).
func TestOrgDesignWriteToolsWaitForTheRowsLock(t *testing.T) {
	mt := newOrgTestTools(t)
	if _, err := mt.Execute("create_org", `{"name":"acme","goal":"Ship"}`); err != nil {
		t.Fatal(err)
	}
	release, err := orgenv.LockRows(mt.profileRoot())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := mt.Execute("add_org_role", `{"org_name":"acme","title":"Writer","reports_to":"lead"}`)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("add_org_role finished while another process held the org rows lock (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("add_org_role did not finish after the lock was released")
	}
}
