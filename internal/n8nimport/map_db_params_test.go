package n8nimport

import "testing"

func TestDBParamTablesMapQueueAndSQLNodes(t *testing.T) {
	wf, rep := convertString(t, `{"name":"t","nodes":[
	 {"name":"M","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	 {"name":"SQL","type":"n8n-nodes-base.microsoftSql","typeVersion":1,"position":[200,0],
	  "parameters":{"operation":"executeQuery","query":"SELECT 1"},"credentials":{"microsoftSql":{"id":"1","name":"SQL"}}},
	 {"name":"K","type":"n8n-nodes-base.kafka","typeVersion":1,"position":[400,0],
	  "parameters":{"topic":"events","sendInputData":false,"message":"hi"},"credentials":{"kafka":{"id":"2","name":"K"}}}],
	 "connections":{"M":{"main":[[{"node":"SQL","type":"main","index":0}]]},"SQL":{"main":[[{"node":"K","type":"main","index":0}]]}}}`)
	sql := nodeByName(wf, "SQL")
	if sql.Type != "db.mssql" || sql.Config["query"] != "SELECT 1" || sql.Config["credential_id"] != "mssql" {
		t.Errorf("mssql: %s %v", sql.Type, sql.Config)
	}
	k := nodeByName(wf, "K")
	if k.Type != "queue.kafka" || k.Config["topic"] != "events" || k.Config["send_input_data"] != false || k.Config["credential_id"] != "kafka" {
		t.Errorf("kafka: %s %v", k.Type, k.Config)
	}
	if !rep.Complete {
		t.Errorf("report: %s", rep.Text())
	}
}
