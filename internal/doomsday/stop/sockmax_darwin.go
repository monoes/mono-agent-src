//go:build darwin

package stop

// platformSockMax is the longest path a unix socket address holds on macOS (sun_path is 104 bytes, one for the NUL).
const platformSockMax = 103
