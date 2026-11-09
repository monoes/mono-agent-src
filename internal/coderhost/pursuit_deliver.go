package coderhost

import (
	"context"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Whether an epoch step may spend now, and whether the lead can be woken (the check-in's conditions, for the pursuit).

// pursuitMayTick says whether an epoch step may spend now: the wake-up it earns must be deliverable, the lead must not owe
// an undelivered one already, and the engine's floor between two epochs must have passed (the engine would hold it anyway,
// after reading a whole snapshot). An epoch the engine left unfinished is resumed under the same conditions.
func (h *Host) pursuitMayTick(ctx context.Context, m pursuitMission, g pursuitGateResult) bool {
	if h.pursuitUndelivered(g) {
		return false
	}
	if g.hourJudges >= MaxPursuitJudgeCallsPerHour {
		return false // the host-wide cap on judge calls in an hour is used; it frees as the oldest call leaves the hour
	}
	if why := h.pursuitDeliverable(m, g); why != "" {
		return false
	}
	if ep := g.snap.Epochs; ep.HasLatest && (ep.Latest.State != types.EpochOpen && ep.Latest.State != types.EpochJudged) && h.Pursuit.clock().Now().Sub(time.Unix(ep.Latest.StartedAt, 0)) < types.MinEpochInterval {
		return false
	}
	return true
}

// pursuitUndelivered: an applied epoch whose wake-up is still owed (not queued yet, or queued and lost).
func (h *Host) pursuitUndelivered(g pursuitGateResult) bool {
	return !g.delivered
}

// pursuitDeliverable says why the lead cannot be woken now, or "": no chat, outside its active hours, not unattended,
// in a turn or with a message waiting, or no open work for it (the check-in this replaces had each of these conditions).
func (h *Host) pursuitDeliverable(m pursuitMission, g pursuitGateResult) string {
	if m.Conv == "" {
		return "the mission is bound to no chat"
	}
	lead, found, err := h.Store.GetSession(m.Profile, m.Conv)
	switch {
	case err != nil || !found:
		return "the lead's session does not read"
	case !lead.Unattended:
		return "the lead is not unattended"
	case lead.State != StateIdle || lead.TurnID != "" || h.Store.PendingFor(m.Profile, m.Conv) > 0:
		return "the lead is not free"
	}
	settings, err := h.Store.GetSettings(m.Profile, m.Conv)
	if err != nil || !withinActiveHours(settings.ActiveHours, h.Store.now()) {
		return "outside the lead's active hours"
	}
	if t := g.snap.Tasks; t.Open()-t.Blocked <= 0 {
		return "the mission has no open work"
	}
	if g.hourWakes >= MaxPursuitWakesPerHour {
		return "the host-wide cap on pursuit wake-ups in an hour is used"
	}
	return h.pursuitStreakStop(m.Profile, m.Conv, settings)
}

// pursuitStreakStop is the check-in limit applied to the pursuit's wake-ups, which are host-made turns like a check-in: no
// wake-up once the lead has had CheckinLimit host-made turns in a row with no message from the person between.
func (h *Host) pursuitStreakStop(profileID, conv string, settings Settings) string {
	streak, _, _, err := h.Store.CheckinStreak(profileID, conv)
	switch {
	case err != nil:
		return readFailed + "the lead's turns do not read"
	case streak >= settings.CheckinLimit:
		return "the lead has had its host-made turns in a row; it waits for a person"
	}
	return ""
}
