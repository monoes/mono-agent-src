//go:build !windows

package chat

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// host.json can be written by any process of the user: `coder host status` and `stop` read it through the bounded reader, so a
// pipe, a link to a device and a huge file neither hang nor fill memory, and are read as "no usable host file".
func TestHostInfoIsReadOnlyFromAPlainBoundedFile(t *testing.T) {
	cfg := runningHostWith(t, nil)
	path := filepath.Join(hostDir(cfg), hostInfoFile)
	plain, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := readHostInfo(cfg); !ok || h.Instance != "host-test" {
		t.Fatalf("the plain file was not read: %+v %v", h, ok)
	}
	replace := func(make func()) {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		make()
	}
	cases := map[string]func(){
		"a pipe":             func() { replace(func() { _ = syscall.Mkfifo(path, 0o600) }) },
		"a link to a device": func() { replace(func() { _ = os.Symlink("/dev/zero", path) }) },
		"a link to a file": func() {
			replace(func() { _ = os.Symlink(path+".real", path); _ = os.WriteFile(path+".real", plain, 0o600) })
		},
		"an oversize file": func() {
			replace(func() {
				_ = os.WriteFile(path, []byte(`{"instance":"x","pad":"`+strings.Repeat("a", 70<<10)+`"}`), 0o600)
			})
		},
	}
	for name, setup := range cases {
		setup()
		done := make(chan bool, 1)
		go func() { _, ok := readHostInfo(cfg); done <- ok }()
		select {
		case ok := <-done:
			if ok {
				t.Errorf("%s was read as a host file", name)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the reader hangs", name)
		}
		if st := hostStatus(cfg); st["pid"] != nil {
			t.Errorf("%s: status shows a host from it: %v", name, st)
		}
	}
}

// What the status shows of the pursuit facts in the file has passed the sanitiser at the read.
func TestHostInfoPursuitFactsAreSanitisedAtTheRead(t *testing.T) {
	cfg := runningHostWith(t, nil)
	path := filepath.Join(hostDir(cfg), hostInfoFile)
	if err := os.WriteFile(path, []byte(`{"pid":1,"instance":"i","pursuit":{"enabled":true,"pin":"no pin here","wakeCapUsd":0.0001,"reason":"forged"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h, ok := readHostInfo(cfg)
	if !ok || h.Pursuit == nil || !h.Pursuit.Invalid || h.Pursuit.Reason != "" || h.Pursuit.WakeCapUSD != 0 {
		t.Fatalf("%+v %v", h.Pursuit, ok)
	}
}
