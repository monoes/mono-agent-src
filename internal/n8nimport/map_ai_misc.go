package n8nimport

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

func init() {
	Register(lc+"openAiAssistant", mapOpenAIAssistant)
	Register(lc+"toolExecutor", mapToolExecutor)
	Register(lc+"code", mapLangChainCode)
	Register("n8n-nodes-base.messageAnAgent", mapMessageAnAgent)
}

// mapOpenAIAssistant runs the assistant's turn on the local codex agent:
// the instructions and model of a new assistant carry over; an existing
// assistant's live on OpenAI's side and are not read.
func mapOpenAIAssistant(c *MapContext) Mapping {
	def := "={{ $json.chatInput }}"
	if !c.version(1.1) {
		def = "={{ $json.chat_input }}"
	}
	cfg := map[string]interface{}{"runtime": "codex", "prompt": c.Str("text", def), "output_key": "output"}
	c.Ignore("options.baseURL", "options.maxRetries", "options.timeout", "notice", "noticeTools")
	if c.Literal("mode", "existing") == "new" {
		c.Ignore("name")
		if ins := c.Str("instructions", ""); ins != "" {
			cfg["system_prompt"] = ins
		}
		if m := modelName(c.Params); m != "" {
			cfg["model"] = m
		}
	} else {
		c.Warn("assistantId", compactValue(c.Raw("assistantId")),
			"the assistant's instructions, model and files live on OpenAI and are not imported: put its instructions in system_prompt")
	}
	if tools, _ := c.Raw("nativeTools").([]interface{}); len(tools) > 0 {
		c.Warn("nativeTools", tools, "OpenAI's hosted tools (code interpreter, file retrieval) are not available to the local agent")
	}
	for _, s := range c.SubNodes {
		c.Warn("tools", s.Name, "tool %q is not available: agent.ask answers without tools", s.Name)
	}
	c.Note("the assistant's turn runs on the local codex agent (OpenAI's Assistants API is not called)")
	return Mapping{Type: "agent.ask", Config: cfg}
}

// mapMessageAnAgent sends the message to a local agent: n8n's own agents
// (their instructions, model and tools) live in n8n and are not imported.
func mapMessageAnAgent(c *MapContext) Mapping {
	cfg := map[string]interface{}{"runtime": "claude", "prompt": c.Str("message", ""), "output_key": "__n8n_answer"}
	c.Ignore("agentSource", "inlineAgent", "jsonSchemaExampleNotice", "structuredOutputNotice",
		"advanced.enableStreaming", "advanced.allowOtherNodesData")
	c.Warn("agentId", compactValue(c.Raw("agentId")),
		"the n8n agent's instructions, model and tools are not imported: the message goes to a local agent (set system_prompt, runtime and model)")
	if c.Literal("advanced.invokeMode", "allItems") == "allItems" {
		cfg["execute_once"] = true
	}
	if c.Has("advanced.sessionId") || c.Has("advanced.session") {
		c.Ignore("advanced.sessionId", "advanced.session")
		c.Note("the agent session is not kept between runs")
	}
	structured := c.Bool("useStructuredOutput", false)
	if structured {
		if schema := messageAgentSchema(c); schema != nil {
			cfg["output_schema"] = schema
		}
	} else {
		c.Ignore("schemaType", "jsonSchemaExample", "outputSchema")
	}
	key := "text"
	if !c.version(2) {
		key = "response"
	}
	body := "return items.map(function (it) {\n" +
		"  var a = it.json.__n8n_answer;\n" +
		"  var s = (a !== null && typeof a === 'object') ? a : null;\n" +
		"  var out = { structuredOutput: s, usage: null, toolCalls: [], finishReason: 'stop', session: null };\n" +
		"  out[" + jsQuote(key) + "] = s ? JSON.stringify(s) : a;\n" +
		"  return out;\n" +
		"});\n"
	return Mapping{Type: "agent.ask", Config: cfg, EntrySuffix: " (ask)",
		Chain: []ChainStep{{Type: "core.code", Config: codeConfig(wrapCode(body))}}}
}

// messageAgentSchema reads the structured-output schema: a JSON Schema, or
// one built from a JSON example.
func messageAgentSchema(c *MapContext) map[string]interface{} {
	if c.Literal("schemaType", "fromJson") == "manual" {
		c.Ignore("jsonSchemaExample")
		var s map[string]interface{}
		if err := json.Unmarshal([]byte(fmt.Sprint(c.Raw("outputSchema"))), &s); err != nil || len(s) == 0 {
			c.Warn("outputSchema", nil, "the output schema is not valid JSON: the answer stays plain text")
			return nil
		}
		return s
	}
	c.Ignore("outputSchema")
	var example interface{}
	if err := json.Unmarshal([]byte(fmt.Sprint(c.Raw("jsonSchemaExample"))), &example); err != nil {
		c.Warn("jsonSchemaExample", nil, "the JSON example is not valid JSON: the answer stays plain text")
		return nil
	}
	return schemaFromExample(example)
}

// langChainRefs are the marks of code that needs LangChain or sub-node
// connections, which core.code (plain JavaScript over items) cannot give.
var langChainRefs = regexp.MustCompile(`require\s*\(|getInputConnectionData|@langchain|\bimport\s`)

// mapLangChainCode maps execute-mode code that is plain JavaScript over the
// items to core.code, like the Code node; code that uses LangChain or the
// node's sub-node connections has no equivalent.
func mapLangChainCode(c *MapContext) Mapping {
	c.Ignore("notice", "inputs", "outputs")
	if c.Has("code.supplyData") {
		c.Ignore("code.supplyData")
		c.Warn("code.supplyData", nil, "a LangChain Code sub-node returns LangChain objects (models, tools), which have no mono equivalent")
		return Mapping{Unmapped: true}
	}
	src, _ := c.Raw("code.execute.code").(string)
	if src == "" {
		src = langChainDefaultExecute
	}
	if langChainRefs.MatchString(src) {
		c.Ignore("code.execute.code")
		c.Warn("code.execute.code", nil, "the code uses LangChain or the node's sub-node connections; rewrite it as a core.code node or an agent.ask node")
		return Mapping{Unmapped: true}
	}
	code := importCode(c, "code.execute.code", src)
	return Mapping{Type: "core.code", Config: codeConfig(wrapCode(code))}
}

// langChainDefaultExecute stands for the node's default execute code, which
// builds a LangChain chain from the attached model.
const langChainDefaultExecute = "const llm = await this.getInputConnectionData('ai_languageModel', 0);"

// fromAIRef matches a $fromAI('key', …) placeholder in a tool's parameters.
var fromAIRef = regexp.MustCompile(`\$fromAI\(\s*['"]([^'"]+)['"][^)]*\)`)

// mapToolExecutor runs the attached tool once with the query's arguments:
// a code step turns the query into the item, and the tool's own mapping
// (an app "…Tool" node maps like its app node) reads $fromAI(key) as
// $json[key].
func mapToolExecutor(c *MapContext) Mapping {
	var tools []SubNode
	for _, s := range c.SubNodes {
		if s.Kind == "ai_tool" {
			tools = append(tools, s)
		} else {
			c.Warn(s.Kind, s.Name, "sub-node %s is not supported", shortType(s.Type))
		}
	}
	toolName := c.Str("toolName", "")
	c.Ignore("node")
	if len(tools) == 0 {
		c.Ignore("query")
		c.Warn("tools", nil, "no tool is attached: the node stops with n8n's error")
		return Mapping{Type: "core.stop_error", Config: map[string]interface{}{"message": "No tool inputs found"}}
	}
	tool := tools[0]
	if len(tools) > 1 {
		for _, t := range tools {
			if strings.EqualFold(toolKey(t.Name), toolKey(toolName)) {
				tool = t
			}
		}
		c.Note("%d tools are attached; %q runs", len(tools), tool.Name)
	}
	m, ok := c.mapSubAsNode(tool)
	if !ok {
		return Mapping{Unmapped: true}
	}
	query := c.Value("query")
	q, _ := json.Marshal(query)
	body := "var q = " + string(q) + ";\n" +
		"if (typeof q === 'string') { try { q = JSON.parse(q); } catch (e) { q = { query: q }; } }\n" +
		"var key = " + jsQuote(toolKey(tool.Name)) + ";\n" +
		"var args = (q && typeof q === 'object' && q[key] !== undefined) ? q[key] : q;\n" +
		"if (args === null || typeof args !== 'object') { args = { query: args }; }\n" +
		"return [{ json: args }];\n"
	chain := append([]ChainStep{{Type: m.Type, Config: m.Config}}, m.Chain...)
	return Mapping{Type: "core.code", Config: codeConfig(wrapCode(body)), EntrySuffix: " (query)",
		Chain: chain, Outputs: m.Outputs}
}

// toolKey is the name n8n gives a tool in a query: the node name with
// spaces as underscores.
func toolKey(name string) string { return strings.ReplaceAll(strings.TrimSpace(name), " ", "_") }

// mapSubAsNode maps an attached sub-node as a node of its own through the
// registry (an app "…Tool" variant as its app node), with $fromAI(key)
// placeholders read from the item. Its report entries join the parent's.
func (c *MapContext) mapSubAsNode(s SubNode) (Mapping, bool) {
	typ := s.Type
	_, native := lookup(typ)
	_, app := lookupApp(typ)
	if !native && !app && strings.HasSuffix(typ, "Tool") {
		typ = strings.TrimSuffix(typ, "Tool")
		_, native = lookup(typ)
		_, app = lookupApp(typ)
	}
	if !native && !app {
		c.Warn("tool", s.Name, "tool %s has no mono equivalent", shortType(s.Type))
		return Mapping{}, false
	}
	params, _ := replaceFromAI(s.Parameters).(map[string]interface{})
	sub := newMapContext(&n8nNode{Name: s.Name, Type: typ, TypeVersion: s.TypeVersion,
		Parameters: params, Credentials: s.Credentials}, c.tr.workflowName)
	sub.SubNodes = s.Children
	var m Mapping
	if native {
		mapper, _ := lookup(typ)
		m = mapper(sub)
	} else {
		a, _ := lookupApp(typ)
		m = mapApp(a, sub)
	}
	if !m.Unmapped {
		sub.reportUnused()
	}
	for _, is := range sub.issues {
		is.Message = s.Name + ": " + is.Message
		c.issues = append(c.issues, is)
	}
	c.exprs = append(c.exprs, sub.exprs...)
	c.creds = append(c.creds, sub.creds...)
	c.tr.notes = append(c.tr.notes, sub.tr.notes...)
	return m, !m.Unmapped
}

// replaceFromAI rewrites $fromAI('key', …) to $json["key"] in every string.
func replaceFromAI(v interface{}) interface{} {
	switch t := v.(type) {
	case string:
		return fromAIRef.ReplaceAllString(t, `$$json["$1"]`)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, x := range t {
			out[k] = replaceFromAI(x)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, x := range t {
			out[i] = replaceFromAI(x)
		}
		return out
	}
	return v
}
