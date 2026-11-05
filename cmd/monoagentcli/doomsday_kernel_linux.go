//go:build linux

package main

import "github.com/monoes/mono-agent/internal/doomsday/stop"

func doomsdayKernel() stop.Kernel { return stop.LinuxKernel{} }
