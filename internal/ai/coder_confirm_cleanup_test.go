package ai

import "testing"

// Deleting a conversation also removes the mission confirmations of its waiting sends, and only
// those: another conversation's, and another profile's, stay.
func TestChatEvents_DeleteConversationRemovesItsMissionConfirmations(t *testing.T) {
	s, db := newChatEventStore(t)
	gone, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	kept, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	for _, name := range []string{
		"coder-mission-confirm/p1/" + gone.ID + "/t1/4",
		"coder-mission-confirm/p1/" + kept.ID + "/t1/5",
		"coder-mission-confirm/p2/" + gone.ID + "/t1/6",
	} {
		if _, err := db.Exec(`INSERT INTO configs (name, config_data) VALUES (?, '{}')`, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteConversation(gone.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	var names []string
	rows, err := db.Query(`SELECT name FROM configs WHERE name LIKE 'coder-mission-confirm/%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	if len(names) != 2 || names[0] != "coder-mission-confirm/p1/"+kept.ID+"/t1/5" {
		t.Fatalf("records left = %v, want the kept conversation's and the other profile's", names)
	}
}
