package decide

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/workflow"
)

type option struct{ id, description string }

type config struct {
	kind                        string
	question                    string
	input                       string
	fields                      []string
	criteriaTrue, criteriaFalse string
	levels                      []string
	options                     []option
	minConfidence               float64
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", workflow.ErrInvalidConfig, NodeType, fmt.Sprintf(format, args...))
}

func parseConfig(raw map[string]interface{}) (*config, error) {
	cfg := &config{
		kind:          strings.TrimSpace(str(raw, "kind")),
		question:      strings.TrimSpace(str(raw, "question")),
		input:         strings.TrimSpace(str(raw, "input")),
		fields:        stringList(decodeJSONString(raw["fields"])),
		criteriaTrue:  strings.TrimSpace(str(raw, "criteria_true")),
		criteriaFalse: strings.TrimSpace(str(raw, "criteria_false")),
		minConfidence: num(raw, "min_confidence", defaultMinConfidence),
	}
	if cfg.question == "" {
		return nil, invalid("question is required")
	}
	if cfg.minConfidence < 0 || cfg.minConfidence > 1 {
		return nil, invalid("min_confidence must be between 0 and 1")
	}
	var err error
	switch cfg.kind {
	case jev.TypeNoul:
	case jev.TypeScore:
		if cfg.levels, err = parseLevels(raw["levels"]); err != nil {
			return nil, err
		}
	case jev.TypeChoice:
		if cfg.options, err = parseOptions(raw["options"]); err != nil {
			return nil, err
		}
	case "":
		return nil, invalid("kind is required: noul, score or choice")
	default:
		return nil, invalid("kind %q: use noul, score or choice", cfg.kind)
	}
	return cfg, nil
}

// parseLevels reads the ordered levels: a list, a JSON array as text, or
// one level per line.
func parseLevels(v any) ([]string, error) {
	v = decodeJSONString(v)
	var levels []string
	if s, ok := v.(string); ok {
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				levels = append(levels, l)
			}
		}
	} else {
		levels = stringList(v)
	}
	if len(levels) < 2 || len(levels) > maxLevels {
		return nil, invalid("score needs 2–%d ordered levels, lowest first", maxLevels)
	}
	return levels, nil
}

// parseOptions reads choice options: {id: description} (ids in sorted
// order), or a list of ids or {id (or value), description} objects (in list
// order), or either as JSON text.
func parseOptions(v any) ([]option, error) {
	v = decodeJSONString(v)
	var opts []option
	switch t := v.(type) {
	case map[string]any:
		// Trimmed, then sorted: the handles the editor shows (decideHandles).
		ids := make([]string, 0, len(t))
		raw := make(map[string]string, len(t))
		for id, d := range t {
			trimmed := strings.TrimSpace(id)
			desc, _ := d.(string)
			ids, raw[trimmed] = append(ids, trimmed), desc
		}
		sort.Strings(ids)
		for _, id := range ids {
			opts = append(opts, option{id: id, description: strings.TrimSpace(raw[id])})
		}
	case []any:
		for _, x := range t {
			switch o := x.(type) {
			case string:
				opts = append(opts, option{id: strings.TrimSpace(o)})
			case map[string]any:
				for _, k := range []string{"id", "value"} {
					if v, ok := o[k]; ok && v != nil {
						if _, isStr := v.(string); !isStr {
							return nil, invalid("option %s %v must be a string", k, v)
						}
					}
				}
				id := strings.TrimSpace(str(o, "id"))
				if id == "" {
					id = strings.TrimSpace(str(o, "value"))
				}
				opts = append(opts, option{id: id, description: strings.TrimSpace(str(o, "description"))})
			default:
				return nil, invalid("each option must be an id or {id, description}")
			}
		}
	case []string:
		for _, s := range t {
			opts = append(opts, option{id: strings.TrimSpace(s)})
		}
	case nil:
	default:
		return nil, invalid("options must be {id: description} or a list")
	}
	seen := map[string]bool{}
	for i := range opts {
		o := &opts[i]
		if o.id == "" {
			return nil, invalid("an option has no id")
		}
		if o.id == LowConfidenceHandle {
			return nil, invalid("option id %q is reserved", LowConfidenceHandle)
		}
		if seen[o.id] {
			return nil, invalid("duplicate option id %q", o.id)
		}
		seen[o.id] = true
		if o.description == "" {
			o.description = o.id
		}
	}
	if len(opts) < 2 {
		return nil, invalid("choice needs at least 2 options")
	}
	if len(opts) > maxOptions {
		return nil, invalid("at most %d options", maxOptions)
	}
	return opts, nil
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
