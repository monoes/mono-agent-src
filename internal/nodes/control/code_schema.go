package control

// CodeNodeSchema documents the config keys CodeNode.Execute reads out of its
// map[string]interface{} config — see SetNodeSchema's doc comment for why
// this is a companion struct rather than the runtime config, and
// internal/tools/schemagen for the tag grammar.
//
// Two drift findings vs. the hand-written schemas/core.code.json this
// replaces:
//
//   - The hand-written schema had a required "language" select
//     (javascript|python, default javascript). CodeNode.Execute never reads
//     config["language"] at all — it always runs the code through the goja
//     JS runtime, regardless of what the UI field said. Selecting "python"
//     would have silently run the code as JavaScript. That field is dropped
//     here rather than perpetuated; there is no config key it maps to.
//   - "timeout_seconds" is a real, actually-read config key (execution
//     timeout default 30s clamped to 1-600) that had no field in the
//     hand-written schema at all. "memory_limit_mb" has no field: the
//     vendored goja cannot limit memory, so the knob does nothing and is
//     not advertised (the key is still accepted in old workflows).
//
// "language" is back as a real key: "python" runs the code with the system
// python3 (code_python.go), with "mode" choosing n8n's all-items or
// per-item run.
type CodeNodeSchema struct {
	Language string `json:"language" schema:"label=Language,type=select,options=javascript|python,default=javascript,help=python runs the code with the system python3 (or MONOAGENT_PYTHON) with n8n's Python names: _items in all-items mode or _item per item; also _input.all()/first()/last() and _json and _('Node Name').all(). Return a list of dicts (or one dict per item)."`

	Mode string `json:"mode" schema:"label=Mode,type=select,options=runOnceForAllItems|runOnceForEachItem,default=runOnceForAllItems,help=Python only: run once with all items or once per item.,depends_on_key=language,depends_on_values=python"`

	Code string `json:"code" schema:"label=Code,type=code,required,language=javascript,help=Return an array of objects. Each object becomes an output item. Access input via $input.all() or $json (first item) and another node's output via $('Node Name').all(). A top-level return or await runs the code as an async function."`

	TimeoutSeconds float64 `json:"timeout_seconds" schema:"label=Timeout (seconds),type=number,default=30,min=1,max=600,help=Execution timeout in seconds."`
}
