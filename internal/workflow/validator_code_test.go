package workflow

import "testing"

// JavaScript in a code field closes nested blocks with "}}" and reads
// $json; that is not a broken template.
func TestValidateWorkflowCodeBracesAreNotBrokenExpressions(t *testing.T) {
	code := "const src = $json.body ?? $json;\nreturn [{json:{ok: !!src}}];"
	rep := ValidateWorkflow(vwf(trig, vnode("c", "core.code", "Code", map[string]interface{}{"code": code})),
		ValidateOptions{KnownNodeType: knownTypes()})
	if !rep.Valid {
		t.Errorf("code node reported invalid: %+v", rep.Issues)
	}
	rep = ValidateWorkflow(vwf(trig, vnode("s", "core.set", "Set", map[string]interface{}{"assignments": []interface{}{
		map[string]interface{}{"field": "a", "value": "{ $json.x }}"}}})), ValidateOptions{KnownNodeType: knownTypes()})
	if rep.Valid {
		t.Error("a broken expression outside code is still an error")
	}
}
