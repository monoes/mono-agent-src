//go:build windows

package main

import "github.com/monoes/mono-agent/internal/doomsday/stop"

// The Windows kernel answers for Job Object units only (see stop.WindowsKernel).
func doomsdayKernel() stop.Kernel { return stop.WindowsKernel{} }
