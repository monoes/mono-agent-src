package n8nimport

import (
	"fmt"
	"sort"
	"strings"
)

// n8n's Guardrails node (v1 and v2) becomes ai.guardrails: the same checks
// with the same item shape ({guardrailsInput, checks}); classify keeps
// n8n's Pass (0) / Fail (1) outputs.

func init() {
	Register(lc+"guardrails", mapGuardrails)
}

// guardrailLLMChecks are the n8n guardrails a chat model judges.
var guardrailLLMChecks = map[string]bool{"jailbreak": true, "nsfw": true, "topicalAlignment": true, "custom": true}

func mapGuardrails(c *MapContext) Mapping {
	c.Ignore("guardrailsUsage")
	op := c.Literal("operation", "classify")
	if op != "classify" && op != "sanitize" {
		return c.OperationUnsupported("", op)
	}
	cfg := map[string]interface{}{"operation": op, "text": c.Str("text", "")}
	g := c.paramMap("guardrails")
	keys := make([]string, 0, len(g))
	for k := range g {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	llm := false
	for _, k := range keys {
		param := "guardrails." + k
		entry, _ := g[k].(map[string]interface{})
		value, _ := entry["value"].(map[string]interface{})
		if guardrailLLMChecks[k] && op == "classify" {
			llm = true
		}
		switch k {
		case "keywords":
			if op == "classify" {
				cfg["keywords"] = c.Translate(param, g[k])
			}
		case "jailbreak", "nsfw":
			if op == "classify" {
				cfg[k] = true
				guardrailThreshold(c, cfg, param, k+"_threshold", value)
				if value["customizePrompt"] == true {
					if p, _ := value["prompt"].(string); strings.TrimSpace(p) != "" {
						cfg[k+"_prompt"] = c.Translate(param, p)
					}
				}
			}
		case "topicalAlignment":
			if op == "classify" {
				cfg["topical_alignment"] = true
				guardrailThreshold(c, cfg, param, "topical_alignment_threshold", value)
				p, _ := value["prompt"].(string)
				if strings.TrimSpace(p) == "" || strings.Contains(p, "[INSERT BUSINESS SCOPE HERE]") {
					c.Warn(param, "", "the topical alignment prompt still has n8n's placeholder scope; set topical_alignment_prompt to your business scope (the node fails until it is set)")
				} else {
					cfg["topical_alignment_prompt"] = c.Translate(param, p)
				}
			}
		case "pii":
			cfg["pii"] = true
			if value["type"] == "selected" {
				if ents, ok := value["entities"].([]interface{}); ok && len(ents) > 0 {
					cfg["pii_entities"] = ents
				}
			}
		case "secretKeys":
			cfg["secret_keys"] = true
			if p, _ := value["permissiveness"].(string); p != "" {
				cfg["secret_keys_permissiveness"] = p
			}
		case "urls":
			mapGuardrailURLs(c, cfg, param, value)
		case "custom":
			if op == "classify" {
				mapCustomGuardrails(c, cfg, param, entry)
			}
		case "customRegex":
			var list []interface{}
			regs, _ := entry["regex"].([]interface{})
			for _, r := range regs {
				if m, ok := r.(map[string]interface{}); ok {
					list = append(list, map[string]interface{}{"name": m["name"], "pattern": m["value"]})
				}
			}
			if len(list) > 0 {
				cfg["custom_regex"] = list
				c.Note("custom regexes run as Go RE2 patterns: lookarounds and backreferences are not supported")
			}
		default:
			c.Warn(param, g[k], "guardrail %q has no mono-agent equivalent", k)
		}
	}
	if op == "classify" && c.Bool("customizeSystemMessage", false) {
		if sys := c.Str("systemMessage", ""); strings.TrimSpace(sys) != "" {
			cfg["system_message"] = sys
		}
	} else {
		c.Ignore("customizeSystemMessage", "systemMessage")
	}
	guardrailsModel(c, cfg, llm)
	if op == "classify" {
		c.Note("output 0 (Pass) is the pass handle, output 1 (Fail) the fail handle")
		return Mapping{Type: "ai.guardrails", Config: cfg, Outputs: []string{"pass", "fail"}}
	}
	return Mapping{Type: "ai.guardrails", Config: cfg}
}

func guardrailThreshold(c *MapContext, cfg map[string]interface{}, param, key string, value map[string]interface{}) {
	switch t := value["threshold"].(type) {
	case float64:
		cfg[key] = t
	case string:
		if t != "" {
			cfg[key] = c.Translate(param, t)
		}
	}
}

func mapGuardrailURLs(c *MapContext, cfg map[string]interface{}, param string, value map[string]interface{}) {
	cfg["urls"] = true
	if s, _ := value["allowedUrls"].(string); s != "" && s != "PLACEHOLDER" {
		cfg["allowed_urls"] = c.Translate(param, s)
	}
	if s, ok := value["allowedSchemes"].([]interface{}); ok {
		cfg["allowed_schemes"] = s
	}
	for n8nKey, key := range map[string]string{"blockUserinfo": "block_userinfo", "allowSubdomains": "allow_subdomains"} {
		if b, ok := value[n8nKey].(bool); ok {
			cfg[key] = b
		}
	}
}

func mapCustomGuardrails(c *MapContext, cfg map[string]interface{}, param string, entry map[string]interface{}) {
	entries, _ := entry["guardrail"].([]interface{})
	var list []interface{}
	for i, e := range entries {
		m, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		g := map[string]interface{}{"name": m["name"], "prompt": c.Translate(fmt.Sprintf("%s[%d]", param, i), m["prompt"])}
		if t, ok := m["threshold"].(float64); ok {
			g["threshold"] = t
		}
		list = append(list, g)
	}
	if len(list) > 0 {
		cfg["custom"] = list
	}
}

// guardrailsModel sets runtime and model from the attached chat model,
// which only the model-judged checks use.
func guardrailsModel(c *MapContext, cfg map[string]interface{}, needed bool) {
	found := false
	for _, s := range c.SubNodes {
		if s.Kind != "ai_languageModel" || found {
			if s.Kind == "ai_languageModel" {
				c.Warn("fallback model", s.Name, "ai.guardrails has no fallback model")
			} else {
				c.Warn(s.Kind, s.Name, "sub-node %s is not supported", shortType(s.Type))
			}
			continue
		}
		found = true
		if !needed {
			c.Note("the chat model %s is not needed: no model-judged guardrail is configured", shortType(s.Type))
			continue
		}
		cfg["runtime"] = "claude"
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
	}
	if needed && !found {
		cfg["runtime"] = "claude"
		c.Note("no chat model is attached; the model-judged guardrails use the claude runtime")
	}
}
