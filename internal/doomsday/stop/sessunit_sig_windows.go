//go:build windows

package stop

import "syscall"

// sessSigStop has no meaning on Windows: the session unit is never selected there (its OS binding is nil), and no signal
// of this number can be sent.
var sessSigStop = syscall.Signal(0)
