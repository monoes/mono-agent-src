package coderhost

import (
	"testing"
	"time"
)

// Group 2.
func TestUnparsableRecordRateCounts(t *testing.T)            { runBad(t, true, siteRecordRate) }
func TestUnparsablePersonItemRateCounts(t *testing.T)        { runBad(t, true, sitePersonRate) }
func TestUnparsableClosedQuestionIsNeverPruned(t *testing.T) { runBad(t, true, sitePrune) }
func TestUnparsableLostNoteIsNeverRedelivered(t *testing.T)  { runBad(t, true, siteRedeliver) }
func TestUnparsableOpenQuestionGetsNoDefault(t *testing.T)   { runBad(t, true, siteDefaults) }
func TestUnparsableSendExpires(t *testing.T) {
	runBad(t, false, func(t *testing.T, cut time.Time, s any) bool {
		return expireSendsCase(t, cut.Add(MaxSendAge), "person-1", "", s)
	})
	runBad(t, false, func(t *testing.T, cut time.Time, s any) bool {
		return expireSendsCase(t, cut.Add(MaxCheckinAge), "checkin-1", "", s)
	})
	runBad(t, false, func(t *testing.T, cut time.Time, s any) bool {
		return expireSendsCase(t, cut.Add(MaxCheckinAge), "person-1", "master", s)
	})
}
func TestUnparsableSpawningRowIsNeverReset(t *testing.T) { runBad(t, true, siteResetNever) }
func TestUnparsableOrphanSpawnIsEnded(t *testing.T)      { runBad(t, false, siteResetOrphan) }
func TestUnparsableOrphanCarveIsNeverFound(t *testing.T) { runBad(t, true, siteOrphanCarve) }
