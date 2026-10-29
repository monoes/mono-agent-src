package task

import "testing"

// A process holding an agent token never acts as the person, whatever else its environment says.
func TestPersonVerbsRefuseAProcessHoldingAnAgentToken(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"MONOAGENT_SESSION_TOKEN", "MONOAGENT_TASK_BINDING", "MONOAGENT_TASK_SESSION", "MONOAGENT_TASK_ID"} {
		t.Run(key, func(t *testing.T) {
			db, _, big := giantWithItem(t)
			setenv(t, key, "x")
			for _, v := range [][]string{{"inbox"}, {"approve", id(big), "--rev", "1"}, {"deny", id(big), "--note", "n"}, {"ask", id(big), "--text", "t"}} {
				doc := failedTaskJSON(t, db, "default", 3, append([]string{"session", "person"}, v...)...)
				if doc["code"] != "operator_only" {
					t.Errorf("%s %v: %v", key, v, doc)
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
