package task

import (
	"strings"
	"testing"
)

func sessionNewDoc(t *testing.T, db string, args ...string) map[string]any {
	t.Helper()
	var d map[string]map[string]any
	mustTaskJSON(t, db, "default", &d, "", append([]string{"session", "new"}, args...)...)
	return d["session"]
}

// `session new --client-id` repeated returns the same session (exit 0) with deduped true
// and no second row; without a key every call makes a session, as before.
func TestSessionNewClientIDRepeatReturnsTheSameSession(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	first := sessionNewDoc(t, db, "Goal", "--client-id", "click-1")
	again := sessionNewDoc(t, db, "Goal", "--client-id", "click-1")
	if first["id"] != again["id"] || again["deduped"] != true || first["deduped"] != nil || again["client_id"] != "click-1" {
		t.Fatalf("first %v again %v", first, again)
	}
	var list struct{ Sessions []map[string]any }
	mustTaskJSON(t, db, "default", &list, "", "session", "list")
	if len(list.Sessions) != 1 {
		t.Fatalf("%d sessions", len(list.Sessions))
	}
	a, b := sessionNewDoc(t, db, "Plain"), sessionNewDoc(t, db, "Plain")
	if a["id"] == b["id"] || a["deduped"] != nil || b["deduped"] != nil {
		t.Fatalf("no key: %v %v", a, b)
	}
}

func TestSessionNewClientIDRefusedWhenGarbled(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, bad := range []string{strings.Repeat("a", 65), "two words", "a/b"} {
		doc := failedTaskJSON(t, db, "default", 3, "session", "new", "Goal", "--client-id", bad)
		if doc["code"] != "invalid_input" {
			t.Errorf("%q: %v", bad, doc)
		}
	}
	var list struct{ Sessions []map[string]any }
	mustTaskJSON(t, db, "default", &list, "", "session", "list")
	if len(list.Sessions) != 0 {
		t.Fatalf("a refused key left %d sessions", len(list.Sessions))
	}
}
