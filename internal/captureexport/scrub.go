package captureexport

import "strings"

// scrubInboxPath replaces the inbox's absolute path in the reasons an
// archive carries, so an OS error text ("open /home/<user>/...") does not
// put the user name into a file that is shared by design (R11-10).
func scrubInboxPath(notes []Note, inbox string) []Note {
	if inbox == "" {
		return notes
	}
	out := make([]Note, len(notes))
	for i, n := range notes {
		n.Reason = strings.ReplaceAll(n.Reason, inbox, "<inbox>")
		out[i] = n
	}
	return out
}
