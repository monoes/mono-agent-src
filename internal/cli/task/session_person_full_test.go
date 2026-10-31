package task

import (
	"strings"
	"testing"
)

type fullView struct {
	Items []struct {
		SessionID int64  `json:"sessionId"`
		Rev       int64  `json:"rev"`
		Digest    string `json:"digest"`
		Tasks     []struct {
			ID        int64  `json:"id"`
			Notes     string `json:"notes"`
			Truncated bool   `json:"truncated"`
		} `json:"tasks"`
	} `json:"items"`
}

type showView struct {
	Item struct {
		SessionID int64  `json:"sessionId"`
		Rev       int64  `json:"rev"`
		Digest    string `json:"digest"`
		Tasks     []struct {
			ID        int64  `json:"id"`
			Notes     string `json:"notes"`
			Truncated bool   `json:"truncated"`
		} `json:"tasks"`
	} `json:"item"`
}

// A note over the cut reaches the inbox cut and marked truncated; `person full` returns it whole
// under the same rev and digest the inbox carried.
func TestPersonShowReturnsTheFullTextUnderTheInboxGuard(t *testing.T) {
	t.Parallel()
	db, _, big := giantWithItem(t)
	long := strings.Repeat("n", 399) + "HIDDEN INSTRUCTION"
	sessionAdd(t, db, big, "long", "--notes", long)
	var in fullView
	mustTaskJSON(t, db, "default", &in, "", "session", "person", "inbox")
	var cut, whole bool
	for _, tk := range in.Items[0].Tasks {
		if tk.Truncated {
			cut = true
			if strings.Contains(tk.Notes, "HIDDEN") {
				t.Fatalf("the inbox must be cut: %q", tk.Notes)
			}
		}
	}
	if !cut {
		t.Fatalf("a cut task must say so: %+v", in.Items[0].Tasks)
	}
	var sh showView
	mustTaskJSON(t, db, "default", &sh, "", "session", "person", "full", id(big))
	for _, tk := range sh.Item.Tasks {
		if tk.Truncated {
			t.Fatalf("show must not truncate: %+v", tk)
		}
		if tk.Notes == long {
			whole = true
		}
	}
	if !whole {
		t.Fatalf("show lost the full note: %+v", sh.Item.Tasks)
	}
	if sh.Item.Rev != in.Items[0].Rev || sh.Item.Digest != in.Items[0].Digest {
		t.Fatalf("show carries another guard: %+v vs %+v", sh.Item, in.Items[0])
	}
}

func TestPersonShowRefusesASessionWithNoOpenItem(t *testing.T) {
	t.Parallel()
	db, root, _ := giantWithItem(t)
	doc := failedTaskJSON(t, db, "default", 2, "session", "person", "full", id(root))
	if doc["error"] == nil {
		t.Fatalf("show for a session nobody raised: %v", doc)
	}
}
