//go:build !linux

package chat

// awaitExitNoReap holds nothing where a child cannot be waited for without reaping it: the turn is then stopped as it
// always was (the group signalled by number while cmd.Wait has not returned, the stragglers killed after the leader).
func awaitExitNoReap(pid int) bool { return false }
