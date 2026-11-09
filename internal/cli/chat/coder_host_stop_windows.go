//go:build windows

package chat

func stopHostOS(_ *Deps, info hostInfo) (bool, error) { return false, hostStopByHand(info.PID) }
