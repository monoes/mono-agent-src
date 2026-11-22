package control

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dop251/goja"
	"github.com/monoes/mono-agent/internal/workflow"
)

// compileCode compiles code as a script. Code that only parses inside a
// function — a top-level return or await, as n8n code nodes write it —
// runs as the body of an async function instead; its result is awaited
// like any returned promise.
func compileCode(code string) (*goja.Program, error) {
	prg, err := goja.Compile("code", code, false)
	if err == nil || (!strings.Contains(code, "return") && !strings.Contains(code, "await")) {
		return prg, err
	}
	if wrapped, werr := goja.Compile("code", "(async function () {\n"+code+"\n})()", false); werr == nil {
		return wrapped, nil
	}
	return nil, err
}

// settle unwraps a returned promise. goja runs the promise jobs before a
// program returns, so a promise is settled by now unless it waits on
// something that never happens (there are no timers or I/O).
func settle(val goja.Value) (goja.Value, error) {
	if val == nil {
		return val, nil
	}
	p, ok := val.Export().(*goja.Promise)
	if !ok {
		return val, nil
	}
	switch p.State() {
	case goja.PromiseStateFulfilled:
		return p.Result(), nil
	case goja.PromiseStateRejected:
		return nil, fmt.Errorf("promise rejected: %v", p.Result())
	}
	return nil, fmt.Errorf("the returned promise never settled")
}

// copyJSON deep-copies an item's JSON: goja wraps Go maps and slices by
// reference, so a shallow copy would let a script mutate upstream data.
func copyJSON(src map[string]interface{}) map[string]interface{} {
	m := make(map[string]interface{}, len(src))
	if b, err := json.Marshal(src); err == nil && json.Unmarshal(b, &m) == nil {
		return m
	}
	for k, v := range src {
		m[k] = v
	}
	return m
}

// jsonMaps copies each item's JSON for the JS runtime.
func jsonMaps(items []workflow.Item) []interface{} {
	out := make([]interface{}, len(items))
	for i, item := range items {
		out[i] = copyJSON(item.JSON)
	}
	return out
}

// setItemAccessors gives obj all(), first(), last() and item over maps.
func setItemAccessors(rt *goja.Runtime, obj *goja.Object, maps []interface{}) error {
	at := func(i int) goja.Value {
		if i < 0 || i >= len(maps) {
			return goja.Undefined()
		}
		return rt.ToValue(maps[i])
	}
	for name, fn := range map[string]func(goja.FunctionCall) goja.Value{
		"all":   func(goja.FunctionCall) goja.Value { return rt.ToValue(maps) },
		"first": func(goja.FunctionCall) goja.Value { return at(0) },
		"last":  func(goja.FunctionCall) goja.Value { return at(len(maps) - 1) },
	} {
		if err := obj.Set(name, fn); err != nil {
			return err
		}
	}
	return obj.Set("item", at(0))
}

// setNodeAccessor defines $('Node Name'): the named node's output items
// with the same accessors as $input. A node that has not run (or does not
// exist) throws.
func setNodeAccessor(rt *goja.Runtime, outputs map[string][]workflow.Item) error {
	return rt.Set("$", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		items, ok := outputs[name]
		if !ok {
			panic(rt.NewTypeError("$(%q): no output from a node with that name (it has not run, or does not exist)", name))
		}
		obj := rt.NewObject()
		if err := setItemAccessors(rt, obj, jsonMaps(items)); err != nil {
			panic(rt.NewGoError(err))
		}
		return obj
	})
}
