package main

import (
	"context"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/monomind"
)

// chatScan lists the installed runtimes (a seam for tests).
var chatScan = monomind.Scan

// errChatRuntimeRequired is the error of a chat turn with no --runtime when
// no single runtime can be chosen for it.
func errChatRuntimeRequired(candidates []string) error {
	if len(candidates) == 0 {
		return errInvalidInput("--runtime is required (see `agent scan --installed`)")
	}
	return errInvalidInput("--runtime is required: more than one runtime can run it (%s); pick one with --runtime", strings.Join(candidates, ", "))
}

// defaultChatRuntime picks the runtime of a chat turn started without
// --runtime: the one runtime with a ready (validated) model, else, when no
// runtime has one, the one installed runtime that can run headless. Any other
// case is errChatRuntimeRequired, naming the candidates.
func defaultChatRuntime(ctx context.Context, cfg *globalConfig) (string, error) {
	scan, err := chatScan(ctx)
	if err != nil {
		return "", errChatRuntimeRequired(nil)
	}
	var results []agentroster.Result
	if db, err := initDB(cfg); err == nil {
		results, _ = agentroster.List(ctx, db.DB)
		db.Close()
	}
	return pickChatRuntime(results, scan)
}

// pickChatRuntime is defaultChatRuntime without the I/O.
func pickChatRuntime(results []agentroster.Result, scan *monomind.ScanResult) (string, error) {
	roster := agentroster.Build(results, scan, time.Now(), agentroster.DefaultMaxAge)
	var ready []string
	for _, rr := range roster {
		if !rr.Installed || (rr.ExecutionSupported != nil && !*rr.ExecutionSupported) {
			continue
		}
		for _, m := range rr.Models {
			if m.State == agentroster.StateReady {
				ready = append(ready, rr.Runtime)
				break
			}
		}
	}
	if len(ready) == 1 {
		return ready[0], nil
	}
	if len(ready) > 1 {
		return "", errChatRuntimeRequired(ready)
	}
	var runnable []string
	if scan != nil {
		for _, a := range scan.Installed() {
			if a.ExecutionSupported != nil && !*a.ExecutionSupported {
				continue
			}
			runnable = append(runnable, a.ID)
		}
	}
	if len(runnable) == 1 {
		return runnable[0], nil
	}
	return "", errChatRuntimeRequired(runnable)
}
