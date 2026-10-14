package agent

// AskNodeSchema documents the config keys AskNode.Execute reads out of its
// map[string]interface{} config — see internal/nodes/control.SetNodeSchema's
// doc comment for why this is a companion struct rather than the runtime
// config, and internal/tools/schemagen for the tag grammar.
type AskNodeSchema struct {
	Runtime string `json:"runtime" schema:"label=Agent Runtime,type=text,required,help=Locally-installed agent runtime id — claude， codex， kimicode， qwen， … (list: monoagentcli agent scan --installed)， or auto to pick a validated one from the agent roster (monoagentcli agent validate)."`

	Prompt string `json:"prompt" schema:"label=Prompt,type=textarea,required,rows=4,default={{$json.text}},help=Supports {{$json.field}} placeholders."`

	Model string `json:"model" schema:"label=Model,type=text,help=Falls back to the runtime's default model."`

	SystemPrompt string `json:"system_prompt" schema:"label=System Prompt,type=textarea,rows=3"`

	OutputKey string `json:"output_key" schema:"label=Output Key,type=text,default=agent_response"`

	Timeout float64 `json:"timeout" schema:"label=Timeout (seconds),type=number,default=300"`

	OutputSchema string `json:"output_schema" schema:"label=Output Schema (JSON Schema),type=code,language=json,rows=5,placeholder={\"type\": \"object\"， \"required\": [\"category\"]， \"properties\": {\"category\": {\"type\": \"string\"}}},help=Ask for JSON matching this schema (type， required， properties， items， enum are checked). The parsed value goes under Output Key; a mismatch is re-asked once， then fails the item."`

	MergeOutput bool `json:"merge_output" schema:"label=Merge Output Into Item,type=boolean,default=false,help=With an output schema and an object answer， also copy its fields onto the item."`

	Concurrency float64 `json:"concurrency" schema:"label=Concurrency,type=number,default=1,min=1,help=How many items run at once. Output keeps the input order."`

	Memory string `json:"memory" schema:"label=Conversation Memory,type=code,language=json,rows=3,placeholder={\"session_key\": \"{{ $json.sessionId }}\"， \"window\": 10},help=Remember the conversation: each item's turn sees the last window messages (default 10; 0 = all) of its session (session_key， resolved per item， default {{ $json.sessionId }}) and is added to it afterwards. Read or edit a session with the agent.memory node. Leave empty (or set \"enabled\": false) for no memory."`
}
