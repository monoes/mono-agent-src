//go:build darwin

package coderhost

import "github.com/monoes/mono-agent/internal/testhome"

// The binding service's socket lives under HOME, and a unix socket address holds 103 bytes (see testhome.ShortHome).
func init() { testhome.ShortHome = "mh" }
