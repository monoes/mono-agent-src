package apiconfig

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// A BEGIN that went through but is reported as cancelled must not leave its connection inside the transaction
// (#1373): the next user of the pool would fail with "cannot start a transaction within a transaction".
func TestUpdateCancelledBeginLeavesNoOpenTransactionOnThePooledConnection(t *testing.T) {
	db, trip := dbconntest.Open(t)
	trip.Arm()
	err := Update(context.Background(), db, func(*Settings) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the setup should fail the begin with the cancellation: %v", err)
	}
	dbconntest.RequireClean(t, db)
}
