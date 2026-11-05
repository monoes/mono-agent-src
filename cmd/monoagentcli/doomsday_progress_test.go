package main

import (
	"bytes"
	"strings"
	"syscall"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

type epipeWriter struct{ calls int }

func (w *epipeWriter) Write([]byte) (int, error) { w.calls++; return 0, syscall.EPIPE }

// The progress line of Stop never fails it: a reader that went away (EPIPE) is no error of the stop.
func TestDoomsdayProgressIgnoresAClosedReader(t *testing.T) {
	w := &epipeWriter{}
	old := doomsdayProgressOut
	doomsdayProgressOut = w
	t.Cleanup(func() { doomsdayProgressOut = old })
	doomsdayProgress(stop.ProgressIntent) // must neither panic nor report
	if w.calls != 1 {
		t.Fatalf("the line was written %d times", w.calls)
	}
}

// The CLI's engine carries the progress sink to Stop's order: without it the operator waits silently.
func TestCLIEngineWiresTheStopProgressLine(t *testing.T) {
	cfg := sendEnv(t)
	var buf bytes.Buffer
	old := doomsdayProgressOut
	doomsdayProgressOut = &buf
	t.Cleanup(func() { doomsdayProgressOut = old })
	fe, ok := doomsdayEngine(cfg).(*doomsdaycli.FinishEngine)
	if !ok {
		t.Fatalf("the CLI engine is %T", doomsdayEngine(cfg))
	}
	if fe.W.Progress == nil {
		t.Fatal("the CLI's Stop has no progress sink")
	}
	fe.W.Progress(stop.ProgressIntent)
	if got := buf.String(); !strings.Contains(got, "marker written; recording intent...") {
		t.Fatalf("progress: %q", got)
	}
}
