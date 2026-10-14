package n8nimport

import (
	"fmt"
	"strings"
)

const lc = "@n8n/n8n-nodes-langchain."

func init() {
	Register(lc+"agent", func(c *MapContext) Mapping { return mapLLMNode(c, "output") })
	Register(lc+"chainLlm", func(c *MapContext) Mapping { return mapLLMNode(c, "text") })
	Register(lc+"chainSummarization", mapSummarization)
	Register(lc+"openAi", mapOpenAI)
}

// modelRuntimes maps n8n chat model sub-node types to the local agent
// runtime that runs the same vendor's models.
var modelRuntimes = map[string]string{
	lc + "lmChatOpenAi": "codex", lc + "lmOpenAi": "codex", lc + "lmChatAzureOpenAi": "codex",
	lc + "lmChatAnthropic": "claude",
}

// modelName reads the model a sub-node selects (plain string or locator).
func modelName(params map[string]interface{}) string {
	for _, k := range []string{"model", "modelName", "modelId"} {
		switch v := params[k].(type) {
		case string:
			return strings.TrimPrefix(v, "models/")
		case map[string]interface{}:
			return strings.TrimPrefix(fmt.Sprint(v["value"]), "models/")
		}
	}
	return ""
}

// applyModel picks runtime and model from the attached chat model and
// reports the sub-nodes agent.ask cannot use.
func applyModel(c *MapContext, cfg map[string]interface{}) {
	cfg["runtime"] = "claude"
	found := false
	for _, s := range c.SubNodes {
		switch s.Kind {
		case "ai_languageModel":
			if found {
				c.Warn("fallback model", s.Name, "agent.ask has no fallback model")
				continue
			}
			found = true
			model := modelName(s.Parameters)
			if rt, ok := modelRuntimes[s.Type]; ok {
				cfg["runtime"] = rt
				if model != "" {
					cfg["model"] = model
				}
				c.Note("model %q (%s) runs through the local %s agent", model, shortType(s.Type), rt)
			} else {
				c.Note("model %q (%s) has no local agent runtime; the claude runtime is used — set runtime/model to choose another", model, shortType(s.Type))
			}
		case "ai_tool":
			c.Warn("tools", s.Name, "tool %q (%s) is not available: agent.ask answers without tools", s.Name, shortType(s.Type))
		case "ai_memory":
			if !applyChatMemory(c, cfg, s) {
				c.Warn("memory", s.Name, "conversation memory (%s) is not kept between runs", shortType(s.Type))
			}
		case "ai_outputParser":
			c.Warn("outputParser", s.Name, "structured output parsing (%s) is not applied; ask for JSON in the prompt", shortType(s.Type))
		default:
			c.Warn(s.Kind, s.Name, "sub-node %s is not supported", shortType(s.Type))
		}
	}
	if !found {
		c.Note("no chat model is attached; the claude runtime is used")
	}
}

// llmPrompt reads the prompt of an agent or LLM chain.
func llmPrompt(c *MapContext) string {
	def := "={{ $json.chatInput }}"
	switch {
	case c.TypeVersion < 1.1:
		def = "={{ $json.input }}"
	case c.TypeVersion < 1.3 && strings.HasSuffix(c.Type, "chainLlm"), c.TypeVersion < 1.2:
		def = "={{ $json.chat_input }}"
	}
	if c.Has("prompt") {
		return c.Str("prompt", def)
	}
	switch c.Literal("promptType", "auto") {
	case "define":
		return c.Str("text", "")
	case "guardrails":
		return c.Str("text", "={{ $json.guardrailsInput }}")
	}
	if c.Has("text") {
		return c.Str("text", def)
	}
	return fmt.Sprint(c.Translate("text", def))
}

func mapLLMNode(c *MapContext, outputKey string) Mapping {
	cfg := map[string]interface{}{"prompt": llmPrompt(c), "output_key": outputKey}
	c.Ignore("agent", "hasOutputParser", "needsFallback", "options.maxIterations", "options.returnIntermediateSteps",
		"options.passthroughBinaryImages", "options.enableStreaming", "options.tracingMetadata")
	if sys := c.Str("options.systemMessage", ""); sys != "" {
		cfg["system_prompt"] = sys
	} else if strings.HasSuffix(c.Type, ".agent") {
		cfg["system_prompt"] = "You are a helpful assistant"
	}
	var system []string
	for i, m := range c.paramList("messages.messageValues") {
		t, _ := m["type"].(string)
		text := fmt.Sprint(c.Translate(fmt.Sprintf("messages[%d]", i), m["message"]))
		if t == "" || t == "SystemMessagePromptTemplate" {
			system = append(system, text)
		} else {
			c.Warn(fmt.Sprintf("messages[%d]", i), t, "only system messages are carried over")
		}
	}
	if len(system) > 0 {
		cfg["system_prompt"] = strings.Join(system, "\n\n")
	}
	applyModel(c, cfg)
	return Mapping{Type: "agent.ask", Config: cfg}
}

func mapSummarization(c *MapContext) Mapping {
	c.Ignore("operationMode", "chunkingMode", "options", "type")
	cfg := map[string]interface{}{"output_key": "output",
		"prompt": "Write a concise summary of the following:\n\n{{ json $json }}"}
	c.Note("summarises each item's JSON in one request (no chunking)")
	applyModel(c, cfg)
	return Mapping{Type: "agent.ask", Config: cfg}
}

// mapOpenAI maps the OpenAI node's text message/response operations.
func mapOpenAI(c *MapContext) Mapping {
	res, op := resOp(c, "text", map[bool]string{true: "response", false: "message"}[c.version(2)])
	if res != "text" || (op != "message" && op != "response") {
		return c.OperationUnsupported(res, op)
	}
	// The codex runtime signs in by itself: the OpenAI credential is not needed.
	cfg := map[string]interface{}{"runtime": "codex", "output_key": "__n8n_answer"}
	if m := c.Str("modelId", ""); m != "" {
		cfg["model"] = m
	}
	list := c.paramList("messages.values")
	contentKey := "content"
	if op == "response" {
		list = c.paramList("responses.values")
	}
	var prompt, system []string
	for i, m := range list {
		text := fmt.Sprint(c.Translate(fmt.Sprintf("messages[%d]", i), m[contentKey]))
		switch m["role"] {
		case "system":
			system = append(system, text)
		case "assistant":
			prompt = append(prompt, "Assistant: "+text)
		default:
			prompt = append(prompt, text)
		}
	}
	if ins := c.Str("options.instructions", ""); ins != "" {
		system = append(system, ins)
	}
	cfg["prompt"] = strings.Join(prompt, "\n\n")
	if len(system) > 0 {
		cfg["system_prompt"] = strings.Join(system, "\n\n")
	}
	c.Ignore("simplify", "jsonOutput", "options.maxTokens", "options.temperature", "options.topP", "builtInTools", "options.store")
	if c.Has("options.temperature") || c.Has("options.maxTokens") {
		c.Note("temperature and token limits are left to the agent runtime")
	}
	for _, s := range c.SubNodes {
		c.Warn("tools", s.Name, "tool %q is not available: agent.ask answers without tools", s.Name)
	}
	shape := "{ message: { role: \"assistant\", content: a } }"
	if op == "response" {
		shape = "{ output: [{ type: \"message\", role: \"assistant\", content: [{ type: \"output_text\", text: a }] }] }"
	}
	body := "return items.map(function (it) { var a = it.json.__n8n_answer; return " + shape + "; });\n"
	return Mapping{Type: "agent.ask", Config: cfg, EntrySuffix: " (ask)",
		Chain: []ChainStep{{Type: "core.code", Config: codeConfig(wrapCode(body))}}}
}
