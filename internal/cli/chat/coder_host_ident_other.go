//go:build !windows && !linux

package chat

func stopStaleProcessOS(cfg *Deps, info hostInfo) (string, error) {
	return stopStaleUnpinned(cfg, info)
}
