package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/taskbind"
)

// The task token of a mission child session (phase P-B2 of docs/mastermind/plans/2026-10-27-mission-child-sessions.md).
//
// Where it is minted: in the coder host's launcher (execLauncher.start), which is the only process that starts a
// child's turn. The lead's chat process asks for the child through the command queue and never holds, mints or sees
// the child's token. The launcher mints it from the host's own child store (which task this conversation is the
// session of), never from a message, a flag or the environment.
//
//   - It is the token a worker gets: agenttokens.CreateMission, role worker, bound to (mission session, task), acting
//     under the name the lead claimed the task as. A child can never obtain a broader binding than that: the role is
//     fixed here, a verifier token is never minted for a child, and the child holds no other token (the lead's
//     session token is not in its environment: the launcher mints the child its own).
//   - Its owner is dynorg.TurnOwner(profile, child conversation, turn), exactly as for an in-turn worker, and it is
//     valid for childTurnLimit plus a margin. Before it is minted every earlier token of that conversation is revoked,
//     so there is one live task token per child, ever (a second run for the same task or attempt starts with a
//     fresh one, and an older one is dead).
//   - It travels to the child's chat process over the inherited pipe that carries a mission confirmation, never in
//     the argument list or the host's environment; the process reads it once, takes it out of the pipe, and puts it
//     in its own environment as MONOAGENT_TASK_BINDING, for its shells and tool servers (the way a worker's is).
//   - Right after the turn starts it is bound to the process (agenttokens.BindMissionProcess), so a process outside
//     that tree is refused. Where the binding cannot be written the turn is stopped and fails: a child never runs
//     with a token that anyone holding the value could use.
//   - It is revoked when the turn's process ends (the launcher's wait), when the host settles the session's turn or
//     stops it, and when the child's row ends (coderhost.Host.settleChild).
//
// A child's turn never starts without it: the launcher refuses the turn when the mission is not running, and the
// chat process itself refuses a turn in a child's conversation that has no valid token for its row (childTaskGate),
// which also stops a host that lacks this code from running a child unbound.

// childTurnLimit is how long a child's turn may run before its token ends on its own (the revoke at the end of the
// run is what normally ends it).
const childTurnLimit = 2 * time.Hour

// childTokenMargin is how long a token outlives childTurnLimit.
const childTokenMargin = 5 * time.Minute

// childTokenMinter is the launcher's half. It implements ChildTokenMinter and coderhost.ChildTokenRevoker.
type childTokenMinter struct {
	store  *coderhost.Store
	tokens *agenttokens.Store
}

func newChildTokenMinter(db *sql.DB) *childTokenMinter {
	return &childTokenMinter{store: coderhost.NewStore(db), tokens: agenttokens.NewStore(db)}
}

// CanMintChildTokens is the launcher's capability (ChildTokenMinter).
func (m *childTokenMinter) CanMintChildTokens() bool {
	return m != nil && m.store != nil && m.tokens != nil
}

// childToken is one minted token.
type childToken struct {
	id, secret, owner string
	task              coderhost.ChildTask
}

// mint gives the conversation's turn its task token. It returns nil, nil for a conversation that is not a mission
// child's live session, and an error for a child that must not run.
func (m *childTokenMinter) mint(profileID, conversationID, turnID string) (*childToken, error) {
	task, isChild, err := m.store.ChildTaskOf(profileID, conversationID)
	if err != nil {
		return nil, err
	}
	if !isChild {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.RevokeChildTokens(profileID, conversationID); err != nil { // one live task token per child
		return nil, fmt.Errorf("could not retire the earlier task tokens of this child: %w", err)
	}
	owner := dynorg.TurnOwner(profileID, conversationID, turnID)
	tok, secret, err := m.tokens.CreateMission(ctx, profileID, agenttokens.MissionBinding{
		Session: task.SessionID, Task: task.TaskID, Role: agenttokens.RoleWorker,
		Actor: dynorg.MissionWorkerName(task.SessionID, task.TaskID), Owner: owner,
	}, childTurnLimit+childTokenMargin)
	if err != nil {
		return nil, fmt.Errorf("could not mint the task token of the child: %w", err)
	}
	return &childToken{id: tok.ID, secret: secret, owner: owner, task: task}, nil
}

// bind names the turn's process for its token and checks that the child is still the attempt the token was minted
// for. It returns an error when the turn must be stopped.
func (m *childTokenMinter) bind(profileID, conversationID string, t *childToken, pid int, stamp string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if agenttokens.MissionProcessSupported() {
		if err := m.tokens.BindMissionProcess(ctx, profileID, t.id, pid, stamp); err != nil {
			return fmt.Errorf("could not bind the child's task token to its process: %w", err)
		}
	}
	ok, err := m.store.ChildAttemptCurrent(profileID, conversationID, t.task)
	if err != nil {
		return fmt.Errorf("could not check the child's attempt: %w", err)
	}
	if !ok {
		return errors.New("the child ended or was replaced while its turn started")
	}
	return nil
}

// revokeToken ends one run's tokens (the owner is the run).
func (m *childTokenMinter) revokeToken(t *childToken) {
	if m == nil || t == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		if _, err := m.tokens.RevokeOwner(ctx, t.owner); err == nil {
			return
		}
		time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
	}
}

// RevokeChildTokens ends every task token minted for the conversation's runs (coderhost.ChildTokenRevoker).
func (m *childTokenMinter) RevokeChildTokens(profileID, conversationID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := m.tokens.SweepMissions(ctx, dynorg.ConversationOwner(profileID, conversationID), "", nil)
	return err
}

// childTaskGate is the chat process's side, run first thing in a journaled turn with the value read from the
// inherited pipe: a task token is taken out of the mission-confirmation slot, and for a conversation that is a
// mission child's live session the turn runs only with a token the database verifies for exactly that row's
// session and task. The token is then put in this process's environment for the agent's own shells and tool
// servers. It returns the mission confirmation to use (a task token is never one).
func childTaskGate(db *sql.DB, profileID, conversationID, piped string) (string, error) {
	taskToken := ""
	if strings.HasPrefix(piped, coderhost.TaskTokenPrefix) {
		taskToken, piped = piped, ""
	}
	task, isChild, err := coderhost.NewStore(db).ChildTaskOf(profileID, conversationID)
	if err != nil {
		return "", err
	}
	if !isChild {
		if taskToken != "" {
			return "", errors.New("a task token was handed to a conversation that is not a mission child's session")
		}
		return piped, nil
	}
	if piped != "" {
		return "", errors.New("a mission child's turn cannot carry a mission confirmation")
	}
	if taskToken == "" {
		return "", errors.New("this conversation is the session of a mission task and runs only with its task token, which the coder host hands over; this turn did not get one")
	}
	b := taskbind.Resolve(context.Background(), db, profileID, taskToken)
	if b.State != taskbind.Valid || b.Verifier || b.Session != task.SessionID || b.Task != task.TaskID {
		return "", errors.New("the task token this child was handed is not the one for its task")
	}
	_ = os.Setenv(agenttokens.EnvMissionToken, taskToken)
	return "", nil
}

// runChildTaskGate is childTaskGate over the profile's database.
func runChildTaskGate(cfg *Deps, profileID, conversationID, piped string) (string, error) {
	db, err := cfg.OpenDB()
	if err != nil {
		return "", err
	}
	defer db.Close()
	return childTaskGate(db.DB, profileID, conversationID, piped)
}
