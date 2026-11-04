package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// A BEGIN that went through but is reported as cancelled must not leave its connection inside the transaction
// (#1373): the next user of the pool would fail with "cannot start a transaction within a transaction".
func TestUpdateSettingsCancelledBeginLeavesNoOpenTransactionOnThePooledConnection(t *testing.T) {
	db, trip := dbconntest.Open(t)
	s := &Store{db: db, now: time.Now}
	trip.Arm()
	_, err := s.UpdateSettings("p", "c", func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) { return cur, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the setup should fail the begin with the cancellation: %v", err)
	}
	dbconntest.RequireClean(t, db)
}
