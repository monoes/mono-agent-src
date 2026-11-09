// Package missionpursuit holds what a mission is pursuing: the ultimate goal as typed criteria (T6).
//
// The lead, or any agent, may only PROPOSE the criteria and may revise its proposal until the person confirms it.
// Setting and confirming are the person's alone (the store refuses every agent context, the CLI refuses it before it
// opens the database), and a confirm is a compare-and-set on the hash of the criteria the person saw. A confirmed goal
// is absolute: it is never amended, a changed goal means a new mission. A mission goal always carries at least one
// rubric criterion, so that the person is the final judge. The rules also live in the database (migration 164).
//
// # What the away limits can and cannot see
//
// The away-spend rule (a share of the budget since the person last acted) counts three things the host measured: the
// METERED spend of the mission session, the judge ledger (every judge call is a row written before the call at the per-call
// cap and settled once with the cost the runtime reported; unknown cost stays at the cap, never free), and the turn
// journals of the lead and its helpers. A helper that ended is on the session meter as well, so it can be counted twice:
// the stop then comes earlier, never later. What no ledger holds is bounded by counts that do not depend on a meter: parked
// questions, wake-ups of the lead since the person last acted, a host-wide cap of wake-ups per hour and of judge calls per
// hour, and three judge rounds in a row that fail (the pursuit then waits for the person). The question policy is not
// touched by a pursuit: Decide is the same as for any mission, so these limits carry the brake. Progress (GetProgress)
// shows how far each has come.
package missionpursuit
