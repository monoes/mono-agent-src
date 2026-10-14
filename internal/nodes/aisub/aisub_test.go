package aisub

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestSubNodesFailWhenRunAsStep(t *testing.T) {
	r := workflow.NewNodeTypeRegistry()
	RegisterAll(r)
	for typ, kind := range map[string]string{TypeModel: KindModel, TypeMemory: KindMemory, TypeOutputParser: KindOutputParser} {
		f, ok := r.Get(typ)
		if !ok {
			t.Fatalf("%s not registered", typ)
		}
		_, err := f().Execute(context.Background(), workflow.NodeInput{}, nil)
		want := typ + " is a sub-node: connect its " + kind + " output to Agent Ask"
		if !errors.Is(err, workflow.ErrInvalidConfig) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", typ, err)
		}
	}
}

func TestBuildSchema(t *testing.T) {
	got, err := BuildSchema(map[string]interface{}{"json_example": `{"b": [{"x": true}], "a": 1, "s": "t"}`})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{"type": "object", "required": []interface{}{"a", "b", "s"}, "properties": map[string]interface{}{
		"a": map[string]interface{}{"type": "number"},
		"s": map[string]interface{}{"type": "string"},
		"b": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "object",
			"required": []interface{}{"x"}, "properties": map[string]interface{}{"x": map[string]interface{}{"type": "boolean"}}}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schema = %#v", got)
	}
	s, err := BuildSchema(map[string]interface{}{"schema_type": "schema", "json_schema": `{"type": "object"}`})
	if err != nil || s["type"] != "object" {
		t.Fatalf("schema mode: %v %v", s, err)
	}
	for _, bad := range []map[string]interface{}{
		{"json_example": ""}, {"json_example": "{"}, {"schema_type": "schema", "json_schema": "[]"}, {"schema_type": "zod"},
	} {
		if _, err := BuildSchema(bad); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}

func TestMemoryOfDefaults(t *testing.T) {
	in := workflow.NodeInput{SubNodes: []workflow.SubNode{{Kind: KindMemory, Config: map[string]interface{}{}}}}
	m, ok := MemoryOf(in)
	if !ok || !reflect.DeepEqual(m, map[string]interface{}{"session_key": "{{ $json.sessionId }}"}) {
		t.Fatalf("memory = %v", m)
	}
	if _, ok := ModelOf(in); ok {
		t.Fatal("model found without one attached")
	}
}
