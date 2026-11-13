package chat

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// The host's log lines come from several goroutines into one stderr. Run with -race: without the serialising writer two goroutines write
// the same bytes.Buffer at once and the race detector fails the test (and lines can interleave).
func TestTheHostLogLinesOfConcurrentGoroutinesDoNotRaceOrInterleave(t *testing.T) {
	var buf bytes.Buffer
	logf := hostLogf(&buf)
	var wg sync.WaitGroup
	const writers, lines = 4, 200
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < lines; i++ {
				logf("coder host: writer %d line %d", g, i)
			}
		}(g)
	}
	wg.Wait()
	got := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(got) != writers*lines {
		t.Fatalf("%d lines, want %d", len(got), writers*lines)
	}
	for _, l := range got {
		if !strings.Contains(l, " coder host: writer ") || strings.Count(l, "coder host:") != 1 {
			t.Fatalf("an interleaved line: %q", l)
		}
	}
}
