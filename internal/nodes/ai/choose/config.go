package choose

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/workflow"
)

// --- config parsing ---

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", workflow.ErrInvalidConfig, NodeType, fmt.Sprintf(format, args...))
}

func parseConfig(raw map[string]interface{}) (*config, error) {
	cfg := &config{
		input:         strings.TrimSpace(str(raw, "input")),
		instructions:  strings.TrimSpace(str(raw, "instructions")),
		minConfidence: num(raw, "min_confidence", defaultMinConfidence),
		outputKey:     strings.TrimSpace(str(raw, "output_key")),
		concurrency:   int(num(raw, "concurrency", defaultConcurrency)),
	}
	if cfg.outputKey == "" {
		cfg.outputKey = defaultOutputKey
	}
	if cfg.minConfidence < 0 || cfg.minConfidence > 1 {
		return nil, invalid("min_confidence must be between 0 and 1")
	}
	if cfg.concurrency <= 0 {
		cfg.concurrency = defaultConcurrency
	}
	if cfg.concurrency > maxConcurrency {
		cfg.concurrency = maxConcurrency
	}
	var err error
	if cfg.cases, err = parseCases(raw["cases"]); err != nil {
		return nil, err
	}
	cfg.fields = stringList(decodeJSONString(raw["fields"]))
	if cfg.extras, err = parseExtras(raw["extra_questions"]); err != nil {
		return nil, err
	}
	return cfg, nil
}

// decodeJSONString lets array/object config come in as a JSON string (the
// GUI's code editor stores text).
func decodeJSONString(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if s[0] == '[' || s[0] == '{' {
		var out any
		if json.Unmarshal([]byte(s), &out) == nil {
			return out
		}
	}
	return v
}

func parseCases(v any) ([]chooseCase, error) {
	v = decodeJSONString(v)
	var list []any
	switch t := v.(type) {
	case []any:
		list = t
	case []string:
		for _, s := range t {
			list = append(list, s)
		}
	case []map[string]any:
		for _, m := range t {
			list = append(list, m)
		}
	case nil:
	default:
		return nil, invalid("cases must be a list")
	}
	var cases []chooseCase
	values := map[string]bool{}
	for _, raw := range list {
		// The GUI's tag input stores text: a pasted {…} object still counts.
		if s, ok := raw.(string); ok && strings.HasPrefix(strings.TrimSpace(s), "{") {
			var obj map[string]any
			if json.Unmarshal([]byte(s), &obj) == nil {
				raw = obj
			}
		}
		var c chooseCase
		switch x := raw.(type) {
		case string:
			c = chooseCase{value: strings.TrimSpace(x)}
		case map[string]any:
			if x["value"] != nil {
				c.value = strings.TrimSpace(fmt.Sprint(x["value"]))
			}
			c.handle = strings.TrimSpace(str(x, "handle"))
			c.description = strings.TrimSpace(str(x, "description"))
		default:
			return nil, invalid("each case must be a string or {value, handle, description}")
		}
		if c.value == "" {
			c.value = c.handle
		}
		if c.value == "" {
			continue
		}
		if c.handle == "" {
			c.handle = c.value
		}
		if c.description == "" {
			c.description = c.value
		}
		if c.handle == LowConfidenceHandle {
			return nil, invalid("case handle %q is reserved", LowConfidenceHandle)
		}
		if values[c.value] {
			return nil, invalid("duplicate case value %q", c.value)
		}
		values[c.value] = true
		cases = append(cases, c)
	}
	if len(cases) < 2 {
		return nil, invalid("needs at least 2 cases")
	}
	if len(cases) > maxOptions {
		return nil, invalid("at most %d cases", maxOptions)
	}
	return cases, nil
}

func parseExtras(v any) ([]extraQuestion, error) {
	v = decodeJSONString(v)
	if v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, invalid("extra_questions must be an object {name: {type, criteria}}")
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []extraQuestion
	for _, name := range names {
		if name == "" || name == primaryQuestion {
			return nil, invalid("extra question name %q is reserved or empty", name)
		}
		spec, ok := m[name].(map[string]any)
		if !ok {
			return nil, invalid("extra question %q must be {type, criteria}", name)
		}
		q := jev.Question{Type: strings.TrimSpace(str(spec, "type"))}
		instr := dataRule
		if s := strings.TrimSpace(str(spec, "instructions")); s != "" {
			instr += "\n" + s
		}
		crit := spec["criteria"]
		switch q.Type {
		case jev.TypeNoul:
			switch c := crit.(type) {
			case string:
				instr += "\nAnswer yes/no: " + c
			case map[string]any:
				q.Criteria = c
			case nil:
				return nil, invalid("extra question %q (noul) needs criteria: the yes/no question", name)
			default:
				return nil, invalid("extra question %q (noul): criteria must be a string or {true, false}", name)
			}
		case jev.TypeChoice:
			opts := map[string]any{}
			switch c := crit.(type) {
			case map[string]any:
				opts = c
			case []any:
				for _, o := range c {
					if s := strings.TrimSpace(fmt.Sprint(o)); s != "" {
						opts[s] = s
					}
				}
			default:
				return nil, invalid("extra question %q (choice): criteria must be {option: description} or a list", name)
			}
			if len(opts) < 2 {
				return nil, invalid("extra question %q (choice) needs at least 2 options", name)
			}
			q.Criteria = opts
		case jev.TypeScore:
			levels := stringList(crit)
			if len(levels) < 2 || len(levels) > 10 {
				return nil, invalid("extra question %q (score) needs 2–10 ordered levels", name)
			}
			q.Criteria = levels
		default:
			return nil, invalid("extra question %q: type must be noul, choice or score", name)
		}
		q.Instructions = instr
		out = append(out, extraQuestion{name: name, question: q})
	}
	return out, nil
}

func stringList(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		var out []string
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func str(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

func num(m map[string]interface{}, key string, def float64) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	}
	return def
}
