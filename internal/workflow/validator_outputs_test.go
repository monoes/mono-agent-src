package workflow

import "testing"

func TestValidateWorkflowUpstreamFields(t *testing.T) {
	cases := []struct {
		name  string
		w     *Workflow
		want  []wantIssue
		valid bool
	}{
		{
			name: "declared upstream field",
			w: vwf(trig, vnode("c", "system.execute_command", "Cmd", map[string]interface{}{"command": "ls"}),
				vnode("s", "core.set", "Set", map[string]interface{}{"assignments": assigns("a", "{{ $json.stdout }}")})),
			valid: true,
		},
		{
			name: "upstream field not declared",
			w: vwf(trig, vnode("c", "system.execute_command", "Cmd", map[string]interface{}{"command": "ls"}),
				vnode("s", "core.set", "Set", map[string]interface{}{"assignments": assigns("a", "{{ $json.stdot }}")})),
			want:  []wantIssue{{"s", "assignments[0].value", IssueUnknownUpstreamField, "warning"}},
			valid: true,
		},
		{
			name: "upstream field through a passthrough node",
			w: vwf(trig, vnode("c", "system.execute_command", "Cmd", map[string]interface{}{"command": "ls"}),
				vnode("l", "core.limit", "Limit", map[string]interface{}{"max_items": 1}),
				vnode("s", "core.set", "Set", map[string]interface{}{"assignments": assigns("a", "{{ $json.exit_code }}{{ $json.nope }}")})),
			want:  []wantIssue{{"s", "assignments[0].value", IssueUnknownUpstreamField, "warning"}},
			valid: true,
		},
		{
			name: "SQL rows are an open key set even when the columns field is set",
			w: vwf(trig, vnode("q", "db.postgres", "Query", map[string]interface{}{"operation": "insert", "table": "t", "columns": "id,name", "connection_string": "postgres://x"}),
				vnode("s", "core.set", "Set", map[string]interface{}{"assignments": assigns("a", "{{ $json.anything }}")})),
			valid: true,
		},
		{
			name: "a list of names in a config-named key is an open set",
			w: vwf(trig, vnode("g", "core.aggregate", "Agg", map[string]interface{}{"group_by": "a, b"}),
				vnode("s", "core.set", "Set", map[string]interface{}{"assignments": assigns("a", "{{ $json.a }}{{ $json.b }}")})),
			valid: true,
		},
		{
			name: "config-named key: <output_key> resolves to the config value or its default",
			w: vwf(trig, vnode("c", "system.execute_command", "Cmd", map[string]interface{}{"command": "ls"}),
				vnode("a", "agent.ask", "Ask", map[string]interface{}{"prompt": "hi", "runtime": "claude"}),
				vnode("s", "core.set", "Set", map[string]interface{}{"assignments": assigns("a", "{{ $json.agent_response }}{{ $json.stdout }}{{ $json.answer }}")})),
			want:  []wantIssue{{"s", "assignments[0].value", IssueUnknownUpstreamField, "warning"}},
			valid: true,
		},
		{
			name: "config-named key: <output_field|field> falls back to field",
			w: vwf(trig, vnode("c", "system.execute_command", "Cmd", map[string]interface{}{"command": "ls"}),
				vnode("m", "data.markdown", "MD", map[string]interface{}{"field": "stdout_html"}),
				vnode("s", "core.set", "Set", map[string]interface{}{"assignments": assigns("a", "{{ $json.stdout_html }}")})),
			valid: true,
		},
		{
			name: "open outputs (API response) are not judged",
			w: vwf(trig, vnode("c", "system.execute_command", "Cmd", map[string]interface{}{"command": "ls"}),
				vnode("s", "core.code", "Code", map[string]interface{}{"code": "return items"}),
				vnode("s2", "core.set", "Set", map[string]interface{}{"assignments": assigns("a", "{{ $json.whatever }}")})),
			valid: true,
		},
		{
			name:  "upstream without declared outputs is not judged",
			w:     vwf(trig, vnode("s", "core.set", "Set", map[string]interface{}{"assignments": assigns("a", "{{ $json.anything }}")})),
			valid: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := ValidateWorkflow(tc.w, ValidateOptions{})
			if len(rep.Issues) != len(tc.want) {
				t.Fatalf("issues = %+v, want %+v", rep.Issues, tc.want)
			}
			for i, is := range rep.Issues {
				if got := (wantIssue{is.NodeID, is.Field, is.Code, is.Severity}); got != tc.want[i] {
					t.Errorf("issue %d = %+v, want %+v", i, got, tc.want[i])
				}
			}
			if rep.Valid != tc.valid {
				t.Errorf("valid = %v, want %v", rep.Valid, tc.valid)
			}
		})
	}
}

// Every declared output has a key, and schemas parse with outputs.
func TestSchemaOutputsWellFormed(t *testing.T) {
	n := 0
	for _, typ := range ListEmbeddedSchemas() {
		s, err := LoadDefaultSchema(typ)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if len(s.Outputs) > 0 {
			n++
		}
		for _, o := range s.Outputs {
			if o.Key == "" || o.Description == "" {
				t.Errorf("%s: output %+v needs key and description", typ, o)
			}
		}
	}
	if n == 0 {
		t.Error("no schema declares outputs")
	}
}
