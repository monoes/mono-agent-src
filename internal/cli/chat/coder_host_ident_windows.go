//go:build windows

package chat

// verifyHostProcess: on Windows the stop goes through a pinned process handle that is compared with the recorded start time
// (stopPinnedHost), which is the identity check there.
var verifyHostProcess = func(int) error { return nil }
