// Package missionpursuit holds what a mission is pursuing: the ultimate goal as typed criteria (T6).
//
// The lead, or any agent, may only PROPOSE the criteria and may revise its proposal until the person confirms it.
// Setting and confirming are the person's alone (the store refuses every agent context, the CLI refuses it before it
// opens the database), and a confirm is a compare-and-set on the hash of the criteria the person saw. A confirmed goal
// is absolute: it is never amended, a changed goal means a new mission. A mission goal always carries at least one
// rubric criterion, so that the person is the final judge. The rules also live in the database (migration 164).
package missionpursuit
