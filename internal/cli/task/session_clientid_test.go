package task

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
)

// --client-id makes a personal add idempotent; a session task cannot honour it,
// so it is refused instead of silently adding a duplicate on a retry.
func TestSessionAddRefusesClientID(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := sessionNew(t, db)
	doc := failedTaskJSON(t, db, "default", 3, "add", "x", "--session", id(s), "--acceptance", "y", "--client-id", "k1")
	if doc["code"] != "invalid_input" || !strings.Contains(doc["error"].(string), "--client-id") {
		t.Fatalf("a session task with --client-id: %v", doc)
	}
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "show", id(s))
	if d.Session.Counts.Total != 0 {
		t.Fatalf("a refused add left %d tasks", d.Session.Counts.Total)
	}
}

// A token that does not verify is a broken worker environment, not a worker without one: the
// process must not act as if nothing bound it.
func TestSessionUnverifiableTokenFailsClosed(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := sessionNew(t, db)
	personal := opsAdd(t, db, "personal", "--ready")
	for _, raw := range []string{"abc", "mat-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		setenv(t, agenttokens.EnvMissionToken, raw)
		for _, args := range [][]string{
			{"add", "x", "--session", id(s), "--acceptance", "y", "--as", "w"},
			{"show", id(personal), "--as", "w"}, {"list", "--as", "w"}, {"next", "--as", "w"},
			{"claim", id(personal), "--as", "w"}, {"add", "forbidden", "--as", "w"}, {"move", id(personal), "done"},
		} {
			if _, _, err := runTask(t, db, "default", true, "", args...); err == nil {
				t.Errorf("token %q: %v was allowed", raw, args)
			}
		}
	}
	setenv(t, agenttokens.EnvMissionToken, "")
	if _, _, err := runTask(t, db, "default", true, "", "show", id(personal)); err != nil {
		t.Fatalf("unbound operator show: %v", err)
	}
	if _, _, err := runTask(t, db, "default", true, "", "list"); err != nil {
		t.Fatalf("unbound operator list: %v", err)
	}
}
