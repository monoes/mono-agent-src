package coderhost

import "testing"

func countOf(t *testing.T, f *mcFix, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.st.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Deleting a child's conversation closes the request that named it (it stays as the lead's record, void, and
// is not an open item); deleting the lead's conversation removes the lead's requests and their person items.
func TestDeletingAConversationClosesOrRemovesItsBudgetRequests(t *testing.T) {
	f, child := budgetFix(t)
	forChild := f.ask(t, TargetChild, 1)
	forLead := f.ask(t, TargetLead, 1)
	if err := mustAI(t, f.st).DeleteConversation(child, "p"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.GetBudgetRequest("p", forChild.ID); got.Status != BudgetRequestVoid || got.Open {
		t.Fatalf("the child's request after the child was deleted: %+v", got)
	}
	if got, _ := f.st.GetBudgetRequest("p", forLead.ID); got.Status != BudgetRequestOpen || !got.Open {
		t.Fatalf("the lead's own request changed when a child was deleted: %+v", got)
	}
	if err := mustAI(t, f.st).DeleteConversation(f.lead.ConversationID, "p"); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, f, `SELECT COUNT(*) FROM coder_budget_requests WHERE lead_conversation_id = ?`, f.lead.ConversationID); n != 0 {
		t.Fatalf("%d requests of the deleted lead remain", n)
	}
	if n := countOf(t, f, `SELECT COUNT(*) FROM coder_session_person_items WHERE kind = ?`, ItemBudgetRequest); n != 0 {
		t.Fatalf("%d budget person items remain", n)
	}
	if items, _ := f.st.OpenPersonItems("p"); len(items) != 0 {
		t.Fatalf("open items after the lead was deleted: %+v", items)
	}
}
