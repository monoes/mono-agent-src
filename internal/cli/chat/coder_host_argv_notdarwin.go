//go:build !windows && !darwin

package chat

import "errors"

func procArgsRaw(int) ([]byte, error) { return nil, errors.New("kern.procargs2 exists on macOS only") }
