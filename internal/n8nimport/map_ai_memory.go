package n8nimport

import (
	"fmt"
	"strings"
)

func init() {
	Register(lc+"memoryManager", mapMemoryManager)
	Register(lc+"memoryChatRetriever", mapMemoryRetriever)
}

// chatMemories are the n8n chat memory sub-nodes monoagent's local chat
// memory stands in for, with the store each one used ("" for n8n's own
// in-process buffer).
var chatMemories = map[string]string{
	lc + "memoryBufferWindow": "",
	lc + "memoryRedisChat":    "Redis",
	lc + "memoryPostgresChat": "Postgres",
	lc + "memoryMongoDbChat":  "MongoDB",
	lc + "memoryXata":         "Xata",
	lc + "memoryMotorhead":    "Motorhead",
}

// fromInputSessionKey is n8n's "Connected Chat Trigger Node" session id.
const fromInputSessionKey = "{{ $json.sessionId }}"

// memoryWindowSince is the first version of each memory sub-node that
// sends only the last contextWindowLength exchanges; before it, the whole
// history was sent. Motorhead's server keeps its own window.
var memoryWindowSince = map[string]float64{
	lc + "memoryBufferWindow": 0, lc + "memoryMongoDbChat": 0,
	lc + "memoryPostgresChat": 1.1, lc + "memoryRedisChat": 1.3, lc + "memoryXata": 1.3,
}

// memorySessionKey reads a chat memory sub-node's session id as a mono
// template: n8n 1.2+ chooses it with sessionIdType (fromInput: the item's
// sessionId; customKey: sessionKey), older versions set it directly.
func memorySessionKey(c *MapContext, s SubNode) string {
	p := s.Parameters
	legacy := s.TypeVersion < 1.2 && p["sessionIdType"] == nil &&
		s.Type != lc+"memoryPostgresChat" && s.Type != lc+"memoryMongoDbChat"
	var raw interface{}
	if legacy {
		param := "sessionKey"
		if s.Type == lc+"memoryXata" || s.Type == lc+"memoryMotorhead" {
			param = "sessionId"
		}
		raw = p[param]
		if raw == nil { // n8n's defaults: 1.1 reads the item's sessionId
			switch {
			case s.TypeVersion >= 1.1:
				raw = "={{ $json.sessionId }}"
			case param == "sessionKey":
				raw = "chat_history"
			}
		}
	} else if t, _ := p["sessionIdType"].(string); t == "customKey" {
		raw = p["sessionKey"]
	} else {
		return fromInputSessionKey
	}
	key := strings.TrimSpace(fmt.Sprint(c.Translate("memory.session_key", raw)))
	if raw == nil || key == "" {
		c.Warn("memory", s.Name, "%s has no session key; the item's sessionId field is used", s.Name)
		return fromInputSessionKey
	}
	return key
}

// memoryWindow is how many messages agent.ask sends for a memory sub-node:
// contextWindowLength exchanges (default 5) are twice as many messages.
func memoryWindow(c *MapContext, s SubNode) int {
	since, ok := memoryWindowSince[s.Type]
	if !ok {
		return 10
	}
	if s.TypeVersion < since {
		return 0
	}
	k := num(s.Parameters, "contextWindowLength", 5)
	if v, isExpr := s.Parameters["contextWindowLength"].(string); isExpr && strings.HasPrefix(v, "=") {
		c.Warn("memory", s.Name, "the context window of %s is an expression; 5 exchanges are used", s.Name)
		k = 5
	}
	if k < 1 {
		k = 1
	}
	return int(k) * 2
}

// applyChatMemory sets agent.ask's memory from an n8n chat memory sub-node.
// It reports false for a memory it cannot stand in for (Zep).
func applyChatMemory(c *MapContext, cfg map[string]interface{}, s SubNode) bool {
	backend, ok := chatMemories[s.Type]
	if !ok {
		return false
	}
	session := memorySessionKey(c, s)
	window := memoryWindow(c, s)
	cfg["memory"] = map[string]interface{}{"session_key": session, "window": window}
	where := "kept in monoagent's local chat memory (per profile, kept across runs)"
	if backend != "" {
		where = "kept in monoagent's local chat memory instead of " + backend
	}
	sizes := fmt.Sprintf("the last %d messages are sent with each prompt", window)
	if window == 0 {
		sizes = "the whole conversation is sent with each prompt"
	}
	c.Note("conversation memory (%s) is %s; %s", shortType(s.Type), where, sizes)
	if session == fromInputSessionKey {
		c.Note("the conversation is chosen by the item's sessionId field: pass {\"sessionId\": \"…\", \"chatInput\": \"…\"} as the run input")
	}
	return true
}

// memoryChild returns the chat memory sub-node of a memory manager or
// retriever, reporting one that is missing or unsupported.
func memoryChild(c *MapContext) string {
	for _, s := range c.SubNodes {
		if s.Kind != "ai_memory" {
			continue
		}
		backend, ok := chatMemories[s.Type]
		if !ok {
			c.Warn("memory", s.Name, "chat memory %s is not available; monoagent's local chat memory is used", shortType(s.Type))
			return fromInputSessionKey
		}
		if backend != "" {
			c.Note("messages are kept in monoagent's local chat memory instead of %s", backend)
		}
		return memorySessionKey(c, s)
	}
	c.Warn("memory", nil, "no chat memory is attached; the session is the item's sessionId field")
	return fromInputSessionKey
}

// mapMemoryManager maps n8n's Chat Memory Manager to agent.memory.
func mapMemoryManager(c *MapContext) Mapping {
	cfg := map[string]interface{}{"session_key": memoryChild(c)}
	mode := c.Literal("mode", "load")
	switch mode {
	case "load":
		cfg["operation"] = "get"
		memoryFormat(c, cfg, c.Bool("simplifyOutput", true))
		group := c.version(1.1)
		if g, ok := c.Raw("options.groupMessages").(bool); ok {
			group = g
		}
		cfg["group"] = group
	case "insert":
		cfg["operation"] = "insert"
		if c.Literal("insertMode", "insert") == "override" {
			cfg["insert_mode"] = "override"
		}
		var msgs []interface{}
		for i, m := range c.paramList("messages.messageValues") {
			role, _ := m["type"].(string)
			if role == "" {
				role = "system"
			}
			if role == "user" {
				role = "human"
			}
			msgs = append(msgs, map[string]interface{}{"role": role,
				"content": c.Translate(fmt.Sprintf("messages[%d]", i), m["message"])})
		}
		if msgs == nil {
			msgs = []interface{}{}
		}
		cfg["messages"] = msgs
	case "delete":
		cfg["operation"] = "delete"
		if c.Literal("deleteMode", "lastN") == "all" {
			cfg["delete_mode"] = "all"
		} else {
			cfg["count"] = c.Num("lastMessagesCount", 2)
		}
	default:
		return c.OperationUnsupported("", mode)
	}
	if mode != "load" && !c.version(1.1) {
		c.Warn("mode", mode, "outputs {success: true}; version 1 of this node output the session's messages")
	}
	return Mapping{Type: "agent.memory", Config: cfg}
}

// mapMemoryRetriever maps n8n's (deprecated) Chat Messages Retriever: the
// session's messages, one item per message, once per run.
func mapMemoryRetriever(c *MapContext) Mapping {
	cfg := map[string]interface{}{"operation": "get", "session_key": memoryChild(c), "group": false, "execute_once": true}
	memoryFormat(c, cfg, c.Bool("simplifyOutput", true))
	return Mapping{Type: "agent.memory", Config: cfg}
}

// memoryFormat picks the get format for n8n's simplifyOutput.
func memoryFormat(c *MapContext, cfg map[string]interface{}, simplify bool) {
	if simplify {
		cfg["format"] = "pairs"
		return
	}
	cfg["format"] = "raw"
	c.Note("unsimplified messages are {role, content, seq, created_at}, not LangChain's serialized message objects")
}
