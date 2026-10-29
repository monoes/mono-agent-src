package tasks

// MissionPlannerName is the name the in-process mission planner of a chat works under. Sessions it
// started before the kind column held mission sessions carry it in created_by (kind "board"), so the
// name is reserved: only the planner itself (Actor.MissionPlanner, set by trusted code) may use it.
const MissionPlannerName = "mission-lead"

// checkMissionKind refuses an agent's attempt to start a session that would pass for the chat's
// mission: kind mission, or the planner's name. The operator and the planner may. Without this a
// prompt-injected agent could link a session to its own chat and confirm a mission with no click.
func checkMissionKind(actor Actor, kind string) error {
	if actor.Kind != Agent || actor.MissionPlanner {
		return nil
	}
	if kind == SessionKindMission {
		return operatorOnly("start a session of kind mission: the operator's mission starts it")
	}
	if actor.Name == MissionPlannerName {
		return operatorOnly("use the name " + MissionPlannerName + ": it is the mission planner's")
	}
	return nil
}

// IsMissionSession says whether a session is a chat's mission session: kind mission, or a session of
// the old kind "board" that the mission planner started (created_by is the planner's name, which
// CreateSession now reserves, so a later agent cannot forge it).
func IsMissionSession(se Session) bool {
	return se.Kind == SessionKindMission || (se.Kind == SessionKindBoard && se.CreatedBy == MissionPlannerName)
}
