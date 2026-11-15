//go:build !darwin

package stop

// platformSockMax is the longest path a unix socket address holds elsewhere (sun_path is 108 bytes, one for the NUL).
const platformSockMax = 107
