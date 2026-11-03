package ai

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// A BEGIN that went through but is reported as cancelled must not leave its connection inside the transaction
// (#1373): the next user of the pool would fail with "cannot start a transaction within a transaction".
func TestDeleteConversationCancelledBeginLeavesNoOpenTransactionOnThePooledConnection(t *testing.T) {
	db, trip := dbconntest.Open(t)
	// The keys are derived before the transaction, from the v2 token tables.
	mig, err := os.ReadFile("../../data/migrations/148_procbind_v2_tables.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(mig)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ai_chat_conversations (id TEXT, profile_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	s := &AIStore{db: db}
	trip.Arm()
	err = s.DeleteConversation("c", "p")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the setup should fail the begin with the cancellation: %v", err)
	}
	dbconntest.RequireClean(t, db)
}
