//go:build !darwin

package stop

import "context"

// realPGroupOS is nil off macOS: the process-group mechanism refuses there.
func realPGroupOS() PGroupOS { return nil }

// ServeAnchors is nothing off macOS: only the macOS unit is a process group made in the host's session.
func ServeAnchors(context.Context, RunDir) error { return nil }
