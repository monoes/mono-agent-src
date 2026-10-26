package chat

import (
	"context"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/tasks"
)

// openMissionSessionID is the mission state a confirmation is bound to: the id
// of the conversation's open mission session, 0 when it has none.
func openMissionSessionID(ctx context.Context, cfg *Deps, profileID, conversationID string) int64 {
	db, err := cfg.OpenDB()
	if err != nil {
		return 0
	}
	defer db.Close()
	m := &dynorg.MissionConfig{Store: tasks.NewStore(db.DB), ProfileID: profileID, ConversationID: conversationID, Actor: missionActor}
	return m.OpenSessionID(ctx)
}

// hostMissionConfirmed is the chat child's check of a host-run turn's mission
// confirmation: the nonce the host passed must match the live record the
// operator's `coder sessions send --mission` wrote for this conversation and
// turn, signed under the profile's key, for the mission state it is in now. It
// consumes the record, so the same turn id cannot start a mission twice. A turn
// without a token, a host-made turn, an agent-written command row or config row,
// a message that says so: none confirms. The host's environment is not trusted
// to mean "the operator": the record is.
func hostMissionConfirmed(cfg *Deps, profileID, conversationID, turnID, token, access string) bool {
	if token == "" || !dynorg.MissionAccess(access) {
		return false
	}
	ctx := context.Background()
	session := openMissionSessionID(ctx, cfg, profileID, conversationID)
	db, err := cfg.OpenDB()
	if err != nil {
		return false
	}
	defer db.Close()
	return coderhost.NewStore(db.DB).ConsumeConfirm(profileID, conversationID, turnID, token, session)
}

// confirmMission writes the operator's confirmation of a send already queued
// as a mission, bound to its command. Only the operator-gated send --mission
// reaches it. A variable so a test can make the record unwritable.
var confirmMission = func(cfg *Deps, cmd coderhost.Command, session int64) error {
	db, err := cfg.OpenDB()
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := coderhost.NewStore(db.DB).Confirm(cmd, session); err != nil {
		return clicore.InvalidInput("%v", err)
	}
	return nil
}

// failQueuedMission ends a queued mission send whose confirmation could not be
// recorded, so it does not wait to be failed by the host.
func failQueuedMission(cfg *Deps, id int64, reason string) {
	if db, err := cfg.OpenDB(); err == nil {
		defer db.Close()
		_ = coderhost.NewStore(db.DB).SetCommand(id, coderhost.CmdFailed, reason)
	}
}
