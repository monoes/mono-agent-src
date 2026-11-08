// Package inputs is the read side of a mission's pursuit (T6): the facts the strategy engine and the scorer judge from, and
// the one answer to "is this pursuit running right now".
//
// It READS host rows and nothing else: task counts per state, the open directions and the epochs from the mission strategy
// store (migration 165), the highest evidence row id of the session's tasks, the session's own budget and spend, and the
// task session and mission_pursuit rows (migrations 126, 166). It never reads text an agent wrote (titles, notes, goal
// text, evidence bodies, directions' titles are carried but only as opaque values for a later judge, never compared or
// parsed here), and nothing it returns is a decision. It has no writer: a test pins that there is no Exec call, no write
// statement, no process, no network, no storage package and no tasks store among its imports.
//
// # RunState
//
// A pursuit runs only when ALL of these hold, read in ONE statement so the answer is a single snapshot:
// task_sessions.state = 'running', the session is a mission, a mission_pursuit row exists with enabled = 1, and paused_by
// is empty. Anything else is "not running", with a Reason; anything unreadable (an error, a closed database, a missing
// table, a value outside what the schema allows, a cancelled context, a session id that is not a plain positive integer)
// is "not running" AND an error. A caller that cannot tell must stop, never assume. Reader.Running(ctx, profile, gen)
// is the policy intake's RunState (profile is the decimal session id, generation 1, as in the mission strategy store) and
// Reader.RunOf(session).Running(ctx) is the epoch engine's.
//
// # The Inputs interface (what the scorer and the host loop consume)
//
//	type Inputs interface {
//	    Check(ctx, session)    (Run, error)      // the run state with its reason
//	    Snapshot(ctx, session) (Snapshot, error) // the facts; zero but for Run when the pursuit is not running
//	}
//
// Snapshot.Run.Running false means every other field is zero: a stopped pursuit carries no data to act on. Tasks counts the
// session's tasks per board status (Unknown for a status this code does not know, so a new status shows up rather than
// vanishing); Blocked counts open tasks flagged blocked. Directions are the open ones (proposed, active, stalled), Epochs the
// count, the latest epoch and the number of finish-parked epochs. EvidenceSeq is the highest task_artifacts id among the
// session's tasks: only its changes matter (the engine's new-evidence trigger), its content is never read. Budget is the
// SESSION's spend against the SESSION's budget; Known is false when the budget is not a positive finite number or the spend
// is negative or not finite (an unknown budget never fires a budget trigger, and the away rule treats it as not known).
// The away-spend share since the person last acted is the host's to compute from the Spend it records; this package only
// reports the session totals.
//
// The Snapshot is the plain-types counterpart of the epoch engine's Snapshot; it does not carry the success criteria or the
// direction activity (the lead's progress), which the host loop supplies from its own rows when it wires the engine.
package inputs
