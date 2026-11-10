package profile

import "strings"

// sessionsBeforeTheirReservations moves the task_sessions entry ahead of task_session_reservations and of the cost
// reports hanging under them (migration 154): a reservation is refused a hand delete while its session exists, and a
// report while its reservation exists. They all go with the session; the reservations come first so that a seeding of
// rows in this order always has the reservation a report is for. The same holds for the mission goal, the mission pursuit and the mission
// strategy tables (migrations 164, 165, 166): the database refuses a hand delete of a confirmed goal's criteria and of the
// strategy rows while their session exists, and removes them with it.
func sessionsBeforeTheirReservations(scope []scopeEntry) []scopeEntry {
	out := make([]scopeEntry, 0, len(scope))
	var held, heldChecks, heldReports []scopeEntry
	for _, e := range scope {
		switch {
		// 169: a check, its results and the person's answers hang under the goal, its criteria and the locked goal they need,
		// so a seeding of rows in this order always has what the next row is for (they go with the session all the same).
		case e.Table == "mission_goal_checks" || e.Table == "mission_goal_check_results" || e.Table == "mission_goal_answers":
			heldChecks = append(heldChecks, e)
			continue
		case e.Table == "task_session_reservations" || strings.HasPrefix(e.Table, "mission_goal") || e.Table == "mission_pursuit" || e.Table == "mission_pursuit_spend" || strings.HasPrefix(e.Table, "mission_strategy_"):
			held = append(held, e)
			continue
		case e.Table == "task_session_reservation_costs":
			heldReports = append(heldReports, e)
			continue
		}
		out = append(out, e)
		if e.Table == "task_sessions" {
			out = append(append(append(out, held...), orderChecks(heldChecks)...), heldReports...)
			held, heldChecks, heldReports = nil, nil, nil
		}
	}
	out = append(append(out, held...), orderChecks(heldChecks)...)
	return append(out, heldReports...)
}

// orderChecks puts the checks first, then their results, then the answers.
func orderChecks(in []scopeEntry) []scopeEntry {
	var out []scopeEntry
	for _, name := range []string{"mission_goal_checks", "mission_goal_check_results", "mission_goal_answers"} {
		for _, e := range in {
			if e.Table == name {
				out = append(out, e)
			}
		}
	}
	return out
}
