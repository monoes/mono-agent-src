package coderhost

import (
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/ai"
)

// Starting a mission task in a child coder session of its own (phase P-B1 of
// docs/mastermind/plans/2026-10-27-mission-child-sessions.md). SpawnForTask reserves the task's row in the mission
// child store first (missionchild_store.go: lead only, a task-bound caller refused, the ceiling and the budget
// share held by the reserving statement) and then runs the ordinary child spawn, so every limit of a master's
// child (depth, max_children, the spawn rate, access, folder, the carve out of the master's budget) applies
// unchanged.
//
// What the caller does not choose, whatever the task says: the access level is never passed (the child gets the
// lead's, capped at workspace, never full), the folder is always the lead's, the carve is the one the reservation
// holds. The brief is forwarded as the child's first message and never read: the caller (the conductor, which
// seals task text in a per-brief fence) owns what it says. The message goes out as a master- turn, which is
// host-made: it cannot carry a mission confirmation, a plan approval or a close.
//
// Nothing here claims, finishes, verifies or approves a task, and the files write no task row.

// ErrChildrenFull is why a task could not get a session of its own now: the mission's child sessions, its running
// turns or its share of the lead's budget are used up. It wraps ErrLimit. Nothing was reserved or created, so the
// conductor leaves the task waiting and asks again later.
var ErrChildrenFull = errors.New("the mission's child sessions are full")

// errCarveRefused marks a spawn whose carve was refused after its conversation existed: not an attempt, so the
// reservation goes back to marked once the conversation is deleted.
var errCarveRefused = errors.New("the carve was refused")

// SpawnForTask starts the session for a task the lead marked (or, in mode all, any task of its mission). sc is the
// lead's LeadScope (see NewLeadScope); brief is the already sealed first message.
func (s *Store) SpawnForTask(sc CallerScope, profileID string, sessionID, taskID int64, brief string) (Spawned, error) {
	l, _, err := s.leadOf(sc, profileID, sessionID)
	if err != nil {
		return Spawned{}, err
	}
	if strings.TrimSpace(brief) == "" || len(brief) > MaxMessageBytes {
		return Spawned{}, fmt.Errorf("%w: the brief must be a message of at most %d bytes", ErrInvalid, MaxMessageBytes)
	}
	c := l.Caller
	// A lead that is itself a child (or a solo session) cannot have children: refused before anything is reserved.
	if _, err := s.mayHaveChildren(c); err != nil {
		if errors.Is(err, ErrLimit) {
			err = fmt.Errorf("%w: %w", ErrChildrenFull, err) // no level left: the task waits
		}
		return Spawned{}, err
	}
	row, err := s.Reserve(sc, profileID, sessionID, taskID)
	if err != nil {
		return Spawned{}, err // including ErrChildrenFull: nothing was reserved
	}
	created := ""
	carveWasRefused := false
	out, err := s.spawnWith(c, SpawnRequest{Message: brief, BudgetUSD: row.ReservedUSD}, spawnHooks{
		onCreated: func(conv string) error {
			created = conv
			if s.afterChildCreated != nil {
				s.afterChildCreated()
			}
			return s.notePending(sc, profileID, sessionID, taskID, conv, row.Generation)
		},
		beforeMessage: func(string) error { return s.touch(sc, profileID, sessionID, taskID, row.Generation) },
		carveRefused:  func() { carveWasRefused = true }, // a refusal, not an attempt; the row keeps the conversation until it is deleted (abandonSpawn)
	})
	if err == nil {
		if _, err = s.setSpawned(sc, profileID, sessionID, taskID, out.ConversationID, row.Generation); err != nil {
			created = out.ConversationID
		}
	}
	if err != nil {
		if carveWasRefused {
			// the lead's budget no longer covers the share: the task waits, as for any other full limit
			err = fmt.Errorf("%w: %w: %w", errCarveRefused, ErrChildrenFull, err)
		}
		if out.ConversationID != "" {
			created = out.ConversationID // made, but its message or its link failed
		}
		return Spawned{}, s.abandonSpawn(c, profileID, sessionID, taskID, row.Generation, created, err)
	}
	return out, nil
}

// abandonSpawn undoes a failed spawn: the conversation, if one was made, is deleted, then the reservation is released. A refusal before anything was made (a limit, the lead's access) returns the task to marked and does
// not count as an attempt; any other failure fails the attempt. It returns cause, with any cleanup error added.
func (s *Store) abandonSpawn(c Caller, profileID string, sessionID, taskID int64, generation int64, conv string, cause error) error {
	if conv != "" {
		// The conversation goes first. The row keeps its pending id until it is gone, so a crash or a failed
		// delete leaves a spawning row that names the conversation, which the host's stale pass ends and stops. A
		// delete that succeeds marks the row gone itself.
		// A refused carve is written down first, so the stale pass can give the attempt back if the delete fails.
		// The conversation is deleted only if no other reservation owns it now (spawn_cleanup.go).
		var err error
		if errors.Is(cause, errCarveRefused) {
			err = s.recordCarveRefused(profileID, sessionID, taskID, c.ConversationID, generation, conv)
		}
		other := false
		if err == nil {
			other, err = s.ownedByAnother(profileID, sessionID, taskID, generation, conv)
		}
		if err == nil && !other {
			var store *ai.AIStore
			if store, err = ai.NewAIStore(s.db); err == nil {
				err = store.DeleteConversation(conv, profileID)
			}
		}
		if err != nil && !errors.Is(err, ai.ErrConversationNotFound) {
			return fmt.Errorf("%w (and deleting the conversation %s failed, so the reservation is left for the host to recover: %w)", cause, conv, err)
		}
	}
	refused := errors.Is(cause, errCarveRefused) || conv == "" && (errors.Is(cause, ErrLimit) || errors.Is(cause, ErrAccess) || errors.Is(cause, ErrInvalid) || errors.Is(cause, ErrNotYours))
	if refused && errors.Is(cause, ErrLimit) && !errors.Is(cause, ErrChildrenFull) {
		cause = fmt.Errorf("%w: %w", ErrChildrenFull, cause) // a spawn limit (rate, children, depth) reads as full: the task waits
	}
	if err := s.releaseReservation(profileID, sessionID, taskID, generation, c.ConversationID, !refused, cause.Error()); err != nil {
		return fmt.Errorf("%w (and releasing the reservation failed: %w)", cause, err)
	}
	return cause
}

// releaseReservation gives a reservation back: failed false returns the task to marked with the attempt not
// counted, failed true ends the attempt as failed. Either way what the row held is released, in one
// transaction with its audit row.
func (s *Store) releaseReservation(profileID string, sessionID, taskID int64, generation int64, lead string, failed bool, why string) error {
	now := s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var res interface{ RowsAffected() (int64, error) }
	if failed {
		res, err = tx.Exec(`UPDATE coder_mission_children SET state = 'failed', end_reason = 'no_finish', reserved_usd = 0, pending_conversation_id = '', ended_at = ?, updated_at = ?
			WHERE profile_id = ? AND session_id = ? AND task_id = ? AND lead_conversation_id = ? AND state = 'spawning' AND generation = ?`, now, now, profileID, sessionID, taskID, lead, generation)
	} else {
		// Also a row the conversation's own deletion just marked gone: a refused carve deletes first, and the
		// deletion is what ended the row, but the refusal was never an attempt.
		res, err = tx.Exec(`UPDATE coder_mission_children SET state = 'marked', end_reason = '', ended_at = '', reserved_usd = 0, pending_conversation_id = '', attempts = MAX(attempts - 1, 0), updated_at = ?
			WHERE profile_id = ? AND session_id = ? AND task_id = ? AND lead_conversation_id = ? AND (state = 'spawning' OR (state = 'gone' AND end_reason = 'deleted')) AND generation = ?`, now, profileID, sessionID, taskID, lead, generation)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // already settled by someone else (the host's stale pass)
	}
	if err := addMissionEvent(tx, profileID, sessionID, taskID, "spawn", "lead:"+lead, "released: "+why, now); err != nil {
		return err
	}
	return tx.Commit()
}
