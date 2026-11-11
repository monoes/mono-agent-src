package procbind

import "time"

// filetimeUnixEpoch is the FILETIME tick count (100 ns since 1601-01-01) of the Unix epoch.
const filetimeUnixEpoch = 116444736000000000

// peerClockSlack is how far after the accept a peer's creation time may fall before it is refused: a clock that is stepped
// back between the peer's start and the accept must not refuse a live peer, and the slack bounds how long after the accept a
// recycled number can still be taken for the connector.
const peerClockSlack = 250 * time.Millisecond

// createdAfterAccept reports whether a process whose creation time is created (FILETIME ticks) came into being after the
// service accepted the connection. The connector existed when it connected, so a process created after the accept cannot be
// it: the kernel's peer number was handed to another process (the connector handed the connection on and ended, and its number
// was recycled). An unknown creation time (zero, or before the Unix epoch) is refused too: a pin that has no usable stamp cannot be shown to predate the connection.
func createdAfterAccept(created uint64, accepted time.Time) bool {
	if created < filetimeUnixEpoch {
		return true
	}
	c := time.Unix(0, int64(created-filetimeUnixEpoch)*100)
	return c.After(accepted.Add(peerClockSlack))
}
