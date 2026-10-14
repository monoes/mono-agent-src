package guardrails

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// Operations.
const (
	OpClassify = "classify"
	OpSanitize = "sanitize"
)

// config is a node execution's parsed config.
type config struct {
	operation string
	text      string

	keywords      []string
	pii           bool
	piiEntities   []string
	secrets       bool
	secretLevel   string
	urls          bool
	urlRules      urlRules
	regexes       []namedRegex
	llm           []llmCheck
	systemMessage string

	runtime, model string
	timeout        time.Duration
}

func (c *config) llmNames() []string {
	out := make([]string, len(c.llm))
	for i, l := range c.llm {
		out[i] = l.name
	}
	return out
}

func invalid(format string, a ...interface{}) error {
	return fmt.Errorf("%w: %s: %s", workflow.ErrInvalidConfig, NodeType, fmt.Sprintf(format, a...))
}

func parseConfig(raw map[string]interface{}) (*config, error) {
	c := &config{
		operation:     strings.ToLower(str(raw, "operation", OpClassify)),
		text:          str(raw, "text", ""),
		systemMessage: strings.TrimSpace(str(raw, "system_message", "")),
		runtime:       str(raw, "runtime", ""),
		model:         str(raw, "model", ""),
		timeout:       time.Duration(num(raw, "timeout", defaultTimeout)) * time.Second,
	}
	if c.operation != OpClassify && c.operation != OpSanitize {
		return nil, invalid("operation %q: use classify or sanitize", c.operation)
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout * time.Second
	}
	if _, ok := raw["text"]; !ok {
		return nil, invalid(`"text" is required (the text to check, e.g. {{ $json.chatInput }})`)
	}

	c.pii = boolean(raw, "pii")
	for _, e := range list(raw["pii_entities"]) {
		e = strings.ToUpper(e)
		if !knownEntity(e) {
			return nil, invalid("pii_entities: unknown entity %q (known: %s)", e, strings.Join(PIIEntityNames(), ", "))
		}
		c.piiEntities = append(c.piiEntities, e)
	}
	c.secrets = boolean(raw, "secret_keys")
	c.secretLevel = strings.ToLower(str(raw, "secret_keys_permissiveness", "balanced"))
	if _, ok := secretLevels[c.secretLevel]; !ok {
		return nil, invalid("secret_keys_permissiveness %q: use strict, balanced or permissive", c.secretLevel)
	}
	c.urls = boolean(raw, "urls")
	c.urlRules = urlRules{allowed: list(raw["allowed_urls"]), schemes: list(raw["allowed_schemes"]),
		blockUserinfo: boolOr(raw, "block_userinfo", true), allowSubdomains: boolOr(raw, "allow_subdomains", true)}
	if _, set := raw["allowed_schemes"]; !set {
		c.urlRules.schemes = []string{"https"}
	}
	regexes, err := objects(raw["custom_regex"], "custom_regex")
	if err != nil {
		return nil, err
	}
	for i, r := range regexes {
		name, pattern := str(r, "name", ""), str(r, "pattern", str(r, "value", ""))
		if name == "" || pattern == "" {
			return nil, invalid("custom_regex[%d] needs a name and a pattern", i)
		}
		re, err := compileRegex(pattern)
		if err != nil {
			return nil, invalid("custom_regex %q: %v (patterns use Go RE2 syntax: no lookarounds or backreferences)", name, err)
		}
		c.regexes = append(c.regexes, namedRegex{name: name, re: re})
	}

	if c.operation == OpSanitize {
		return c, nil // keywords and model-judged checks only classify
	}
	c.keywords = list(raw["keywords"])
	if boolean(raw, "jailbreak") {
		c.llm = append(c.llm, llmCheck{"jailbreak", strOr(raw, "jailbreak_prompt", jailbreakPrompt), num(raw, "jailbreak_threshold", defaultThreshold)})
	}
	if boolean(raw, "nsfw") {
		c.llm = append(c.llm, llmCheck{"nsfw", strOr(raw, "nsfw_prompt", nsfwPrompt), num(raw, "nsfw_threshold", defaultThreshold)})
	}
	if boolean(raw, "topical_alignment") {
		p := str(raw, "topical_alignment_prompt", "")
		if strings.TrimSpace(p) == "" {
			return nil, invalid("topical_alignment needs topical_alignment_prompt: describe the business scope the text must stay within")
		}
		c.llm = append(c.llm, llmCheck{"topicalAlignment", p, num(raw, "topical_alignment_threshold", defaultThreshold)})
	}
	customs, err := objects(raw["custom"], "custom")
	if err != nil {
		return nil, err
	}
	for i, g := range customs {
		name, prompt := str(g, "name", "Custom Guardrail"), str(g, "prompt", "")
		if strings.TrimSpace(prompt) == "" {
			return nil, invalid("custom[%d] (%s) needs a prompt saying what to flag", i, name)
		}
		c.llm = append(c.llm, llmCheck{name, prompt, num(g, "threshold", defaultThreshold)})
	}
	return c, nil
}

// str reads a string value; non-strings are formatted.
func str(m map[string]interface{}, key, def string) string {
	switch v := m[key].(type) {
	case nil:
		return def
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// strOr is str with an empty or blank string meaning def.
func strOr(m map[string]interface{}, key, def string) string {
	if s := str(m, key, ""); strings.TrimSpace(s) != "" {
		return s
	}
	return def
}

func num(m map[string]interface{}, key string, def float64) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return def
}

func boolOr(m map[string]interface{}, key string, def bool) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return def
}

func boolean(m map[string]interface{}, key string) bool { return boolOr(m, key, false) }

// list reads a list of strings: an array, or comma-separated text.
func list(v interface{}) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	switch x := v.(type) {
	case string:
		for _, s := range strings.Split(x, ",") {
			add(s)
		}
	case []interface{}:
		for _, s := range x {
			if s != nil {
				add(fmt.Sprint(s))
			}
		}
	case []string:
		for _, s := range x {
			add(s)
		}
	}
	return out
}

// objects reads a list of objects: an array, or its JSON text.
func objects(v interface{}, key string) ([]map[string]interface{}, error) {
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return nil, nil
		}
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, invalid("%s is not a JSON array: %v", key, err)
		}
	}
	if v == nil {
		return nil, nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil, invalid("%s must be an array of objects", key)
	}
	out := make([]map[string]interface{}, 0, len(arr))
	for i, e := range arr {
		m, ok := e.(map[string]interface{})
		if !ok {
			return nil, invalid("%s[%d] must be an object", key, i)
		}
		out = append(out, m)
	}
	return out, nil
}
