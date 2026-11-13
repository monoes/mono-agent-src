package chat

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// lockedWriter serialises writes to w. The host logs from its run goroutine and from the goroutines it starts (the retention sweep, the
// strategy loop), all into the command's stderr, which need not be safe for concurrent use (a test hands it a bytes.Buffer).
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// hostLogf is the host's timestamped log line writer; its lines never interleave or race, whichever goroutine writes them.
func hostLogf(w io.Writer) func(string, ...any) {
	lw := &lockedWriter{w: w}
	return func(f string, a ...any) {
		fmt.Fprintf(lw, time.Now().Format(time.RFC3339)+" "+f+"\n", a...)
	}
}
