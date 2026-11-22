package chat

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// A turn stopped while it was still starting up (a handshake cut short by the
// stop) ends cancelled/stopped, not as a runner error.
func TestJournalFailOrStopRecordsAStopAsCancelled(t *testing.T) {
	cases := []struct {
		name       string
		cancelled  bool
		wantStatus chatevents.TurnStatus
	}{
		{"stopped during start-up", true, chatevents.StatusCancelled},
		{"a real start-up failure", false, chatevents.StatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := newChatCLITestDB(t)
			store := openTestChatStore(t, dbPath)
			conv, _ := store.CreateConversation("default", "agent", "general", "fake", "", "")
			if _, _, err := store.CreateTurn(conv.ID, "default", "t1", "", "hi"); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			j := newTurnJournal(store, "default", conv.ID, "t1", "fake", &out)
			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancelled {
				cancel()
			}
			defer cancel()
			j.failOrStop(ctx, errors.New("handshake with monomind failed: context canceled"))
			turn, err := store.GetTurn("t1", "default")
			if err != nil {
				t.Fatal(err)
			}
			if turn.Status != string(tc.wantStatus) {
				t.Errorf("status = %q (%s), want %q", turn.Status, turn.Reason, tc.wantStatus)
			}
		})
	}
}
