package task

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// A budget lowered under a live cap run is refused with its own code and the exit status of a limit, in words that
// name the run and the true ways out, without the store's sentinel text in front.
func TestSessionBudgetLowerWhileACapRunIsLiveIsRefusedWithItsOwnCode(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	session := runningSession(t, db)
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "budget", id(session), "--usd", "10")
	d2, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if _, err := tasks.NewStore(d2.DB).BeginCapRun(context.Background(), "default", session, "owner-x", "run-x"); err != nil {
		t.Fatal(err)
	}
	doc := failedTaskJSON(t, db, "default", 3, "session", "budget", id(session), "--usd", "4")
	if doc["code"] != "accounting_mode" {
		t.Fatalf("code = %v in %v", doc["code"], doc)
	}
	msg, _ := doc["error"].(string)
	for _, want := range []string{"run-x", "owner-x", "task session stop"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message %q does not show %q", msg, want)
		}
	}
	if strings.Contains(msg, "accounted in the other mode") || strings.Contains(msg, "Pause the session") {
		t.Errorf("sentinel noise or the wrong remedy in %q", msg)
	}
	if _, _, err := runTask(t, db, "default", true, "", "session", "budget", id(session), "--usd", "12"); err != nil {
		t.Fatalf("a raise is allowed: %v", err)
	}
}
