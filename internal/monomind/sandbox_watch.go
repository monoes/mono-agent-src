package monomind

import (
	"context"
	"sync"
)

// SandboxWatch is how a caller that shows a turn's result learns that the turn
// ran without a sandbox, when the code between it and Exec returns only the
// answer.
type SandboxWatch struct {
	mu      sync.Mutex
	missing string
}

type sandboxWatchKey struct{}

// WatchSandbox returns a context under which every turn's verdict is told to
// the returned watch. A watch set under it again takes over for its turns.
func WatchSandbox(ctx context.Context) (context.Context, *SandboxWatch) {
	w := &SandboxWatch{}
	return context.WithValue(ctx, sandboxWatchKey{}, w), w
}

// NoteSandbox tells ctx's watch, if any, a turn's verdict. Exec calls it with
// each start event's.
func NoteSandbox(ctx context.Context, status string) {
	w, _ := ctx.Value(sandboxWatchKey{}).(*SandboxWatch)
	if w == nil || !SandboxMissing(status) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.missing = status
}

// Missing is the verdict of a watched turn that ran without a sandbox
// (SandboxMissing), "" when none did.
func (w *SandboxWatch) Missing() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.missing
}
