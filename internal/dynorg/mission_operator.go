package dynorg

import "github.com/monoes/mono-agent/internal/tasks"

// operatorActor is the operator's identity, which the store requires for the few writes
// only the operator may make: a session's spend, the branch and merge artifacts, and the
// close of a session. The mission's conductor makes those writes from the operator's own
// process, and only from the functions in operatorSites (mission_operator_test.go fails when
// another one appears). Use missionActor for everything else.
func operatorActor() tasks.Actor { return tasks.Actor{Kind: tasks.Human} }
