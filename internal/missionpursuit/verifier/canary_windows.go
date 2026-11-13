//go:build windows

package verifier

import "errors"

func listenUnix(string) (func(), error) {
	return nil, errors.New("no unix sockets are planted on this platform")
}
