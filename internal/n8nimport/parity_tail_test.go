package n8nimport_test

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/workflow"
)

func convertNodes(t *testing.T, nodes string) (*workflow.WorkflowFile, *n8nimport.Report) {
	t.Helper()
	wf, rep, err := n8nimport.Convert([]byte(`{"name":"t","nodes":[` + nodes + `],"connections":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	return wf, rep
}

// n8n's Twitter v1 node names the DM recipient userId; it imports as the
// v2 package's user field (numeric ids are used as is at run time).
func TestTwitterV1DirectMessageUserID(t *testing.T) {
	wf, rep := convertNodes(t, `{"name":"DM","type":"n8n-nodes-base.twitter","typeVersion":1,"position":[0,0],
	  "parameters":{"resource":"directMessage","userId":"783214","text":"hi"}}`)
	dm := node(t, wf, "DM")
	if dm.Type != "app.twitter" {
		t.Fatalf("type %s", dm.Type)
	}
	wantConfig(t, dm, map[string]interface{}{"resource": "directMessage", "operation": "create", "user": "783214", "text": "hi"})
	if _, ok := dm.Config["userId"]; ok {
		t.Errorf("userId kept: %v\n%s", dm.Config, issueText(rep, "DM"))
	}
}

// A SharePoint trigger on a document library polls the library's files
// (Graph delta) and passes on new or changed ones; deletions are reported
// as lost.
func TestSharePointLibraryTriggerPollsFiles(t *testing.T) {
	wf, rep := convertNodes(t, `{"name":"SP","type":"n8n-nodes-base.microsoftSharePointTrigger","typeVersion":1,"position":[0,0],
	  "parameters":{"site":{"__rl":true,"mode":"id","value":"contoso.sharepoint.com,a,b"},
	    "drive":{"__rl":true,"mode":"id","value":"b!x1"}}}`)
	list := node(t, wf, "SP (list)")
	if list.Type != "app.microsoft_share_point" {
		t.Fatalf("list type %s:\n%s", list.Type, issueText(rep, "SP"))
	}
	wantConfig(t, list, map[string]interface{}{"resource": "file", "operation": "getChanges",
		"site": "contoso.sharepoint.com,a,b", "drive": "b!x1"})
	if d := node(t, wf, "SP").Config["dedupeValue"]; d != "{{ $json.id }}|{{ $json.lastModifiedDateTime }}" {
		t.Errorf("dedupeValue = %v", d)
	}
	if text := issueText(rep, "SP"); !strings.Contains(text, "deleted files are not reported") {
		t.Errorf("report:\n%s", text)
	}

	_, rep = convertNodes(t, `{"name":"SP","type":"n8n-nodes-base.microsoftSharePointTrigger","typeVersion":1,"position":[0,0],
	  "parameters":{"site":"s","resource":"list","list":"L","events":["deleted"]}}`)
	if nr := nodeReport(t, rep, "SP"); nr.Status != n8nimport.StatusUnmapped || !strings.Contains(issueText(rep, "SP"), "deleted list items cannot be listed") {
		t.Errorf("deleted only: %s %s", nr.Status, issueText(rep, "SP"))
	}
	wf, rep = convertNodes(t, `{"name":"SP","type":"n8n-nodes-base.microsoftSharePointTrigger","typeVersion":1,"position":[0,0],
	  "parameters":{"site":"s","resource":"list","list":"L","events":["changed"]}}`)
	wantConfig(t, node(t, wf, "SP (list)"), map[string]interface{}{"resource": "item", "operation": "getAll", "list": "L", "returnAll": true})
	if strings.Contains(issueText(rep, "SP"), "not reported") {
		t.Errorf("changed only warns:\n%s", issueText(rep, "SP"))
	}
}

// A Drive trigger on one file, or on the watched folder itself, reads it
// each poll and passes it on when its modifiedTime changed.
func TestDriveTriggerSpecificFileAndWatchedFolder(t *testing.T) {
	wf, rep := convertNodes(t, `{"name":"F","type":"n8n-nodes-base.googleDriveTrigger","typeVersion":1,"position":[0,0],
	  "parameters":{"triggerOn":"specificFile","event":"fileUpdated","fileToWatch":{"__rl":true,"mode":"list","value":"1AbCdEfGhIjK"}}},
	 {"name":"D","type":"n8n-nodes-base.googleDriveTrigger","typeVersion":1,"position":[0,200],
	  "parameters":{"triggerOn":"specificFolder","event":"watchFolderUpdated","folderToWatch":{"__rl":true,"mode":"id","value":"0FoLdEr123"}}}`)
	for name, id := range map[string]string{"F": "1AbCdEfGhIjK", "D": "0FoLdEr123"} {
		list := node(t, wf, name+" (list)")
		if list.Type != "service.google_drive" {
			t.Fatalf("%s list type %s:\n%s", name, list.Type, issueText(rep, name))
		}
		wantConfig(t, list, map[string]interface{}{"resource": "file", "operation": "get", "fileId": id})
		if d := node(t, wf, name).Config["dedupeValue"]; d != "{{ $json.id }}|{{ $json.modifiedTime }}" {
			t.Errorf("%s dedupeValue = %v", name, d)
		}
		if nr := nodeReport(t, rep, name); nr.Status == n8nimport.StatusUnmapped {
			t.Errorf("%s: %s", name, issueText(rep, name))
		}
	}
}

func shapeOf(t *testing.T, nodeJSON string) *workflow.WorkflowFile {
	t.Helper()
	wf, _ := convertNodes(t, nodeJSON)
	return wf
}

// Wufoo, Formstack and Form.io triggers reshape the posted payload the way
// n8n's simplified output did.
func TestFormTriggersShapePayloads(t *testing.T) {
	wufoo := shapeOf(t, `{"name":"W","type":"n8n-nodes-base.wufooTrigger","typeVersion":1,"position":[0,0],"webhookId":"wf","parameters":{"form":"f1"}}`)
	fields := `{"Fields":[{"ID":"Field1","Title":"Name","Type":"text"},{"ID":"Field2","Title":"CV","Type":"file"},
	  {"ID":"Field3","Title":"Likes","Type":"checkbox","SubFields":[{"ID":"Field3","Label":"Tea"},{"ID":"Field4","Label":"Coffee"}]},
	  {"ID":"Field5","Title":"Who","Type":"shortname","SubFields":[{"ID":"Field5","Label":"First"},{"ID":"Field6","Label":"Last"}]}]}`
	post := map[string]interface{}{"Field1": "Ada", "Field2": "cv.pdf", "Field2-url": "https://x.test/cv.pdf", "Field3": "Tea", "Field4": "",
		"Field5": "Ada", "Field6": "L", "FieldStructure": fields, "FormStructure": `{"Name":"Jobs"}`, "EntryId": "7", "HandshakeKey": "k"}
	expectJSON(t, "wufoo", runNode(t, wufoo, "W", []map[string]interface{}{{"body": post}}),
		`[{"Name":"Ada","CV":"https://x.test/cv.pdf","Likes":["Tea"],"Who":{"First":"Ada","Last":"L"}}]`)
	full := shapeOf(t, `{"name":"W","type":"n8n-nodes-base.wufooTrigger","typeVersion":1,"position":[0,0],"webhookId":"wf","parameters":{"form":"f1","onlyAnswers":false}}`)
	got := runNode(t, full, "W", []map[string]interface{}{{"body": post}})
	if len(got) != 1 || got[0]["entryId"] != "7" || got[0]["formStructure"].(map[string]interface{})["Name"] != "Jobs" {
		t.Errorf("wufoo full: %v", got)
	}

	fs := shapeOf(t, `{"name":"S","type":"n8n-nodes-base.formstackTrigger","typeVersion":1,"position":[0,0],"webhookId":"fs","parameters":{"formId":"9"}}`)
	expectJSON(t, "formstack", runNode(t, fs, "S", []map[string]interface{}{{"body": map[string]interface{}{
		"FormID": "9", "name": map[string]interface{}{"value": "Ada", "label": "Name"}, "tags": []interface{}{"a"}}}}),
		`[{"FormID":"9","name":"Ada","tags":["a"]}]`)
	raw := shapeOf(t, `{"name":"S","type":"n8n-nodes-base.formstackTrigger","typeVersion":1,"position":[0,0],"webhookId":"fs","parameters":{"formId":"9","simple":false}}`)
	for _, n := range raw.Nodes {
		if n.Type == "core.code" {
			t.Errorf("formstack raw output needs no shaping step: %s", n.Name)
		}
	}

	fio := shapeOf(t, `{"name":"I","type":"n8n-nodes-base.formIoTrigger","typeVersion":1,"position":[0,0],"webhookId":"fi","parameters":{"projectId":"p","formId":"f"}}`)
	sub := map[string]interface{}{"request": map[string]interface{}{"_id": "s1", "data": map[string]interface{}{"email": "ada@x.test"}}}
	expectJSON(t, "form.io", runNode(t, fio, "I", []map[string]interface{}{{"body": sub}}), `[{"email":"ada@x.test"}]`)
	fioRaw := shapeOf(t, `{"name":"I","type":"n8n-nodes-base.formIoTrigger","typeVersion":1,"position":[0,0],"webhookId":"fi","parameters":{"simple":false}}`)
	expectJSON(t, "form.io raw", runNode(t, fioRaw, "I", []map[string]interface{}{{"body": sub}}), `[{"_id":"s1","data":{"email":"ada@x.test"}}]`)
}
