//go:build !windows

package coderhost

import "time"

// missionTokenTimeoutForTest shortens the pipe read deadline; the returned
// function restores it.
func missionTokenTimeoutForTest(d time.Duration) func() {
	old := missionTokenTimeout
	missionTokenTimeout = d
	return func() { missionTokenTimeout = old }
}
