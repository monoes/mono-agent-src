//go:build doomsday && !linux

package main

import "github.com/monoes/mono-agent/internal/doomsday/stop"

// No containment kernel off Linux: the probe refuses to start the mode there, and Stop reports a unit as not proven.
func doomsdayKernel() stop.Kernel { return nil }
