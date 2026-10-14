package summarycli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

func TestSummaryTasksSectionIsTheProfilesBoard(t *testing.T) {
	cfg := newSummaryCLITestDB(t)
	cfg.JSONOutput = false
	if text, err := runSummary(t, cfg, "--section", "tasks"); err != nil || strings.Contains(text, "tasks ") {
		t.Errorf("an empty board prints a tasks line: %q, %v", text, err)
	}
	cfg.JSONOutput = true
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES ('p-work', 'Work')`); err != nil {
		t.Fatal(err)
	}
	store := tasks.NewStore(db.DB)
	var next tasks.Task
	for _, a := range []struct {
		profile, title string
		ready          bool
	}{{"default", "captured", false}, {"default", "Fix the flaky test", true}, {"p-work", "another profile's", true}} {
		tk, _, err := store.Add(context.Background(), a.profile, tasks.AddInput{Title: a.title, Ready: a.ready}, tasks.Actor{Kind: tasks.Human})
		if err != nil {
			t.Fatal(err)
		}
		if a.title == "Fix the flaky test" {
			next = tk
		}
	}
	db.Close()

	out, err := runSummary(t, cfg, "--section", "tasks")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tasks *struct {
			Inbox int `json:"inbox"`
			Ready int `json:"ready"`
			Next  *struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			} `json:"next"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Tasks == nil {
		t.Fatalf("no tasks section: %v\n%s", err, out)
	}
	if got.Tasks.Inbox != 1 || got.Tasks.Ready != 1 || got.Tasks.Next == nil || got.Tasks.Next.ID != next.ID {
		t.Errorf("tasks = %s", out)
	}

	cfg.JSONOutput = false
	text, err := runSummary(t, cfg, "--section", "tasks")
	if err != nil || !strings.Contains(text, "tasks         1 inbox, 1 ready") || !strings.Contains(text, "Fix the flaky test") {
		t.Errorf("text: %q, %v", text, err)
	}
	cfg.JSONOutput = true

	// A board is one profile's (D9): the all-profiles view has none, and asking for it there is refused.
	out, err = runSummary(t, cfg, "--all-profiles")
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		t.Fatal(err)
	}
	if _, ok := all["tasks"]; ok {
		t.Errorf("summary --all-profiles counts tasks across profiles: %s", all["tasks"])
	}
	if _, err := runSummary(t, cfg, "--all-profiles", "--section", "tasks"); exitCode(err) != 3 {
		t.Errorf("--all-profiles --section tasks: exit %d, want 3", exitCode(err))
	}
}
