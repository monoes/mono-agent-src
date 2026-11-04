package coderhost

import "errors"

// ErrDeferred is a launcher's answer when it cannot start a turn NOW but may later (the resource governor of an
// open-ended run has no free slot). The host leaves the send pending, in order, exactly as it does for a master whose
// children are at max_concurrent; nothing is failed and nothing is started. A launcher that never returns it (every
// launcher of a host without the mode) leaves the host as it was.
var ErrDeferred = errors.New("coder host: no capacity for this turn now; the send waits")
