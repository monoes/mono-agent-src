package ref

import (
	"strings"
	"testing"
)

// `ref tasks` says how an agent reaches the board over MCP, and how a session-start hook shows
// the ready work (spec 9 items 1 and 6, 15.2).
func TestRefTasksDescribesTheTaskToolsAndTheHook(t *testing.T) {
	t.Parallel()
	text := strings.Join(strings.Fields(TasksText), " ")
	for _, want := range []string{
		"FROM MCP", "task_list", "task_next", "task_claim", "task_add", "--allow-mutations", "_untrusted",
		"agent:<client>#<4 hex digits>", "No tool approves, edits, moves or archives a task", "MONOAGENT_MCP_TASKS_ONLY",
		"claude mcp add monoagent-tasks -- monoagentcli mcp --tasks-only --allow-mutations",
		"SESSION START", `"SessionStart"`, `"command": "monoagentcli --profile <id> task digest"`, "Nothing installs it",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("`ref tasks` does not say %q", want)
		}
	}
}

// `ref tasks` documents the session idempotency key and the flag-error convention.
func TestRefTasksDescribesClientIDAndFlagErrors(t *testing.T) {
	t.Parallel()
	text := strings.Join(strings.Fields(TasksText), " ")
	for _, want := range []string{"session new takes an optional --client-id KEY", "deduped true", `exit 1 with {"error"} and no code`} {
		if !strings.Contains(text, want) {
			t.Errorf("`ref tasks` does not say %q", want)
		}
	}
}

func TestRefTasksDescribesTheMissionWritersDefault(t *testing.T) {
	t.Parallel()
	text := strings.Join(strings.Fields(TasksText), " ")
	for _, want := range []string{"defaults to isolated while the operator never set orgWriters", "writers show the mode in effect"} {
		if !strings.Contains(text, want) {
			t.Errorf("`ref tasks` does not say %q", want)
		}
	}
}
