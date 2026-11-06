//go:build darwin

package main

import "github.com/monoes/mono-agent/internal/doomsday/stop"

// The macOS kernel answers for process-group units only (see stop.DarwinKernel).
func doomsdayKernel() stop.Kernel { return stop.DarwinKernel{} }
