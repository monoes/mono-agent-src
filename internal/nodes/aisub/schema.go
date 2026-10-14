package aisub

// ModelNodeSchema documents ai.model's config — see
// internal/nodes/control.SetNodeSchema's doc comment and
// internal/tools/schemagen for the tag grammar.
type ModelNodeSchema struct {
	Runtime string `json:"runtime" schema:"label=Agent Runtime,type=text,required,help=Locally-installed agent runtime id — claude， codex， kimicode， qwen， … (list: monoagentcli agent scan --installed)， or auto to pick a validated one from the agent roster (monoagentcli agent validate)."`

	Model string `json:"model" schema:"label=Model,type=text,help=Falls back to the runtime's default model."`
}

// MemoryNodeSchema documents ai.memory's config: agent.ask's memory option.
type MemoryNodeSchema struct {
	SessionKey string `json:"session_key" schema:"label=Session Key,type=text,default={{ $json.sessionId }},help=The conversation to use， resolved per item. The default reads the sessionId field a chat trigger puts on its items."`

	Window float64 `json:"window" schema:"label=Context Window (messages),type=number,default=10,min=0,help=How many earlier messages each turn sees (10 = 5 exchanges; 0 = the whole session)."`

	SkipWhenMissing bool `json:"skip_when_missing" schema:"label=Skip When No Session Key,type=boolean,default=false,help=Run an item whose session key resolves to nothing as a fresh conversation instead of failing it."`
}

// OutputParserNodeSchema documents ai.output_parser's config.
type OutputParserNodeSchema struct {
	SchemaType string `json:"schema_type" schema:"label=Schema Type,type=select,options=example|schema,default=example,help=example: generate the schema from a JSON example (every key required). schema: write the JSON Schema yourself."`

	JSONExample string `json:"json_example" schema:"label=JSON Example,type=code,language=json,rows=5,depends_on_key=schema_type,depends_on_values=example,placeholder={\"category\": \"lead\"， \"score\": 0.9}"`

	JSONSchema string `json:"json_schema" schema:"label=JSON Schema,type=code,language=json,rows=6,depends_on_key=schema_type,depends_on_values=schema,placeholder={\"type\": \"object\"， \"required\": [\"category\"]， \"properties\": {\"category\": {\"type\": \"string\"}}}"`
}
