//go:build linux

package coderhost

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// The host-row write has the same rule as every raw BEGIN IMMEDIATE (#1373).
func TestWriteHostRowCancelledBeginLeavesNoOpenTransactionOnThePooledConnection(t *testing.T) {
	db, trip := dbconntest.Open(t)
	h := &BindingHost{DB: db}
	trip.Arm()
	_, err := h.writeHostRow(context.Background(), []byte("k"), "p", "gen-1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the setup should fail the begin with the cancellation: %v", err)
	}
	dbconntest.RequireClean(t, db)
}
