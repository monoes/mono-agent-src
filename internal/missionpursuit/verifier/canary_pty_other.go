//go:build !linux

package verifier

import "errors"

func plantPty() (string, func(), error) {
	return "", nil, errors.New("no pseudo-terminals are planted on this platform")
}
