package n8nimport_test

import "testing"

// n8n's Always Output Data and Execute Once node settings carry over.
func TestNodeSettingsAlwaysOutputAndExecuteOnce(t *testing.T) {
	wf, rep := convertNodes(t, `{"name":"F","type":"n8n-nodes-base.filter","typeVersion":2,"position":[0,0],
	  "alwaysOutputData":true,"executeOnce":true,"parameters":{}}`)
	f := node(t, wf, "F")
	if f.Config["always_output_data"] != true || f.Config["execute_once"] != true {
		t.Fatalf("config %v\n%s", f.Config, issueText(rep, "F"))
	}
}
