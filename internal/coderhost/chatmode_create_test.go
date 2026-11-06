package coderhost

import (
	"context"
	"database/sql"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
)

// A conversation is created with its mode in one transaction: when the mode cannot be set, no conversation and no
// session row is left behind (so creation needs no cleanup delete).
func TestConversationAndChatModeAreCreatedTogetherOrNotAtAll(t *testing.T) {
	st := newStoreT(t)
	a, _ := ai.NewAIStore(st.db)
	count := func(q string) int {
		var n int
		if err := st.db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	make := func(mode string) (ai.Conversation, error) {
		return a.CreateConversationAccessIn("p", "agent", "", "claude", "", "m", ai.AccessFull, t.TempDir(), "",
			func(ctx context.Context, conn *sql.Conn, id string) error { return st.SetNewChatModeOn(ctx, conn, "p", id, mode) })
	}
	for _, bad := range []string{"open", "doomsday"} { // open is not available yet; the other is no mode
		if _, err := make(bad); err == nil {
			t.Fatalf("mode %q was accepted", bad)
		}
	}
	if n := count(`SELECT COUNT(*) FROM ai_chat_conversations`) + count(`SELECT COUNT(*) FROM coder_sessions`); n != 0 {
		t.Fatalf("a refused creation left %d row(s) behind", n)
	}
	c, err := make("mission")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ChatMode("p", c.ID); got != ChatModeMission {
		t.Fatalf("mode = %q", got)
	}
}
