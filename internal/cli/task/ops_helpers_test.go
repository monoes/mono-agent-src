package task

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// opsTask is a task as the operator's commands print it, with what a test compares to
// see that a command changed nothing.
type opsTask struct {
	taskJSON
	Position  int64  `json:"position"`
	UpdatedAt string `json:"updated_at"`
}

// opsShown is `task show --json`.
type opsShown struct {
	Task   opsTask `json:"task"`
	Events []struct {
		Actor      string `json:"actor"`
		Kind       string `json:"kind"`
		FromStatus string `json:"from_status"`
		ToStatus   string `json:"to_status"`
		Note       string `json:"note"`
	} `json:"events"`
}

// kinds are the kinds of the events of the task, oldest first, one word each: "created moved".
func (s opsShown) kinds() string {
	words := make([]string, 0, len(s.Events))
	for _, e := range s.Events {
		words = append(words, e.Kind)
	}
	return strings.Join(words, " ")
}

// opsShow reads one task of the default profile with its history.
func opsShow(t *testing.T, db string, n int64) opsShown {
	t.Helper()
	var s opsShown
	mustTaskJSON(t, db, "default", &s, "", "show", id(n))
	return s
}

// opsAdd adds a task of the default profile by the operator's command and returns its id.
func opsAdd(t *testing.T, db, title string, flags ...string) int64 {
	t.Helper()
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", append([]string{"add", title}, flags...)...)
	return added.Task.ID
}

// opsOrder is the titles of one column of the default profile, top to bottom, joined by commas.
func opsOrder(t *testing.T, db, status string) string {
	t.Helper()
	var l listJSON
	mustTaskJSON(t, db, "default", &l, "", "list", "--status", status)
	return strings.Join(titlesOf(l), ",")
}

// opsRev is the revision of the default profile's board.
func opsRev(t *testing.T, db string) int64 {
	t.Helper()
	var b struct {
		Rev int64 `json:"rev"`
	}
	mustTaskJSON(t, db, "default", &b, "", "board")
	return b.Rev
}

// opsBrokenDB is a database path that can never be opened: its parent is a regular file.
// A command that gets as far as opening the database fails on it with a plain error.
func opsBrokenDB(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "a-regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(file, "sub", "tasks.db")
}

// opsKeys are the top-level keys of a JSON document, sorted and joined by commas.
func opsKeys(t *testing.T, doc string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, doc)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
