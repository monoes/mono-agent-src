package profile

// sessionsBeforeTheirReservations moves the task_sessions entry ahead of task_session_reservations and of the cost
// reports hanging under them (migration 154): a reservation is refused a hand delete while its session exists, and a
// report while its reservation exists. They all go with the session; the reservations come first so that a seeding of
// rows in this order always has the reservation a report is for.
func sessionsBeforeTheirReservations(scope []scopeEntry) []scopeEntry {
	out := make([]scopeEntry, 0, len(scope))
	var held, heldReports []scopeEntry
	for _, e := range scope {
		switch e.Table {
		case "task_session_reservations":
			held = append(held, e)
			continue
		case "task_session_reservation_costs":
			heldReports = append(heldReports, e)
			continue
		}
		out = append(out, e)
		if e.Table == "task_sessions" {
			out = append(append(out, held...), heldReports...)
			held, heldReports = nil, nil
		}
	}
	out = append(out, held...)
	return append(out, heldReports...)
}
