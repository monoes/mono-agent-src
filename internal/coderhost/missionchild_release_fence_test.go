package coderhost

import (
	"reflect"
	"testing"
)

// The not-an-attempt release also accepts a row the conversation's own deletion marked gone, but only of its own
// generation. A newer reservation of the same task that has since been ended by a deletion leaves a gone/deleted
// row that an older attempt's late release must not touch: it would hand back an attempt the newer one used.
func TestStaleReleaseCannotRefundNewerGoneDeletedRow(t *testing.T) {
	f := newHostFix(t)
	f.mark(t, 0)
	old, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	// Attempt 1's release is held back. Its row is given up some other way and the task is marked again.
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET state = 'marked', pending_conversation_id = '', reserved_usd = 0 WHERE session_id = ? AND task_id = ?`, f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	newer := f.task(t, 0, 1)
	if err := mustAI(t, f.st).DeleteConversation(newer.ConversationID, "p"); err != nil {
		t.Fatal(err)
	}
	before := f.row(t, 0)
	if before.State != ChildGone || before.EndReason != EndDeleted {
		t.Fatalf("setup: the newer row should be gone/deleted, got %+v", before)
	}
	if before.Generation <= old.Generation {
		t.Fatalf("setup: the newer reservation needs a higher generation: old %d, new %d", old.Generation, before.Generation)
	}
	if err := f.st.releaseReservation("p", f.se.ID, f.ids[0], old.Generation, f.lead.ConversationID, false, "late refusal"); err != nil {
		t.Fatal(err)
	}
	if after := f.row(t, 0); !reflect.DeepEqual(before, after) {
		t.Fatalf("the older attempt's release changed the newer gone row (attempts refunded?): before=%+v after=%+v", before, after)
	}
}
