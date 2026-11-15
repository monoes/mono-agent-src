//go:build windows

package chat

// stopStaleProcess: on Windows the stop goes through a pinned process handle that is compared with the recorded start time
// (stopPinnedHost), which is the identity check there.
var stopStaleProcess = func(cfg *Deps, info hostInfo) (string, error) { return stopHost(cfg, info) }
