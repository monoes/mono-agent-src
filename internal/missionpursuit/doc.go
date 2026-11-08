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
// The away-spend rule (a share of the budget since the person last acted) reads the METERED spend of the mission session
// only. The judge calls of the strategy engine, the wake-up turns of the lead and children that are still running are not
// all in that meter, so unknown cost is never taken as free: the pursuit is also bounded by counts that do not depend on
// the meter (parked questions, wake-ups of the lead since the person last acted, and a host-wide cap of wake-ups per hour),
// and by the epoch engine's own floor and hourly cap. Progress (GetProgress) shows how far each has come.
package missionpursuit
