package ref

import (
	"strings"
	"testing"
)

// `ref tasks` says where a task from the Chrome extension lands (task board spec 11, 15.2).
func TestRefTasksSaysWhereChromeTasksLand(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"FROM CHROME", "MonoAgent Bridge", "Saving into", "Inbox"} {
		if !strings.Contains(TasksText, want) {
			t.Errorf("`ref tasks` does not mention %q", want)
		}
	}
}
