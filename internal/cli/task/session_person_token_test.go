package task

import (
	"strconv"
	"testing"
)

// personVerbArgs is every verb of `task session person` with arguments that would otherwise
// succeed. TestEveryPersonVerbIsCovered fails when a verb is added without a row here, so a new
// operator-only verb cannot skip the token-holder check unnoticed.
func personVerbArgs(big int64) map[string][]string {
	return map[string][]string{
		"inbox":      {"inbox"},
		"approve":    {"approve", id(big), "--rev", "1"},
		"deny":       {"deny", id(big), "--note", "n"},
		"ask":        {"ask", id(big), "--text", "t"},
		"answer-ask": {"answer-ask", "--ask", strconv.Itoa(1), "--text", "t"},
	}
}

func TestEveryPersonVerbIsCovered(t *testing.T) {
	t.Parallel()
	rows := personVerbArgs(1)
	for _, c := range testDeps(t, "", "default", true).newSessionPersonCmd().Commands() {
		if _, ok := rows[c.Name()]; !ok {
			t.Errorf("person verb %q has no row in personVerbArgs: add it so the token-holder refusal is tested", c.Name())
		}
	}
}

// A process holding an agent token never acts as the person, whatever else its environment says:
// every person verb, every marker.
func TestPersonVerbsRefuseAProcessHoldingAnAgentToken(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"MONOAGENT_SESSION_TOKEN", "MONOAGENT_TASK_BINDING", "MONOAGENT_TASK_SESSION", "MONOAGENT_TASK_ID"} {
		t.Run(key, func(t *testing.T) {
			db, _, big := giantWithItem(t)
			setenv(t, key, "x")
			for name, v := range personVerbArgs(big) {
				doc := failedTaskJSON(t, db, "default", 3, append([]string{"session", "person"}, v...)...)
				if doc["code"] != "operator_only" {
					t.Errorf("%s %s: %v", key, name, doc)
				}
			}
			if items := openItems(t, db); len(items) != 1 {
				t.Fatalf("a refused process changed the items: %+v", items)
			}
			if n := len(queuedFor(t, db, "conv-lead")); n != 0 {
				t.Fatalf("a refused process queued %d messages", n)
			}
		})
	}
}
