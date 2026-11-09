//go:build darwin

package autostart

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A restart is launchd's own: kickstart -k, which kills the running instance and starts the job
// again, for the job of this user's domain. Nothing here runs launchctl.
func TestRestartKicksTheJobAndKillsTheRunningInstance(t *testing.T) {
	var calls [][]string
	orig := launchctl
	t.Cleanup(func() { launchctl = orig })
	launchctl = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return []byte("kicked"), nil
	}
	if err := (darwinInstaller{}).Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || strings.Join(calls[0], " ") != "kickstart -k "+launchdDomain()+"/"+Label {
		t.Errorf("launchctl was run as %v", calls)
	}

	launchctl = func(context.Context, ...string) ([]byte, error) {
		return []byte("Could not find service"), errors.New("exit status 113")
	}
	err := (darwinInstaller{}).Restart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "launchctl kickstart -k") || !strings.Contains(err.Error(), "exit status 113") || !strings.Contains(err.Error(), "Could not find service") {
		t.Errorf("a failed kickstart should say what ran and what launchctl said: %v", err)
	}
}

// A plist alone isn't "starts at login": launchd must have the job loaded.
func TestStatusNeedsTheJobLoaded(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	ctx := context.Background()
	loaded := false
	var calls [][]string
	orig := launchctl
	t.Cleanup(func() { launchctl = orig })
	launchctl = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if loaded {
			return []byte("state = running\n"), nil
		}
		return []byte("Could not find service\n"), errors.New("exit status 113")
	}
	in := darwinInstaller{}
	if ok, where := in.Status(ctx); ok || where != "" || len(calls) != 0 {
		t.Fatalf("no plist: %v %q, calls %v", ok, where, calls)
	}
	path, _ := plistPath()
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("<plist/>"), 0o644)
	if ok, where := in.Status(ctx); ok || !strings.Contains(where, "not loaded") {
		t.Fatalf("not loaded: %v %q", ok, where)
	}
	loaded = true
	if ok, where := in.Status(ctx); !ok || where != path {
		t.Fatalf("loaded: %v %q", ok, where)
	}
	if got := strings.Join(calls[len(calls)-1], " "); got != "print "+launchdDomain()+"/"+Label {
		t.Fatalf("launchctl %s", got)
	}
}

// The daemon serves every profile, so the job names none.
func TestThePlistNamesNoProfile(t *testing.T) {
	plist, err := renderPlist("/usr/local/bin/monoagentcli", "/Users/ada/.monoagent/logs")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plist, "--profile") || !strings.Contains(plist, "<string>daemon</string>") {
		t.Fatalf("the plist should run plain `daemon`:\n%s", plist)
	}
}
