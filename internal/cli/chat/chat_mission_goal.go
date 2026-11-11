package chat

import (
	"context"
	"database/sql"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit"
)

// goalCloseGate is the lead's close refusal for the ultimate goal (dynorg.MissionConfig.GoalClose): a mission whose goal is locked
// by a started pursuit is not proposed for close until the person has accepted a check of it. Whether the host runs goal checks at
// all is read from the state the host published (display-only, so it can only ADD a refusal: a pin asked for counts as on, and a
// missing or unreadable file counts as off only while no check has ever run). The person's own close does not pass here.
func goalCloseGate(db *sql.DB, profileID, dbPath string) func(context.Context, int64) string {
	return func(ctx context.Context, session int64) string {
		info, _ := coderhost.ReadHostPursuit(clicore.ExpandPath(dbPath))
		return missionpursuit.NewStore(db).CloseRefusal(ctx, profileID, session, info.VerifierPin != "")
	}
}
