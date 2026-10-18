//go:build devaccount

package operator

import (
	"os"
	"strconv"
)

// AncestorStopEnv names a parent process the check (ancestorMarker) does not
// look at or above, in a devaccount build only: the account smoke runs the
// real binary from its test process, and what runs that test (an agent's
// shell, for a contributor's local run) must not mark the binary. A shipped
// build has no such variable: it would let anything clear the check.
const AncestorStopEnv = "MONOAGENT_ANCESTOR_STOP_AT"

func init() {
	if pid, err := strconv.Atoi(os.Getenv(AncestorStopEnv)); err == nil && pid > 1 {
		ancestorsStopAt = pid
	}
}
