package chat

import (
	"fmt"
	"strings"
	"time"
)

// `coder set --timeout`: the wall-clock cap of a coder message, the lead's and its workers'.

// maxTurnTimeout is the longest cap: monomind's timer overflows above about 596h, and then ends the turn at once.
const maxTurnTimeout = 576 * time.Hour

// parseTurnTimeout reads a cap written as a Go duration with its unit ("60m", "2h"), of at least 1s, cut to
// maxTurnTimeout. Anything else is no cap: "3mins" or a bare number is never guessed at.
func parseTurnTimeout(v string) (time.Duration, bool) {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d < time.Second {
		return 0, false
	}
	return min(d, maxTurnTimeout), true
}

// timeout is the cap a turn runs under: the stored one, else (an unreadable one counts as none) the default.
func (s coderSettings) timeout() time.Duration {
	if d, ok := parseTurnTimeout(s.Timeout); ok {
		return d
	}
	d, _ := time.ParseDuration(defaultCoderTimeout)
	return d
}

// turnTimeoutText is the status line's value: the stored cap, and what applies instead when turns don't run under it.
func turnTimeoutText(s coderSettings) string {
	if _, ok := parseTurnTimeout(s.Timeout); !ok {
		return fmt.Sprintf("%s (the default: the stored value %q is not a duration of at least 1s with its unit; set it with `coder set --timeout`)",
			s.timeout(), shortLine(s.Timeout, maxVersionLen))
	}
	if asked, _ := time.ParseDuration(strings.TrimSpace(s.Timeout)); asked > maxTurnTimeout {
		return fmt.Sprintf("%s (the maximum: the stored value %q is above it)", maxTurnTimeout, shortLine(s.Timeout, maxVersionLen))
	}
	return s.Timeout
}
