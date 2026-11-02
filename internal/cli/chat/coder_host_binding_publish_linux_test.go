//go:build linux

package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/storage"
)

func readInfo(t *testing.T, path string) hostInfo {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return hostInfo{}
	}
	var h hostInfo
	_ = json.Unmarshal(raw, &h)
	return h
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A service that starts shows up in the info file with its start number and counts, and its registrations are counted;
// after Shutdown the section is gone.
func TestThePublisherShowsARunningServiceAndItsCounts(t *testing.T) {
	if !procbind.PeerPidfdSupported() {
		t.Skip("SO_PEERPIDFD needs Linux 6.5")
	}
	old := bindingPublishEvery
	bindingPublishEvery = 10 * time.Millisecond
	t.Cleanup(func() { bindingPublishEvery = old })
	db, err := storage.NewDatabase(newChatCLITestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root, err := os.MkdirTemp("", "bp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	binding := &coderhost.BindingHost{DB: db.DB, Binder: procbind.NewBinder(time.Now), Root: root}
	t.Cleanup(binding.Shutdown)
	infoPath := filepath.Join(t.TempDir(), hostInfoFile)
	info := &hostInfo{PID: 1, Instance: "host-test"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); publishBindingStatus(ctx, info, infoPath, binding) }()
	t.Cleanup(func() { cancel(); <-done })

	if err := binding.Ensure(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the running service in the info file", func() bool {
		b := readInfo(t, infoPath).Binding
		return len(b) == 1 && b[0].Running && b[0].Start == 1 && b[0].Active == 0
	})
	procs, err := procbind.NewLinuxProcs()
	if err != nil {
		t.Fatal(err)
	}
	self, err := procs.Open(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	gen, err := binding.Binder.Register(procbind.EntrySpec{TokenID: "t", Kind: procbind.KindSession, Profile: "default", Scope: "session:7", Session: "c", Turn: "t", Expires: time.Now().Add(time.Hour)}, self)
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Binder.Activate("default", "t", gen); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the active registration to be counted", func() bool {
		b := readInfo(t, infoPath).Binding
		return len(b) == 1 && b[0].Active == 1
	})
	binding.Shutdown()
	waitFor(t, "the section to go after Shutdown", func() bool { return len(readInfo(t, infoPath).Binding) == 0 })
}
