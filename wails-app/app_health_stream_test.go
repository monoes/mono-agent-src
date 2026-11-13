//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCLI writes an executable shell script standing in for monoagentcli.
func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), fakeCLIName)
	if err := writeFakeCLI(p, "#!/bin/sh\n"+body); err != nil {
		t.Fatal(err)
	}
	return p
}

type eventLog struct {
	mu  sync.Mutex
	evs []healthFixEvent
}

func (l *eventLog) emit(ev healthFixEvent) { l.mu.Lock(); l.evs = append(l.evs, ev); l.mu.Unlock() }

func (l *eventLog) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, ev := range l.evs {
		out = append(out, ev.Kind+":"+ev.Message)
	}
	return out
}

func stream(t *testing.T, cli string, run *healthRun, ctx context.Context) (healthFixEvent, *eventLog) {
	t.Helper()
	if ctx == nil {
		ctx = context.Background()
	}
	log := &eventLog{}
	final := (&App{}).streamHealthFix(ctx, run, cli, "core.db.migrate", nil, log.emit)
	return final, log
}

func TestStreamHealthFixRelaysLinesAndTheFinalEvent(t *testing.T) {
	cli := fakeCLI(t, `echo 'plain output, not JSON'
echo '{"kind":"line","message":"applying migrations"}'
echo '{"no_kind":true}'
echo '{"kind":"done","message":"database ready"}'
`)
	final, log := stream(t, cli, nil, nil)
	want := []string{"line:plain output, not JSON", "line:applying migrations", `line:{"no_kind":true}`}
	if got := log.messages(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	if final.Kind != "done" || final.Message != "database ready" || final.FixID != "core.db.migrate" {
		t.Fatalf("final = %+v", final)
	}
}

func TestStreamHealthFixWithoutFinalEvent(t *testing.T) {
	cli := fakeCLI(t, `echo 'working'
echo 'npm ERR! code EACCES' >&2
exit 3
`)
	final, log := stream(t, cli, nil, nil)
	if got := log.messages(); len(got) != 1 || got[0] != "line:working" {
		t.Fatalf("lines = %q", got)
	}
	if final.Kind != "error" || !strings.Contains(final.Message, "exit status 3") || !strings.Contains(final.Message, "npm ERR! code EACCES") {
		t.Fatalf("final = %+v, want an error with the exit status and stderr", final)
	}

	// Exit 0 without a final event is still not a success.
	final, _ = stream(t, fakeCLI(t, "echo hi\n"), nil, nil)
	if final.Kind != "error" || !strings.Contains(final.Message, "without reporting a result") {
		t.Fatalf("final = %+v", final)
	}
}

// Cancel asks the CLI to stop with SIGTERM (it can end its installers)
// instead of killing it, and the result says it was cancelled.
func TestStreamHealthFixCancelTerminatesGently(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "got-term")
	cli := fakeCLI(t, `trap 'echo term > `+marker+`; exit 143' TERM
echo '{"kind":"line","message":"downloading"}'
while :; do sleep 0.05; done
`)
	run, ctx, ok := beginHealthRun(context.Background(), "test.cancel", time.Minute)
	if !ok {
		t.Fatal("run did not start")
	}
	defer endHealthRun("test.cancel", run)
	go func() {
		time.Sleep(300 * time.Millisecond)
		(&App{}).CancelHealthRun("test.cancel")
	}()
	start := time.Now()
	final, _ := stream(t, cli, run, ctx)
	if !final.Cancelled || final.Kind != "error" {
		t.Fatalf("final = %+v, want cancelled", final)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the CLI was not sent SIGTERM (its trap did not run)")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancel waited for the grace period although the CLI stopped")
	}
}

// A successful Node or runtime install activates the managed Node again
// before the final event goes out (#137 item 4); a failed one does not.
func TestStartStreamedActivatesNodeAfterInstall(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	prev := activateNode
	activateNode = func(context.Context) { mu.Lock(); calls++; mu.Unlock() }
	defer func() { activateNode = prev }()
	count := func() int { mu.Lock(); defer mu.Unlock(); return calls }

	a := &App{}
	waitDone := func(id string) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			healthRunsMu.Lock()
			_, busy := healthRuns[runKey(id)]
			healthRunsMu.Unlock()
			if !busy {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not finish", id)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	ok := fakeCLI(t, `echo '{"kind":"done"}'`+"\n")
	a.startStreamed(ok, "monomind.node.install", nil, "busy")
	waitDone("monomind.node.install")
	if count() != 1 {
		t.Fatalf("Activate calls after a Node install = %d, want 1", count())
	}
	a.startStreamed(ok, "core.db.migrate", nil, "busy")
	waitDone("core.db.migrate")
	a.startStreamed(fakeCLI(t, `echo '{"kind":"error","message":"no"}'`+"\n"), "agent.install:codex", nil, "busy")
	waitDone("agent.install:codex")
	if count() != 1 {
		t.Fatalf("Activate calls = %d, want 1 (not after other fixes or a failure)", count())
	}
}
