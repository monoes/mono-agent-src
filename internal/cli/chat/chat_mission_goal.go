package chat

import (
	"context"
	"database/sql"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit"
)

// readHostState is the host's published state and whether the host that wrote it is alive; a variable only so a test can say so.
var readHostState = coderhost.ReadHostPursuitState

// goalCloseGate is the lead's close refusal for the ultimate goal (dynorg.MissionConfig.GoalClose): a mission whose goal is locked
// by a started pursuit is not proposed for close until the person has accepted a check of it. Whether the host runs goal checks at
// all is read from the state the host published (display-only), and counts as on only when a live host says its checks are set up: a
// leftover file of a host that died, or of one restarted with the checks off, adds no refusal, since no check would ever come. Checks
// that already exist hold the close back whatever the file says. The person's own close does not pass here.
func goalCloseGate(db *sql.DB, profileID, dbPath string) func(context.Context, int64) string {
	return func(ctx context.Context, session int64) string {
		info, found, alive := readHostState(clicore.ExpandPath(dbPath))
		return missionpursuit.NewStore(db).CloseRefusal(ctx, profileID, session, found && alive && info.VerifierOn)
	}
}
