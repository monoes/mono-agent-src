//go:build !windows

package meshclient

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Ids are computed as monomind's project-id.ts computes them; the vectors come
// from running that code under node.
func TestProjectIDMatchesMonomind(t *testing.T) {
	for path, want := range map[string]string{
		"/nonexistent/proj one": "proj-one-e2073fd39a926fca",
		"/":                     "project-8a5edab282632443",
		"/nonexistent/ünï":      "-n--444437972e00e7ae",
		"/nonexistent/a😀b":      "a--b-5f9bdbd42f069291", // an emoji is two UTF-16 units, so two dashes
	} {
		if got := ProjectID(path); got != want {
			t.Errorf("ProjectID(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestCoderAliasFitsTheHubLimitAndIsStable(t *testing.T) {
	started := time.Date(2026, 10, 26, 9, 5, 7, 0, time.Local)
	got := CoderAlias("/home/u/my project", started)
	if got != "coder-my-project-20261026-090507" {
		t.Fatalf("alias = %q", got)
	}
	long := CoderAlias("/x/"+strings.Repeat("n", 200), started)
	if len(long) > 61 { // 64 minus room for a -NN suffix
		t.Fatalf("alias is %d characters: %s", len(long), long)
	}
	if CoderAlias("/", started) != "coder-project-20261026-090507" {
		t.Fatal("a root folder has no name")
	}
}

// fakeHub answers one request per connection from handler, on a socket in a
// private directory.
func fakeHub(t *testing.T, handler func(req map[string]any) map[string]any) (dir string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "mc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "hub.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return
				}
				var req map[string]any
				_ = json.Unmarshal(line, &req)
				out, _ := json.Marshal(handler(req))
				c.Write(append(out, '\n'))
			}()
		}
	}()
	return dir
}

func TestCallRoundTripsOneJSONLine(t *testing.T) {
	dir := fakeHub(t, func(req map[string]any) map[string]any {
		return map[string]any{"id": req["id"], "ok": true, "echo": req["op"]}
	})
	reply, err := Client{Dir: dir}.Call(map[string]any{"op": "who", "id": "r1"})
	if err != nil || reply["ok"] != true || reply["echo"] != "who" || reply["id"] != "r1" {
		t.Fatalf("reply = %v, %v", reply, err)
	}
}

func TestCallIsOfflineWhenThereIsNoHubOrTheDirIsNotPrivate(t *testing.T) {
	if _, err := (Client{Dir: filepath.Join(t.TempDir(), "none")}).Call(map[string]any{"op": "who"}); !errors.Is(err, ErrOffline) {
		t.Fatalf("no directory: %v", err)
	}
	dir := fakeHub(t, func(map[string]any) map[string]any { return map[string]any{"ok": true} })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Client{Dir: dir}.Call(map[string]any{"op": "who"})
	if !errors.Is(err, ErrOffline) || !strings.Contains(err.Error(), "not private") {
		t.Fatalf("a group-readable directory must be refused before anything is sent: %v", err)
	}
}

func TestCallTimesOutOnASilentHubAndRefusesAMalformedReply(t *testing.T) {
	silent := fakeHub(t, func(map[string]any) map[string]any { time.Sleep(2 * time.Second); return nil })
	start := time.Now()
	if _, err := (Client{Dir: silent, Timeout: 100 * time.Millisecond}).Call(map[string]any{"op": "who"}); !errors.Is(err, ErrOffline) || time.Since(start) > time.Second {
		t.Fatalf("silent hub: %v after %s", err, time.Since(start))
	}
	dir, _ := os.MkdirTemp("", "mc")
	t.Cleanup(func() { os.RemoveAll(dir) })
	os.Chmod(dir, 0o700)
	ln, _ := net.Listen("unix", filepath.Join(dir, "hub.sock"))
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err == nil {
			bufio.NewReader(c).ReadBytes('\n')
			c.Write([]byte("not json\n"))
			c.Close()
		}
	}()
	if _, err := (Client{Dir: dir}).Call(map[string]any{"op": "who"}); !errors.Is(err, ErrOffline) {
		t.Fatalf("malformed reply: %v", err)
	}
}
