package browserjev

import (
	"context"
	"database/sql"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// serverInTurn is the turn check a server process asks (the registry scan `workflow run` asks through the
// command line): a var so a test can place the process in a turn.
var serverInTurn = coderhost.CallerInARegisteredTurn

// MarkAICallerForServer is MarkAICallerFor for a server (MCP, HTTP API) that starts a run for a caller that is
// not the user's terminal (why says so). db is the profile's open database, where a run token is looked up;
// dbPath names it for the turn check. A turn check that fails counts as a turn, as it does for `workflow run`.
func MarkAICallerForServer(ctx context.Context, db *sql.DB, dbPath string, data map[string]interface{}, why string) {
	inTurn, err := serverInTurn(dbPath)
	if err != nil {
		inTurn = true
	}
	MarkAICallerFor(ctx, func() (*sql.DB, error) { return db, nil }, data, why, inTurn)
}
