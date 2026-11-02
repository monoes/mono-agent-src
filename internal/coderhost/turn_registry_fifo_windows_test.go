//go:build windows

package coderhost

import "testing"

func fifoSupported(t *testing.T) { t.Skip("no FIFOs on windows") }

func makeFifo(t *testing.T, path string) {}
