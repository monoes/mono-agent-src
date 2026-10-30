package dynorg

// MissionWorkerName is the name a mission task is claimed and acted on under: the in-turn worker holds its claim
// as this, and the task token of a mission child session names the same actor, so the child finishes the claim the
// lead made for it and no other.
func MissionWorkerName(session, task int64) string { return missionWorkerName(session, task) }
